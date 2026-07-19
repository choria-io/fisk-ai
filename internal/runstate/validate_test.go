//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ValidateID", func() {
	It("Should accept a KSUID and an operator name", func() {
		Expect(ValidateID("2abcXYZ012345678901234567")).To(Succeed())
		Expect(ValidateID("my-run_1")).To(Succeed())
	})

	It("Should reject a traversal, a separator, or a leading dot", func() {
		for _, id := range []string{"../evil", "a/b", ".hidden", "-lead", "", "a b"} {
			Expect(ValidateID(id)).To(MatchError(ErrInvalidID), id)
		}
	})

	It("Should reject an over-long id even when the charset is valid", func() {
		Expect(ValidateID(strings.Repeat("a", 129))).To(MatchError(ErrInvalidID))
		Expect(ValidateID(strings.Repeat("a", 128))).To(Succeed())
	})
})

var _ = Describe("CheckAppend", func() {
	It("Should report the next seq as neither a skip nor a gap", func() {
		skip, err := CheckAppend(4, 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(skip).To(BeFalse())
	})

	It("Should treat a duplicate or older seq as an idempotent skip", func() {
		skip, err := CheckAppend(5, 5)
		Expect(err).NotTo(HaveOccurred())
		Expect(skip).To(BeTrue())

		skip, err = CheckAppend(5, 3)
		Expect(err).NotTo(HaveOccurred())
		Expect(skip).To(BeTrue())
	})

	It("Should reject a seq that skips ahead of the journal", func() {
		skip, err := CheckAppend(5, 7)
		Expect(err).To(MatchError(ErrSeqGap))
		Expect(skip).To(BeFalse())
	})
})
