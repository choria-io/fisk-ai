//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestA2A(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "A2A")
}

var _ = Describe("A2A", func() {
	Describe("NewID", func() {
		It("Should mint distinct ids", func() {
			Expect(NewID()).NotTo(Equal(NewID()))
		})
	})

	Describe("Decode", func() {
		It("Should dispatch on the protocol id", func() {
			req := NewRequest("hello")
			req.ID = NewID()
			req.Request = req.ID

			body, err := json.Marshal(req)
			Expect(err).ToNot(HaveOccurred())

			msg, err := Decode(body)
			Expect(err).ToNot(HaveOccurred())

			got, ok := msg.(*Request)
			Expect(ok).To(BeTrue())
			Expect(got.Prompt).To(Equal("hello"))
			Expect(got.Protocol).To(Equal(RequestProtocol))
		})

		It("Should reject an unknown protocol", func() {
			_, err := Decode([]byte(`{"protocol":"io.choria.fisk-ai.v1.bogus"}`))
			Expect(err).To(MatchError(ErrUnknownProtocol))
		})

		It("Should decode every message type to its concrete pointer", func() {
			cases := []struct {
				msg  any
				want any
			}{
				{NewRequest("p"), &Request{}},
				{NewEvent(NewTextBlock("hi")), &Event{}},
				{NewResult(StopEndTurn), &Result{}},
				{NewError("boom"), &ErrorMessage{}},
				{NewCancel(), &Cancel{}},
				{NewAck(true), &Ack{}},
				{NewToolRequest("nats_server_info", json.RawMessage(`{"id":1}`)), &ToolRequest{}},
				{NewToolReply("ok", false), &ToolReply{}},
				{NewDiscoveryRequest(), &DiscoveryRequest{}},
				{NewDiscoveryReply("agent-a", "1.0.0"), &DiscoveryReply{}},
			}

			for _, tc := range cases {
				body, err := json.Marshal(tc.msg)
				Expect(err).ToNot(HaveOccurred())

				decoded, err := Decode(body)
				Expect(err).ToNot(HaveOccurred())
				Expect(decoded).To(BeAssignableToTypeOf(tc.want))
			}
		})
	})

	Describe("Block", func() {
		It("Should add a type discriminator on marshal", func() {
			body, err := json.Marshal(NewTextBlock("hi"))
			Expect(err).ToNot(HaveOccurred())

			var fields map[string]any
			Expect(json.Unmarshal(body, &fields)).To(Succeed())
			Expect(fields["type"]).To(Equal(string(BlockText)))
			Expect(fields["text"]).To(Equal("hi"))
		})

		It("Should round-trip every block type", func() {
			blocks := []Block{
				NewThinkingBlock("reasoning"),
				NewTextBlock("answer"),
				NewToolCallBlock("c1", "nats_server_info", json.RawMessage(`{"a":1}`)),
				NewToolResultBlock("c1", "output", false),
				NewBlock(AgentCallBlock{ID: "a1", Name: "remote", Task: "t1"}),
				NewBlock(StatusBlock{Iteration: 2, Phase: "calling-llm"}),
			}

			for _, b := range blocks {
				body, err := json.Marshal(b)
				Expect(err).ToNot(HaveOccurred())

				var got Block
				Expect(json.Unmarshal(body, &got)).To(Succeed())
				Expect(got.Type()).To(Equal(b.Type()))
				Expect(got.AsAny()).To(Equal(b.AsAny()))
			}
		})

		It("Should dispatch via a type switch on AsAny", func() {
			var got Block
			body, err := json.Marshal(NewToolResultBlock("c1", "out", true))
			Expect(err).ToNot(HaveOccurred())
			Expect(json.Unmarshal(body, &got)).To(Succeed())

			switch v := got.AsAny().(type) {
			case ToolResultBlock:
				Expect(v.CallID).To(Equal("c1"))
				Expect(v.IsError).To(BeTrue())
			default:
				Fail("expected a ToolResultBlock")
			}
		})

		It("Should reject an unknown block type", func() {
			var got Block
			err := json.Unmarshal([]byte(`{"type":"bogus"}`), &got)
			Expect(err).To(MatchError(ErrUnknownBlockType))
		})

		It("Should fail to marshal an empty block", func() {
			_, err := json.Marshal(Block{})
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Request", func() {
		It("Should default to streaming when unset", func() {
			Expect(NewRequest("p").WantsStream()).To(BeTrue())
		})

		It("Should honor an explicit stream false across a round-trip", func() {
			no := false
			req := NewRequest("p")
			req.Stream = &no

			body, err := json.Marshal(req)
			Expect(err).ToNot(HaveOccurred())

			decoded, err := Decode(body)
			Expect(err).ToNot(HaveOccurred())
			Expect(decoded.(*Request).WantsStream()).To(BeFalse())
		})
	})

	Describe("ErrorMessage", func() {
		It("Should satisfy the error interface", func() {
			var err error = NewError("boom")
			Expect(err.Error()).To(Equal("boom"))
		})
	})

	Describe("Header", func() {
		It("Should marshal flat into the body", func() {
			req := NewRequest("p")
			req.ID = "id1"
			req.Sender = Identity{Name: "agent-a"}

			body, err := json.Marshal(req)
			Expect(err).ToNot(HaveOccurred())

			var fields map[string]any
			Expect(json.Unmarshal(body, &fields)).To(Succeed())
			Expect(fields).To(HaveKey("protocol"))
			Expect(fields).To(HaveKey("id"))
			Expect(fields).To(HaveKey("sender"))
			Expect(fields).To(HaveKey("prompt"))
		})
	})
})
