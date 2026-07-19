//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DefaultDenyPrompter", func() {
	ctx := context.Background()
	p := DefaultDenyPrompter()

	It("fails closed on every method, returning both a denial value and an error", func() {
		choice, err := p.ApproveCommand(ctx, GateRequest{})
		Expect(err).To(HaveOccurred())
		Expect(choice).To(Equal(ConfirmNo))

		ok, err := p.Confirm(ctx, "proceed?")
		Expect(err).To(HaveOccurred())
		Expect(ok).To(BeFalse())

		idx, err := p.Select(ctx, "which?", []string{"a", "b"})
		Expect(err).To(HaveOccurred())
		Expect(idx).To(Equal(-1))

		val, err := p.Input(ctx, "value?", "default")
		Expect(err).To(HaveOccurred())
		Expect(val).To(BeEmpty())
	})
})
