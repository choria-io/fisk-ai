//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	"golang.org/x/term"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/tui"
	"github.com/choria-io/fisk-ai/internal/util"
)

// printResumeTranscript replays the conversation so far so a resumed run starts
// with context rather than a blank screen. It is written to stderr, matching how
// this narration first appeared during the live run (all of it was intermediate,
// never the final answer) and keeping the resumed run's eventual answer clean on
// stdout for piping.
func printResumeTranscript(w io.Writer, rs *runstate.RunState, byName map[string]*fisk.FiskCommandTool, noColor bool) {
	fmt.Fprintf(w, "\n--- resuming %q, %d LLM call(s) so far ---\n", rs.RunID, rs.Counters.LlmCalls)

	for i, msg := range rs.Messages {
		switch {
		case i == 0:
			// The first message is the original prompt.
			prompt := messageText(msg)
			if prompt != "" {
				fmt.Fprintf(w, "\n> %s\n", prompt)
			}
		case msg.Role == llm.RoleAssistant:
			printAssistantTurn(w, msg, byName, noColor)
		case msg.Role == llm.RoleUser:
			// An interior user message is a chat follow-up: show its text as a prompt.
			// Its tool_result blocks (if it also carries any) are the prior turn's results,
			// not shown in this narration view, matching the live run's stderr output.
			if text := messageText(msg); text != "" {
				fmt.Fprintf(w, "\n> %s\n", text)
			}
		}
	}

	// The in-flight turn is held separately from Messages; show it too so its
	// tool calls (about to be resumed) are visible.
	if rs.Pending != nil {
		printAssistantTurn(w, rs.Pending.Assistant, byName, noColor)
	}

	fmt.Fprintln(w, "\n--- continuing ---")
}

// printAssistantTurn renders one assistant turn's narration and the tools it
// called, mirroring the live run's stderr output.
func printAssistantTurn(w io.Writer, msg llm.Message, byName map[string]*fisk.FiskCommandTool, noColor bool) {
	text := messageText(msg)
	if text != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, util.RenderMarkdownTo(text, os.Stderr, noColor))
	}

	for _, block := range msg.Content {
		if block.ToolUse != nil {
			fmt.Fprintf(w, "-> %s\n", toolCallDisplay(byName, block.ToolUse))
		}
	}
}

// toolCallDisplay renders a replayed tool call the same way the live run does: the
// full resolved command line when the tool is known, so the resume transcript
// reads identically to interactive output. It falls back to the raw name and JSON
// arguments for a tool not in the registry (removed, built-in or remote).
func toolCallDisplay(byName map[string]*fisk.FiskCommandTool, use *llm.ToolUseBlock) string {
	input := use.Input
	if len(input) == 0 {
		input = []byte("<unrenderable input>")
	}

	t, ok := byName[use.Name]
	if ok && json.Valid(input) {
		return t.TraceLineShort(input)
	}

	return fmt.Sprintf("%s %s", use.Name, string(input))
}

// dumpTranscript writes the conversation for inspection: the prompt, thinking,
// assistant narration and tool calls with their inputs. Tool result output is
// verbose and is included only when toolOutput is set.
func dumpTranscript(w io.Writer, rs *runstate.RunState, noColor, toolOutput bool) {
	for i, msg := range rs.Messages {
		switch {
		case i == 0:
			fmt.Fprintf(w, "> %s\n", messageText(msg))
		case msg.Role == llm.RoleAssistant:
			dumpAssistant(w, msg, noColor)
		default:
			// An interior user message carries a chat follow-up's text and/or the prior
			// turn's tool results. Show the follow-up as a prompt; the results follow when
			// tool output is being shown.
			if text := messageText(msg); text != "" {
				fmt.Fprintf(w, "\n> %s\n", text)
			}
			if toolOutput {
				dumpToolResults(w, toolResults(msg.Content))
			}
		}
	}

	if rs.Pending != nil {
		dumpAssistant(w, rs.Pending.Assistant, noColor)
		if toolOutput {
			dumpToolResults(w, rs.Pending.Results)
		}
	}
}

