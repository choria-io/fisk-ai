//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Confirm", func() {
	Describe("NeedsConfirm", func() {
		It("Should gate a tool carrying the always-on ConfirmTag regardless of extra tags", func() {
			Expect(NeedsConfirm([]string{ConfirmTag}, nil)).To(BeTrue())
			Expect(NeedsConfirm([]string{"impact:rw", ConfirmTag}, nil)).To(BeTrue())
		})

		It("Should gate a tool whose tag the operator listed under extra tags", func() {
			Expect(NeedsConfirm([]string{"impact:rw"}, []string{"impact:rw"})).To(BeTrue())
		})

		It("Should not gate a tool with no matching tag", func() {
			Expect(NeedsConfirm([]string{"impact:ro"}, []string{"impact:rw"})).To(BeFalse())
			Expect(NeedsConfirm(nil, []string{"impact:rw"})).To(BeFalse())
		})
	})

	Describe("ConfirmTrigger", func() {
		It("Should name the always-on ConfirmTag ahead of any extra tag", func() {
			Expect(ConfirmTrigger([]string{"impact:rw", ConfirmTag}, []string{"impact:rw"})).To(Equal(ConfirmTag))
		})

		It("Should name the first tool tag matching an extra tag, in the tool's tag order", func() {
			Expect(ConfirmTrigger([]string{"impact:rw", "danger"}, []string{"danger", "impact:rw"})).To(Equal("impact:rw"))
		})

		It("Should return empty for a tool that is not gated", func() {
			Expect(ConfirmTrigger([]string{"impact:ro"}, []string{"impact:rw"})).To(Equal(""))
			Expect(ConfirmTrigger(nil, nil)).To(Equal(""))
		})
	})
})
