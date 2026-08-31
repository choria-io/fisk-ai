//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook_test

import (
	"context"
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// recordingSearcher answers one canned result and remembers what it was asked, so a
// spec can assert on the query the terms became and on the count that reached the
// index.
type recordingSearcher struct {
	res  *rag.SearchResult
	err  error
	q    string
	topK int
	runs int
}

func (s *recordingSearcher) Search(_ context.Context, query string, topK int) (*rag.SearchResult, error) {
	s.runs++
	s.q = query
	s.topK = topK

	return s.res, s.err
}

var _ = Describe("Run", func() {
	ctx := context.Background()

	hits := []rag.Hit{
		{DocPath: "docs/guide.md", Ordinal: 1, HeadingPath: "Guide > Framing"},
		{DocPath: "docs/guide.md", Ordinal: 5, HeadingPath: "Guide > Cache"},
	}

	ok := func() *recordingSearcher {
		return &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK, Hits: hits}}
	}

	It("Should search for the terms the message was left with and render them", func() {
		s := ok()

		res, err := prompthook.Run(ctx, s, "<rag> how does framing work", prompthook.RunOptions{TopK: 8})

		Expect(err).ToNot(HaveOccurred())
		Expect(s.q).To(Equal("framing work"))
		Expect(s.topK).To(Equal(8))
		Expect(res.Decision.Mode).To(Equal(prompthook.ModeTag))
		Expect(res.Search.Status).To(Equal(rag.StatusOK))
		Expect(res.Block).To(ContainSubstring("docs/guide.md#1"))
		Expect(res.Block).To(ContainSubstring("Guide > Cache"))
		Expect(res.Outcome()).To(Equal(prompthook.OutcomeBlock))
	})

	It("Should take the block options from the caller and the mode from the decision", func() {
		s := ok()

		res, err := prompthook.Run(ctx, s, "<rag framing cache>", prompthook.RunOptions{
			Block: prompthook.BlockOptions{BinaryPath: "/opt/fisk", ConfigPath: "/srv/corpus/agent.yaml", PerDoc: 1},
		})

		Expect(err).ToNot(HaveOccurred())
		Expect(res.Block).To(ContainSubstring("/opt/fisk rag agent show --config /srv/corpus/agent.yaml"))
		Expect(res.Block).To(ContainSubstring("The words inside the <rag ...> tag chose these sections"))
		Expect(res.Block).To(ContainSubstring("docs/guide.md#1"))
		Expect(res.Block).ToNot(ContainSubstring("docs/guide.md#5"), "the per-document cap keeps one section of the document")
	})

	It("Should return before the search when a gate stops the lookup", func() {
		s := ok()

		res, err := prompthook.Run(ctx, s, "how does framing work", prompthook.RunOptions{})

		Expect(err).ToNot(HaveOccurred())
		Expect(s.runs).To(Equal(0))
		Expect(res.Decision.Skip).To(Equal(prompthook.SkipNoTag))
		Expect(res.Search).To(BeNil())
		Expect(res.Block).To(BeEmpty())
		Expect(res.Outcome()).To(Equal(prompthook.OutcomeSkipped))
	})

	// Hits alongside a status that says the lookup never reached a built index are
	// not an answer to hand a model. The hits are what make this spec fail when the
	// status is not read.
	DescribeTable("Should render no block for a status other than ok, whatever hits came with it",
		func(status rag.SearchStatus, outcome prompthook.Outcome) {
			s := &recordingSearcher{res: &rag.SearchResult{Status: status, Hits: hits}}

			res, err := prompthook.Run(ctx, s, "<rag framing>", prompthook.RunOptions{})

			Expect(err).ToNot(HaveOccurred())
			Expect(res.Search.Status).To(Equal(status))
			Expect(res.Block).To(BeEmpty())
			Expect(res.Outcome()).To(Equal(outcome))
		},
		Entry("no index has been built", rag.StatusIndexNotBuilt, prompthook.OutcomeIndexNotBuilt),
		Entry("the index holds nothing", rag.StatusIndexEmpty, prompthook.OutcomeIndexEmpty),
	)

	It("Should render no block when nothing ranked", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK}}

		res, err := prompthook.Run(ctx, s, "<rag framing>", prompthook.RunOptions{})

		Expect(err).ToNot(HaveOccurred())
		Expect(res.Search.Status).To(Equal(rag.StatusOK))
		Expect(res.Block).To(BeEmpty())
		Expect(res.Outcome()).To(Equal(prompthook.OutcomeNoHits))
	})

	// An implementation of the exported interface may answer with neither, where
	// rag.Store never does. Reading the nil as an empty result would report an index
	// that ranked nothing.
	It("Should refuse a searcher that answers with neither a result nor an error", func() {
		s := &recordingSearcher{}

		res, err := prompthook.Run(ctx, s, "<rag framing>", prompthook.RunOptions{})

		Expect(err).To(MatchError(prompthook.ErrNoSearchResult))
		Expect(res.Search).To(BeNil())
		Expect(res.Outcome()).To(Equal(prompthook.OutcomeNoSearch))
	})

	It("Should return a search error with the decision it had already made", func() {
		s := &recordingSearcher{err: errors.New("disk gone")}

		res, err := prompthook.Run(ctx, s, "<rag framing>", prompthook.RunOptions{})

		Expect(err).To(MatchError(ContainSubstring("disk gone")))
		Expect(res.Decision.Query).To(Equal("framing"))
		Expect(res.Search).To(BeNil())
		Expect(res.Block).To(BeEmpty())
	})
})
