//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/tui"
)

var _ = Describe("tcellEvents mapping", func() {
	thinking := func(s string) llm.ContentBlock { return llm.ContentBlock{Thinking: &llm.ThinkingBlock{Text: s}} }
	text := func(s string) llm.ContentBlock { return llm.ContentBlock{Text: &llm.TextBlock{Text: s}} }
	resp := func(blocks ...llm.ContentBlock) llm.Response { return llm.Response{Content: blocks} }

	Describe("messageLines", func() {
		It("Should map thinking then prose and set apart a terminal answer", func() {
			lines, answer := messageLines(resp(thinking("pondering"), text("the answer")), true)
			Expect(answer).To(Equal("the answer"))
			Expect(lines).To(HaveLen(3))
			Expect(lines[0].Kind).To(Equal(tui.LineThinking))
			Expect(lines[0].Text).To(Equal("pondering"))
			Expect(lines[1].Kind).To(Equal(tui.LineMeta))
			Expect(lines[1].Text).To(ContainSubstring("answer"))
			Expect(lines[2].Kind).To(Equal(tui.LineNarration))
			Expect(lines[2].Text).To(Equal("the answer"))
		})

		It("Should not add the answer delimiter for an intermediate turn", func() {
			lines, _ := messageLines(resp(text("working on it")), false)
			Expect(lines).To(HaveLen(1))
			Expect(lines[0].Kind).To(Equal(tui.LineNarration))
			Expect(lines[0].Text).To(Equal("working on it"))
		})

		It("Should return nothing for a turn with no text", func() {
			lines, answer := messageLines(resp(), true)
			Expect(lines).To(BeEmpty())
			Expect(answer).To(BeEmpty())
		})
	})

	Describe("toolTraceLine", func() {
		It("Should show a local tool's resolved command line", func() {
			line, ok := toolTraceLine(agent.ToolTrace{Kind: agent.ToolLocal, Display: "stream rm ORDERS", DisplayShort: "stream rm ORD...RS"}, false)
			Expect(ok).To(BeTrue())
			Expect(line.Kind).To(Equal(tui.LineToolCall))
			Expect(line.Text).To(Equal("stream rm ORDERS"))
			Expect(line.Short).To(Equal("stream rm ORD...RS"))
		})

		It("Should name a remote tool's agent", func() {
			line, ok := toolTraceLine(agent.ToolTrace{Kind: agent.ToolRemote, Name: "deploy", Agent: "ops"}, false)
			Expect(ok).To(BeTrue())
			Expect(line.Text).To(Equal("deploy (remote ops)"))
		})

		It("Should hide a built-in tool unless verbose", func() {
			_, ok := toolTraceLine(agent.ToolTrace{Kind: agent.ToolBuiltin, Name: "ask_human_confirm"}, false)
			Expect(ok).To(BeFalse())

			line, ok := toolTraceLine(agent.ToolTrace{Kind: agent.ToolBuiltin, Name: "ask_human_confirm"}, true)
			Expect(ok).To(BeTrue())
			Expect(line.Text).To(Equal("ask_human_confirm"))
		})

		It("Should show a memory tool's call line even without verbose", func() {
			line, ok := toolTraceLine(agent.ToolTrace{Kind: agent.ToolMemory, Name: "memory_write", Display: "memory_write build.notes"}, false)
			Expect(ok).To(BeTrue())
			Expect(line.Kind).To(Equal(tui.LineToolCall))
			Expect(line.Text).To(Equal("memory_write build.notes"))
		})
	})

	Describe("toolResultLine", func() {
		It("Should show a tool's output as a result line", func() {
			line := toolResultLine("all good", false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal("all good"))
		})

		It("Should mark a silent success so an executed tool always leaves a visible result", func() {
			line := toolResultLine("", false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal("(no output)"))
		})

		It("Should render a failure as an error line carrying an (error) marker", func() {
			line := toolResultLine("boom", true)
			Expect(line.Kind).To(Equal(tui.LineToolError))
			Expect(line.Text).To(Equal("(error) boom"))
		})

		It("Should mark a silent failure without a trailing space", func() {
			line := toolResultLine("", true)
			Expect(line.Kind).To(Equal(tui.LineToolError))
			Expect(line.Text).To(Equal("(error)"))
		})

		mustResult := func(res toolkit.CommandResult) string {
			GinkgoHelper()
			data, err := json.Marshal(res)
			Expect(err).ToNot(HaveOccurred())
			return string(data)
		}

		It("Should unwrap a CommandResult envelope to just its output", func() {
			line := toolResultLine(mustResult(toolkit.CommandResult{Command: "tools memory_read --key=x", Output: "the stored note\nsecond line"}), false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal("the stored note\nsecond line"))
		})

		It("Should show a silent successful command as (no output)", func() {
			line := toolResultLine(mustResult(toolkit.CommandResult{Command: "tools memory_write --key=x", Output: ""}), false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal("(no output)"))
		})

		It("Should keep an exit marker when a command that ran exited non-zero", func() {
			line := toolResultLine(mustResult(toolkit.CommandResult{Command: "stream info missing", ExitCode: 1, Output: "no such stream"}), false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal("(exit 1) no such stream"))
		})

		It("Should mark a silent non-zero exit with just the exit marker", func() {
			line := toolResultLine(mustResult(toolkit.CommandResult{ExitCode: 2}), false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal("(exit 2)"))
		})

		It("Should leave a non-CommandResult body unchanged", func() {
			line := toolResultLine(`{"role":"user","other":true}`, false)
			Expect(line.Kind).To(Equal(tui.LineToolResult))
			Expect(line.Text).To(Equal(`{"role":"user","other":true}`))
		})
	})

	Describe("warningMessage", func() {
		It("Should format a warning without the leading prefix so both UIs share the wording", func() {
			Expect(warningMessage(agent.Warning{Kind: agent.WarnUnknownTool, Name: "frob"})).To(Equal(`model called unknown tool "frob"`))
			Expect(warningMessage(agent.Warning{Kind: agent.WarnConfirmNoTerminal, Count: 3})).To(ContainSubstring("3 tool(s) require confirmation"))
		})

		It("Should name the tool count and the cause-specific remedy for a tool-search degradation", func() {
			unsupported := warningMessage(agent.Warning{Kind: agent.WarnToolSearchUnsupported, Count: 12})
			Expect(unsupported).To(ContainSubstring("12 tools"))
			Expect(unsupported).To(ContainSubstring("does not support server-side tool search"))

			disabled := warningMessage(agent.Warning{Kind: agent.WarnToolSearchDisabled, Count: 12})
			Expect(disabled).To(ContainSubstring("12 tools"))
			Expect(disabled).To(ContainSubstring("no_tool_search"))
		})
	})

	Describe("runUsesTUI", func() {
		AfterEach(func() {
			noTUI = false
			httpDebug = false
		})

		It("Should turn the TUI off when --no-tui or NO_TUI is set", func() {
			noTUI = true
			Expect(runUsesTUI(config.NewConfig())).To(BeFalse())
		})

		It("Should turn the TUI off when the agent config sets no_tui", func() {
			cfg := config.NewConfig()
			cfg.Harness.NoTUI = true
			Expect(runUsesTUI(cfg)).To(BeFalse())
		})

		It("Should turn the TUI off when --http-debug is set, since its output corrupts the alt-screen", func() {
			httpDebug = true
			Expect(runUsesTUI(config.NewConfig())).To(BeFalse())
		})
	})
})
