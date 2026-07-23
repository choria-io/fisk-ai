//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	"golang.org/x/term"

	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/tui"
	"github.com/choria-io/fisk-ai/internal/util"
)

// cliEvents renders a run's events for the terminal: it owns every byte of
// wording and the stdout-vs-stderr split, so agent.Run can stay free of
// presentation. Answers go to stdout; narration, traces and advisories go to
// stderr, keeping a piped result clean.
type cliEvents struct {
	verbose        bool
	noColor        bool
	showToolOutput bool
}

func (c *cliEvents) Warn(w agent.Warning) {
	if msg := warningMessage(w); msg != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", msg)
	}
}

// Panicked prints the recovered panic value and the verbatim stack to stderr, so a
// developer keeps a copy-pasteable stack for a bug report. The generic, actionable
// "please report" line is the returned PanicError, printed by the CLI after this, so it
// is the last thing on screen rather than buried above the stack.
func (c *cliEvents) Panicked(value any, stack []byte) {
	fmt.Fprintf(os.Stderr, "\npanic: %v\n\n%s\n", value, stack)
}

// warningMessage is the operator-facing text for a Warning, without the "warning: "
// prefix, so both the line UI and the full-screen UI render the same wording (the
// line UI prints it after "warning: "; the full-screen UI shows it as a warning
// line). An empty string means the kind carries no message.
func warningMessage(w agent.Warning) string {
	switch w.Kind {
	case agent.WarnHITLNoTerminal:
		return "human_in_the_loop is enabled but no interactive terminal is attached; its tools will decline rather than prompt"
	case agent.WarnConfirmNoTerminal:
		return fmt.Sprintf("%d tool(s) require confirmation before running but no interactive terminal is attached; they will always be declined rather than run", w.Count)
	case agent.WarnConfirmTagUnmatched:
		return fmt.Sprintf("confirm_tags entry %q matches no loaded tool; check the tag spelling (run 'fisk-ai info' to list tags)", w.Name)
	case agent.WarnUnknownTool:
		return fmt.Sprintf("model called unknown tool %q", w.Name)
	case agent.WarnMissingRequired:
		return fmt.Sprintf("model called tool %q without required parameter(s): %s; not run", w.Name, strings.Join(w.Params, ", "))
	case agent.WarnJournalTerminal:
		return fmt.Sprintf("recording terminal state: %v", w.Err)
	case agent.WarnJournalUser:
		return fmt.Sprintf("recording your follow-up failed: %v; the session ended but remains resumable from the last saved turn", w.Err)
	case agent.WarnResumePausedTurn:
		return "this session was suspended at a paused turn; if its server-side tool state has expired the resume may error"
	case agent.WarnMaxIterInteractive:
		return fmt.Sprintf("the previous turn reached the iteration cap (%d) before finishing; send a follow-up to steer it, or Ctrl-D to end", w.Count)
	case agent.WarnTurnErrorInteractive:
		return fmt.Sprintf("the previous turn failed: %v; send a follow-up to retry, or Ctrl-D to end", w.Err)
	case agent.WarnMemoryIndex:
		return fmt.Sprintf("reading memory for the start-of-run index failed: %v; continuing without it, the memory tools still work", w.Err)
	case agent.WarnToolSearchUnsupported:
		return fmt.Sprintf("%d tools are available but the model backend does not support server-side tool search, so all are sent to the model directly and use more context each request; use a provider that supports tool search to defer them", w.Count)
	case agent.WarnToolSearchDisabled:
		return fmt.Sprintf("%d tools are available but tool search is disabled (no_tool_search), so all are sent to the model directly and use more context each request; unset no_tool_search to defer them behind the search tool", w.Count)
	case agent.WarnKnowledgeIndexAbsent:
		return fmt.Sprintf("knowledge is enabled but no index exists at %q; if you built it with 'knowledge index', re-run it with a matching --store-dir (or an absolute knowledge directory), or knowledge_search will return nothing", w.Name)
	case agent.WarnTraceClose:
		return fmt.Sprintf("closing trace file: %v", w.Err)
	case agent.WarnJournalClose:
		return fmt.Sprintf("closing session journal: %v", w.Err)
	case agent.WarnTraceWrite:
		return fmt.Sprintf("trace write failed, trace will be incomplete: %v", w.Err)
	default:
		return ""
	}
}