func dumpAssistant(w io.Writer, msg llm.Message, noColor bool) {
	for _, block := range msg.Content {
		if block.Thinking != nil && block.Thinking.Text != "" {
			fmt.Fprintf(w, "\n[thinking]\n%s\n", util.SanitizeForDisplay(block.Thinking.Text))
		}
	}

	text := messageText(msg)
	if text != "" {
		fmt.Fprintln(w)
		fmt.Fprintln(w, util.RenderMarkdownTo(text, os.Stdout, noColor))
	}

	for _, block := range msg.Content {
		if block.ToolUse == nil {
			continue
		}
		full, short := toolCallDump(block.ToolUse)
		fmt.Fprintf(w, "-> %s\n", fitToolCall(full, short))
	}
}

// toolCallDumpPrefixWidth is the visible width of the "-> " glyph a dumped tool call
// is prefixed with, and toolCallDumpFitMargin keeps one cell in hand so a line that
// lands on the last column does not wrap; both mirror the full-screen viewer so the
// line dump elides at the same point.
const (
	toolCallDumpPrefixWidth = len("-> ")
	toolCallDumpFitMargin   = 1
)

// fitToolCall picks the displayed form of a dumped tool call for the line view: the
// full arguments when they fit the terminal width, otherwise the pre-elided short
// form, mirroring how the full-screen viewer chooses between the two. Off a terminal
// (piped or redirected) the width is unknown, so the full form is always shown, the
// same choice renderMarkdown makes when it declines to reflow piped output.
func fitToolCall(full, short string) string {
	width, ok := stdoutWidth()
	if !ok {
		return full
	}

	return elideToolCall(full, short, width)
}

// elideToolCall chooses between a dumped tool call's full and pre-elided short forms
// for a given content width, the width-aware core fitToolCall drives once it knows
// the terminal size. The "-> " prefix and a one-cell margin are reserved so the
// chosen line does not wrap, and width is measured in runes on the sanitized text so
// the comparison matches what a terminal renders.
func elideToolCall(full, short string, width int) string {
	avail := width - toolCallDumpPrefixWidth - toolCallDumpFitMargin
	if avail > 0 && utf8.RuneCountInString(util.SanitizeForDisplay(full)) <= avail {
		return full
	}

	return short
}

// stdoutWidth reports the terminal's column count, and false when stdout is not a
// terminal or its size cannot be read, so the caller shows the unabridged form
// rather than eliding against an unknown width.
func stdoutWidth() (int, bool) {
	fd := int(os.Stdout.Fd())
	if !term.IsTerminal(fd) {
		return 0, false
	}

	width, _, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		return 0, false
	}

	return width, true
}

func dumpToolResults(w io.Writer, results []llm.ToolResultBlock) {
	for _, res := range results {
		tag := ""
		if res.IsError {
			tag = " (error)"
		}
		fmt.Fprintf(w, "<- [%s]%s %s\n", res.ToolUseID, tag, util.SanitizeForDisplay(res.Content))
	}
}

