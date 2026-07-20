//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/tui"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// userMsg builds a user turn carrying a single text block.
func userMsg(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: text}}}}
}

// assistantMsg builds an assistant turn from the given content blocks.
func assistantMsg(blocks ...llm.ContentBlock) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: blocks}
}

// textBlk, useBlk and resultBlk build the neutral content blocks a turn carries.
func textBlk(text string) llm.ContentBlock {
	return llm.ContentBlock{Text: &llm.TextBlock{Text: text}}
}

func useBlk(id, name, input string) llm.ContentBlock {
	return llm.ContentBlock{ToolUse: &llm.ToolUseBlock{ID: id, Name: name, Input: json.RawMessage(input)}}
}

func resultBlk(id, content string) llm.ContentBlock {
	return llm.ContentBlock{ToolResult: &llm.ToolResultBlock{ToolUseID: id, Content: content}}
}

var _ = Describe("printResumeTranscript", func() {
	It("replays the prompt, narration and tool calls, including the pending turn", func() {
		rs := &runstate.RunState{
			RunID:    "sess1",
			Counters: runstate.Counters{LlmCalls: 2},
			Messages: []llm.Message{
				userMsg("deploy the thing"),
				assistantMsg(textBlk("checking the current state"), useBlk("t1", "status", "{}")),
				{Role: llm.RoleUser, Content: []llm.ContentBlock{resultBlk("t1", "ok")}},
			},
			Pending: &runstate.PendingTurn{
				Assistant: assistantMsg(textBlk("now applying"), useBlk("t2", "apply", "{}")),
			},
		}

		var buf bytes.Buffer
		printResumeTranscript(&buf, rs, nil, true)
		out := buf.String()

		Expect(out).To(ContainSubstring("resuming \"sess1\", 2 LLM call(s)"))
		Expect(out).To(ContainSubstring("> deploy the thing"))
		Expect(out).To(ContainSubstring("checking the current state"))
		Expect(out).To(ContainSubstring("-> status"))
		Expect(out).To(ContainSubstring("now applying"))
		Expect(out).To(ContainSubstring("-> apply"))
		Expect(out).To(ContainSubstring("--- continuing ---"))
		// Tool results are model-facing, not part of the human replay.
		Expect(out).NotTo(ContainSubstring("ok"))
	})
})

var _ = Describe("dumpTranscript", func() {
	It("prints prompt, tool inputs and tool results including the pending turn", func() {
		rs := &runstate.RunState{
			RunID: "sess1",
			Messages: []llm.Message{
				userMsg("do work"),
				assistantMsg(useBlk("t1", "list", `{"path":"/etc"}`)),
				{Role: llm.RoleUser, Content: []llm.ContentBlock{resultBlk("t1", "file-a\nfile-b")}},
			},
			Pending: &runstate.PendingTurn{
				Assistant: assistantMsg(useBlk("t2", "apply", "{}")),
				Results:   []llm.ToolResultBlock{{ToolUseID: "t2", Content: "applied"}},
			},
		}

		var withOutput bytes.Buffer
		dumpTranscript(&withOutput, rs, true, true)
		full := withOutput.String()

		Expect(full).To(ContainSubstring("> do work"))
		Expect(full).To(ContainSubstring(`-> list {"path":"/etc"}`))
		Expect(full).To(ContainSubstring("<- [t1]"))
		Expect(full).To(ContainSubstring("file-a"))
		Expect(full).To(ContainSubstring("-> apply"))
		Expect(full).To(ContainSubstring("<- [t2]"))
		Expect(full).To(ContainSubstring("applied"))

		// Without --tool-output the calls show but the results are withheld.
		var noOutput bytes.Buffer
		dumpTranscript(&noOutput, rs, true, false)
		brief := noOutput.String()

		Expect(brief).To(ContainSubstring(`-> list {"path":"/etc"}`))
		Expect(brief).To(ContainSubstring("-> apply"))
		Expect(brief).NotTo(ContainSubstring("<- [t1]"))
		Expect(brief).NotTo(ContainSubstring("file-a"))
		Expect(brief).NotTo(ContainSubstring("applied"))
	})

	It("strips terminal escapes from tool result output so a raw escape cannot bleed on the terminal", func() {
		rs := &runstate.RunState{
			RunID: "sess2",
			Messages: []llm.Message{
				userMsg("do work"),
				assistantMsg(useBlk("t1", "run", "{}")),
				{Role: llm.RoleUser, Content: []llm.ContentBlock{resultBlk("t1", "before \x1b[31mred\x1b[0m after")}},
			},
		}

		var buf bytes.Buffer
		dumpTranscript(&buf, rs, true, true)
		out := buf.String()

		Expect(out).To(ContainSubstring("before red after"))
		Expect(out).NotTo(ContainSubstring("\x1b["))
	})
})

