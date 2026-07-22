//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package jetstream

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/memory"
)

// These specs cover construction failures that surface before any NATS access, so
// they need no server and run in the unit suite.
var _ = Describe("newStore construction", func() {
	It("Should require a bucket", func() {
		_, err := newStore(memory.RuntimeEnv{}, "agent", []byte(`{}`))
		Expect(err).To(MatchError(ContainSubstring("options.bucket is required")))
	})

	It("Should require a NATS connection", func() {
		_, err := newStore(memory.RuntimeEnv{}, "agent", []byte(`{"bucket":"mem"}`))
		Expect(err).To(MatchError(ContainSubstring("requires a NATS connection")))
	})

	It("Should reject an unknown option key", func() {
		_, err := newStore(memory.RuntimeEnv{}, "agent", []byte(`{"bucket":"mem","nonesuch":"x"}`))
		Expect(err).To(MatchError(ContainSubstring("invalid jetstream memory options")))
	})

	It("Should reject an explicit prefix that is not a legal key", func() {
		_, err := newStore(memory.RuntimeEnv{}, "agent", []byte(`{"bucket":"mem","prefix":"bad/prefix"}`))
		Expect(err).To(MatchError(ContainSubstring("options.prefix")))
	})

	It("Should reject an identity that cannot be used as a default prefix", func() {
		_, err := newStore(memory.RuntimeEnv{}, "bad/identity", []byte(`{"bucket":"mem"}`))
		Expect(err).To(MatchError(ContainSubstring("cannot be used as a key prefix")))
	})
})
