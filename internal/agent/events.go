//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"

	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
)

// Events receives a run's narration, tool traces and advisories as it happens, so
// the caller owns all wording and rendering: the package decides what happened, the
// caller decides how it looks.
//
// Contract:
//   - Methods are called from the single run goroutine, so a per-run instance sees
//     exactly one run and needs no locking of its own. A caller that shares one Events
//     across concurrent runs (an aggregating server sink) must make its implementation
//     safe for concurrent use; that shared-aggregator contract is defined with the job
//     system, not here.
//   - Methods may be called during teardown, including Panicked from Run's deferred
//     recover after the run body has already returned or unwound, so an implementation
//     must stay callable until Run returns rather than tearing itself down early.
//   - It is a structured sink: methods carry typed data, not preformatted prose, so a
//     consumer is free to render, log, or stream it. The two terminal renderers happen
//     to flatten to prose; a structured (for example slog) consumer keeps the types.
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
	ResumeTranscript(rs *runstate.RunState, tools map[string]*fisk.FiskCommandTool)
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
	Message(resp llm.Response, terminal bool)
	// SessionRotated reports that a context reset started a fresh checkpoint session,
	// leaving the previous one saved and resumable under prevID, so the caller can show
	// the operator how to return to it.
	SessionRotated(prevID string)

	// Panicked reports that the run crashed: Run recovered a panic on its goroutine and
	// is returning a PanicError. value is the recovered panic value and stack is the
	// captured goroutine stack. It is terminal, not an advisory (every Warning is a
	// continue-anyway note), so it is its own method and each surface renders it its own
	// way. The stack reaches this sink and nowhere else: it leaks absolute paths, module
	// layout and frame arguments, so an implementation on a path that forwards to a
	// remote peer must keep the stack local (a server log) and send the peer only the
	// generic PanicError message. It is called from Run's deferred recover during
	// unwind, so an implementation must not itself panic or block.
	Panicked(value any, stack []byte)
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
	// WarnToolSearchUnsupported: Count tools crossed the tool-search threshold but the
	// active provider does not support server-side tool search, so every tool is sent
	// to the model directly and uses more context on each request.
	WarnToolSearchUnsupported
	// WarnToolSearchDisabled: Count tools crossed the tool-search threshold but
	// no_tool_search is set, so every tool is sent to the model directly and uses more
	// context on each request.
	WarnToolSearchDisabled
	// WarnKnowledgeIndexAbsent: knowledge is enabled and a store base (StoreDir) is in
	// effect, but no index exists at the resolved path Name. Most often the knowledge
	// CLI wrote the index elsewhere because it ran with a different (or no) store base;
	// without this the run would start clean and knowledge_search would silently return
	// nothing.
	WarnKnowledgeIndexAbsent
	// WarnTraceClose: closing the trace file at run end failed with Err. It is routed
	// through the events sink rather than written to the shared process stderr so it
	// stays attributable to its run when many run at once.
	WarnTraceClose
	// WarnJournalClose: closing the session journal at run end failed with Err. Routed
	// through the events sink for the same reason as WarnTraceClose.
	WarnJournalClose
	// WarnTraceWrite: writing a line to the trace file failed with Err, so the trace is
	// incomplete. Reported once per run; the run continues, the trace is best-effort.
	WarnTraceWrite
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
	// NoApplication is true when the run wraps no application (application_path is
	// unset), so it runs on built-in and remote tools alone.
	NoApplication bool
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
