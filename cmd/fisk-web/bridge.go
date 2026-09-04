//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/choria-io/fisk-ai/internal/a2a"
	wire "github.com/choria-io/fisk-ai/internal/a2a/wire/v1"
)

// maxRequestBytes caps one POST body. useChat resends the whole history on every request
// and only the newest message is read out of it, so a large body is a page misbehaving
// rather than a person typing.
const maxRequestBytes = 4 << 20

// bridge answers the chat endpoint by driving one a2a client against a remote Fisk
// worker.
//
// The chat id useChat posts and the a2a conversation token are two ids rather than one.
// The worker refuses a token it did not mint, so a first turn sends none and takes the one
// the ack hands back. The map here is what holds them together, which means a thread
// lasts as long as this process and no longer.
type bridge struct {
	client *a2a.Client
	agent  string
	origin string
	log    *slog.Logger

	mu sync.Mutex
	// tokens is the conversation each chat is on, absent until its first turn is
	// accepted.
	tokens map[string]string
}

func newBridge(client *a2a.Client, agent string, origin string, log *slog.Logger) *bridge {
	return &bridge{client: client, agent: agent, origin: origin, log: log, tokens: map[string]string{}}
}

// mux is the routing. One path is served and the mux answers 404 for everything else and
// 405 for another method on this one.
func (b *bridge) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("OPTIONS /api/chat", b.preflight)
	mux.HandleFunc("POST /api/chat", b.chat)

	return mux
}

// preflight answers the OPTIONS the browser sends before the POST. It is not optional:
// application/json is not a CORS-safelisted content type, so the page's first request to
// this endpoint is always this one.
func (b *bridge) preflight(w http.ResponseWriter, _ *http.Request) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", b.origin)
	h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type")
	h.Set("Access-Control-Max-Age", "86400")

	w.WriteHeader(http.StatusNoContent)
}

// chat runs one turn and streams it back.
//
// Everything the request gets wrong is answered with a status code, since nothing has
// been written yet. Once the stream opens the status is spent, and a failure from there
// on travels as an error part instead.
func (b *bridge) chat(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", b.origin)

	var body chatRequest

	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&body)
	if err != nil {
		http.Error(w, "the body is not a chat request: "+err.Error(), http.StatusBadRequest)

		return
	}

	if body.ID == "" {
		http.Error(w, "the request names no chat", http.StatusBadRequest)

		return
	}

	req, err := b.request(&body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	b.turn(r.Context(), w, body.ID, req)
}

// request is the a2a request one POST asks for.
//
// An answer resumes the run that stopped to ask, adding no turn: a deferred call takes it
// as its result and an approval is held for the gate to read when the resumed run
// dispatches the call again. So one request does the whole of it and no resume follows it.
func (b *bridge) request(body *chatRequest) (*wire.Request, error) {
	answer, err := body.answer()
	if err != nil {
		return nil, err
	}

	token := b.token(body.ID)

	if answer != nil {
		if token == "" {
			return nil, fmt.Errorf("an answer continues a conversation and this chat has not started one")
		}

		return streaming(wire.NewAnswerRequest(token, answer)), nil
	}

	prompt := body.prompt()
	if prompt == "" {
		return nil, fmt.Errorf("the newest message carried no text to run")
	}

	// A first turn carries no token and is handed one; every turn after it names the
	// conversation it is continuing. Sending a token the worker never minted is refused
	// rather than treated as a new conversation, so this is not optional.
	req := wire.NewRequest(prompt)
	req.ConversationToken = token

	return streaming(req), nil
}

