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
	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// marshalTool renders a tool param to its API JSON form, as the SDK would send
// it, so assertions check what the model actually receives. param.Opt fields are
// only observable after marshaling, so this is the only faithful way to test them.
func marshalTool(t *Tool, deferLoading bool) map[string]any {
	GinkgoHelper()

	data, err := json.Marshal(AnthropicTool(t, deferLoading))
	Expect(err).NotTo(HaveOccurred())

	out := map[string]any{}
	Expect(json.Unmarshal(data, &out)).To(Succeed())
	return out
}

var _ = Describe("AnthropicTool", func() {
	It("Should map name, description and deferred loading, and not be strict", func() {
		app := fisk.New("app", "an app")
		app.Command("deploy", "deploy things")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))

		got := marshalTool(tools[0], true)
		Expect(got["name"]).To(Equal("deploy"))
		Expect(got["description"]).To(Equal("deploy things"))
		Expect(got["defer_loading"]).To(BeTrue())
		Expect(got["type"]).To(Equal("custom"))
		// Strict mode is not used: its grammar compilation caps total optional
		// parameters across all tools, which a broad command tree exceeds.
		Expect(got).NotTo(HaveKey("strict"))
	})

	It("Should not set defer_loading when loading is not deferred", func() {
		app := fisk.New("app", "an app")
		app.Command("deploy", "deploy things")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))

		got := marshalTool(tools[0], false)
		// defer_loading:false is sent explicitly, telling the API to load the
		// tool immediately rather than behind tool search.
		Expect(got["defer_loading"]).To(BeFalse())
	})

	It("Should carry the restricted schema including additionalProperties and required", func() {
		app := fisk.New("app", "an app")
		cmd := app.Command("deploy", "deploy things")
		cmd.Arg("target", "where to deploy").Required().String()
		cmd.Flag("force", "force the deploy").Bool()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())
		Expect(tools).To(HaveLen(1))

		got := marshalTool(tools[0], true)

		schema, ok := got["input_schema"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(schema["type"]).To(Equal("object"))
		// additionalProperties:false is forwarded verbatim; strict mode requires it.
		Expect(schema["additionalProperties"]).To(Equal(false))

		props, ok := schema["properties"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(props).To(HaveKey("target"))
		Expect(props).To(HaveKey("force"))

		Expect(schema["required"]).To(ConsistOf("target"))

		// Optionality is marked in the description: the required parameter is
		// left as written, the optional one is annotated so the model does not
		// mistake it for mandatory.
		target, ok := props["target"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(target["description"]).To(Equal("where to deploy"))

		force, ok := props["force"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(force["description"]).To(Equal("force the deploy (optional)"))
	})

	It("Should produce an object schema even for a command with no arguments", func() {
		app := fisk.New("app", "an app")
		app.Command("noop", "does nothing")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		got := marshalTool(tools[0], true)
		schema, ok := got["input_schema"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(schema["type"]).To(Equal("object"))
	})
})

// appWithCommands builds an application exposing n distinct tools, named cmd0..cmdN-1.
func appWithCommands(n int) []*Tool {
	GinkgoHelper()

	app := fisk.New("app", "an app")
	for i := 0; i < n; i++ {
		app.Command(fmt.Sprintf("cmd%d", i), "a command")
	}

	tools, err := ApplicationTools(introspect(app))
	Expect(err).NotTo(HaveOccurred())
	Expect(tools).To(HaveLen(n))

	return tools
}

var _ = Describe("AnthropicTools", func() {
	It("Should convert every tool, preserving order", func() {
		app := fisk.New("app", "an app")
		app.Command("one", "first command")
		app.Command("two", "second command")

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		params := AnthropicTools(tools, true)
		Expect(params).To(HaveLen(2))

		got := make([]string, len(params))
		for i, p := range params {
			Expect(p.OfTool).NotTo(BeNil())
			got[i] = p.OfTool.Name
		}
		Expect(got).To(Equal([]string{"one", "two"}))
	})

	It("Should return an empty slice for no tools", func() {
		Expect(AnthropicTools(nil, true)).To(BeEmpty())
	})
})

var _ = Describe("AnthropicToolParams", func() {
	// deferred reports whether the marshaled tool param carries defer_loading:true.
	deferred := func(p anthropic.ToolUnionParam) bool {
		GinkgoHelper()

		data, err := json.Marshal(p)
		Expect(err).NotTo(HaveOccurred())

		got := map[string]any{}
		Expect(json.Unmarshal(data, &got)).To(Succeed())
		return got["defer_loading"] == true
	}

	It("Should send every tool directly without a search tool below the threshold", func() {
		params := AnthropicToolParams(appWithCommands(toolSearchThreshold - 1))

		Expect(params).To(HaveLen(toolSearchThreshold - 1))
		for _, p := range params {
			Expect(p.OfTool).NotTo(BeNil())
			Expect(deferred(p)).To(BeFalse())
		}
	})

	It("Should defer every tool and append the search tool at the threshold", func() {
		params := AnthropicToolParams(appWithCommands(toolSearchThreshold))

		// One extra entry for the appended tool search tool.
		Expect(params).To(HaveLen(toolSearchThreshold + 1))

		last := params[len(params)-1]
		Expect(last.OfToolSearchToolBm25_20251119).NotTo(BeNil())

		for _, p := range params[:len(params)-1] {
			Expect(p.OfTool).NotTo(BeNil())
			Expect(deferred(p)).To(BeTrue())
		}
	})

	It("Should return no tools for an empty tool set", func() {
		Expect(AnthropicToolParams(nil)).To(BeEmpty())
	})

	// paramByName finds the tool param for a named custom tool.
	paramByName := func(params []anthropic.ToolUnionParam, name string) anthropic.ToolUnionParam {
		GinkgoHelper()

		for _, p := range params {
			if p.OfTool != nil && p.OfTool.Name == name {
				return p
			}
		}
		Fail(fmt.Sprintf("tool %q not found in params", name))
		return anthropic.ToolUnionParam{}
	}

	It("Should keep ai:no_defer tools loaded directly while deferring the rest", func() {
		app := fisk.New("app", "an app")
		// One pinned tool plus enough others to cross the defer threshold.
		app.Command("always", "always loaded").Tag(noDeferTag)
		for i := 0; i < toolSearchThreshold; i++ {
			app.Command(fmt.Sprintf("cmd%d", i), "a command")
		}

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		params := AnthropicToolParams(tools)

		// The pinned tool is sent directly; a deferred peer is not.
		Expect(deferred(paramByName(params, "always"))).To(BeFalse())
		Expect(deferred(paramByName(params, "cmd0"))).To(BeTrue())

		// Something is still deferred, so the search tool is present.
		last := params[len(params)-1]
		Expect(last.OfToolSearchToolBm25_20251119).NotTo(BeNil())
	})

	It("Should omit the search tool when every deferred-eligible tool is pinned", func() {
		app := fisk.New("app", "an app")
		for i := 0; i < toolSearchThreshold; i++ {
			app.Command(fmt.Sprintf("cmd%d", i), "a command").Tag(noDeferTag)
		}

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		params := AnthropicToolParams(tools)

		// Nothing was deferred, so no tool search tool is appended.
		Expect(params).To(HaveLen(toolSearchThreshold))
		for _, p := range params {
			Expect(p.OfTool).NotTo(BeNil())
			Expect(deferred(p)).To(BeFalse())
		}
	})
})

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

var _ = Describe("annotateOptional", func() {
	It("Should annotate only optional properties, leaving required ones untouched", func() {
		props := map[string]any{
			"target": map[string]any{"type": "string", "description": "where to deploy"},
			"force":  map[string]any{"type": "boolean", "description": "force the deploy"},
		}

		got := annotateOptional(props, []string{"target"}).(map[string]any)
		Expect(got["target"].(map[string]any)["description"]).To(Equal("where to deploy"))
		Expect(got["force"].(map[string]any)["description"]).To(Equal("force the deploy (optional)"))
	})

	It("Should label an optional property that has no description", func() {
		props := map[string]any{"dir": map[string]any{"type": "string"}}

		got := annotateOptional(props, nil).(map[string]any)
		Expect(got["dir"].(map[string]any)["description"]).To(Equal("Optional."))
	})

	It("Should not mutate the input, so repeated calls stay idempotent", func() {
		original := map[string]any{"dir": map[string]any{"type": "string", "description": "Directory to test"}}

		first := annotateOptional(original, nil).(map[string]any)
		Expect(first["dir"].(map[string]any)["description"]).To(Equal("Directory to test (optional)"))

		// The shared input is unchanged, so a second pass yields the same result
		// rather than stacking another "(optional)".
		Expect(original["dir"].(map[string]any)["description"]).To(Equal("Directory to test"))
		second := annotateOptional(original, nil).(map[string]any)
		Expect(second["dir"].(map[string]any)["description"]).To(Equal("Directory to test (optional)"))
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

var _ = Describe("ExecuteToolUse", func() {
	// useBlock builds a tool_use block addressing tool t with the given raw input.
	useBlock := func(t *Tool, id, input string) anthropic.ToolUseBlock {
		return anthropic.ToolUseBlock{ID: id, Name: t.Name(), Input: json.RawMessage(input)}
	}

	// doTool builds an "app do" command tool with a required argument, bound to
	// the given application path.
	doTool := func(appPath string) *Tool {
		GinkgoHelper()

		app := fisk.New("app", "an app")
		app.Command("do", "do a thing").Arg("subject", "the subject").Required().String()

		tools, err := ApplicationTools(introspect(app))
		Expect(err).NotTo(HaveOccurred())

		tool := toolsByName(tools)["do"]
		Expect(tool).NotTo(BeNil())
		tool.AppPath = appPath
		return tool
	}

	// resultBlock extracts the tool_result fields from a content block param.
	resultBlock := func(block anthropic.ContentBlockParamUnion) (id string, text string, isError bool) {
		GinkgoHelper()

		Expect(block.OfToolResult).NotTo(BeNil())
		res := block.OfToolResult
		Expect(res.Content).To(HaveLen(1))
		Expect(res.Content[0].OfText).NotTo(BeNil())
		return res.ToolUseID, res.Content[0].OfText.Text, res.IsError.Value
	}

	It("Should return a successful tool_result carrying the command JSON", func() {
		tool := doTool(writeExecutable("#!/bin/sh\necho ran\n"))

		block := ExecuteToolUse(context.Background(), tool, useBlock(tool, "tu_1", `{"subject":"x"}`))
		id, text, isError := resultBlock(block)
		Expect(id).To(Equal("tu_1"))
		Expect(isError).To(BeFalse())

		var result CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.ExitCode).To(Equal(0))
		Expect(result.Output).To(Equal("ran\n"))
	})

	It("Should deliver a non-zero exit as a successful tool_result", func() {
		tool := doTool(writeExecutable("#!/bin/sh\nexit 4\n"))

		block := ExecuteToolUse(context.Background(), tool, useBlock(tool, "tu_2", `{"subject":"x"}`))
		_, text, isError := resultBlock(block)
		Expect(isError).To(BeFalse())

		var result CommandResult
		Expect(json.Unmarshal([]byte(text), &result)).To(Succeed())
		Expect(result.ExitCode).To(Equal(4))
	})

	It("Should report an execution failure as an error tool_result", func() {
		tool := doTool("/nonexistent/binary")

		block := ExecuteToolUse(context.Background(), tool, useBlock(tool, "tu_3", `{"subject":"x"}`))
		id, text, isError := resultBlock(block)
		Expect(id).To(Equal("tu_3"))
		Expect(isError).To(BeTrue())
		Expect(text).To(ContainSubstring("running command"))
	})

	It("Should run with no arguments when the model sends a null input", func() {
		tool := doTool(writeExecutable("#!/bin/sh\necho ok\n"))

		block := ExecuteToolUse(context.Background(), tool, useBlock(tool, "tu_4", `null`))
		_, _, isError := resultBlock(block)
		Expect(isError).To(BeFalse())
	})
})
