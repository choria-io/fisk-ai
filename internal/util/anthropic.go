//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// ToolSearchThreshold is the number of tools at or above which loading is
// deferred and the tool search tool is added. Anthropic recommends the tool
// search tool once a request carries 10+ tools, where it starts to save context
// and reduce confusion; below that the tools are cheap enough to send in full.
const ToolSearchThreshold = 10

// BuildToolParams builds the tool list for a request from local and imported
// remote tools, choosing how they are presented based on the combined count. At
// or above ToolSearchThreshold the tools are deferred and the tool search tool is
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
// Each tool decides whether it honors deferral through its own ToolParam: a local
// tool tagged ai:no_defer is always sent directly even when the set is deferred,
// while a remote tool always defers when asked. The tool search tool is appended
// only when at least one tool actually deferred for it to discover, detected from
// the built definitions rather than assumed from the request to defer.
func BuildToolParams(tools []toolkit.Tool, builtins int) []anthropic.ToolUnionParam {
	deferLoading := len(tools)+builtins >= ToolSearchThreshold

	out := make([]anthropic.ToolUnionParam, 0, len(tools)+1)
	anyDeferred := false
	for _, t := range tools {
		p := t.ToolParam(deferLoading)
		if p.OfTool != nil && p.OfTool.DeferLoading.Value {
			anyDeferred = true
		}
		out = append(out, p)
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
