//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ValidateKey", func() {
	It("Should accept keys within the shared NATS-KV and filename charset", func() {
		for _, k := range []string{"a", "build", "build.notes", "api_endpoints", "v1=beta", "a-b.c_d=e", strings.Repeat("k", maxKeyRunes)} {
			Expect(ValidateKey(k)).To(Succeed(), "key %q should be valid", k)
		}
	})

	It("Should reject an empty key", func() {
		Expect(ValidateKey("")).To(MatchError(ContainSubstring("must not be empty")))
	})

	It("Should reject a slash so a key can never traverse or nest", func() {
		Expect(ValidateKey("a/b")).To(MatchError(ContainSubstring("invalid")))
		Expect(ValidateKey("../etc/passwd")).To(MatchError(ContainSubstring("invalid")))
	})

	It("Should reject spaces and characters outside the charset", func() {
		for _, k := range []string{"a b", "a:b", "a*b", "a>b", "café", "a\tb"} {
			Expect(ValidateKey(k)).To(HaveOccurred(), "key %q should be rejected", k)
		}
	})

	It("Should reject a leading or trailing dot and empty tokens", func() {
		Expect(ValidateKey(".hidden")).To(MatchError(ContainSubstring("start or end with '.'")))
		Expect(ValidateKey("trailing.")).To(MatchError(ContainSubstring("start or end with '.'")))
		Expect(ValidateKey("a..b")).To(MatchError(ContainSubstring("'..'")))
		Expect(ValidateKey("..")).To(HaveOccurred())
	})

	It("Should reject an over-long key", func() {
		Expect(ValidateKey(strings.Repeat("k", maxKeyRunes+1))).To(MatchError(ContainSubstring("too long")))
	})
})
