//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agenttest

import (
	"sync"

	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	fisktool "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
)

// RecordedMessage pairs an assistant turn with whether the loop reported it as the
// terminal answer, so a spec can distinguish the final message from intermediate
// narration.
type RecordedMessage struct {
	Response llm.Response
	Terminal bool
}

// RecordingEvents is an agent.Events that records every event for later assertion.
// It is safe for concurrent use, so a single instance can aggregate several runs at
// once (the N-concurrent-runs acceptance test); accessors return copies so a caller
// never races the run goroutines still writing.
type RecordingEvents struct {
	mu          sync.Mutex
	warnings    []agent.Warning
	messages    []RecordedMessage
	starts      []agent.RunInfo
	toolCalls   []agent.ToolTrace
	toolResults []agent.ToolResultTrace
	rotated     []string
	panics      []RecordedPanic
}

// RecordedPanic is a crash reported through Panicked: the recovered value and the
// captured goroutine stack.
type RecordedPanic struct {
	Value any
	Stack []byte
}

// NewRecordingEvents returns an empty recorder.
func NewRecordingEvents() *RecordingEvents { return &RecordingEvents{} }

func (e *RecordingEvents) Warn(w agent.Warning) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.warnings = append(e.warnings, w)
}

func (e *RecordingEvents) Starting(info agent.RunInfo) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts = append(e.starts, info)
}

func (e *RecordingEvents) Message(resp llm.Response, terminal bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = append(e.messages, RecordedMessage{Response: resp, Terminal: terminal})
}

func (e *RecordingEvents) ToolCall(t agent.ToolTrace) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.toolCalls = append(e.toolCalls, t)
}

func (e *RecordingEvents) ToolResult(t agent.ToolResultTrace) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.toolResults = append(e.toolResults, t)
}

func (e *RecordingEvents) SessionRotated(prevID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rotated = append(e.rotated, prevID)
}

func (e *RecordingEvents) Panicked(value any, stack []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.panics = append(e.panics, RecordedPanic{Value: value, Stack: stack})
}

// Panics returns a copy of the crashes reported through Panicked.
func (e *RecordingEvents) Panics() []RecordedPanic {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]RecordedPanic, len(e.panics))
	copy(out, e.panics)
	return out
}

// The remaining events carry nothing a spec asserts on today, so they are accepted
// and dropped rather than recorded; they exist to satisfy the interface.
func (e *RecordingEvents) RemoteHostNotes([]remotetools.HostImport) {}
func (e *RecordingEvents) ResumeTranscript(*runstate.RunState, map[string]*fisktool.FiskCommandTool) {
}
func (e *RecordingEvents) LLMRequest(string) {}

// Warnings returns a copy of the warnings emitted, in order.
func (e *RecordingEvents) Warnings() []agent.Warning {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]agent.Warning, len(e.warnings))
	copy(out, e.warnings)
	return out
}

// HasWarning reports whether a warning of the given kind was emitted.
func (e *RecordingEvents) HasWarning(kind agent.WarningKind) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, w := range e.warnings {
		if w.Kind == kind {
			return true
		}
	}
	return false
}

// Messages returns a copy of the assistant turns recorded, in order.
func (e *RecordingEvents) Messages() []RecordedMessage {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]RecordedMessage, len(e.messages))
	copy(out, e.messages)
	return out
}

// FinalMessage returns the terminal assistant turn and true, or a zero message and
// false when no terminal turn was recorded.
func (e *RecordingEvents) FinalMessage() (llm.Response, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := len(e.messages) - 1; i >= 0; i-- {
		if e.messages[i].Terminal {
			return e.messages[i].Response, true
		}
	}
	return llm.Response{}, false
}

// ToolResults returns a copy of the tool result traces recorded, in order.
func (e *RecordingEvents) ToolResults() []agent.ToolResultTrace {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]agent.ToolResultTrace, len(e.toolResults))
	copy(out, e.toolResults)
	return out
}
