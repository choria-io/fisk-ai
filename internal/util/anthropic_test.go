//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/toolkit"
)

var _ = Describe("AnthropicToolSearchTool", func() {
	It("Should be a bm25 tool search tool that is neither strict nor deferred", func() {
		param := AnthropicToolSearchTool()
		Expect(param.OfToolSearchToolBm25_20251119).NotTo(BeNil())

		data, err := json.Marshal(param)
		Expect(err).NotTo(HaveOccurred())

		got := map[string]any{}
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		Expect(got["type"]).To(Equal("tool_search_tool_bm25"))
		// Not strict: strict mode would compile this server tool's built-in
		// schema, whose integer bounds strict mode rejects.
		Expect(got).NotTo(HaveKey("strict"))
		// Not deferred: it must always be present so the model can reach the
		// deferred custom tools.
		Expect(got).NotTo(HaveKey("defer_loading"))
	})
})

var _ = Describe("LLMRequestSummary", func() {
	It("Should preview a user text turn with message count and size", func() {
		messages := []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("What is the biggest stream")),
		}

		got := LLMRequestSummary(messages)
		Expect(got).To(ContainSubstring(`"What is the biggest stream"`))
		Expect(got).To(ContainSubstring("msgs=1"))
	})

	It("Should truncate a long preview to a bounded length", func() {
		long := strings.Repeat("a", 200)
		messages := []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(long)),
		}

		got := LLMRequestSummary(messages)
		Expect(got).To(ContainSubstring("…"))
		Expect(len(got)).To(BeNumerically("<", 120))
	})

	It("Should summarize a turn carrying tool results", func() {
		messages := []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("q")),
			anthropic.NewUserMessage(
				anthropic.NewToolResultBlock("tu_1", "out", false),
				anthropic.NewToolResultBlock("tu_2", "out", false),
			),
		}

		got := LLMRequestSummary(messages)
		Expect(got).To(ContainSubstring("2 tool results"))
		Expect(got).To(ContainSubstring("msgs=2"))
	})
})

var _ = Describe("humanBytes", func() {
	It("Should render bytes, kilobytes and megabytes", func() {
		Expect(humanBytes(512)).To(Equal("512 B"))
		Expect(humanBytes(2048)).To(Equal("2.0 KB"))
		Expect(humanBytes(3 * 1024 * 1024)).To(Equal("3.0 MB"))
	})
})

var _ = Describe("truncateText", func() {
	It("Should collapse newlines and keep short text intact", func() {
		Expect(truncateText("a\nb", 10)).To(Equal("a b"))
	})

	It("Should cut overlong text on a rune boundary with an ellipsis", func() {
		Expect(truncateText("abcdef", 3)).To(Equal("abc…"))
	})
})

// fakeTool is a minimal toolkit.Tool for exercising BuildToolParams' presentation
// logic without a concrete tool kind: its ToolParam honors the requested deferral,
// like a remote tool, so the threshold and tool-search behavior can be tested over
// any number of tools. Its execution is never invoked here.
type fakeTool struct{ name string }

func (f fakeTool) Name() string              { return f.name }
func (fakeTool) Description() string         { return "" }
func (fakeTool) InputSchema() map[string]any { return nil }

func (f fakeTool) ToolParam(deferLoading bool) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Type:         anthropic.ToolTypeCustom,
		Name:         f.name,
		DeferLoading: anthropic.Bool(deferLoading),
	}}
}

func (fakeTool) ExecuteUse(context.Context, anthropic.ToolUseBlock, toolkit.ExecDeps) anthropic.ContentBlockParamUnion {
	return anthropic.ContentBlockParamUnion{}
}

// fakeTools builds n fakeTools named t0..tN-1.
func fakeTools(n int) []toolkit.Tool {
	out := make([]toolkit.Tool, n)
	for i := range out {
		out[i] = fakeTool{name: fmt.Sprintf("t%d", i)}
	}

	return out
}

var _ = Describe("BuildToolParams", func() {
	It("Should send a small set directly with no tool search tool", func() {
		params := BuildToolParams(fakeTools(5), 0)
		Expect(params).To(HaveLen(5))
		for _, p := range params {
			Expect(p.OfTool).NotTo(BeNil())
			Expect(p.OfTool.DeferLoading.Value).To(BeFalse())
		}
	})

	It("Should defer and add the tool search tool when the set crosses the threshold", func() {
		params := BuildToolParams(fakeTools(13), 0)

		var searchTools, deferred int
		for _, p := range params {
			if p.OfToolSearchToolBm25_20251119 != nil {
				searchTools++
				continue
			}
			if p.OfTool.DeferLoading.Value {
				deferred++
			}
		}
		Expect(searchTools).To(Equal(1))
		Expect(deferred).To(Equal(13))
	})

	It("Should let the built-in count tip a just-under set into deferred discovery", func() {
		// Eight tools alone stay under the threshold and load directly.
		direct := BuildToolParams(fakeTools(8), 0)
		for _, p := range direct {
			Expect(p.OfToolSearchToolBm25_20251119).To(BeNil())
			Expect(p.OfTool.DeferLoading.Value).To(BeFalse())
		}

		// Counting the built-ins the caller appends separately crosses the
		// threshold, so the tools defer and the tool search tool appears. The
		// built-ins themselves are not part of this call and are never deferred.
		withBuiltins := BuildToolParams(fakeTools(8), 3)
		var searchTools, deferred int
		for _, p := range withBuiltins {
			if p.OfToolSearchToolBm25_20251119 != nil {
				searchTools++
				continue
			}
			if p.OfTool.DeferLoading.Value {
				deferred++
			}
		}
		Expect(searchTools).To(Equal(1))
		Expect(deferred).To(Equal(8))
	})
})
