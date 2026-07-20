//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// fakeInvoker is a hand-written RemoteInvoker for driving RemoteTool without a
// transport: it records the last call and returns a canned reply or error.
type fakeInvoker struct {
	reply *ToolReply
	err   error

	gotAgent string
	gotTool  string
	gotInput json.RawMessage
}

func (f *fakeInvoker) InvokeTool(_ context.Context, agent, tool string, input json.RawMessage) (*ToolReply, error) {
	f.gotAgent = agent
	f.gotTool = tool
	f.gotInput = input

	return f.reply, f.err
}

var _ = Describe("RemoteTool", func() {
	descriptor := ToolDescriptor{
		Name:        "stream_info",
		Description: "Reports on a stream",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"stream":{"type":"string"}},"required":["stream"]}`),
	}

	Describe("NewRemoteTool", func() {
		It("Should prefix the local name and keep the remote name on the wire", func() {
			rt, err := NewRemoteTool("nats_stream_info", "nats", descriptor, &fakeInvoker{})
			Expect(err).NotTo(HaveOccurred())
			Expect(rt.Name()).To(Equal("nats_stream_info"))
			Expect(rt.RemoteName()).To(Equal("stream_info"))
			Expect(rt.Agent()).To(Equal("nats"))
			Expect(rt.Description()).To(Equal("Reports on a stream"))
			Expect(rt.InputSchema()).To(HaveKey("properties"))
		})

		It("Should default to an object schema when none is advertised", func() {
			rt, err := NewRemoteTool("nats_x", "nats", ToolDescriptor{Name: "x"}, &fakeInvoker{})
			Expect(err).NotTo(HaveOccurred())
			Expect(rt.InputSchema()).To(Equal(map[string]any{"type": "object"}))
		})

		It("Should reject an unparsable input schema", func() {
			_, err := NewRemoteTool("nats_x", "nats", ToolDescriptor{Name: "x", InputSchema: json.RawMessage(`not json`)}, &fakeInvoker{})
			Expect(err).To(MatchError(ContainSubstring("unparsable input schema")))
		})
	})

	Describe("Definition", func() {
		It("Should render a custom tool with the local name and advertised schema", func() {
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, &fakeInvoker{})
			def := rt.Definition(true)
			Expect(def.Name).To(Equal("nats_stream_info"))
			Expect(def.DeferLoading).To(BeTrue())
			Expect(toolkit.SchemaRequired(def.InputSchema["required"])).To(ConsistOf("stream"))
		})
	})

	Describe("ExecuteRemoteUse", func() {
		use := llm.ToolUseBlock{ID: "call-1", Name: "nats_stream_info", Input: json.RawMessage(`{"stream":"ORDERS"}`)}

		It("Should map a successful reply to a CommandResult JSON result", func() {
			inv := &fakeInvoker{reply: &ToolReply{ToolResult: ToolResult{
				Output: "all good",
				Exec:   &ExecResult{Command: "stream info ORDERS", ExitCode: 0, Truncated: false},
			}}}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := rt.ExecuteUse(context.Background(), use, toolkit.ExecDeps{})
			Expect(block.IsError).To(BeFalse())

			var result toolkit.CommandResult
			Expect(json.Unmarshal([]byte(block.Content), &result)).To(Succeed())
			Expect(result.Command).To(Equal("stream info ORDERS"))
			Expect(result.Output).To(Equal("all good"))

			// The remote name and input travel on the wire, not the local alias name.
			Expect(inv.gotTool).To(Equal("stream_info"))
			Expect(inv.gotAgent).To(Equal("nats"))
			Expect(string(inv.gotInput)).To(Equal(`{"stream":"ORDERS"}`))
		})

		It("Should preserve a non-zero exit as a successful result", func() {
			inv := &fakeInvoker{reply: &ToolReply{ToolResult: ToolResult{
				Output: "boom",
				Exec:   &ExecResult{Command: "stream info ORDERS", ExitCode: 3},
			}}}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := rt.ExecuteUse(context.Background(), use, toolkit.ExecDeps{})
			Expect(block.IsError).To(BeFalse())

			var result toolkit.CommandResult
			Expect(json.Unmarshal([]byte(block.Content), &result)).To(Succeed())
			Expect(result.ExitCode).To(Equal(3))
		})

		It("Should map a remote harness failure to an error result", func() {
			inv := &fakeInvoker{reply: &ToolReply{ToolResult: ToolResult{IsError: true, Output: "tool not available"}}}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := rt.ExecuteUse(context.Background(), use, toolkit.ExecDeps{})
			Expect(block.IsError).To(BeTrue())
			Expect(block.Content).To(Equal("tool not available"))
		})

		It("Should map a transport error to an error result", func() {
			inv := &fakeInvoker{err: errors.New("no responders")}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := rt.ExecuteUse(context.Background(), use, toolkit.ExecDeps{})
			Expect(block.IsError).To(BeTrue())
			Expect(block.Content).To(ContainSubstring("no responders"))
		})
	})
})
