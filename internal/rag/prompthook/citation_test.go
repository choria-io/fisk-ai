//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

var _ = Describe("ParseCitation", func() {
	DescribeTable("Should read both citation forms",
		func(citation string, path string, first int, last int) {
			gotPath, gotFirst, gotLast, err := prompthook.ParseCitation(citation)

			Expect(err).ToNot(HaveOccurred())
			Expect(gotPath).To(Equal(path))
			Expect(gotFirst).To(Equal(first))
			Expect(gotLast).To(Equal(last))
		},
		Entry("a single ordinal repeats as first and last", "docs/a2a.md#4", "docs/a2a.md", 4, 4),
		Entry("the first chunk of a document", "docs/a2a.md#0", "docs/a2a.md", 0, 0),
		Entry("a range", "docs/a2a.md#4-6", "docs/a2a.md", 4, 6),
		Entry("a range of one section", "docs/a2a.md#4-4", "docs/a2a.md", 4, 4),
		Entry("a path holding a hash keeps it", "docs/c#4/a2a.md#7", "docs/c#4/a2a.md", 7, 7),
		Entry("a path holding a hash under a range", "docs/c#4/a2a.md#7-9", "docs/c#4/a2a.md", 7, 9),
	)

	DescribeTable("Should refuse a token it cannot resolve",
		func(citation string) {
			_, _, _, err := prompthook.ParseCitation(citation)

			Expect(err).To(HaveOccurred())
		},
		Entry("a range ending before it starts", "docs/a2a.md#6-4"),
		Entry("a path with no ordinal", "docs/a2a.md"),
		Entry("a hash with nothing behind it", "docs/a2a.md#"),
		Entry("a range missing its last ordinal", "docs/a2a.md#4-"),
		Entry("a range missing its first ordinal", "docs/a2a.md#-6"),
		Entry("a range that is only its separator", "docs/a2a.md#-"),
		Entry("an ordinal that is not a number", "docs/a2a.md#four"),
		Entry("a third ordinal", "docs/a2a.md#4-6-8"),
		Entry("no path", "#4"),
		Entry("nothing at all", ""),
	)
})
