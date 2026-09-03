//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sync"

	acp "github.com/coder/acp-go-sdk"

	wire "github.com/choria-io/fisk-ai/internal/a2a/wire/v1"
)

// taskHandler renders one a2a task as ACP session updates and puts its questions to the
// ACP client.
//
// It is the a2a.TaskHandler for a single RunTask call, so it lives for one turn.
type taskHandler struct {
	bridge  *bridge
	session acp.SessionId

	mu sync.Mutex
	// calls remembers a tool call's name by its id, so a result can be titled with the
	// tool that produced it. A result arrives with the call id alone.
	calls map[string]string
}

// Block turns one block of the run's narration into a session update.
//
// Four of the eleven kinds are rendered. Prompt blocks are this caller's own turn coming
// back, delta blocks are not asked for, and warnings, agent calls, status and unknown
// blocks have no counterpart in ACP v1 worth inventing for a POC.
func (h *taskHandler) Block(block wire.Block) {
	ctx := context.Background()

	switch b := block.AsAny().(type) {
	case wire.TextBlock:
		if b.Text == "" {
			return
		}

		_ = h.bridge.update(ctx, h.session, acp.UpdateAgentMessageText(b.Text))

	case wire.ThinkingBlock:
		if b.Text == "" {
			return
		}

		_ = h.bridge.update(ctx, h.session, acp.UpdateAgentThoughtText(b.Text))

	case wire.ToolCallBlock:
		h.mu.Lock()
		h.calls[b.ID] = b.Name
		h.mu.Unlock()

		_ = h.bridge.update(ctx, h.session, acp.StartToolCall(
			acp.ToolCallId(b.ID),
			b.Name,
			// Fisk's tool kind names the provider rather than the verb ACP wants, so
			// every call is "other" until something derives one from the impact tags.
			acp.WithStartKind(acp.ToolKindOther),
			acp.WithStartStatus(acp.ToolCallStatusInProgress),
			acp.WithStartRawInput(rawInput(b.Input)),
		))

	case wire.ToolResultBlock:
		status := acp.ToolCallStatusCompleted
		if b.IsError {
			status = acp.ToolCallStatusFailed
		}

		opts := []acp.ToolCallUpdateOpt{
			acp.WithUpdateStatus(status),
			acp.WithUpdateContent([]acp.ToolCallContent{acp.ToolContent(acp.TextBlock(b.Output))}),
		}

		h.mu.Lock()
		name := h.calls[b.CallID]
		h.mu.Unlock()

		if name != "" {
			opts = append(opts, acp.WithUpdateTitle(name))
		}

		_ = h.bridge.update(ctx, h.session, acp.UpdateToolCall(acp.ToolCallId(b.CallID), opts...))
	}
}

