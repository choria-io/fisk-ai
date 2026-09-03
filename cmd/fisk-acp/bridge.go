//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	acp "github.com/eino-contrib/acp"
	acpconn "github.com/eino-contrib/acp/conn"

	"github.com/choria-io/fisk-ai/internal/a2a"
	wire "github.com/choria-io/fisk-ai/internal/a2a/wire/v1"
)

// bridge is the ACP agent: it answers the protocol's methods by driving one a2a client
// against a remote Fisk worker.
//
// An ACP session and an a2a conversation are two ids rather than one. The worker refuses
// a token it did not mint, answering codeUnknownConversation, so a session opens by
// sending no token and takes the one the ack hands back. The map here is what holds them
// together, which means an ACP session lasts as long as this process and no longer. A
// session that outlives the process needs that pair stored somewhere, which is
// session/load's problem and not a POC's.
//
// Everything not implemented is inherited from acp.BaseAgent, which answers method not
// found rather than succeeding silently. That is the whole point of declaring no optional
// capability: a client that asks anyway gets a truthful refusal.
type bridge struct {
	acp.BaseAgent

	client *a2a.Client
	agent  string
	log    *slog.Logger

	mu sync.Mutex
	// conn is set once, after construction, because the connection needs the agent and
	// the agent needs the connection.
	conn *acpconn.AgentConnection
	// elicits records whether the client declared form elicitation at initialize. The
	// three question tools may only be put to a client that did.
	elicits bool
	// tokens is the conversation each session is on, empty until its first turn is
	// accepted.
	tokens map[acp.SessionID]string
}

var _ acp.Agent = (*bridge)(nil)

func newBridge(client *a2a.Client, agent string, log *slog.Logger) *bridge {
	return &bridge{client: client, agent: agent, log: log, tokens: map[acp.SessionID]string{}}
}

func (b *bridge) setConnection(conn *acpconn.AgentConnection) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.conn = conn
}

func (b *bridge) connection() *acpconn.AgentConnection {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.conn
}

func (b *bridge) formElicitation() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.elicits
}

// token is the conversation a session is on, empty for one whose first turn has not been
// accepted yet.
func (b *bridge) token(session acp.SessionID) string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.tokens[session]
}

// setToken records the conversation an ack handed back. It is written once per session
// and the value never changes, so a later turn sends what the first was given.
func (b *bridge) setToken(session acp.SessionID, token string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, seen := b.tokens[session]; !seen {
		b.log.Debug("Session opened a conversation", "session", session, "conversation", token)
	}

	b.tokens[session] = token
}

// Initialize declares what this bridge can do, which is prompting and nothing else, and
// records what the client can do. Every optional agent capability is left off rather
// than declared and answered badly.
func (b *bridge) Initialize(_ context.Context, params acp.InitializeRequest) (acp.InitializeResponse, error) {
	form := params.ClientCapabilities != nil &&
		params.ClientCapabilities.Elicitation != nil &&
		params.ClientCapabilities.Elicitation.Form != nil

	b.mu.Lock()
	b.elicits = form
	b.mu.Unlock()

	b.log.Debug("Initialized", "client_form_elicitation", form)

	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion(acp.CurrentProtocolVersion),
		AgentInfo:       &acp.Implementation{Name: "fisk-acp", Version: "0.1.0"},
	}, nil
}

// NewSession mints the id the client will carry. No conversation exists yet: the first
// prompt opens one and the ack says which.
func (b *bridge) NewSession(_ context.Context, _ acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	id := acp.SessionID(wire.NewID())

	b.mu.Lock()
	b.tokens[id] = ""
	b.mu.Unlock()

	b.log.Debug("New session", "session", id)

	return acp.NewSessionResponse{SessionID: id}, nil
}

