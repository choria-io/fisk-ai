//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package llm

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("mergeEnvNames", func() {
	It("Should return the sorted, deduplicated union across lists", func() {
		got := mergeEnvNames([][]string{
			{"B_KEY", "A_KEY"},
			{"A_KEY", "C_KEY"},
		})
		Expect(got).To(Equal([]string{"A_KEY", "B_KEY", "C_KEY"}))
	})

	It("Should trim whitespace and drop empty names", func() {
		got := mergeEnvNames([][]string{
			{"  A_KEY  ", "", "   "},
			{"A_KEY"},
		})
		Expect(got).To(Equal([]string{"A_KEY"}))
	})

	It("Should be case sensitive so distinct casings both survive", func() {
		got := mergeEnvNames([][]string{{"a_key", "A_KEY"}})
		Expect(got).To(Equal([]string{"A_KEY", "a_key"}))
	})

	It("Should return nil for no names", func() {
		Expect(mergeEnvNames(nil)).To(BeNil())
		Expect(mergeEnvNames([][]string{{}, {""}})).To(BeNil())
	})
})