func (c *cliEvents) Starting(info agent.RunInfo) {
	if c.verbose {
		thinkingState := "disabled"
		if info.ThinkingEnabled {
			thinkingState = "enabled"
		}
		fmt.Fprintf(os.Stderr, "Running agent with %d tools, thinking %s\n", info.Tools, thinkingState)
		if info.ConfirmTools > 0 {
			triggers := "ai:confirm (always)"
			if len(info.ConfirmTags) > 0 {
				triggers += ", " + strings.Join(info.ConfirmTags, ", ") + " (config)"
			}
			fmt.Fprintf(os.Stderr, "confirmation gating: %s -> %d tool(s) gated\n", triggers, info.ConfirmTools)
		}
	}

	if info.NoApplication {
		fmt.Fprintln(os.Stderr, "running without a wrapped application; built-in and remote tools only")
	}

	if info.TraceFile != "" {
		fmt.Fprintf(os.Stderr, "tracing session to %s\n", info.TraceFile)
	}
	if info.SessionID != "" && !info.Resumed {
		fmt.Fprintf(os.Stderr, "checkpointing session %q\n", info.SessionID)
	}
}

func (c *cliEvents) RemoteHostNotes(imports []remotetools.HostImport) {
	for _, imp := range imports {
		warnHostNotes(nil, imp)
		if len(imp.Tools) == 0 {
			fmt.Fprintf(os.Stderr, "warning: remote agent %q contributed no tools after filtering; check the include/exclude for that host\n", imp.Host.Name)
		}
	}
}

func (c *cliEvents) ResumeTranscript(rs *runstate.RunState, tools map[string]*fisk.FiskCommandTool) {
	printResumeTranscript(os.Stderr, rs, tools, c.noColor)
}

func (c *cliEvents) LLMRequest(summary string) {
	fmt.Fprintf(os.Stderr, "~> llm: %s\n", summary)
}

func (c *cliEvents) ToolCall(t agent.ToolTrace) {
	switch t.Present {
	case toolkit.PresentSelfRendered: // renders its own prompt (HITL, or a custom tool with no trace)
		if c.verbose {
			fmt.Fprintf(os.Stderr, "-> %s\n", t.Name)
		}
	case toolkit.PresentTraced: // memory / knowledge tools
		fmt.Fprintf(os.Stderr, "-> %s\n", t.Display)
	case toolkit.PresentRemote:
		fmt.Fprintf(os.Stderr, "-> %s (remote %s)\n", t.Name, t.Agent)
	default: // command tool, and the safe default for an unforeseen presentation
		fmt.Fprintf(os.Stderr, "-> %s\n", lineToolCallText(t.Display, t.DisplayShort))
	}
}

// ToolResult shows tool output only under --tool-output; by default the line UI
// keeps the stderr trace to the calls and a piped stdout to the final answer. A
// self-rendering built-in tool's call is shown only under verbose, so its result is
// suppressed the same way to keep call and result paired rather than leaving an
// orphaned result line. A traced built-in's result (a memory or knowledge tool) is
// the stored note itself, which is noisy and not useful in the live trace, so it is
// omitted too; the call line alone records that the tool was used.
func (c *cliEvents) ToolResult(t agent.ToolResultTrace) {
	if !c.showToolOutput {
		return
	}
	if t.Present == toolkit.PresentTraced || (t.Present == toolkit.PresentSelfRendered && !c.verbose) {
		return
	}

	fmt.Fprintf(os.Stderr, "<-\n%s\n", toolResultLine(t.Output, t.IsError).Text)
}

func (c *cliEvents) Message(resp llm.Response, terminal bool) {
	util.PrintText(resp, terminal, c.noColor)
}

// SessionRotated reports the previous session's resume command after a context reset
// rotated to a fresh one. Chat runs in the full-screen UI, so this line path is a
// defensive fallback rather than the usual surface.
func (c *cliEvents) SessionRotated(prevID string) {
	fmt.Fprintf(os.Stderr, "previous session saved; resume with: fisk-ai run --resume %s\n", prevID)
}

// lineToolCallText picks the tool-call command shown in the line UI, sharing the
// TUI's elision via tui.ToolCallText: the full display when it fits stderr's width,
// otherwise the pre-elided short. When the width cannot be measured (stderr is not a
// terminal, so the trace is piped or redirected) the full form is used, since there
// is no row to overflow.
func lineToolCallText(display, short string) string {
	width, _, err := term.GetSize(int(os.Stderr.Fd()))
	if err != nil || width <= 0 {
		return display
	}

	return tui.ToolCallText(display, short, width)
}
