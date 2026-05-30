//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"github.com/anthropics/anthropic-sdk-go"

	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/util"
)

// Events receives a run's narration, tool traces and advisories as it happens, so
// the caller owns all wording and rendering: the package decides what happened,
// the caller decides how it looks. Methods are called from the run goroutine.
type Events interface {
	// Warn reports an operator-facing advisory as structured data.
	Warn(Warning)
	// Starting reports the resolved run parameters once, before the loop begins.
	Starting(RunInfo)
	// RemoteHostNotes reports the per-host outcome of importing remote tools, for
	// advisory rendering.
	RemoteHostNotes([]remotetools.HostImport)
	// ResumeTranscript asks the caller to replay the prior conversation of a
	// resumed run before it continues; tools is the registry used to render tool
	// calls the way a live run does.
	ResumeTranscript(rs *runstate.RunState, tools map[string]*util.Tool)
	// LLMRequest reports one request's summary; emitted only when verbose.
	LLMRequest(summary string)
	// ToolCall reports a tool invocation as it is dispatched.
	ToolCall(ToolTrace)
	// ToolResult reports a tool's returned output once it has run, so a caller that
	// shows tool output can render it. It is emitted for every executed tool
	// (built-in, remote and local, including an approved confirm-gated one), but not
	// for a call that never ran (an unknown tool or a denied confirmation).
	ToolResult(ToolResultTrace)
	// Message reports an assistant turn: intermediate narration or, when terminal,
	// the final answer.
	Message(msg *anthropic.Message, terminal bool)
	// SessionRotated reports that a context reset started a fresh checkpoint session,
	// leaving the previous one saved and resumable under prevID, so the caller can show
	// the operator how to return to it.
	SessionRotated(prevID string)
}

// WarningKind selects which advisory a Warning carries and which of its fields
// are set.
type WarningKind int

const (
	// WarnHITLNoTerminal: human_in_the_loop is enabled with no interactive
	// terminal, so its tools will decline rather than prompt.
	WarnHITLNoTerminal WarningKind = iota
	// WarnConfirmNoTerminal: Count confirmation-gated tools cannot be approved
	// because no terminal is attached.
	WarnConfirmNoTerminal
	// WarnConfirmTagUnmatched: the confirm_tags entry Name matches no loaded tool.
	WarnConfirmTagUnmatched
	// WarnUnknownTool: the model called a tool Name that is not registered.
	WarnUnknownTool
	// WarnMissingRequired: the model called application tool Name without the
	// required parameters listed in Params, so the call was rejected before it ran.
	WarnMissingRequired
	// WarnJournalTerminal: recording the terminal record failed with Err.
	WarnJournalTerminal
	// WarnJournalUser: recording an interactive follow-up (user turn) failed with Err;
	// the session ends here so the journal stays resumable at the last coherent boundary.
	WarnJournalUser
	// WarnResumePausedTurn: resuming at a paused-turn boundary whose server-side
	// tool state may have expired.
	WarnResumePausedTurn
	// WarnMaxIterInteractive: an interactive turn hit the per-turn iteration cap; the
	// session is not ended (the operator can steer with a follow-up), so it is an
	// advisory rather than the fatal max-iterations outcome a one-shot run returns.
	WarnMaxIterInteractive
	// WarnTurnErrorInteractive: an interactive turn failed (Err carries the cause, e.g.
	// an LLM call timeout). The session is not ended; the operator is handed back to the
	// input bar to retry or steer, so a transient failure does not stall the chat.
	WarnTurnErrorInteractive
	// WarnMemoryIndex: reading the memory store to build the start-of-run index failed
	// with Err. The run continues without the index; the model can still reach the
	// store through the memory tools.
	WarnMemoryIndex
	// WarnSessionRotate: a context reset could not start a fresh checkpoint session (Err
	// carries the cause, e.g. the store failed to create the new journal). The reset is
	// not applied; the turn runs on in the current session, which stays resumable.
	WarnSessionRotate
)

// Warning is a typed operator advisory. Kind selects which fields are meaningful;
// the caller formats the message text.
type Warning struct {
	Kind  WarningKind
	Count int
	Name  string
	Err   error
	// Params carries the parameter names for a kind that reports on a set of them,
	// such as the missing required parameters of a rejected tool call.
	Params []string
}

// RunInfo reports the resolved parameters of a run for the caller to display.
type RunInfo struct {
	Tools           int
	ThinkingEnabled bool
	ConfirmTools    int
	ConfirmTags     []string
	// TraceFile is set when the run is tracing to a file.
	TraceFile string
	// SessionID is set when the run is checkpointed.
	SessionID string
	// Resumed is true when continuing an existing session rather than starting one.
	Resumed bool
}

// ToolKind distinguishes how a traced tool call is dispatched.
type ToolKind int

const (
	// ToolLocal is an application tool; Display holds its resolved command line.
	ToolLocal ToolKind = iota
	// ToolRemote is a tool served by another agent; Agent names that agent.
	ToolRemote
	// ToolBuiltin is an in-process built-in tool that renders its own operator
	// interaction (the human-in-the-loop tools), so its call and result are not
	// traced except under verbose, to avoid duplicating what the tool itself shows.
	ToolBuiltin
	// ToolMemory is an in-process built-in tool that has no operator interaction of
	// its own (the memory tools), so it is traced like an application tool: Display
	// holds its call line and its result is shown.
	ToolMemory
)

// ToolTrace describes one tool invocation for display. Display is the full,
// un-elided command line; DisplayShort is the same line with long argument values
// middle-elided. A width-aware surface (the TUI viewport) shows Display when it fits
// a row and falls back to DisplayShort otherwise, while a plain stream that cannot
// measure a screen uses DisplayShort. Both are empty for non-local tools.
type ToolTrace struct {
	Name         string
	Display      string
	DisplayShort string
	Kind         ToolKind
	Agent        string
}

// ToolResultTrace describes one tool's returned output for display. Kind mirrors
// the ToolTrace it answers, so a caller can apply the same per-kind rules to the
// result as to the call (for example showing a built-in tool's result only when
// verbose). Output is the raw result text, untrusted and unsanitized; IsError
// reports whether the tool reported a failure.
type ToolResultTrace struct {
	Kind    ToolKind
	Output  string
	IsError bool
}
