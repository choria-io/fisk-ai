//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// userText builds a user message carrying a single text block.
func userText(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: text}}}}
}

var _ = Describe("LLMRequestSummary", func() {
	It("Should preview a user text turn with message count and size", func() {
		messages := []llm.Message{userText("What is the biggest stream")}

		got := LLMRequestSummary(messages)
		Expect(got).To(ContainSubstring(`"What is the biggest stream"`))
		Expect(got).To(ContainSubstring("msgs=1"))
	})

	It("Should truncate a long preview to a bounded length", func() {
		messages := []llm.Message{userText(strings.Repeat("a", 200))}

		got := LLMRequestSummary(messages)
		Expect(got).To(ContainSubstring("…"))
		Expect(len(got)).To(BeNumerically("<", 120))
	})

	It("Should summarize a turn carrying tool results", func() {
		messages := []llm.Message{
			userText("q"),
			{Role: llm.RoleUser, Content: []llm.ContentBlock{
				{ToolResult: &llm.ToolResultBlock{ToolUseID: "tu_1", Content: "out"}},
				{ToolResult: &llm.ToolResultBlock{ToolUseID: "tu_2", Content: "out"}},
			}},
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
// logic without a concrete tool kind: its Definition honors the requested deferral,
// like a remote tool, so the threshold and tool-search behavior can be tested over
// any number of tools. Its execution is never invoked here.
type fakeTool struct{ name string }

func (f fakeTool) Name() string              { return f.name }
func (fakeTool) Description() string         { return "" }
func (fakeTool) InputSchema() map[string]any { return nil }

func (f fakeTool) Definition(deferLoading bool) llm.ToolDef {
	return llm.ToolDef{Name: f.name, DeferLoading: deferLoading}
}

func (fakeTool) ExecuteUse(context.Context, llm.ToolUseBlock, toolkit.ExecDeps) llm.ToolResultBlock {
	return llm.ToolResultBlock{}
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
	It("Should send a small set directly without requesting tool search", func() {
		defs, toolSearch := BuildToolParams(fakeTools(5), 0, true)
		Expect(defs).To(HaveLen(5))
		Expect(toolSearch).To(BeFalse())
		for _, d := range defs {
			Expect(d.DeferLoading).To(BeFalse())
		}
	})

	It("Should defer and request tool search when the set crosses the threshold", func() {
		defs, toolSearch := BuildToolParams(fakeTools(13), 0, true)

		Expect(defs).To(HaveLen(13))
		Expect(toolSearch).To(BeTrue())
		deferred := 0
		for _, d := range defs {
			if d.DeferLoading {
				deferred++
			}
		}
		Expect(deferred).To(Equal(13))
	})

	It("Should let the built-in count tip a just-under set into deferred discovery", func() {
		// Eight tools alone stay under the threshold and load directly.
		direct, toolSearch := BuildToolParams(fakeTools(8), 0, true)
		Expect(toolSearch).To(BeFalse())
		for _, d := range direct {
			Expect(d.DeferLoading).To(BeFalse())
		}

		// Counting the built-ins the caller appends separately crosses the
		// threshold, so the tools defer and tool search is requested. The built-ins
		// themselves are not part of this call and are never deferred.
		withBuiltins, toolSearch := BuildToolParams(fakeTools(8), 3, true)
		Expect(toolSearch).To(BeTrue())
		deferred := 0
		for _, d := range withBuiltins {
			if d.DeferLoading {
				deferred++
			}
		}
		Expect(deferred).To(Equal(8))
	})

	It("Should send every tool directly when tool search is not allowed, even above the threshold", func() {
		defs, toolSearch := BuildToolParams(fakeTools(13), 0, false)

		Expect(defs).To(HaveLen(13))
		Expect(toolSearch).To(BeFalse())
		for _, d := range defs {
			Expect(d.DeferLoading).To(BeFalse())
		}
	})
})