// turn runs the request and writes the message it produced.
func (b *bridge) turn(ctx context.Context, w http.ResponseWriter, chat string, req *wire.Request) {
	s, err := newStream(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	s.part(startPart{Type: "start", MessageID: wire.NewID()})

	handler := newTaskHandler(b, s, req.Request)

	out, err := b.client.RunTask(ctx, b.agent, req, handler)

	// The ack carries the conversation whether it opened one or continued one, and it
	// arrives even for a turn that ended badly, so it is recorded before the outcome is
	// read. The outcome travels beside an error rather than instead of it.
	if out != nil && out.Ack != nil && out.Ack.ConversationToken != "" {
		b.setToken(chat, out.Ack.ConversationToken)
	}

	// The last model call's status block never arrives on a run that died, so anything it
	// left open is closed here.
	handler.close()

	if err != nil {
		b.log.Warn("Reading the reply set failed", "chat", chat, "request", req.Request, "error", err)
		s.part(errorPart{Type: "error", ErrorText: err.Error()})
		s.part(finishPart{Type: "finish", FinishReason: "error"})
		s.done()

		return
	}

	if out.Gaps > 0 {
		b.log.Warn("Event messages never arrived", "gaps", out.Gaps, "chat", chat)
	}

	// A question is how this turn ended, and the suspension the worker reports it as is
	// the same fact said twice. Sending the error as well would put "the run was
	// suspended" on the page beside the thing it is asking the person.
	ask := handler.question()
	if ask != nil {
		b.log.Debug("The run stopped to ask something", "chat", chat, "kind", ask.Kind, "tool_use", ask.ToolUseID)
		b.ask(s, ask)
		s.part(finishPart{Type: "finish", FinishReason: "tool-calls"})
		s.done()

		return
	}

	if out.Error != nil {
		s.part(errorPart{Type: "error", ErrorText: out.Error.Err})
		s.part(finishPart{Type: "finish", FinishReason: "error"})
		s.done()

		return
	}

	reason := "stop"
	if out.Result != nil {
		reason = finishReason(out.Result.StopReason)
	}

	s.part(finishPart{Type: "finish", FinishReason: reason})
	s.done()
}

// ask sends the question the run stopped on.
//
// An approval has a shape of its own: tool-approval-request mutates the tool part that
// call's tool-input-available already created, so it is only valid after that part, which
// has gone out by the time the gate asks. The other three have no shape in the AI SDK, so
// they go as a data part, which the page reads without a schema.
func (b *bridge) ask(s *stream, ask *wire.ElicitRequest) {
	if ask.Kind == wire.ElicitApprove {
		s.part(approvalPart{
			Type:       "tool-approval-request",
			ApprovalID: ask.QuestionID,
			ToolCallID: ask.ToolUseID,
			Reason:     ask.Display,
		})

		return
	}

	s.part(dataPart{
		Type: "data-question",
		ID:   ask.QuestionID,
		Data: questionData{
			QuestionID: ask.QuestionID,
			ToolUseID:  ask.ToolUseID,
			Kind:       string(ask.Kind),
			Question:   ask.Question,
			Options:    ask.Options,
			Default:    ask.Default,
		},
	})
}

// token is the conversation a chat is on, empty for one whose first turn has not been
// accepted yet.
func (b *bridge) token(chat string) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.tokens[chat]
}

// setToken records the conversation an ack handed back.
func (b *bridge) setToken(chat string, token string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, seen := b.tokens[chat]; !seen {
		b.log.Debug("Chat opened a conversation", "chat", chat, "conversation", token)
	}

	b.tokens[chat] = token
}

// streaming asks for the event stream and for the fragments of an assistant turn as the
// model writes them, which is what makes the page type rather than wait.
func streaming(req *wire.Request) *wire.Request {
	yes := true
	req.Stream = &yes
	req.Deltas = &yes

	return req
}

// finishReason maps how the run ended onto the AI SDK's six reasons. Fisk names more
// endings than the SDK does, so several collapse onto other.
func finishReason(r wire.StopReason) string {
	switch r {
	case wire.StopEndTurn:
		return "stop"
	case wire.StopMaxTokens, wire.StopBudgetExhausted, wire.StopMaxIterations:
		return "length"
	case wire.StopRefusal:
		return "content-filter"
	case wire.StopError:
		return "error"
	default:
		return "other"
	}
}

// chatRequest is the body useChat posts. Three fields are read and the rest is dropped.
//
// id, messages, trigger and messageId are applied after the body spread and cannot be
// overridden, so a value of this bridge's own goes in a field of its own.
type chatRequest struct {
	ID       string      `json:"id"`
	Messages []uiMessage `json:"messages"`
	// Answer carries the answer to a confirm, select or input question. Those three have
	// no approval flow in the AI SDK, so the page sends the answer itself.
	Answer *chatAnswer `json:"fiskAnswer"`
}

