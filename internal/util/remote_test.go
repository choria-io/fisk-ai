//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/a2a"
)

// fakeInvoker is a hand-written RemoteInvoker for driving RemoteTool without a
// transport: it records the last call and returns a canned reply or error.
type fakeInvoker struct {
	reply *a2a.ToolReply
	err   error

	gotAgent string
	gotTool  string
	gotInput json.RawMessage
}

func (f *fakeInvoker) InvokeTool(_ context.Context, agent, tool string, input json.RawMessage) (*a2a.ToolReply, error) {
	f.gotAgent = agent
	f.gotTool = tool
	f.gotInput = input

	return f.reply, f.err
}

var _ = Describe("RemoteTool", func() {
	descriptor := a2a.ToolDescriptor{
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
			rt, err := NewRemoteTool("nats_x", "nats", a2a.ToolDescriptor{Name: "x"}, &fakeInvoker{})
			Expect(err).NotTo(HaveOccurred())
			Expect(rt.InputSchema()).To(Equal(map[string]any{"type": "object"}))
		})

		It("Should reject an unparsable input schema", func() {
			_, err := NewRemoteTool("nats_x", "nats", a2a.ToolDescriptor{Name: "x", InputSchema: json.RawMessage(`not json`)}, &fakeInvoker{})
			Expect(err).To(MatchError(ContainSubstring("unparsable input schema")))
		})
	})

	Describe("ToolParam", func() {
		It("Should render a custom tool with the local name and advertised schema", func() {
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, &fakeInvoker{})
			param := rt.ToolParam(true)
			Expect(param.OfTool).NotTo(BeNil())
			Expect(param.OfTool.Name).To(Equal("nats_stream_info"))
			Expect(param.OfTool.DeferLoading.Value).To(BeTrue())
			Expect(param.OfTool.InputSchema.Required).To(ConsistOf("stream"))
		})
	})

	Describe("ExecuteRemoteUse", func() {
		use := anthropic.ToolUseBlock{ID: "call-1", Name: "nats_stream_info", Input: json.RawMessage(`{"stream":"ORDERS"}`)}

		It("Should map a successful reply to a CommandResult JSON result", func() {
			inv := &fakeInvoker{reply: &a2a.ToolReply{ToolResult: a2a.ToolResult{
				Output: "all good",
				Exec:   &a2a.ExecResult{Command: "stream info ORDERS", ExitCode: 0, Truncated: false},
			}}}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := ExecuteRemoteUse(context.Background(), rt, use)
			Expect(block.OfToolResult).NotTo(BeNil())
			Expect(block.OfToolResult.IsError.Value).To(BeFalse())

			var result CommandResult
			Expect(json.Unmarshal([]byte(blockText(block)), &result)).To(Succeed())
			Expect(result.Command).To(Equal("stream info ORDERS"))
			Expect(result.Output).To(Equal("all good"))

			// The remote name and input travel on the wire, not the local alias name.
			Expect(inv.gotTool).To(Equal("stream_info"))
			Expect(inv.gotAgent).To(Equal("nats"))
			Expect(string(inv.gotInput)).To(Equal(`{"stream":"ORDERS"}`))
		})

		It("Should preserve a non-zero exit as a successful result", func() {
			inv := &fakeInvoker{reply: &a2a.ToolReply{ToolResult: a2a.ToolResult{
				Output: "boom",
				Exec:   &a2a.ExecResult{Command: "stream info ORDERS", ExitCode: 3},
			}}}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := ExecuteRemoteUse(context.Background(), rt, use)
			Expect(block.OfToolResult.IsError.Value).To(BeFalse())

			var result CommandResult
			Expect(json.Unmarshal([]byte(blockText(block)), &result)).To(Succeed())
			Expect(result.ExitCode).To(Equal(3))
		})

		It("Should map a remote harness failure to an error result", func() {
			inv := &fakeInvoker{reply: &a2a.ToolReply{ToolResult: a2a.ToolResult{IsError: true, Output: "tool not available"}}}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := ExecuteRemoteUse(context.Background(), rt, use)
			Expect(block.OfToolResult.IsError.Value).To(BeTrue())
			Expect(blockText(block)).To(Equal("tool not available"))
		})

		It("Should map a transport error to an error result", func() {
			inv := &fakeInvoker{err: errors.New("no responders")}
			rt, _ := NewRemoteTool("nats_stream_info", "nats", descriptor, inv)

			block := ExecuteRemoteUse(context.Background(), rt, use)
			Expect(block.OfToolResult.IsError.Value).To(BeTrue())
			Expect(blockText(block)).To(ContainSubstring("no responders"))
		})
	})
})

var _ = Describe("BuildToolParams", func() {
	It("Should send a small combined set directly with no tool search tool", func() {
		local := appWithCommands(3)
		remote := tinyRemoteTools(2)
		params := BuildToolParams(local, remote, 0)
		Expect(params).To(HaveLen(5))
		for _, p := range params {
			Expect(p.OfTool).NotTo(BeNil())
			Expect(p.OfTool.DeferLoading.Value).To(BeFalse())
		}
	})

	It("Should defer and add the tool search tool when local plus remote crosses the threshold", func() {
		local := appWithCommands(8)
		remote := tinyRemoteTools(5)
		params := BuildToolParams(local, remote, 0)

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
		local := appWithCommands(8)

		// Eight command tools alone stay under the threshold and load directly.
		direct := BuildToolParams(local, nil, 0)
		for _, p := range direct {
			Expect(p.OfToolSearchToolBm25_20251119).To(BeNil())
			Expect(p.OfTool.DeferLoading.Value).To(BeFalse())
		}

		// Counting the built-ins the caller appends separately crosses the
		// threshold, so the command tools defer and the tool search tool appears.
		// The built-ins themselves are not part of this call and are never deferred.
		withBuiltins := BuildToolParams(local, nil, 3)
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

// blockText extracts the text of a tool_result content block for assertions.
func blockText(block anthropic.ContentBlockParamUnion) string {
	GinkgoHelper()
	Expect(block.OfToolResult).NotTo(BeNil())
	Expect(block.OfToolResult.Content).NotTo(BeEmpty())

	return block.OfToolResult.Content[0].OfText.Text
}

// tinyRemoteTools builds n trivial remote tools for deferral tests.
func tinyRemoteTools(n int) []*RemoteTool {
	GinkgoHelper()
	out := make([]*RemoteTool, n)
	for i := range out {
		rt, err := NewRemoteTool(fmt.Sprintf("r_%d", i), "agent", a2a.ToolDescriptor{Name: fmt.Sprintf("t%d", i)}, &fakeInvoker{})
		Expect(err).NotTo(HaveOccurred())
		out[i] = rt
	}

	return out
}
