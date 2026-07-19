//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package nats

import (
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/a2a"
	"github.com/choria-io/fisk-ai/internal/conns"
)

func TestNats(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/A2A/Nats")
}

var _ = Describe("Subjects", func() {
	It("Should namespace discovery and tool subjects under the prefix and identity", func() {
		Expect(DiscoverySubject("nats")).To(Equal("choria.fisk-ai.discovery.nats"))
		Expect(ToolSubject("orders-db")).To(Equal("choria.fisk-ai.tool.orders-db"))
	})
})

var _ = Describe("newTransport", func() {
	It("Should fail when the provider carries no NATS connection", func() {
		tr, err := newTransport(conns.New(), a2a.TransportConfig{Identity: "svc"})
		Expect(err).To(MatchError(ContainSubstring("requires a NATS connection")))
		Expect(tr).To(BeNil())
	})

	It("Should reject unknown transport options strictly", func() {
		p := conns.New(conns.WithNats(&nats.Conn{}))
		_, err := newTransport(p, a2a.TransportConfig{Identity: "svc", Options: json.RawMessage(`{"nope":true}`)})
		Expect(err).To(MatchError(ContainSubstring("decoding nats transport options")))
	})

	It("Should register itself under the nats name", func() {
		Expect(a2a.Transports()).To(ContainElement("nats"))
	})
})