var _ = Describe("transcriptLines", func() {
	rs := func() *runstate.RunState {
		return &runstate.RunState{
			Messages: []llm.Message{
				userMsg("go"),
				assistantMsg(useBlk("a", "first", "{}"), useBlk("b", "second", "{}")),
				{Role: llm.RoleUser, Content: []llm.ContentBlock{resultBlk("a", "res-a"), resultBlk("b", "res-b")}},
			},
		}
	}

	It("interleaves each tool call with its result so a multi-call turn reads as call/result pairs", func() {
		lines := transcriptLines(rs(), true)

		Expect(lines).To(HaveLen(5))
		Expect(lines[0].Kind).To(Equal(tui.LinePrompt))
		Expect(lines[1]).To(Equal(tui.Line{Kind: tui.LineToolCall, Text: "first {}", Short: "first {}"}))
		Expect(lines[2]).To(Equal(tui.Line{Kind: tui.LineToolResult, Text: "res-a"}))
		Expect(lines[3]).To(Equal(tui.Line{Kind: tui.LineToolCall, Text: "second {}", Short: "second {}"}))
		Expect(lines[4]).To(Equal(tui.Line{Kind: tui.LineToolResult, Text: "res-b"}))
	})

	It("shows only the calls, still paired in order, when tool output is withheld", func() {
		lines := transcriptLines(rs(), false)

		Expect(lines).To(HaveLen(3))
		Expect(lines[1]).To(Equal(tui.Line{Kind: tui.LineToolCall, Text: "first {}", Short: "first {}"}))
		Expect(lines[2]).To(Equal(tui.Line{Kind: tui.LineToolCall, Text: "second {}", Short: "second {}"}))
	})

	It("renders an interactive follow-up as a prompt line, without dropping the merged tool result", func() {
		// The post-error fold shape: a follow-up merged into the trailing tool-results
		// user turn, so one user message carries both the result and the follow-up text.
		rs := &runstate.RunState{
			Messages: []llm.Message{
				userMsg("go"),
				assistantMsg(useBlk("a", "first", "{}")),
				{Role: llm.RoleUser, Content: []llm.ContentBlock{resultBlk("a", "res-a"), textBlk("please continue")}},
			},
		}

		lines := transcriptLines(rs, true)

		// prompt, tool call, tool result (paired), then the follow-up prompt.
		Expect(lines).To(HaveLen(4))
		Expect(lines[0].Kind).To(Equal(tui.LinePrompt))
		Expect(lines[1]).To(Equal(tui.Line{Kind: tui.LineToolCall, Text: "first {}", Short: "first {}"}))
		Expect(lines[2]).To(Equal(tui.Line{Kind: tui.LineToolResult, Text: "res-a"}))
		Expect(lines[3]).To(Equal(tui.Line{Kind: tui.LinePrompt, Text: "please continue"}))
	})
})

var _ = Describe("toolCallDump", func() {
	It("elides long string values but keeps keys, numbers and structure", func() {
		use := &llm.ToolUseBlock{
			Name:  "stream_add",
			Input: json.RawMessage(`{"subject":"orders.events.created","name":"ORDERS","replicas":3}`),
		}

		full, short := toolCallDump(use)
		Expect(full).To(HavePrefix("stream_add "))
		Expect(short).To(HavePrefix("stream_add "))

		// The full form keeps every value, the short form elides only long strings;
		// both keep keys, numbers and structure.
		Expect(full).To(ContainSubstring(`"subject":"orders.events.created"`))
		Expect(short).To(ContainSubstring(`"subject":"orders.eve...reated"`))
		Expect(full).To(ContainSubstring(`"name":"ORDERS"`))
		Expect(short).To(ContainSubstring(`"name":"ORDERS"`))
		Expect(full).To(ContainSubstring(`"replicas":3`))
		Expect(short).To(ContainSubstring(`"replicas":3`))
	})

	It("leaves a short argument set unchanged in both forms", func() {
		use := &llm.ToolUseBlock{Name: "list", Input: json.RawMessage(`{"path":"/etc"}`)}

		full, short := toolCallDump(use)
		Expect(full).To(Equal(`list {"path":"/etc"}`))
		Expect(short).To(Equal(`list {"path":"/etc"}`))
	})
})

var _ = Describe("elideToolCall", func() {
	const (
		full  = `stream_add {"subject":"orders.events.created"}`
		short = `stream_add {"subject":"orders.eve...reated"}`
	)

	It("shows the full form when it fits the width", func() {
		Expect(elideToolCall(full, short, 100)).To(Equal(full))
	})

	It("falls back to the short form when the full form exceeds the width", func() {
		Expect(elideToolCall(full, short, 20)).To(Equal(short))
	})

	It("reserves the prefix and margin so a line that would land on the last column elides", func() {
		// full is 46 runes; with the 3-cell "-> " prefix and 1-cell margin it fits at 50
		// but not at 49, where the reserved margin tips it over to the short form.
		Expect(elideToolCall(full, short, 50)).To(Equal(full))
		Expect(elideToolCall(full, short, 49)).To(Equal(short))
	})
})