// transcriptLines flattens a saved session into the structured lines the
// full-screen viewer renders. It mirrors dumpTranscript's walk (prompt, then each
// assistant turn's thinking, narration and tool calls, then optionally tool
// results) but yields tui.Line values the viewer sanitizes and styles, rather than
// writing formatted text. Tool calls are shown by name and raw arguments, as they
// are stored, since the tool registry is not loaded for a read-only view.
func transcriptLines(rs *runstate.RunState, toolOutput bool) []tui.Line {
	// Pair each tool call with its result by tool_use id so a turn's calls and their
	// output interleave (call, result, call, result), matching how a live run shows
	// them, rather than listing every call and then every result. A call and its
	// result live in adjacent messages, so results are indexed across the whole
	// conversation; an unanswered call (a suspended partial turn) simply has no entry.
	results := map[string]tui.Line{}
	if toolOutput {
		for _, msg := range rs.Messages {
			collectToolResults(results, toolResults(msg.Content))
		}
		if rs.Pending != nil {
			collectToolResults(results, rs.Pending.Results)
		}
	}

	var out []tui.Line

	for i, msg := range rs.Messages {
		switch {
		case i == 0:
			out = appendLine(out, tui.LinePrompt, messageText(msg))
		case msg.Role == llm.RoleAssistant:
			out = append(out, assistantLines(msg, results)...)
		case msg.Role == llm.RoleUser:
			// An interior user message is a chat follow-up: show its text as a prompt line.
			// Any tool_result blocks it also carries are the prior turn's results, already
			// paired with the assistant's calls above through the results map, so only the
			// follow-up text is emitted here.
			out = appendLine(out, tui.LinePrompt, messageText(msg))
		}
	}

	if rs.Pending != nil {
		out = append(out, assistantLines(rs.Pending.Assistant, results)...)
	}

	return out
}

// assistantLines renders one assistant turn as viewer lines: its thinking blocks,
// its prose, then each tool call followed immediately by its result, looked up by
// tool_use id in results, so a call and its output stay together as they do live. A
// call with no matching result -- an unanswered tool in a suspended turn -- shows on
// its own. results is empty when tool output is not being shown, so only calls emit.
func assistantLines(msg llm.Message, results map[string]tui.Line) []tui.Line {
	var out []tui.Line

	for _, block := range msg.Content {
		if block.Thinking != nil && block.Thinking.Text != "" {
			out = appendLine(out, tui.LineThinking, block.Thinking.Text)
		}
	}

	out = appendLine(out, tui.LineNarration, messageText(msg))

	for _, block := range msg.Content {
		if block.ToolUse == nil {
			continue
		}
		full, short := toolCallDump(block.ToolUse)
		out = append(out, tui.Line{Kind: tui.LineToolCall, Text: full, Short: short})
		if res, ok := results[block.ToolUse.ID]; ok {
			out = append(out, res)
		}
	}

	return out
}

// collectToolResults indexes the tool results in a message's content by tool_use id,
// as the viewer lines they will render to, so each call can later be paired with its
// result. The internal tool_use id that the text dump carries is not shown: it is
// debug noise in the operator's viewport and does not appear on live results.
func collectToolResults(into map[string]tui.Line, results []llm.ToolResultBlock) {
	for _, res := range results {
		into[res.ToolUseID] = toolResultLine(res.Content, res.IsError)
	}
}

// toolResultLine builds the viewer line for a tool's output, shared by a live run
// and a replayed transcript so the two read the same. A failed tool becomes a
// LineToolError, which stays visible even when tool output is folded and carries
// an "(error)" marker so the failure reads on a monochrome terminal where the
// color is lost. A successful tool's body is unwrapped from its CommandResult
// envelope to the plain output an operator wants to read; a silent success is
// shown as "(no output)" so an executed tool always leaves a visible result
// rather than a blank.
func toolResultLine(output string, isError bool) tui.Line {
	if isError {
		if output == "" {
			return tui.Line{Kind: tui.LineToolError, Text: "(error)"}
		}
		return tui.Line{Kind: tui.LineToolError, Text: "(error) " + output}
	}

	if unwrapped, ok := commandResultOutput(output); ok {
		output = unwrapped
	}

	if output == "" {
		return tui.Line{Kind: tui.LineToolResult, Text: "(no output)"}
	}
	return tui.Line{Kind: tui.LineToolResult, Text: output}
}

