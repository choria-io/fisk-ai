//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/util"

	"github.com/choria-io/fisk-ai/config"
)

// The built-in human-in-the-loop tool names share the ask_human_ prefix, which
// groups them and keeps them clear of a typical fisk command path so they do not
// collide with an introspected application tool.
const (
	askHumanConfirmName = "ask_human_confirm"
	askHumanSelectName  = "ask_human_select"
	askHumanInputName   = "ask_human_input"
)

// maxPromptRunes caps the length of a model-supplied string shown to the operator
// (a question, an option label, a default), so a tool call cannot flood the
// terminal with a wall of text.
const maxPromptRunes = 500

// maxSelectOptions caps how many options ask_human_select will present, so a tool
// call cannot flood the terminal with an unusable list.
const maxSelectOptions = 25

// BuiltinTool is a tool fisk-ai implements itself and runs in-process, as opposed
// to a *FiskCommandTool, which runs a command of the introspected fisk application. Built-in
// tools provide capabilities that are not part of any application, such as putting
// a question to the operator. They run with the agent's own privileges and
// environment — not the stripped, LLMFORMAT-tagged environment a command
// subprocess gets (see commandEnv) — so a handler must never read a sensitive
// variable or hand the ambient environment to a subprocess.
type BuiltinTool struct {
	name        string
	description string
	schema      map[string]any
	// handler runs the tool in-process and returns the JSON result for the model.
	// A returned error is an invocation failure surfaced to the model as an error
	// result; an outcome the model should reason about (such as a declined
	// confirmation) is a normal result carried in the returned string. The prompter
	// is the per-run path to the operator; a handler that puts a question to the
	// operator uses it and treats any error it returns as a denial.
	handler func(ctx context.Context, input json.RawMessage, prompter toolkit.Prompter) (string, error)
	// trace, when set, renders a one-line call trace from the tool's input for a
	// tool that does not render its own operator interaction (the memory tools), so
	// its call and result are shown like an application tool's. It is nil for a
	// self-rendering tool (the human-in-the-loop tools), whose trace would only
	// duplicate the prompt the tool itself puts on the screen.
	trace func(input json.RawMessage) string
}

// Name is the tool name presented to the model.
func (b *BuiltinTool) Name() string { return b.name }

// Traced reports whether this tool's call and result should be shown like an
// application tool's rather than suppressed. It is true for a tool that has no
// operator interaction of its own (the memory tools) and false for a
// self-rendering one (the human-in-the-loop tools).
func (b *BuiltinTool) Traced() bool { return b.trace != nil }

// TraceLine renders the one-line call trace for input, or "" when the tool is not
// traced.
func (b *BuiltinTool) TraceLine(input json.RawMessage) string {
	if b.trace == nil {
		return ""
	}

	return b.trace(input)
}

// Description is the tool description presented to the model.
func (b *BuiltinTool) Description() string { return b.description }

// InputSchema returns the tool's JSON-schema input definition, for a caller that
// registers the built-in outside the Anthropic tool path (the MCP server, which
// consumes the raw schema directly).
func (b *BuiltinTool) InputSchema() map[string]any { return b.schema }

// Call runs the built-in in-process and returns its JSON result string. It is the
// direct handler seam for a non-agent caller (the MCP server); the agent path uses
// ExecuteBuiltinUse. The prompter is the operator path a handler uses for a
// question; a caller with no operator (MCP) passes DefaultDenyPrompter so any
// unexpected prompt fails closed rather than hanging. A returned error is an
// invocation failure; an outcome the caller should reason about is carried in the
// string.
func (b *BuiltinTool) Call(ctx context.Context, input json.RawMessage, prompter toolkit.Prompter) (string, error) {
	return b.handler(ctx, input, prompter)
}

// ToolParam renders the built-in as an Anthropic tool definition. It is never
// deferred: built-in tools are few and always relevant, and deferring one behind
// the tool-search tool could leave it undiscovered. The deferLoading argument is
// ignored to satisfy the Tool contract; a built-in never opts into deferral.
func (b *BuiltinTool) ToolParam(_ bool) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Type:         anthropic.ToolTypeCustom,
		Name:         b.name,
		Description:  anthropic.String(b.description),
		DeferLoading: anthropic.Bool(false),
		InputSchema:  toolkit.AnthropicInputSchema(b.schema),
	}}
}

