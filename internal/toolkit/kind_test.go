//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Kind", func() {
	// allKinds is every provider kind. Go cannot force a switch to be exhaustive, so
	// the token test walks this list to assert every kind resolves to a token; a new
	// kind added without a String case or an entry here is what these tests catch.
	allKinds := []Kind{KindUnknown, KindApplication, KindBuiltin, KindRemote, KindCustom}

	It("Should give every kind a distinct, non-empty token", func() {
		seen := map[string]Kind{}
		for _, k := range allKinds {
			token := k.String()
			Expect(token).NotTo(BeEmpty(), "kind %d has no token", int(k))
			_, dup := seen[token]
			Expect(dup).To(BeFalse(), "token %q is shared by two kinds", token)
			seen[token] = k
		}
	})

	It("Should map each kind to its stable token", func() {
		Expect(KindUnknown.String()).To(Equal("unknown"))
		Expect(KindApplication.String()).To(Equal("application"))
		Expect(KindBuiltin.String()).To(Equal("builtin"))
		Expect(KindRemote.String()).To(Equal("remote"))
		Expect(KindCustom.String()).To(Equal("custom"))
	})

	It("Should be the safe sentinel at the zero value", func() {
		var zero Kind
		Expect(zero).To(Equal(KindUnknown))
		Expect(zero.String()).To(Equal("unknown"))
	})

	It("Should fall back to the unknown token for an unrecognized value", func() {
		Expect(Kind(99).String()).To(Equal("unknown"))
	})
})
