//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/builtin"
	fisk2 "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	"github.com/choria-io/fisk-ai/internal/toolkit/functool"
	"github.com/choria-io/fisk-ai/internal/util"
)

// describelessTool is a model-facing Tool that does not implement toolkit.Describer,
// so traceCall must fall back to its safe default: run it, trace it by name alone,
// with no dependencies and not as a remote call.
type describelessTool struct{}

func (describelessTool) Name() string                { return "mystery" }
func (describelessTool) Description() string         { return "a tool of unforeseen kind" }
func (describelessTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (describelessTool) Definition(bool) llm.ToolDef { return llm.ToolDef{Name: "mystery"} }
func (describelessTool) ExecuteUse(context.Context, llm.ToolUseBlock, toolkit.ExecDeps) llm.ToolResultBlock {
	return llm.ToolResultBlock{}
}

// findBuiltin returns the built-in tool with the given name, failing the spec when
// it is absent so a rename in the tool set is caught rather than silently skipped.
func findBuiltin(tools []*functool.Tool, name string) *functool.Tool {
	GinkgoHelper()
	for _, t := range tools {
		if t.Name() == name {
			return t
		}
	}
	Fail("built-in tool not found: " + name)

	return nil
}

// These tests pin the observable output of traceCall per tool kind: the ToolKind
// carried on the emitted call trace (which drives output suppression and the
// machine-readable slog tokens downstream), the trace's Display/DisplayShort/Agent,
// the per-run ExecDeps the kind receives, and the remote flag (which drives the
// remote-call stat and the journaled Remote flag). They assert today's behavior so a
// later refactor of how the kind is derived cannot silently change what the operator
// or the journal sees.
var _ = Describe("runner.traceCall parity", func() {
	const workDir = "/run/work-42"

	newRunner := func(ev *captureEvents, tools map[string]toolkit.Tool) *runner {
		return &runner{
			stats:       &util.RunStats{},
			events:      ev,
			tools:       tools,
			prompter:    toolkit.DefaultDenyPrompter(),
			toolWorkDir: workDir,
		}
	}

	It("Should trace a local command tool as ToolLocal with the full and short call lines, given the work dir", func() {
		ev := &captureEvents{}
		tool := &fisk2.FiskCommandTool{Path: []string{"stream", "info"}, Model: &fisk.CmdModel{}}
		r := newRunner(ev, map[string]toolkit.Tool{"stream_info": tool})
		use := llm.ToolUseBlock{ID: "t1", Name: "stream_info", Input: json.RawMessage(`{}`)}

		kind, deps, remote := r.traceCall(use)
		Expect(kind).To(Equal(ToolLocal))
		Expect(remote).To(BeFalse())
		Expect(deps.WorkDir).To(Equal(workDir))
		Expect(deps.Prompter).To(BeNil())

		Expect(ev.calls).To(HaveLen(1))
		Expect(ev.calls[0].Kind).To(Equal(ToolLocal))
		Expect(ev.calls[0].Display).To(Equal(tool.TraceLine(use.Input)))
		Expect(ev.calls[0].DisplayShort).To(Equal(tool.TraceLineShort(use.Input)))
		Expect(ev.calls[0].Display).NotTo(BeEmpty())
	})

	It("Should trace a remote tool as ToolRemote naming its agent, flag it remote, and pass no dependencies", func() {
		ev := &captureEvents{}
		desc := a2a.ToolDescriptor{Name: "info", Description: "reports info", InputSchema: json.RawMessage(`{"type":"object"}`)}
		rt, err := a2a.NewRemoteTool("nats_info", "nats", desc, stubInvoker{reply: a2a.NewToolReply("ok", false)})
		Expect(err).NotTo(HaveOccurred())
		r := newRunner(ev, map[string]toolkit.Tool{"nats_info": rt})
		use := llm.ToolUseBlock{ID: "t1", Name: "nats_info"}

		kind, deps, remote := r.traceCall(use)
		Expect(kind).To(Equal(ToolRemote))
		Expect(remote).To(BeTrue())
		Expect(deps.Prompter).To(BeNil())
		Expect(deps.WorkDir).To(Equal(""))

		Expect(ev.calls).To(HaveLen(1))
		Expect(ev.calls[0].Kind).To(Equal(ToolRemote))
		Expect(ev.calls[0].Agent).To(Equal("nats"))
	})

	It("Should trace a memory built-in as ToolMemory with its call line and pass the operator prompter", func() {
		ev := &captureEvents{}
		memCfg := &config.Config{Harness: config.HarnessConfig{Memory: &config.MemoryConfig{Enabled: true}}}
		tool := findBuiltin(builtin.MemoryTools(memCfg, nil), "memory_list")
		r := newRunner(ev, map[string]toolkit.Tool{"memory_list": tool})
		use := llm.ToolUseBlock{ID: "t1", Name: "memory_list", Input: json.RawMessage(`{}`)}

		kind, deps, remote := r.traceCall(use)
		Expect(kind).To(Equal(ToolMemory))
		Expect(remote).To(BeFalse())
		Expect(deps.Prompter).NotTo(BeNil())

		Expect(ev.calls).To(HaveLen(1))
		Expect(ev.calls[0].Kind).To(Equal(ToolMemory))
		Expect(ev.calls[0].Display).To(Equal(tool.TraceLine(use.Input)))
		Expect(ev.calls[0].Display).NotTo(BeEmpty())
	})

	It("Should trace a human-in-the-loop built-in as ToolBuiltin with no call line and pass the operator prompter", func() {
		ev := &captureEvents{}
		hitlCfg := &config.Config{Harness: config.HarnessConfig{HumanInTheLoop: &config.HumanInTheLoopConfig{Enabled: true}}}
		tool := findBuiltin(builtin.HITLTools(hitlCfg), "ask_human_confirm")
		r := newRunner(ev, map[string]toolkit.Tool{"ask_human_confirm": tool})
		use := llm.ToolUseBlock{ID: "t1", Name: "ask_human_confirm", Input: json.RawMessage(`{"question":"go?"}`)}

		kind, deps, remote := r.traceCall(use)
		Expect(kind).To(Equal(ToolBuiltin))
		Expect(remote).To(BeFalse())
		Expect(deps.Prompter).NotTo(BeNil())

		Expect(ev.calls).To(HaveLen(1))
		Expect(ev.calls[0].Kind).To(Equal(ToolBuiltin))
		// A self-rendering tool shows its own prompt, so its call line is suppressed.
		Expect(ev.calls[0].Display).To(Equal(""))
	})

	It("Should trace a tool that does not describe itself as ToolLocal by name, with no dependencies", func() {
		ev := &captureEvents{}
		r := newRunner(ev, map[string]toolkit.Tool{"mystery": describelessTool{}})
		use := llm.ToolUseBlock{ID: "t1", Name: "mystery", Input: json.RawMessage(`{}`)}

		kind, deps, remote := r.traceCall(use)
		Expect(kind).To(Equal(ToolLocal))
		Expect(remote).To(BeFalse())
		Expect(deps.Prompter).To(BeNil())
		Expect(deps.WorkDir).To(Equal(""))

		Expect(ev.calls).To(HaveLen(1))
		Expect(ev.calls[0].Kind).To(Equal(ToolLocal))
		Expect(ev.calls[0].Name).To(Equal("mystery"))
		Expect(ev.calls[0].Display).To(Equal(""))
	})
})
