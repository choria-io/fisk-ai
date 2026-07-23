//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"log/slog"

	"github.com/choria-io/fisk-ai/internal/llm"

	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"
)

// slogMaxOutputBytes caps how much of a tool's result SlogEvents records on a
// single line. The run path already bounds tool output, but that ceiling is still
// large for a log record emitted on every tool call, so the structured sink keeps
// only a head and marks the record truncated.
const slogMaxOutputBytes = 2048

// SlogEvents is an agent.Events that renders a run's events as structured slog
// records rather than terminal prose. It is the non-interactive sink for a server
// or job runner, where cliEvents and tcellEvents (which own the stdout/stderr
// split and every byte of wording) do not apply: here the typed data is kept as
// slog attributes for a log pipeline to filter and route.
//
// It is safe for concurrent use. It holds no mutable state of its own and slog
// handlers are themselves safe for concurrent use, so a single SlogEvents can back
// several runs at once. To attribute records to a specific run, construct it with a
// logger that already carries the run's identity, for example
// NewSlogEvents(base.With("run_id", id), verbose); every record then inherits that
// attribute without the interface needing to carry a run id.
type SlogEvents struct {
	log     *slog.Logger
	verbose bool
}

var _ Events = (*SlogEvents)(nil)

// NewSlogEvents returns a SlogEvents that writes to log; a nil log uses
// slog.Default. When verbose is true it additionally emits per-request records
// (LLMRequest) at debug level, matching the terminal renderers' verbose behavior.
func NewSlogEvents(log *slog.Logger, verbose bool) *SlogEvents {
	if log == nil {
		log = slog.Default()
	}

	return &SlogEvents{log: log, verbose: verbose}
}

func (s *SlogEvents) Starting(info RunInfo) {
	s.log.Info("agent run starting",
		"tools", info.Tools,
		"thinking", info.ThinkingEnabled,
		"confirm_tools", info.ConfirmTools,
		"confirm_tags", info.ConfirmTags,
		"trace_file", info.TraceFile,
		"session_id", info.SessionID,
		"resumed", info.Resumed,
		"no_application", info.NoApplication,
	)
}

func (s *SlogEvents) Warn(w Warning) {
	attrs := []any{"kind", warningKindName(w.Kind)}
	if w.Count != 0 {
		attrs = append(attrs, "count", w.Count)
	}
	if w.Name != "" {
		attrs = append(attrs, "name", w.Name)
	}
	if len(w.Params) > 0 {
		attrs = append(attrs, "params", w.Params)
	}
	if w.Err != nil {
		attrs = append(attrs, "error", w.Err.Error())
	}

	s.log.Warn("agent advisory", attrs...)
}

func (s *SlogEvents) RemoteHostNotes(imports []remotetools.HostImport) {
	for _, imp := range imports {
		attrs := []any{
			"host", imp.Host.Name,
			"discovered", imp.Discovered,
			"kept", len(imp.Tools),
		}
		if imp.Err != nil {
			attrs = append(attrs, "error", imp.Err.Error())
		}

		s.log.Info("remote tool host imported", attrs...)

		if imp.IgnoredIncludeTags {
			s.log.Warn("remote tool include filter uses tags, which discovery does not carry; the tag filter was ignored", "host", imp.Host.Name)
		}
		if len(imp.Skipped) > 0 {
			s.log.Warn("remote tools skipped during import", "host", imp.Host.Name, "skipped", imp.Skipped)
		}
	}
}

// ResumeTranscript on a structured sink has no transcript to replay for a human, so
// it records that a resume replayed a prior conversation and how long that prefix
// was, keeping the lifecycle event visible in the log without rendering the turns.
func (s *SlogEvents) ResumeTranscript(rs *runstate.RunState, _ map[string]*fisk.FiskCommandTool) {
	messages := 0
	if rs != nil {
		messages = len(rs.Messages)
	}

	s.log.Info("resuming prior transcript", "messages", messages)
}

