//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package fisk

import (
	"github.com/anthropics/anthropic-sdk-go"
	"github.com/choria-io/fisk-ai/internal/util"

	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// AnthropicTool converts a FiskCommandTool into an Anthropic Messages API tool definition.
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
func AnthropicTool(t *FiskCommandTool, deferLoading bool) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Type:         anthropic.ToolTypeCustom,
		Name:         t.Name(),
		Description:  anthropic.String(t.ModelDescription()),
		DeferLoading: anthropic.Bool(deferLoading),
		InputSchema:  toolkit.AnthropicInputSchema(t.InputSchema()),
	}}
}

// AnthropicTools converts a slice of Tools into Anthropic tool definitions,
// applying deferLoading uniformly to every tool.
func AnthropicTools(tools []*FiskCommandTool, deferLoading bool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, len(tools))
	for i, t := range tools {
		out[i] = AnthropicTool(t, deferLoading)
	}

	return out
}

// AnthropicToolParams builds the tool list for a request from local tools only.
// It is BuildToolParams with no remote or built-in tools; see BuildToolParams for
// how the presentation is chosen.
func AnthropicToolParams(tools []*FiskCommandTool) []anthropic.ToolUnionParam {
	deferrable := make([]toolkit.Tool, len(tools))
	for i, t := range tools {
		deferrable[i] = t
	}

	return util.BuildToolParams(deferrable, 0)
}