// uiMessage is one message of the history.
type uiMessage struct {
	Role  string   `json:"role"`
	Parts []uiPart `json:"parts"`
}

// uiPart is one part of a message. A text part and a tool part are the two this reads,
// and they share a struct because only one shape is ever populated at a time.
type uiPart struct {
	Type       string      `json:"type"`
	Text       string      `json:"text"`
	ToolCallID string      `json:"toolCallId"`
	State      string      `json:"state"`
	Approval   *uiApproval `json:"approval"`
}

// uiApproval is what the person said about a tool-approval-request, as the client writes
// it back into the part.
type uiApproval struct {
	ID       string `json:"id"`
	Approved bool   `json:"approved"`
}

// chatAnswer is the answer to a question this bridge sent as a data part. It names the
// call and the question because the worker refuses an answer that names neither, and
// because reconstructing them from a history nobody verifies would be this bridge
// deciding what the person answered.
type chatAnswer struct {
	ToolUseID string `json:"toolUseId"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	Confirmed bool   `json:"confirmed"`
}

// prompt is the newest user message as one string.
//
// useChat resends the whole UIMessage history on every POST and the AI SDK's own
// documentation treats that history as the conversation. Fisk has an authoritative
// journal on the worker, so only the turn being asked for is read out of the body and
// nothing else in it is trusted.
func (c *chatRequest) prompt() string {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role != "user" {
			continue
		}

		var text []string

		for _, part := range c.Messages[i].Parts {
			if part.Type == "text" && part.Text != "" {
				text = append(text, part.Text)
			}
		}

		return strings.TrimSpace(strings.Join(text, "\n"))
	}

	return ""
}

// answer is the answer this POST carries, or nil for one that only prompts.
func (c *chatRequest) answer() (*wire.Answer, error) {
	if c.Answer != nil {
		return c.Answer.wire()
	}

	return c.approval(), nil
}

// approval reads an answered tool-approval-request out of the history.
//
// Only the newest message is looked at, and only when it is the assistant's. Answering an
// approval submits with no new user message, so the assistant message is last; a POST
// whose newest message is the user's is a new turn, and the approvals still sitting in its
// history were answered on the POST that made each of them newest.
func (c *chatRequest) approval() *wire.Answer {
	if len(c.Messages) == 0 {
		return nil
	}

	last := c.Messages[len(c.Messages)-1]
	if last.Role != "assistant" {
		return nil
	}

	for _, part := range last.Parts {
		if part.State != "approval-responded" || part.Approval == nil || part.ToolCallID == "" {
			continue
		}

		return &wire.Answer{
			ToolUseID: part.ToolCallID,
			Kind:      wire.ElicitApprove,
			Answer:    wire.AnswerChoice,
			Choice:    approvalChoice(part.Approval.Approved),
		}
	}

	return nil
}

// wire is the answer in the shape a request carries. The worker refuses an answer whose
// value does not fit its kind, so each kind fills the one field it is answered with.
func (a *chatAnswer) wire() (*wire.Answer, error) {
	if a.ToolUseID == "" {
		return nil, fmt.Errorf("the answer names no tool call")
	}

	out := &wire.Answer{ToolUseID: a.ToolUseID, Kind: wire.ElicitKind(a.Kind)}

	switch out.Kind {
	case wire.ElicitApprove:
		out.Answer = wire.AnswerChoice
		out.Choice = approvalChoice(a.Confirmed)

	case wire.ElicitConfirm:
		out.Answer = wire.AnswerConfirmed
		out.Confirmed = a.Confirmed

	case wire.ElicitSelect, wire.ElicitInput:
		// A selection names the option rather than its position: the run that offered the
		// list has ended, and a position into a list the worker no longer holds says
		// nothing.
		out.Answer = wire.AnswerValue
		out.Value = a.Value

	default:
		return nil, fmt.Errorf("%q is not a question this agent asks", a.Kind)
	}

	return out, nil
}

// approvalChoice turns the AI SDK's boolean into Fisk's three-valued choice. The standing
// allow is the value with no boolean to come from, and it is left out rather than smuggled
// into a field no generic component sets.
func approvalChoice(approved bool) wire.ElicitChoice {
	if approved {
		return wire.ChoiceOnce
	}

	return wire.ChoiceNo
}
