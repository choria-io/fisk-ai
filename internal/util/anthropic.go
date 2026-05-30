//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// toolSearchThreshold is the number of tools at or above which loading is
// deferred and the tool search tool is added. Anthropic recommends the tool
// search tool once a request carries 10+ tools, where it starts to save context
// and reduce confusion; below that the tools are cheap enough to send in full.
const toolSearchThreshold = 10

// AnthropicTool converts a Tool into an Anthropic Messages API tool definition.
//
// When deferLoading is set the tool is kept out of the initial request and
// surfaced to the model only when the tool search tool returns it; this keeps a
// large command tree from bloating the request. Deferred tools are unreachable
// unless AnthropicToolSearchTool also accompanies them, so deferral is only used
// for tool sets large enough to warrant it (see AnthropicToolParams).
//
// Tools are intentionally not marked Strict. Strict mode compiles every strict
// tool's schema into a grammar and caps the combined optional parameters across
// all of them at 24, which a broad command tree exceeds. The input schema is
// still always sent and still drives the model's arguments; only the
// grammar-enforced conformance guarantee is given up.
func AnthropicTool(t *Tool, deferLoading bool) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Type:         anthropic.ToolTypeCustom,
		Name:         t.Name(),
		Description:  anthropic.String(t.ModelDescription()),
		DeferLoading: anthropic.Bool(deferLoading),
		InputSchema:  anthropicInputSchema(t.InputSchema()),
	}}
}

// AnthropicTools converts a slice of Tools into Anthropic tool definitions,
// applying deferLoading uniformly to every tool.
func AnthropicTools(tools []*Tool, deferLoading bool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		out[i] = AnthropicTool(t, deferLoading)
	}

	return out
}

// AnthropicToolParams builds the tool list for a request from local tools only.
// It is BuildToolParams with no remote or built-in tools; see BuildToolParams for
// how the presentation is chosen.
func AnthropicToolParams(tools []*Tool) []anthropic.ToolUnionParam {
	return BuildToolParams(tools, nil, 0)
}

// BuildToolParams builds the tool list for a request from local and imported
// remote tools, choosing how they are presented based on the combined count. At
// or above toolSearchThreshold the tools are deferred and the tool search tool is
// appended so the model can discover them on demand; below it every tool is sent
// directly (defer:false) and no tool search tool is needed. The threshold is
// applied to the combined set, so a small local set plus enough remote tools
// still switches to deferred discovery.
//
// builtins is the number of built-in tools (human-in-the-loop, memory) the caller
// appends separately after this returns. Those tools are never deferred, but they
// still occupy the model's context, so they count toward the threshold: Anthropic
// recommends tool search once ten or more tools are available, counting every tool
// the model can see. Passing their count here lets a set that is just under the
// threshold on its command tools alone tip into deferred discovery once the
// built-ins are added, rather than silently sending an oversized direct set.
//
// A local tool carrying the ai:no_defer tag is always sent directly even when the
// set is deferred, keeping the handful of tools the model needs on most requests
// immediately available; remote tools carry no tags and so are always eligible
// for deferral. The tool search tool is appended only when at least one tool is
// actually deferred for it to discover.
func BuildToolParams(tools []*Tool, remote []*RemoteTool, builtins int) []anthropic.ToolUnionParam {
	deferLoading := len(tools)+len(remote)+builtins >= toolSearchThreshold

	out := make([]anthropic.ToolUnionParam, 0, len(tools)+len(remote)+1)
	anyDeferred := false
	for _, t := range tools {
		toolDefer := deferLoading && !slices.Contains(t.Tags(), noDeferTag)
		if toolDefer {
			anyDeferred = true
		}
		out = append(out, AnthropicTool(t, toolDefer))
	}

	for _, r := range remote {
		if deferLoading {
			anyDeferred = true
		}
		out = append(out, r.ToolParam(deferLoading))
	}

	if anyDeferred {
		out = append(out, AnthropicToolSearchTool())
	}

	return out
}

// AnthropicToolSearchTool returns the BM25 tool search server tool. It is never
// deferred, so it is always present in the request and lets the model search the
// deferred custom tools by name and description and pull in the ones it needs.
func AnthropicToolSearchTool() anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfToolSearchToolBm25_20251119: &anthropic.ToolSearchToolBm25_20251119Param{
		Type: anthropic.ToolSearchToolBm25_20251119TypeToolSearchToolBm25,
	}}
}

// ExecuteToolUse runs the command behind a Tool for a model tool_use block and
// returns the matching tool_result content block.
//
// Execution failures (a missing binary, a canceled context, or arguments that
// cannot be turned into a command line) become a tool_result with is_error set,
// so the model learns the call failed. A command that runs but exits non-zero is
// a successful tool_result whose JSON body carries the exit code and output for
// the model to reason about.
func ExecuteToolUse(ctx context.Context, t *Tool, use anthropic.ToolUseBlock) anthropic.ContentBlockParamUnion {
	result, err := t.Execute(ctx, use.Input)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, err.Error(), true)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, fmt.Sprintf("marshaling tool result: %v", err), true)
	}

	return anthropic.NewToolResultBlock(use.ID, string(data), false)
}

