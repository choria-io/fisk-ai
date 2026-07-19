//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package conns

import (
	"testing"

	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestConns(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/Conns")
}

var _ = Describe("Provider", func() {
	Describe("Nats", func() {
		It("Should return the provisioned connection", func() {
			nc := &nats.Conn{}
			Expect(New(WithNats(nc)).Nats()).To(BeIdenticalTo(nc))
		})

		It("Should return nil when no NATS connection was provisioned", func() {
			Expect(New().Nats()).To(BeNil())
		})

		It("Should be nil-safe on a nil Provider", func() {
			var p *Provider
			Expect(p.Nats()).To(BeNil())
		})
	})
})