// commandResultOutput unwraps a tool's JSON result envelope to the plain output
// worth showing. A local command tool returns its result as a util.CommandResult
// JSON body (command, exit code, combined output) and a remote tool returns the
// same shape carrying just the output, so the raw viewport line would otherwise
// bury the actual output in envelope noise. When the body is a CommandResult it is
// unwrapped to its Output, keeping an "(exit N)" marker when the command exited
// non-zero so a failure that still ran is not hidden. A body that is not a
// recognizable CommandResult (a builtin's plain text, some other tool's own JSON)
// is left unchanged, reported by the false return.
func commandResultOutput(output string) (string, bool) {
	trimmed := strings.TrimSpace(output)
	if !strings.HasPrefix(trimmed, "{") {
		return "", false
	}

	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.DisallowUnknownFields()

	var res toolkit.CommandResult
	err := dec.Decode(&res)
	if err != nil {
		return "", false
	}

	if res.ExitCode == 0 {
		return res.Output, true
	}
	if res.Output == "" {
		return fmt.Sprintf("(exit %d)", res.ExitCode), true
	}
	return fmt.Sprintf("(exit %d) %s", res.ExitCode, res.Output), true
}

// toolCallDump renders a stored tool call as its name and JSON arguments, the
// registry-free rendering used by a read-only view (the session viewer and the TUI
// resume) that cannot resolve a command line. It returns the full arguments and a
// form with long string values elided; a width-aware viewer shows the full form when
// it fits a row and falls back to the short one otherwise, matching what the live
// view does for a resolved command line.
func toolCallDump(use *llm.ToolUseBlock) (full, short string) {
	input := use.Input
	if len(input) == 0 {
		s := fmt.Sprintf("%s %s", use.Name, "<unrenderable input>")
		return s, s
	}

	fullArgs, shortArgs := dumpJSONArgs(input)

	return fmt.Sprintf("%s %s", use.Name, string(fullArgs)), fmt.Sprintf("%s %s", use.Name, string(shortArgs))
}

// dumpJSONArgs returns a tool call's raw JSON arguments in two forms: the full
// arguments, and a copy with the middle of every long string value elided (keys,
// numbers, booleans and structure left untouched) so a chatty argument stays readable
// in a view that renders it without the tool registry. Both come from a single decode
// so they share key order and number formatting and differ only in the elision.
// Numbers decode as json.Number so re-marshaling does not reformat them or lose
// integer precision, and the input is returned unchanged for both when it cannot be
// parsed.
func dumpJSONArgs(input []byte) (full, short []byte) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()

	var v any
	if err := dec.Decode(&v); err != nil {
		return input, input
	}

	full, err := json.Marshal(v)
	if err != nil {
		full = input
	}

	// elideJSONValue mutates v in place, so the full form above is marshaled first.
	short, err = json.Marshal(elideJSONValue(v))
	if err != nil {
		short = full
	}

	return full, short
}

// elideJSONValue walks a decoded JSON value and elides the middle of every string it
// contains, recursing into arrays and objects. Non-string scalars are returned as-is.
func elideJSONValue(v any) any {
	switch t := v.(type) {
	case string:
		return util.ElideMiddle(t)
	case []any:
		for i, e := range t {
			t[i] = elideJSONValue(e)
		}
		return t
	case map[string]any:
		for k, e := range t {
			t[k] = elideJSONValue(e)
		}
		return t
	default:
		return v
	}
}

// appendLine appends a non-empty line of the given kind, dropping empties so the
// viewer shows no blank entries.
func appendLine(out []tui.Line, kind tui.LineKind, text string) []tui.Line {
	if text == "" {
		return out
	}

	return append(out, tui.Line{Kind: kind, Text: text})
}

// messageText concatenates the text blocks of a message.
func messageText(msg llm.Message) string {
	var text strings.Builder
	for _, block := range msg.Content {
		if block.Text != nil {
			text.WriteString(block.Text.Text)
		}
	}

	return text.String()
}

// toolResults extracts the tool_result blocks from a message's content, in order.
func toolResults(content []llm.ContentBlock) []llm.ToolResultBlock {
	var out []llm.ToolResultBlock
	for _, block := range content {
		if block.ToolResult != nil {
			out = append(out, *block.ToolResult)
		}
	}
	return out
}