// LLMRequestSummary renders a one-line summary of an outgoing model request for
// tracing: a short preview of the latest turn being sent, the number of messages
// in the conversation, and its serialized size. The size grows each turn, so the
// summary gives a sense of how large the context has become.
func LLMRequestSummary(messages []anthropic.MessageParam) string {
	return fmt.Sprintf("%s (msgs=%d, %s)", latestTurnPreview(messages), len(messages), humanBytes(messagesSize(messages)))
}

// latestTurnPreview summarizes the most recent message: a short quote of its
// first text block, or a count of the tool results it carries.
func latestTurnPreview(messages []anthropic.MessageParam) string {
	if len(messages) == 0 {
		return "(empty)"
	}

	last := messages[len(messages)-1]

	var text string
	var toolResults int
	for _, block := range last.Content {
		switch {
		case block.OfText != nil && text == "":
			text = block.OfText.Text
		case block.OfToolResult != nil:
			toolResults++
		}
	}

	switch {
	case text != "":
		return fmt.Sprintf("%q", truncateText(text, 60))
	case toolResults == 1:
		return "1 tool result"
	case toolResults > 1:
		return fmt.Sprintf("%d tool results", toolResults)
	default:
		return "(no text)"
	}
}

// messagesSize is the serialized byte size of the conversation, the part of the
// request that grows each turn.
func messagesSize(messages []anthropic.MessageParam) int {
	data, err := json.Marshal(messages)
	if err != nil {
		return 0
	}

	return len(data)
}

// truncateText collapses newlines and shortens s to at most maxRunes runes,
// appending an ellipsis when it had to cut.
func truncateText(s string, maxRunes int) string {
	s = strings.ReplaceAll(s, "\n", " ")

	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}

	return string(runes[:maxRunes]) + "…"
}

// humanBytes renders a byte count as B, KB or MB.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// anthropicInputSchema turns a fisk restricted JSON schema into the Anthropic
// tool input schema. The schema's properties and required list map to dedicated
// fields; every other key (notably additionalProperties, which strict mode
// requires to be false) is forwarded verbatim. fisk's restricted schema already
// targets the Anthropic strict subset, so no further sanitizing is done here.
// The schema type is always object and is filled in by the SDK.
//
// Optional parameters (those not in the required list) have "(optional)" appended
// to their description. JSON schema marks optionality only by absence from the
// required list, a signal models reliably under-weight: they tend to treat a
// lone, meaningful-looking parameter as mandatory and ask the user for it rather
// than relying on the command's own default. The explicit description text states
// optionality in the way models respond to most.
func anthropicInputSchema(schema map[string]any) anthropic.ToolInputSchemaParam {
	required := schemaRequired(schema["required"])

	out := anthropic.ToolInputSchemaParam{
		Properties: annotateOptional(schema["properties"], required),
		Required:   required,
	}

	var extra map[string]any
	for k, v := range schema {
		switch k {
		case "type", "properties", "required":
			// type is fixed to object by the SDK; properties and required are
			// mapped to their own fields above.
		default:
			if extra == nil {
				extra = make(map[string]any)
			}
			extra[k] = v
		}
	}
	out.ExtraFields = extra

	return out
}

// annotateOptional returns a copy of the JSON schema properties map in which the
// description of every property not in required has "(optional)" appended. It
// copies rather than mutates: properties is the tool's shared schema, reused on
// every request, so an in-place edit would append the marker again each call.
//
// A property with no description gets "Optional." A non-map properties value, or
// a property whose value is not a map, is returned unchanged since there is no
// description to annotate.
func annotateOptional(properties any, required []string) any {
	props, ok := properties.(map[string]any)
	if !ok {
		return properties
	}

	out := make(map[string]any, len(props))
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok || slices.Contains(required, name) {
			out[name] = raw
			continue
		}

		// Shallow copy is enough: only the description key is changed, and it
		// holds a string.
		clone := make(map[string]any, len(prop)+1)
		for k, v := range prop {
			clone[k] = v
		}

		desc, _ := clone["description"].(string)
		switch desc {
		case "":
			clone["description"] = "Optional."
		default:
			clone["description"] = desc + " (optional)"
		}

		out[name] = clone
	}

	return out
}

// schemaRequired extracts the JSON schema "required" list, tolerating both the
// []string form and the []any form a schema decoded from JSON carries.
func schemaRequired(v any) []string {
	switch r := v.(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, item := range r {
			s, ok := item.(string)
			if !ok {
				continue
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}
