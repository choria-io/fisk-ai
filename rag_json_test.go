//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag"
)

var _ = Describe("knowledge JSON rendering", func() {
	Describe("search", func() {
		hit := rag.Hit{
			ChunkID:        7,
			Citation:       "docs/design.md#3",
			DocPath:        "docs/design.md",
			Ordinal:        3,
			HeadingPath:    "Design > Cancellation",
			Content:        "the run is canceled by closing the context",
			MappedCitation: "docs/design.md#3",
		}

		It("omits chunk bodies unless full is asked for", func() {
			out := newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{hit}}, true, false)
			Expect(out.Hits).To(HaveLen(1))
			Expect(out.Hits[0].Content).To(BeEmpty())
			Expect(out.Hits[0].Citation).To(Equal("docs/design.md#3"))
			Expect(out.Hits[0].HeadingPath).To(Equal("Design > Cancellation"))

			full := newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{hit}}, true, true)
			Expect(full.Hits[0].Content).To(Equal("the run is canceled by closing the context"))
		})

		// Unmapped, MappedCitation holds the citation token itself. Emitting it would
		// present a corpus path as a published address.
		It("carries a mapped citation only when a rule matched", func() {
			out := newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{hit}}, true, false)
			Expect(out.Hits[0].Mapped).To(BeFalse())
			Expect(out.Hits[0].MappedCitation).To(BeEmpty())

			mapped := hit
			mapped.Mapped = true
			mapped.MappedCitation = "https://example.net/design#cancellation"

			out = newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{mapped}}, true, false)
			Expect(out.Hits[0].Mapped).To(BeTrue())
			Expect(out.Hits[0].MappedCitation).To(Equal("https://example.net/design#cancellation"))
		})

		// A hit on the first chunk of a document has no heading path, so the title tells
		// a consumer which document answered. A document with no heading has no title,
		// and the key is left out rather than emitted empty.
		It("carries the document title only when the document has one", func() {
			titled := hit
			titled.DocTitle = "Design"

			body, err := json.Marshal(newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{titled}}, true, false))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`"doc_title":"Design"`))

			body, err = json.Marshal(newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{hit}}, true, false))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).ToNot(ContainSubstring("doc_title"))
		})

		// The tier cannot be read off Degraded: a store with no embeddings is lexical
		// and has not degraded.
		It("reports the configured tier separately from a degradation", func() {
			out := newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK}, false, false)
			Expect(out.VectorEnabled).To(BeFalse())
			Expect(out.Degraded).To(BeFalse())

			out = newRAGSearchJSON("cancel", &rag.SearchResult{
				Status:        rag.StatusOK,
				Degraded:      true,
				DegradeKind:   rag.DegradeTimeout,
				DegradeReason: "context deadline exceeded",
			}, true, false)
			Expect(out.VectorEnabled).To(BeTrue())
			Expect(out.Degraded).To(BeTrue())
			Expect(out.DegradeKind).To(Equal("timeout"))
			Expect(out.DegradeReason).To(Equal("context deadline exceeded"))
		})

		// A soft outcome is a status a consumer branches on, not a failure and not a
		// null it has to guard before ranging over.
		It("renders an unbuilt index as a status with an empty hit array", func() {
			body, err := json.Marshal(newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusIndexNotBuilt}, false, false))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`"status":"index_not_built"`))
			Expect(string(body)).To(ContainSubstring(`"hits":[]`))
		})

		// Sanitizing rewrites what the corpus holds, which is right for a screen and
		// wrong for a parser; the encoder escapes the control characters instead, and
		// the value survives a round trip byte for byte.
		It("keeps corpus text unsanitized and lets the encoder escape it", func() {
			raw := hit
			raw.Content = "before\x1b[31mafter"

			body, err := json.Marshal(newRAGSearchJSON("cancel", &rag.SearchResult{Status: rag.StatusOK, Hits: []rag.Hit{raw}}, true, true))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).ToNot(ContainSubstring("\x1b"))
			Expect(string(body)).To(ContainSubstring("before\\u001b[31mafter"))

			var back ragSearchJSON
			Expect(json.Unmarshal(body, &back)).To(Succeed())
			Expect(back.Hits[0].Content).To(Equal("before\x1b[31mafter"))
		})
	})

	Describe("match", func() {
		res := &rag.EnumerateResult{
			Status:           rag.EnumOK,
			Compiled:         `"cancel"`,
			Matched:          2,
			Returned:         1,
			Truncated:        true,
			IndexedDocuments: 40,
			Docs: []rag.MatchedDoc{{
				Path:           "docs/design.md",
				Citation:       "docs/design.md#1",
				MappedCitation: "docs/design.md#1",
				BodyMatches:    3,
				HeadingMatches: 1,
				TotalChunks:    9,
			}},
			Terms: []rag.TermReport{{
				Surface: "cancel",
				Stem:    "cancel",
				Docs:    2,
				Literal: 1,
				Related: []string{"cancellation"},
			}},
		}

		It("carries the counts and the term reports", func() {
			out := newRAGMatchJSON("cancel", res)

			Expect(out.Matched).To(Equal(2))
			Expect(out.Returned).To(Equal(1))
			Expect(out.Truncated).To(BeTrue())
			Expect(out.IndexedDocuments).To(Equal(40))
			Expect(out.Compiled).To(Equal(`"cancel"`))

			Expect(out.Documents).To(HaveLen(1))
			Expect(out.Documents[0].Path).To(Equal("docs/design.md"))
			Expect(out.Documents[0].BodyMatches).To(Equal(3))
			Expect(out.Documents[0].Mapped).To(BeFalse())
			Expect(out.Documents[0].MappedCitation).To(BeEmpty())

			Expect(out.Terms).To(HaveLen(1))
			Expect(out.Terms[0].Documents).To(Equal(2))
			Expect(out.Terms[0].Literal).To(Equal(1))
			Expect(out.Terms[0].Related).To(Equal([]string{"cancellation"}))
		})

		// An empty query is an error on the screen because a printed zero would read
		// as absence. The status says which case it is, so the machine format reports
		// it as an answer.
		It("renders an empty query as a status with empty arrays", func() {
			body, err := json.Marshal(newRAGMatchJSON("a", &rag.EnumerateResult{Status: rag.EnumQueryEmpty}))
			Expect(err).ToNot(HaveOccurred())
			Expect(string(body)).To(ContainSubstring(`"status":"query_empty"`))
			Expect(string(body)).To(ContainSubstring(`"documents":[]`))
			Expect(string(body)).To(ContainSubstring(`"terms":[]`))
		})
	})
})