// HITLTools returns the built-in human-in-the-loop tools enabled by cfg, or nil
// when they are disabled.
func HITLTools(cfg *config.Config) []*BuiltinTool {
	if !cfg.HumanInTheLoopEnabled() {
		return nil
	}

	return []*BuiltinTool{
		askHumanConfirmTool(),
		askHumanSelectTool(),
		askHumanInputTool(),
	}
}

// HITLSystemNote returns a system-prompt note to add when the human-in-the-loop
// tools are present, or "" when there are none. The agent loop ends on a turn that
// produces only text, so a model that "asks the user" in prose silently ends the
// run instead of reaching anyone; without this the model has no way to know that
// the operator is unreachable except through these tools. The note names the tools
// actually enabled so it stays accurate as the set changes.
func HITLSystemNote(builtins []*BuiltinTool) string {
	if len(builtins) == 0 {
		return ""
	}

	names := make([]string, len(builtins))
	for i, b := range builtins {
		names[i] = b.Name()
	}

	return fmt.Sprintf("You are running as a non-interactive agent: the operator cannot see your "+
		"intermediate messages and cannot reply to them, so a question written as plain text will not "+
		"reach them and only ends the run. The only way to get an answer or a decision from the operator "+
		"is to call one of these tools: %s. Use them whenever your instructions tell you to check with the "+
		"operator: to confirm an action, to choose between options, to collect a value, or to decide "+
		"whether to continue, repeat, or stop.", strings.Join(names, ", "))
}

// A BuiltinTool is a model-facing Tool. It is not Confirmable: a built-in has no
// operator confirmation of its own (a human-in-the-loop tool does its own asking).
var _ toolkit.Tool = (*BuiltinTool)(nil)

// ExecuteUse runs the built-in for a model tool_use block and returns the matching
// tool_result content block, mirroring the command tool: a handler error becomes an
// error result; its output becomes a normal result. A human-in-the-loop built-in
// reaches the operator through deps.Prompter.
func (b *BuiltinTool) ExecuteUse(ctx context.Context, use anthropic.ToolUseBlock, deps toolkit.ExecDeps) anthropic.ContentBlockParamUnion {
	out, err := b.handler(ctx, use.Input, deps.Prompter)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, err.Error(), true)
	}

	return anthropic.NewToolResultBlock(use.ID, out, false)
}

// askHumanConfirmTool builds the ask_human_confirm confirmation tool.
func askHumanConfirmTool() *BuiltinTool {
	return &BuiltinTool{
		name: askHumanConfirmName,
		description: "Ask the human operator a yes/no question at the terminal and wait for their answer. " +
			"Use this only for a decision you should not make alone: confirming an irreversible or destructive action before you take it (deleting data, overwriting, restarting a service), or resolving a genuine ambiguity that turns on the operator's intent. " +
			"Do not use it for anything you can determine yourself, to narrate progress, or to ask permission for ordinary read-only steps. " +
			"The operator is a person at a terminal and each call interrupts them, so ask only when their answer changes what you do next. " +
			"It returns {\"confirmed\": true} only when the operator answered yes; any other outcome (a no, or no operator could be reached) returns {\"confirmed\": false} with a reason. A false result is authoritative and must not be retried.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The yes/no question to put to the operator, phrased so the consequence of yes is clear, e.g. \"Delete stream ORDERS? This cannot be undone.\"",
				},
			},
			"required": []any{"question"},
		},
		handler: askHumanConfirm,
	}
}

// confirmOutcome is the JSON result the ask_human_confirm tool returns to the model.
type confirmOutcome struct {
	// Confirmed is true only when the operator explicitly answered yes.
	Confirmed bool `json:"confirmed"`
	// Reason explains a false result that was not a plain "no" from the operator,
	// such as no interactive terminal being available.
	Reason string `json:"reason,omitempty"`
}

