//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Chunking", func() {
	It("builds a heading breadcrumb and folds it into the content", func() {
		md := "# Design\n\n## Backpressure\n\nThe buffer fills and producers slow down.\n"
		chunks := ChunkDocument(md)

		Expect(chunks).To(HaveLen(1))
		Expect(chunks[0].HeadingPath).To(Equal("Design > Backpressure"))
		Expect(chunks[0].Content).To(HavePrefix("Design > Backpressure"))
		Expect(chunks[0].Content).To(ContainSubstring("producers slow down"))
	})

	It("keeps a fenced code block intact even when it exceeds the chunk size", func() {
		var b strings.Builder
		b.WriteString("# Code\n\n```go\n")
		for i := 0; i < 200; i++ {
			b.WriteString("line of code that is fairly long to push past the target size\n")
		}
		b.WriteString("```\n")

		chunks := ChunkDocument(b.String())

		fenced := 0
		for _, c := range chunks {
			if strings.Contains(c.Content, "```go") {
				fenced++
				Expect(strings.Count(c.Content, "```")).To(Equal(2), "the fence must open and close in the same chunk")
			}
		}
		Expect(fenced).To(Equal(1))
	})

	It("does not treat a # inside a code fence as a heading", func() {
		md := "# Real\n\n```\n# not a heading\n```\n\nbody\n"
		chunks := ChunkDocument(md)

		for _, c := range chunks {
			Expect(c.HeadingPath).ToNot(ContainSubstring("not a heading"))
		}
	})

	It("packs multiple small sections and returns the first heading as the title", func() {
		md := "# Top\n\nintro paragraph\n\n## A\n\nalpha\n\n## B\n\nbravo\n"
		Expect(DocumentTitle(md)).To(Equal("Top"))
		Expect(ChunkDocument(md)).ToNot(BeEmpty())
	})

	It("returns no chunks for empty or whitespace-only input", func() {
		Expect(ChunkDocument("")).To(BeEmpty())
		Expect(ChunkDocument("   \n\n  \n")).To(BeEmpty())
	})
})
