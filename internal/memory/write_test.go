//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ValidateWrite", func() {
	It("Should return the normalized single-line description", func() {
		desc, err := ValidateWrite("k", "line one\nline two\t indented", "c")
		Expect(err).ToNot(HaveOccurred())
		Expect(desc).To(Equal("line one line two indented"))
	})

	It("Should reject a description that is empty after normalization", func() {
		_, err := ValidateWrite("k", "   ", "c")
		Expect(err).To(MatchError(ContainSubstring("description must not be empty")))
	})

	It("Should reject content over the size limit", func() {
		_, err := ValidateWrite("k", "d", strings.Repeat("x", MaxContentBytes+1))
		Expect(err).To(MatchError(ContainSubstring("too large")))
	})

	It("Should reject an invalid key before anything else", func() {
		_, err := ValidateWrite("bad/key", "d", "c")
		Expect(err).To(MatchError(ContainSubstring("invalid")))
	})
})

var _ = Describe("CheckCapacity", func() {
	It("Should permit a create below the cap", func() {
		Expect(CheckCapacity(MaxEntries - 1)).To(Succeed())
	})

	It("Should reject a create at or above the cap", func() {
		Expect(CheckCapacity(MaxEntries)).To(MatchError(ContainSubstring("memory is full")))
	})
})