// askHumanConfirm is the ask_human_confirm handler. It denies by default: only an
// explicit affirmative answer yields confirmed true. A missing terminal, an
// interrupt, an EOF, or any other prompt error is reported as a normal (non-error)
// result with confirmed false, so the model treats the refusal as authoritative
// rather than as a tool failure to route around. Only malformed input is an error.
func askHumanConfirm(ctx context.Context, input json.RawMessage, prompter toolkit.Prompter) (string, error) {
	var args struct {
		Question string `json:"question"`
	}
	if err := decodeArgs(input, &args); err != nil {
		return "", fmt.Errorf("invalid %s input: %w", askHumanConfirmName, err)
	}

	question := sanitizePrompt(args.Question)
	if question == "" {
		return "", fmt.Errorf("%s requires a non-empty question", askHumanConfirmName)
	}

	if !util.StdinIsTerminal() {
		return outcomeJSON(askHumanConfirmName, confirmOutcome{Reason: util.NoTerminalReason})
	}
	if err := ctx.Err(); err != nil {
		return outcomeJSON(askHumanConfirmName, confirmOutcome{Reason: fmt.Sprintf("the run ended before the operator could answer: %v", err)})
	}

	confirmed, err := prompter.Confirm(ctx, question)
	if err != nil {
		return outcomeJSON(askHumanConfirmName, confirmOutcome{Reason: fmt.Sprintf("the operator did not confirm: %v", err)})
	}

	return outcomeJSON(askHumanConfirmName, confirmOutcome{Confirmed: confirmed})
}

// ----- ask_human_select: choose one of a list -----

// askHumanSelectTool builds the ask_human_select chooser tool.
func askHumanSelectTool() *BuiltinTool {
	return &BuiltinTool{
		name: askHumanSelectName,
		description: "Ask the human operator to choose one option from a list you provide, at the terminal, and wait for their choice. " +
			"Use this when the decision depends on the operator's intent or knowledge and you have a concrete, bounded set of options to pick among (which environment, which of several matching resources, which approach). " +
			"Do not use it for a yes/no question (use ask_human_confirm) or for anything you can determine yourself. " +
			"It returns {\"selected\": \"<the chosen option>\"}; if the operator cancels or none could be reached it returns {\"selected\": null} with a reason, which is authoritative and must not be retried.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to put to the operator, naming what they are choosing.",
				},
				"options": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": fmt.Sprintf("The options to choose from, in the order to show them. At least one, at most %d.", maxSelectOptions),
				},
			},
			"required": []any{"question", "options"},
		},
		handler: askHumanSelect,
	}
}

// selectOutcome is the JSON result the ask_human_select tool returns to the model.
type selectOutcome struct {
	// Selected is the chosen option, or null when no choice was made.
	Selected *string `json:"selected"`
	// Reason explains a null result (the operator canceled, or none was reached).
	Reason string `json:"reason,omitempty"`
}

// askHumanSelect is the ask_human_select handler. Like ask_human_confirm it makes
// no choice by default: a missing terminal, an interrupt, or an EOF returns a null
// selection with a reason rather than silently picking the first option.
func askHumanSelect(ctx context.Context, input json.RawMessage, prompter toolkit.Prompter) (string, error) {
	var args struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := decodeArgs(input, &args); err != nil {
		return "", fmt.Errorf("invalid %s input: %w", askHumanSelectName, err)
	}

	question := sanitizePrompt(args.Question)
	if question == "" {
		return "", fmt.Errorf("%s requires a non-empty question", askHumanSelectName)
	}

	options := sanitizeOptions(args.Options)
	switch {
	case len(options) == 0:
		return "", fmt.Errorf("%s requires at least one option", askHumanSelectName)
	case len(options) > maxSelectOptions:
		return "", fmt.Errorf("%s supports at most %d options, got %d", askHumanSelectName, maxSelectOptions, len(options))
	}

	if !util.StdinIsTerminal() {
		return outcomeJSON(askHumanSelectName, selectOutcome{Reason: util.NoTerminalReason})
	}
	if err := ctx.Err(); err != nil {
		return outcomeJSON(askHumanSelectName, selectOutcome{Reason: fmt.Sprintf("the run ended before the operator could choose: %v", err)})
	}

	idx, err := prompter.Select(ctx, question, options)
	if err != nil {
		return outcomeJSON(askHumanSelectName, selectOutcome{Reason: fmt.Sprintf("the operator did not choose: %v", err)})
	}
	if idx < 0 || idx >= len(options) {
		return outcomeJSON(askHumanSelectName, selectOutcome{Reason: "no option was chosen"})
	}

	return outcomeJSON(askHumanSelectName, selectOutcome{Selected: &options[idx]})
}