func (s *SlogEvents) LLMRequest(summary string) {
	if !s.verbose {
		return
	}

	s.log.Debug("llm request", "summary", summary)
}

func (s *SlogEvents) ToolCall(t ToolTrace) {
	attrs := []any{"tool", t.Name, "kind", toolKindName(t.Kind)}
	if t.Agent != "" {
		attrs = append(attrs, "agent", t.Agent)
	}
	if t.Display != "" {
		attrs = append(attrs, "display", t.Display)
	}

	s.log.Info("tool call", attrs...)
}

func (s *SlogEvents) ToolResult(t ToolResultTrace) {
	output, truncated := capForLog(t.Output)

	s.log.Info("tool result",
		"kind", toolKindName(t.Kind),
		"is_error", t.IsError,
		"truncated", truncated,
		"output", output,
	)
}

func (s *SlogEvents) Message(resp llm.Response, terminal bool) {
	s.log.Info("assistant message",
		"terminal", terminal,
		"stop_reason", string(resp.StopReason),
		"tokens_in", resp.Usage.In,
		"tokens_out", resp.Usage.Out,
		"cache_read", resp.Usage.CacheRead,
		"cache_create", resp.Usage.CacheCreate,
	)
}

func (s *SlogEvents) SessionRotated(prevID string) {
	s.log.Info("session rotated", "prev_session_id", prevID)
}

// Panicked records the crash at error level with the full goroutine stack. This is
// the sink the Events contract reserves the stack for: it is kept local to the log
// and never forwarded to a remote peer, so a server can point SlogEvents at its own
// log while sending the peer only the generic PanicError message.
func (s *SlogEvents) Panicked(value any, stack []byte) {
	s.log.Error("agent run panicked",
		"value", value,
		"stack", string(stack),
	)
}

// capForLog trims output to slogMaxOutputBytes for a log record and reports whether
// it was shortened, so a consumer knows the value is a head rather than the whole.
func capForLog(output string) (string, bool) {
	if len(output) <= slogMaxOutputBytes {
		return output, false
	}

	return output[:slogMaxOutputBytes], true
}

// toolKindName is the stable machine-readable token for a ToolKind, so a log
// pipeline can filter on it rather than on presentation prose.
func toolKindName(k ToolKind) string {
	switch k {
	case ToolLocal:
		return "local"
	case ToolRemote:
		return "remote"
	case ToolBuiltin:
		return "builtin"
	case ToolMemory:
		return "memory"
	default:
		return "unknown"
	}
}

// warningKindName is the stable machine-readable token for a WarningKind. It is
// kept here, private to the structured sink, rather than as a String method on the
// kind, so it does not change how the enum formats anywhere else.
func warningKindName(k WarningKind) string {
	switch k {
	case WarnHITLNoTerminal:
		return "hitl_no_terminal"
	case WarnConfirmNoTerminal:
		return "confirm_no_terminal"
	case WarnConfirmTagUnmatched:
		return "confirm_tag_unmatched"
	case WarnUnknownTool:
		return "unknown_tool"
	case WarnMissingRequired:
		return "missing_required"
	case WarnJournalTerminal:
		return "journal_terminal"
	case WarnJournalUser:
		return "journal_user"
	case WarnResumePausedTurn:
		return "resume_paused_turn"
	case WarnMaxIterInteractive:
		return "max_iter_interactive"
	case WarnTurnErrorInteractive:
		return "turn_error_interactive"
	case WarnMemoryIndex:
		return "memory_index"
	case WarnSessionRotate:
		return "session_rotate"
	case WarnToolSearchUnsupported:
		return "tool_search_unsupported"
	case WarnToolSearchDisabled:
		return "tool_search_disabled"
	case WarnKnowledgeIndexAbsent:
		return "knowledge_index_absent"
	case WarnTraceClose:
		return "trace_close"
	case WarnJournalClose:
		return "journal_close"
	case WarnTraceWrite:
		return "trace_write"
	default:
		return "unknown"
	}
}
