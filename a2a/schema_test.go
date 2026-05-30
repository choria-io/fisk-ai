//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fillHeader populates the required header fields with valid values so a message
// satisfies the common header schema.
func fillHeader(h *Header) {
	h.ID = NewID()
	h.Request = h.ID
	h.Conversation = NewID()
	h.Sequence = 1
	h.Time = time.Now().UTC()
	h.Sender = Identity{Name: "agent-a"}
}

// tamper round-trips a body through a map so a test can mutate it before
// re-validating.
func tamper(data []byte, mut func(map[string]any)) []byte {
	var m map[string]any
	Expect(json.Unmarshal(data, &m)).To(Succeed())
	mut(m)
	out, err := json.Marshal(m)
	Expect(err).ToNot(HaveOccurred())

	return out
}

var _ = Describe("Validator", func() {
	var v *Validator

	BeforeEach(func() {
		var err error
		v, err = NewValidator()
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("valid messages", func() {
		It("Should accept every fully populated message type", func() {
			request := NewRequest("do the thing")
			request.Budget = &Budget{MaxTokens: 1000, MaxIterations: 5, CallTimeout: "60s"}

			toolResult := NewBlock(ToolResultBlock{
				CallID:     "c1",
				ToolResult: ToolResult{Output: "ok", Exec: &ExecResult{Command: "nats server info", ExitCode: 0}},
			})

			result := NewResult(StopEndTurn)
			result.Text = "all done"
			result.Usage = &Usage{InputTokens: 10, OutputTokens: 20}

			toolReply := NewToolReply("done", false)
			toolReply.Exec = &ExecResult{Command: "nats server info", ExitCode: 0}

			discoveryReply := NewDiscoveryReply("agent-a", "1.2.3")
			discoveryReply.Description = "manages nats auth"
			discoveryReply.Protocols = []string{ProtocolNamespace}
			discoveryReply.Tools = []ToolDescriptor{{
				Name:        "nats_server_info",
				Description: "show server info",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			}}

			messages := []any{
				request,
				NewEvent(NewThinkingBlock("hmm")),
				NewEvent(NewTextBlock("answer")),
				NewEvent(NewToolCallBlock("c1", "nats_server_info", json.RawMessage(`{"id":1}`))),
				NewEvent(toolResult),
				NewEvent(NewBlock(AgentCallBlock{ID: "a1", Name: "remote", Task: NewID()})),
				NewEvent(NewBlock(StatusBlock{Iteration: 2, Phase: "calling-llm", Usage: &Usage{InputTokens: 1}})),
				result,
				NewError("it broke"),
				NewCancel(),
				NewAck(true),
				NewToolRequest("nats_server_info", json.RawMessage(`{"id":1}`)),
				toolReply,
				NewDiscoveryRequest(),
				discoveryReply,
			}

			for _, msg := range messages {
				switch m := msg.(type) {
				case *Request:
					fillHeader(&m.Header)
				case *Event:
					fillHeader(&m.Header)
				case *Result:
					fillHeader(&m.Header)
				case *ErrorMessage:
					fillHeader(&m.Header)
				case *Cancel:
					fillHeader(&m.Header)
				case *Ack:
					fillHeader(&m.Header)
				case *ToolRequest:
					fillHeader(&m.Header)
				case *ToolReply:
					fillHeader(&m.Header)
				case *DiscoveryRequest:
					fillHeader(&m.Header)
				case *DiscoveryReply:
					fillHeader(&m.Header)
				default:
					Fail("unhandled message type in test")
				}

				err := v.ValidateMessage(msg)
				Expect(err).ToNot(HaveOccurred(), "%T should validate", msg)
			}
		})
	})

	Describe("invalid messages", func() {
		var validRequest []byte

		BeforeEach(func() {
			req := NewRequest("hello")
			fillHeader(&req.Header)
			var err error
			validRequest, err = json.Marshal(req)
			Expect(err).ToNot(HaveOccurred())
			Expect(v.Validate(validRequest)).To(Succeed())
		})

		It("Should reject an unevaluated extra property", func() {
			bad := tamper(validRequest, func(m map[string]any) {
				m["bogus"] = "nope"
			})
			Expect(v.Validate(bad)).To(HaveOccurred())
		})

		It("Should reject a missing required field", func() {
			bad := tamper(validRequest, func(m map[string]any) {
				delete(m, "prompt")
			})
			Expect(v.Validate(bad)).To(HaveOccurred())
		})

		It("Should reject an invalid sender identity", func() {
			bad := tamper(validRequest, func(m map[string]any) {
				m["sender"] = map[string]any{"name": ""}
			})
			Expect(v.Validate(bad)).To(HaveOccurred())
		})

		It("Should reject an unknown protocol id", func() {
			bad := tamper(validRequest, func(m map[string]any) {
				m["protocol"] = "io.choria.fisk-ai.v1.bogus"
			})
			Expect(v.Validate(bad)).To(MatchError(ErrUnknownProtocol))
		})

		It("Should reject an event whose block fails the oneOf", func() {
			ev := NewEvent(NewTextBlock("hi"))
			fillHeader(&ev.Header)
			body, err := json.Marshal(ev)
			Expect(err).ToNot(HaveOccurred())

			bad := tamper(body, func(m map[string]any) {
				m["block"] = map[string]any{"type": "bogus"}
			})
			Expect(v.Validate(bad)).To(HaveOccurred())
		})
	})
})