// Prompt runs one turn against the remote agent and streams its narration back as
// session updates.
//
// Deltas are not asked for, so each block arrives whole and becomes one update. That
// costs the typewriter effect and saves reconciling fragments against the blocks that
// follow them.
func (b *bridge) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	prompt := promptText(params.Prompt)
	if prompt == "" {
		return acp.PromptResponse{}, fmt.Errorf("the prompt carried no text")
	}

	// A first turn carries no token and is handed one; every turn after it names the
	// conversation it is continuing. Sending a token the worker never minted is refused
	// rather than treated as a new conversation, so this is not optional.
	req := wire.NewRequest(prompt)
	req.ConversationToken = b.token(params.SessionID)

	handler := &taskHandler{
		bridge:  b,
		session: params.SessionID,
		calls:   map[string]string{},
	}

	out, err := b.client.RunTask(ctx, b.agent, req, handler)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	// The ack carries the conversation whether it opened one or continued one, and it
	// arrives even for a turn that ended badly, so it is recorded before the outcome is
	// read.
	if out.Ack != nil && out.Ack.ConversationToken != "" {
		b.setToken(params.SessionID, out.Ack.ConversationToken)
	}

	if out.Gaps > 0 {
		b.log.Warn("Event messages never arrived", "gaps", out.Gaps, "session", params.SessionID)
	}

	// An error ending is not a failed method call: the turn ran and stopped for a
	// reason, and saying so leaves the conversation usable where a JSON-RPC error would
	// read as the bridge breaking.
	if out.Error != nil {
		_ = b.message(ctx, params.SessionID, "The run ended: "+out.Error.Err)

		return acp.PromptResponse{StopReason: acp.StopReasonRefusal}, nil
	}

	reason := acp.StopReasonEndTurn
	if out.Result != nil {
		reason = stopReason(out.Result.StopReason)
	}

	return acp.PromptResponse{StopReason: reason}, nil
}

// SessionCancel has nothing to do: the connection cancels the context it gave Prompt,
// which ends RunTask and with it the reading and any question still outstanding.
//
// That is a stop rather than the boundary a wire cancel asks for, so the worker carries
// on to its own next boundary and this bridge stops listening. Asking properly needs the
// request id RunTask stamps, which is not visible from here, and a POC does not need it.
func (b *bridge) SessionCancel(_ context.Context, params acp.CancelNotification) error {
	b.log.Debug("Cancel", "session", params.SessionID)

	return nil
}

// update sends one session notification, logging a send failure rather than failing the
// turn: the narration is advisory and the answer is in the prompt response.
func (b *bridge) update(ctx context.Context, session acp.SessionID, u acp.SessionUpdate) error {
	conn := b.connection()
	if conn == nil {
		return nil
	}

	err := conn.SessionUpdate(ctx, acp.SessionNotification{SessionID: session, Update: u})
	if err != nil {
		b.log.Warn("Sending a session update failed", "session", session, "error", err)
	}

	return err
}

// message sends one assistant message chunk, which is how the bridge says something in
// its own voice rather than the model's.
func (b *bridge) message(ctx context.Context, session acp.SessionID, text string) error {
	return b.update(ctx, session, acp.NewSessionUpdateAgentMessageChunk(acp.ContentChunk{
		Content: acp.NewContentBlockText(acp.TextContent{Text: text}),
	}))
}

// promptText is the user's turn as one string. Text blocks are what every agent must
// support and the only kind this asks for, so anything else in the message is dropped.
func promptText(blocks []acp.ContentBlock) string {
	var parts []string

	for _, block := range blocks {
		text, ok := block.AsText()
		if !ok {
			continue
		}

		parts = append(parts, text.Text)
	}

	return strings.TrimSpace(strings.Join(parts, "\n"))
}

// stopReason maps how the run ended onto ACP's five reasons.
//
// Fisk names more endings than ACP does, so several collapse. A suspended run is one
// that stopped to ask something nobody answered, which is a refusal from the client's
// side: the turn ended and the answer is not in it.
func stopReason(r wire.StopReason) acp.StopReason {
	switch r {
	case wire.StopEndTurn:
		return acp.StopReasonEndTurn
	case wire.StopMaxTokens, wire.StopBudgetExhausted:
		return acp.StopReasonMaxTokens
	case wire.StopMaxIterations:
		return acp.StopReasonMaxTurnRequests
	case wire.StopCanceled:
		return acp.StopReasonCancelled
	case wire.StopRefusal, wire.StopError, wire.StopSuspended:
		return acp.StopReasonRefusal
	default:
		return acp.StopReasonEndTurn
	}
}