// Question puts one of the run's four questions to the ACP client and returns what it
// said.
//
// The approval goes to session/request_permission, which is baseline, so it reaches every
// client. The other three go to elicitation/create, which most clients do not implement,
// and are declined where the client did not declare form support: the protocol forbids
// asking anyway, and declining ends the question at once rather than holding the run for
// a whole window first.
//
// The window is not this method's problem. RunTask tells the worker every AckInterval
// that the question is still in front of somebody, around this call rather than in it, so
// a person may take as long as they like.
func (h *taskHandler) Question(ctx context.Context, ask *wire.ElicitRequest) (*wire.ElicitReply, error) {
	if ask.Kind == wire.ElicitApprove {
		return h.approve(ctx, ask)
	}

	if !h.bridge.formElicitation() {
		h.bridge.log.Warn("Declining a question the client cannot render", "kind", ask.Kind, "session", h.session)

		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	return h.elicit(ctx, ask)
}

// approve asks the client to allow or reject a confirm-gated command.
//
// The four option kinds ACP offers are the three ConfirmChoice carries plus a standing
// reject Fisk has no answer for, so three are offered and the fourth is left out rather
// than mapped onto something it is not.
func (h *taskHandler) approve(ctx context.Context, ask *wire.ElicitRequest) (*wire.ElicitReply, error) {
	conn := h.bridge.connection()
	if conn == nil {
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	title := ask.Command
	if ask.Display != "" {
		title = ask.Display
	}

	resp, err := conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionId: h.session,
		ToolCall: acp.ToolCallUpdate{
			ToolCallId: acp.ToolCallId(ask.ToolUseID),
			Title:      acp.Ptr(title),
			Kind:       acp.Ptr(acp.ToolKindExecute),
			Status:     acp.Ptr(acp.ToolCallStatusPending),
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow once", OptionId: acp.PermissionOptionId(string(wire.ChoiceOnce))},
			{Kind: acp.PermissionOptionKindAllowAlways, Name: "Always allow " + ask.Command, OptionId: acp.PermissionOptionId(string(wire.ChoiceAlways))},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionId: acp.PermissionOptionId(string(wire.ChoiceNo))},
		},
	})
	if err != nil {
		h.bridge.log.Warn("Asking the client for permission failed", "error", err, "tool_use", ask.ToolUseID)

		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	// A canceled outcome is nobody having answered, which is not a decline: the
	// no-operator reply says so and the gate fails the command closed without recording
	// a decision the person never made.
	if resp.Outcome.Selected == nil {
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	choice := wire.ElicitChoice(resp.Outcome.Selected.OptionId)
	switch choice {
	case wire.ChoiceNo, wire.ChoiceOnce, wire.ChoiceAlways:
		return wire.NewApproveReply(ask, clientSender, choice), nil
	default:
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}
}

// elicit asks the client to render a form for one of the three human-in-the-loop tools.
//
// Every field is a string or a string enum. A confirm is a boolean in Go and is not sent
// as one: the client that renders forms best outside an editor handles string, array and
// enum and not boolean, so a two-option enum is what both ends can agree on.
func (h *taskHandler) elicit(ctx context.Context, ask *wire.ElicitRequest) (*wire.ElicitReply, error) {
	conn := h.bridge.connection()
	if conn == nil {
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	const field = "answer"

	property := map[string]any{"type": "string", "title": ask.Question}

	switch ask.Kind {
	case wire.ElicitConfirm:
		property["oneOf"] = []any{
			map[string]any{"const": "yes", "title": "Yes"},
			map[string]any{"const": "no", "title": "No"},
		}

	case wire.ElicitSelect:
		options := make([]any, len(ask.Options))
		for i, option := range ask.Options {
			options[i] = map[string]any{"const": fmt.Sprintf("%d", i), "title": option}
		}
		property["oneOf"] = options

	case wire.ElicitInput:
		if ask.Default != "" {
			property["default"] = ask.Default
		}

	default:
		return nil, fmt.Errorf("the run asked a %q question, which this bridge does not know how to put to anybody", ask.Kind)
	}

	resp, err := conn.UnstableCreateElicitation(ctx, acp.UnstableCreateElicitationRequest{
		Form: &acp.UnstableCreateElicitationForm{
			Mode:    "form",
			Message: ask.Question,
			RequestedSchema: acp.UnstableElicitationSchema{
				Type:       acp.UnstableElicitationSchemaTypeObject,
				Properties: map[string]any{field: property},
				Required:   []string{field},
			},
		},
	})
	if err != nil {
		h.bridge.log.Warn("Asking the client a question failed", "error", err, "kind", ask.Kind)

		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	// Declined and canceled are both nobody answering. A decline is a person choosing
	// not to answer this question rather than choosing an answer, and recording either as
	// one would put a decision in the journal that nobody made.
	if resp.Accept == nil {
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	value, _ := resp.Accept.Content[field].(string)

	switch ask.Kind {
	case wire.ElicitConfirm:
		return wire.NewConfirmReply(ask, clientSender, value == "yes"), nil

	case wire.ElicitSelect:
		index := -1
		for i := range ask.Options {
			if value == fmt.Sprintf("%d", i) {
				index = i

				break
			}
		}
		if index < 0 {
			return wire.NewNoOperatorReply(ask, clientSender), nil
		}

		return wire.NewSelectReply(ask, clientSender, index), nil

	default:
		return wire.NewInputReply(ask, clientSender, value), nil
	}
}

// rawInput is a tool call's arguments as something the client can render. The wire
// carries them as JSON the model produced, and a client shows them under the call.
func rawInput(input []byte) any {
	if len(input) == 0 {
		return nil
	}

	return string(input)
}
