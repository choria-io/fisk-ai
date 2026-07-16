//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"math"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("FTS query building", func() {
	It("reduces free text to OR-ed quoted terms of at least two runes", func() {
		Expect(ftsQuery("how does backpressure work")).To(Equal(`"how" OR "does" OR "backpressure" OR "work"`))
		Expect(ftsQuery("a bb ccc")).To(Equal(`"bb" OR "ccc"`)) // single-char "a" dropped
	})

	It("quotes every term so punctuation and quotes cannot break out of MATCH syntax", func() {
		// Punctuation (including a double quote) is a term delimiter, so terms are
		// alphanumeric runs, each wrapped in its own quotes; nothing can escape into
		// MATCH operator syntax.
		out := ftsQuery(`drop" table; injection`)
		Expect(out).To(Equal(`"drop" OR "table" OR "injection"`))
		// The number of quote characters is exactly two per term (balanced).
		Expect(strings.Count(out, `"`) % 2).To(Equal(0))
	})

	It("clamps a pathological many-term query", func() {
		var b strings.Builder
		for i := 0; i < 200; i++ {
			b.WriteString("term ")
		}
		out := ftsQuery(b.String())
		Expect(strings.Count(out, " OR ")).To(BeNumerically("<=", maxFTSTerms-1))
	})

	It("returns empty for a query with no searchable terms", func() {
		Expect(ftsQuery("   ")).To(Equal(""))
		Expect(ftsQuery("! @ # $")).To(Equal(""))
	})
})

var _ = Describe("RRF fusion", func() {
	It("fuses on rank and breaks ties deterministically by chunk id", func() {
		lex := []result{{chunkID: 1}, {chunkID: 2}, {chunkID: 3}}
		vec := []result{{chunkID: 3}, {chunkID: 2}, {chunkID: 1}}

		fused := rrf([][]result{lex, vec})

		// chunks 1 and 3 each score 1/61 + 1/63 (rank 0 in one list, rank 2 in the
		// other), just above chunk 2's 1/62 + 1/62, so they lead. The 1/3 tie breaks
		// to the smaller id first.
		Expect(fused[0].chunkID).To(Equal(int64(1)))
		Expect(fused[1].chunkID).To(Equal(int64(3)))
		Expect(fused[2].chunkID).To(Equal(int64(2)))
	})
})

var _ = Describe("Normalization", func() {
	It("produces a unit-length vector within tolerance", func() {
		v := normalize([]float32{3, 4, 0, 12})
		var sum float64
		for _, f := range v {
			sum += float64(f) * float64(f)
		}
		Expect(math.Abs(math.Sqrt(sum) - 1)).To(BeNumerically("<=", 1e-6))
	})

	It("leaves a zero vector unchanged", func() {
		Expect(normalize([]float32{0, 0, 0})).To(Equal([]float32{0, 0, 0}))
	})
})
