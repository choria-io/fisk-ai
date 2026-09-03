//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	acp "github.com/eino-contrib/acp"

	wire "github.com/choria-io/fisk-ai/internal/a2a/wire/v1"
)

// taskHandler renders one a2a task as ACP session updates and puts its questions to the
// ACP client.
//
// It is the a2a.TaskHandler for a single RunTask call, so it lives for one turn.
type taskHandler struct {
	bridge  *bridge
	session acp.SessionID

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

		_ = h.bridge.message(ctx, h.session, b.Text)

	case wire.ThinkingBlock:
		if b.Text == "" {
			return
		}

		_ = h.bridge.update(ctx, h.session, acp.NewSessionUpdateAgentThoughtChunk(acp.ContentChunk{
			Content: acp.NewContentBlockText(acp.TextContent{Text: b.Text}),
		}))

	case wire.ToolCallBlock:
		h.mu.Lock()
		h.calls[b.ID] = b.Name
		h.mu.Unlock()

		status := acp.ToolCallStatusInProgress
		// Fisk's tool kind names the provider rather than the verb ACP wants, so every
		// call is "other" until something derives one from the impact tags.
		kind := acp.ToolKindOther

		_ = h.bridge.update(ctx, h.session, acp.NewSessionUpdateToolCall(acp.ToolCall{
			ToolCallID: acp.ToolCallID(b.ID),
			Title:      b.Name,
			Kind:       &kind,
			Status:     &status,
			RawInput:   b.Input,
		}))

	case wire.ToolResultBlock:
		status := acp.ToolCallStatusCompleted
		if b.IsError {
			status = acp.ToolCallStatusFailed
		}

		h.mu.Lock()
		name := h.calls[b.CallID]
		h.mu.Unlock()

		_ = h.bridge.update(ctx, h.session, acp.NewSessionUpdateToolCallUpdate(acp.ToolCallUpdate{
			ToolCallID: acp.ToolCallID(b.CallID),
			Title:      name,
			Status:     &status,
			Content: []acp.ToolCallContent{acp.NewToolCallContentContent(acp.Content{
				Content: acp.NewContentBlockText(acp.TextContent{Text: b.Output}),
			})},
		}))
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

	status := acp.ToolCallStatusPending
	kind := acp.ToolKindExecute

	resp, err := conn.RequestPermission(ctx, acp.RequestPermissionRequest{
		SessionID: h.session,
		ToolCall: acp.ToolCallUpdate{
			ToolCallID: acp.ToolCallID(ask.ToolUseID),
			Title:      title,
			Kind:       &kind,
			Status:     &status,
		},
		Options: []acp.PermissionOption{
			{Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow once", OptionID: acp.PermissionOptionID(wire.ChoiceOnce)},
			{Kind: acp.PermissionOptionKindAllowAlways, Name: "Always allow " + ask.Command, OptionID: acp.PermissionOptionID(wire.ChoiceAlways)},
			{Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject", OptionID: acp.PermissionOptionID(wire.ChoiceNo)},
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

	choice := wire.ElicitChoice(resp.Outcome.Selected.OptionID)
	switch choice {
	case wire.ChoiceNo, wire.ChoiceOnce, wire.ChoiceAlways:
		return wire.NewApproveReply(ask, clientSender, choice), nil
	default:
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}
}

// elicit asks the client to render a form for one of the three human-in-the-loop tools.
//
// The request is session scoped and names the tool call it is about, which is what lets a
// client attach the form to the right conversation and the right row. It also carries the
// tool_use id, so a client that groups a question under the call that asked it can.
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

	property := acp.StringPropertySchema{Title: ask.Question}

	switch ask.Kind {
	case wire.ElicitConfirm:
		property.OneOf = []acp.EnumOption{
			{Const: "yes", Title: "Yes"},
			{Const: "no", Title: "No"},
		}

	case wire.ElicitSelect:
		property.OneOf = make([]acp.EnumOption, len(ask.Options))
		for i, option := range ask.Options {
			property.OneOf[i] = acp.EnumOption{Const: strconv.Itoa(i), Title: option}
		}

	case wire.ElicitInput:
		property.Default = ask.Default

	default:
		return nil, fmt.Errorf("the run asked a %q question, which this bridge does not know how to put to anybody", ask.Kind)
	}

	schemaType := acp.ElicitationSchemaTypeObject
	message := ask.Question

	scope := acp.ElicitationSessionScope{SessionID: h.session}
	if ask.ToolUseID != "" {
		call := acp.ToolCallID(ask.ToolUseID)
		scope.ToolCallID = &call
	}

	resp, err := conn.UnstableCreateElicitation(ctx, acp.NewCreateElicitationRequestForm(acp.CreateElicitationRequestForm{
		Message: &message,
		ElicitationFormMode: acp.NewElicitationFormModeElicitationSessionScope(acp.ElicitationFormModeElicitationSessionScope{
			ElicitationSessionScope: scope,
			RequestedSchema: acp.ElicitationSchema{
				Type:       &schemaType,
				Properties: map[string]acp.ElicitationPropertySchema{field: acp.NewElicitationPropertySchemaString(property)},
				Required:   []string{field},
			},
		}),
	}))
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

	value := ""
	if answer, ok := resp.Accept.Content[field]; ok && answer.String != nil {
		value = string(*answer.String)
	}

	switch ask.Kind {
	case wire.ElicitConfirm:
		return wire.NewConfirmReply(ask, clientSender, value == "yes"), nil

	case wire.ElicitSelect:
		index, err := strconv.Atoi(value)
		if err != nil || index < 0 || index >= len(ask.Options) {
			return wire.NewNoOperatorReply(ask, clientSender), nil
		}

		return wire.NewSelectReply(ask, clientSender, index), nil

	default:
		return wire.NewInputReply(ask, clientSender, value), nil
	}
}