// askHumanInputTool builds the ask_human_input free-text tool.
func askHumanInputTool() *BuiltinTool {
	return &BuiltinTool{
		name: askHumanInputName,
		description: "Ask the human operator to type a free-text value at the terminal and wait for their answer. " +
			"Use this for a value you genuinely cannot determine yourself and that depends on the operator (a name, a path, an identifier, a short reason). " +
			"You may provide a default the operator can accept or edit, which is the preferred way to let them correct a value you drafted. " +
			"Do not use it for a yes/no question (use ask_human_confirm), for choosing among known options (use ask_human_select), or to collect a secret or password. " +
			"It returns {\"value\": \"<the text>\"} (which may be empty if the operator entered nothing); if the operator cancels or none could be reached it returns {\"value\": null} with a reason.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{
					"type":        "string",
					"description": "The question to put to the operator, naming the value you need.",
				},
				"default": map[string]any{
					"type":        "string",
					"description": "A value shown pre-filled for the operator to accept or edit.",
				},
			},
			"required": []any{"question"},
		},
		handler: askHumanInput,
	}
}

// inputOutcome is the JSON result the ask_human_input tool returns to the model.
type inputOutcome struct {
	// Value is the text the operator entered (possibly empty), or null when they
	// gave no answer.
	Value *string `json:"value"`
	// Reason explains a null result (the operator canceled, or none was reached).
	Reason string `json:"reason,omitempty"`
}

// askHumanInput is the ask_human_input handler. An empty answer is a valid value
// (a non-null empty string); only a missing terminal, an interrupt, or an EOF
// yields a null value with a reason.
func askHumanInput(ctx context.Context, input json.RawMessage, prompter toolkit.Prompter) (string, error) {
	var args struct {
		Question string `json:"question"`
		Default  string `json:"default"`
	}
	if err := decodeArgs(input, &args); err != nil {
		return "", fmt.Errorf("invalid %s input: %w", askHumanInputName, err)
	}

	question := sanitizePrompt(args.Question)
	if question == "" {
		return "", fmt.Errorf("%s requires a non-empty question", askHumanInputName)
	}

	if !util.StdinIsTerminal() {
		return outcomeJSON(askHumanInputName, inputOutcome{Reason: util.NoTerminalReason})
	}
	if err := ctx.Err(); err != nil {
		return outcomeJSON(askHumanInputName, inputOutcome{Reason: fmt.Sprintf("the run ended before the operator could answer: %v", err)})
	}

	value, err := prompter.Input(ctx, question, sanitizePrompt(args.Default))
	if err != nil {
		return outcomeJSON(askHumanInputName, inputOutcome{Reason: fmt.Sprintf("the operator did not answer: %v", err)})
	}

	return outcomeJSON(askHumanInputName, inputOutcome{Value: &value})
}

// decodeArgs unmarshals a tool input into v, treating an empty or null input as an
// empty object so a tool that takes only optional arguments still works.
func decodeArgs(input json.RawMessage, v any) error {
	if len(input) == 0 || string(input) == "null" {
		return nil
	}

	return json.Unmarshal(input, v)
}

// outcomeJSON marshals a tool outcome to its JSON result string.
func outcomeJSON(tool string, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshaling %s result: %w", tool, err)
	}

	return string(data), nil
}

// sanitizePrompt makes a model-supplied question, option, or default safe to
// print to the operator's terminal, capped at maxPromptRunes.
func sanitizePrompt(s string) string {
	return util.SanitizeForTerminal(s, maxPromptRunes)
}

// sanitizeOptions sanitizes every option label a model offers for selection, since
// each is printed to the operator's terminal.
func sanitizeOptions(options []string) []string {
	out := make([]string, len(options))
	for i, o := range options {
		out[i] = sanitizePrompt(o)
	}

	return out
}
