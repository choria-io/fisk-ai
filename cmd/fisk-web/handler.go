//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	wire "github.com/choria-io/fisk-ai/internal/a2a/wire/v1"
)

// errQuestionEndsTurn is what the handler answers a question with. Nothing reads it:
// RunTask treats an error from Question as nobody having answered, which is the point,
// and it exists so the return says why rather than being a bare nil.
var errQuestionEndsTurn = fmt.Errorf("the question goes to the browser on the next request")

// taskHandler renders one a2a task as UI message stream parts and records the question
// that ended it.
//
// It is the a2a.TaskHandler for a single RunTask call, so it lives for one turn.
type taskHandler struct {
	bridge  *bridge
	stream  *stream
	request string

	// text and reasoning hold the stream part id open for each block index of the model
	// call running now. Index restarts at zero on every call, so both are cleared by the
	// status block that ends one, which is llm.DeltaAssembler's Reset.
	text      map[int]string
	reasoning map[int]string
	// parts counts the ids minted so far. An id has to be unique across the whole
	// message and Index is unique only within a model call, so the index cannot be the
	// id.
	parts int

	mu sync.Mutex
	// ask is the question the run stopped on, read by the turn once RunTask has
	// returned. RunTask waits for the question goroutines before it does, so the turn
	// sees whatever was recorded.
	ask *wire.ElicitRequest
}

func newTaskHandler(b *bridge, s *stream, request string) *taskHandler {
	return &taskHandler{
		bridge:    b,
		stream:    s,
		request:   request,
		text:      map[int]string{},
		reasoning: map[int]string{},
	}
}

// Block turns one block of the run's narration into stream parts.
//
// Four of the ten kinds are rendered. Prompt blocks are this caller's own turn coming
// back, and warnings, agent calls and unknown blocks have no part the AI SDK client knows
// how to draw. The status block is read for what it says about the model call rather than
// shown.
func (h *taskHandler) Block(block wire.Block) {
	switch b := block.AsAny().(type) {
	case wire.TextDeltaBlock:
		h.fragment(h.text, "text", b.Index, b.Text)

	case wire.TextBlock:
		h.whole(h.text, "text", b.Index, b.Text)

	case wire.ThinkingDeltaBlock:
		h.fragment(h.reasoning, "reasoning", b.Index, b.Text)

	case wire.ThinkingBlock:
		h.whole(h.reasoning, "reasoning", b.Index, b.Text)

	case wire.ToolCallBlock:
		// The client parses input as unknown, so an absent one would pass as null and
		// leave the page rendering that word. An empty object is what a call with no
		// arguments means.
		input := b.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}

		h.stream.part(toolInputPart{
			Type:       "tool-input-available",
			ToolCallID: b.ID,
			ToolName:   b.Name,
			Input:      input,
			Dynamic:    true,
		})

	case wire.ToolResultBlock:
		if b.IsError {
			h.stream.part(toolErrorPart{
				Type:       "tool-output-error",
				ToolCallID: b.CallID,
				ErrorText:  b.Output,
				Dynamic:    true,
			})

			return
		}

		h.stream.part(toolOutputPart{
			Type:       "tool-output-available",
			ToolCallID: b.CallID,
			Output:     b.Output,
			Dynamic:    true,
		})

	case wire.StatusBlock:
		// The status block ends a model call and the next call counts its blocks from
		// zero, so every id open on this one is closed here. A part the client is never
		// told the end of stays in its streaming state for the rest of the message.
		h.close()
	}
}

// Question records the question and ends the turn rather than holding it.
//
// A browser is not a prompter: the page that would answer is reading this response, and
// it cannot answer until the response is over. So the question travels as the last part
// of this turn and its answer arrives on the next request, which is what the AI SDK's own
// server does with an approval.
//
// Returning alone would leave the worker waiting out its whole window, so the run is
// canceled first. That closes t.stop on the worker, which returns the ask at once and
// brings the terminal message back. The request id is available because a request mints
// its own before it is sent.
func (h *taskHandler) Question(ctx context.Context, ask *wire.ElicitRequest) (*wire.ElicitReply, error) {
	h.mu.Lock()
	first := h.ask == nil
	if first {
		h.ask = ask
	}
	h.mu.Unlock()

	// A second question on a run already being canceled has nowhere to go: one response
	// carries one question. Saying so fails its gated call closed rather than leaving the
	// worker to time it out.
	if !first {
		return wire.NewNoOperatorReply(ask, clientSender), nil
	}

	_, err := h.bridge.client.Cancel(ctx, h.bridge.agent, h.request, "the question is being put to a browser")
	if err != nil {
		h.bridge.log.Warn("Canceling the run that asked a question failed", "request", h.request, "error", err)
	}

	return nil, errQuestionEndsTurn
}

// question is what the run stopped to ask, or nil for a turn that asked nothing.
func (h *taskHandler) question() *wire.ElicitRequest {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.ask
}

// fragment appends one delta to the part its index names, opening the part on the first
// fragment for that index.
func (h *taskHandler) fragment(ids map[int]string, kind string, index int, text string) {
	id, open := ids[index]
	if !open {
		id = h.mintID()
		ids[index] = id

		h.stream.part(boundaryPart{Type: kind + "-start", ID: id})
	}

	// A final fragment with nothing left to send carries no text, and delta is a
	// required field, so an empty one is not sent at all.
	if text == "" {
		return
	}

	h.stream.part(deltaPart{Type: kind + "-delta", ID: id, Delta: text})
}

// whole ends the part an index streamed, or writes the whole block as start, delta and
// end together for an index that streamed nothing.
//
// A whole block replaces its index's fragments, which is llm.DeltaAssembler's rule, so
// text the client already has is not sent again. The assembler's other case, a block
// trimmed at MaxBlockText where the fragments are the fuller copy, does not arise here:
// this never re-sends the block's text over fragments it already wrote.
func (h *taskHandler) whole(ids map[int]string, kind string, index int, text string) {
	id, open := ids[index]
	if open {
		delete(ids, index)
		h.stream.part(boundaryPart{Type: kind + "-end", ID: id})

		return
	}

	if text == "" {
		return
	}

	id = h.mintID()

	h.stream.part(boundaryPart{Type: kind + "-start", ID: id})
	h.stream.part(deltaPart{Type: kind + "-delta", ID: id, Delta: text})
	h.stream.part(boundaryPart{Type: kind + "-end", ID: id})
}

// close ends every text and reasoning part still open. The turn calls it before it
// finishes the message, for the last model call, whose status block may never arrive on a
// run that failed.
func (h *taskHandler) close() {
	for index, id := range h.text {
		delete(h.text, index)
		h.stream.part(boundaryPart{Type: "text-end", ID: id})
	}

	for index, id := range h.reasoning {
		delete(h.reasoning, index)
		h.stream.part(boundaryPart{Type: "reasoning-end", ID: id})
	}
}

// mintID names one text or reasoning part within this message.
func (h *taskHandler) mintID() string {
	h.parts++

	return strconv.Itoa(h.parts)
}
