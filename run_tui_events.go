//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"strings"

	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"

	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/tui"
)

// tcellEvents satisfies agent.Events, the same contract cliEvents implements.
var _ agent.Events = (*tcellEvents)(nil)

// tcellEvents renders a run's events into the full-screen live view. Like cliEvents
// it owns the wording, but everything goes to one scrolling viewport rather than
// splitting across stdout and stderr, so the final answer is set apart with a
// delimiter. The raw answer text and the warnings are captured as they arrive so
// they can be re-printed to the normal buffer once the alt-screen is torn down.
type tcellEvents struct {
	live    *tui.Live
	verbose bool

	// answer is the raw markdown of the terminal message, kept for the exit re-print
	// so the result survives in native scrollback and stays pipe-compatible.
	answer string
	// warnings collects advisory messages to re-print on exit, since the alt-screen
	// they were shown in is gone once the terminal is restored.
	warnings []string
	// rotatedSessions collects the ids of sessions a context reset rotated away from, so
	// their resume commands survive the alt-screen teardown and stay findable in native
	// scrollback rather than being lost with the transcript.
	rotatedSessions []string
}

func (e *tcellEvents) Warn(w agent.Warning) {
	msg := warningMessage(w)
	if msg == "" {
		return
	}

	e.warnings = append(e.warnings, msg)
	e.live.Append(tui.Line{Kind: tui.LineWarning, Text: msg})
}

func (e *tcellEvents) Starting(info agent.RunInfo) {
	// The no-application notice is intentionally not surfaced here: the TUI has no
	// place for incidental notes yet. Restore it once a logs pane exists.
	if !e.verbose {
		return
	}

	thinking := "disabled"
	if info.ThinkingEnabled {
		thinking = "enabled"
	}

	e.live.Append(tui.Line{Kind: tui.LineMeta, Text: fmt.Sprintf("running with %d tools, thinking %s", info.Tools, thinking)})
}

func (e *tcellEvents) RemoteHostNotes(imports []remotetools.HostImport) {
	var lines []tui.Line
	for _, imp := range imports {
		if len(imp.Tools) == 0 {
			lines = append(lines, tui.Line{Kind: tui.LineWarning, Text: fmt.Sprintf("remote agent %q contributed no tools after filtering; check the include/exclude for that host", imp.Host.Name)})
		}
	}

	e.live.Append(lines...)
}

func (e *tcellEvents) ResumeTranscript(rs *runstate.RunState, _ map[string]*fisk.FiskCommandTool) {
	// A resumed run draws its restored transcript straight away, so the startup card has
	// nothing to wait for; drop it before the transcript lines land.
	e.live.HideSplash()

	// Seed the live token counter from the restored counters (the resumed RunStats
	// starts from the same numbers) so the running total and the end-of-run summary
	// still agree once this session's own usage accumulates on top.
	e.live.SeedUsage(rs.Counters.InTokens, rs.Counters.OutTokens, rs.Counters.CacheReadTokens, rs.Counters.CacheCreateTokens)

	lines := []tui.Line{{Kind: tui.LineMeta, Text: "--- resuming ---"}}
	lines = append(lines, transcriptLines(rs, true)...)
	lines = append(lines, tui.Line{Kind: tui.LineMeta, Text: "--- continuing ---"})

	e.live.Append(lines...)
}

func (e *tcellEvents) LLMRequest(summary string) {
	if e.verbose {
		e.live.Append(tui.Line{Kind: tui.LineMeta, Text: "llm: " + summary})
	}
}

func (e *tcellEvents) ToolCall(t agent.ToolTrace) {
	if line, ok := toolTraceLine(t, e.verbose); ok {
		e.live.Append(line)
	}
}

func (e *tcellEvents) ToolResult(t agent.ToolResultTrace) {
	// A self-rendering built-in tool's call is shown only under verbose (its own
	// prompt follows on the next line), so its result is suppressed the same way,
	// keeping call and result paired rather than leaving an orphaned result line. A
	// memory tool's result is the stored note itself, which is noisy and not useful in
	// the live trace, so it is omitted too; the call line alone records the access.
	if t.Kind == agent.ToolMemory || (t.Kind == agent.ToolBuiltin && !e.verbose) {
		return
	}

	e.live.Append(toolResultLine(t.Output, t.IsError))
}

func (e *tcellEvents) Message(resp llm.Response, terminal bool) {
	// The first response is ready to draw, so drop the startup card even if this message
	// carries only a tool call and appends no lines. Idempotent, so later messages are
	// no-ops.
	e.live.HideSplash()

	lines, answer := messageLines(resp, terminal)
	if terminal && answer != "" {
		e.answer = answer
	}

	// Accumulate the live token counter from the same usage the runner sums into
	// RunStats, so the statusbar number and the end-of-run summary agree.
	e.live.AddUsage(resp.Usage.In, resp.Usage.Out, resp.Usage.CacheRead, resp.Usage.CacheCreate)
	e.live.Append(lines...)
}

// SessionRotated records, in the transcript, that a context reset started a fresh
// checkpoint session and that the previous one is saved and resumable. The line follows
// the "--- context cleared ---" divider the input row already appended.
func (e *tcellEvents) SessionRotated(prevID string) {
	e.rotatedSessions = append(e.rotatedSessions, prevID)
	e.live.Append(tui.Line{Kind: tui.LineMeta, Text: "previous session saved; resume with: fisk-ai run --resume " + prevID})
}

// toolTraceLine maps a tool trace to a viewport line, mirroring cliEvents.ToolCall:
// a built-in tool is shown only when verbose (its own prompt follows), a remote tool
// names its agent, and a local tool shows its resolved command line. The bool is
// false when nothing should be shown.
func toolTraceLine(t agent.ToolTrace, verbose bool) (tui.Line, bool) {
	switch t.Kind {
	case agent.ToolBuiltin:
		if !verbose {
			return tui.Line{}, false
		}
		return tui.Line{Kind: tui.LineToolCall, Text: t.Name}, true
	case agent.ToolMemory:
		return tui.Line{Kind: tui.LineToolCall, Text: t.Display}, true
	case agent.ToolRemote:
		return tui.Line{Kind: tui.LineToolCall, Text: fmt.Sprintf("%s (remote %s)", t.Name, t.Agent)}, true
	default:
		return tui.Line{Kind: tui.LineToolCall, Text: t.Display, Short: t.DisplayShort}, true
	}
}

// messageLines maps an assistant turn to viewport lines: its thinking blocks, then
// its prose. A terminal turn's prose is the final answer, set apart with a delimiter
// since the viewport has no separate answer channel; its raw text is returned so the
// caller can re-print it on exit.
func messageLines(resp llm.Response, terminal bool) ([]tui.Line, string) {
	var lines []tui.Line

	for _, block := range resp.Content {
		if block.Thinking != nil && block.Thinking.Text != "" {
			lines = append(lines, tui.Line{Kind: tui.LineThinking, Text: block.Thinking.Text})
		}
	}

	var answer strings.Builder
	for _, block := range resp.Content {
		if block.Text != nil {
			answer.WriteString(block.Text.Text)
		}
	}

	if answer.Len() == 0 {
		return lines, ""
	}

	if terminal {
		lines = append(lines, tui.Line{Kind: tui.LineMeta, Text: "--- answer ---"})
	}
	lines = append(lines, tui.Line{Kind: tui.LineNarration, Text: answer.String()})

	return lines, answer.String()
}
