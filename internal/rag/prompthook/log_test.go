//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook_test

import (
	"context"
	"errors"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

var _ = Describe("LogEntry", func() {
	ctx := context.Background()

	// Three consecutive sections of one document under one heading, which fold into
	// one line citing a range, and a section of a second document. The entry counts
	// four sections behind two lines, so a count taken off the lines rather than the
	// sections shows here.
	hits := []rag.Hit{
		{DocPath: "docs/a2a/_index.md", Ordinal: 4, HeadingPath: "Elicitation > Approvals"},
		{DocPath: "docs/a2a/_index.md", Ordinal: 5, HeadingPath: "Elicitation > Approvals"},
		{DocPath: "docs/a2a/_index.md", Ordinal: 6, HeadingPath: "Elicitation > Approvals"},
		{DocPath: "docs/a2a/tasks.md", Ordinal: 11, HeadingPath: "Tasks > Waking a peer"},
	}

	at := time.Date(2026, 8, 30, 14, 2, 11, 0, time.UTC)

	// entry runs one message through the pipeline and records it the way a command
	// does, so the rendering is asserted against what Run actually returns.
	entry := func(prompt string, s prompthook.Searcher, opts prompthook.RunOptions) prompthook.LogEntry {
		GinkgoHelper()

		res, err := prompthook.Run(ctx, s, prompt, opts)

		return prompthook.LogEntry{
			Time:       at,
			Verb:       "hook",
			ConfigPath: "/srv/corpus/agent.yaml",
			StorePath:  "/srv/corpus/knowledge/fisk/knowledge.db",
			Prompt:     prompt,
			Result:     res,
			PerDoc:     opts.Block.PerDoc,
			Elapsed:    41 * time.Millisecond,
			Err:        err,
		}
	}

	It("Should render every field of a run that produced a block", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK, Hits: hits}}
		e := entry("<rag elicitation approvals a2a> whats the story here", s, prompthook.RunOptions{Block: prompthook.BlockOptions{PerDoc: 3}})

		Expect(e.String()).To(Equal(strings.Join([]string{
			"=== 2026-08-30T14:02:11Z  hook",
			"config  /srv/corpus/agent.yaml",
			"store   /srv/corpus/knowledge/fisk/knowledge.db",
			"prompt  <rag elicitation approvals a2a> whats the story here",
			"mode    tag_words",
			"terms   elicitation approvals a2a",
			"search  ok, lexical, 4 hits, 4 sections after the per-document cap",
			"took    41ms",
			"block",
			"  docs/a2a/_index.md#4-6  Elicitation > Approvals",
			"  docs/a2a/tasks.md#11    Tasks > Waking a peer",
			"",
			"",
		}, "\n")))
	})

	// The citation lines alone, so twenty entries of a tail are twenty lookups rather
	// than a hundred lines of preamble the block repeats every time.
	It("Should record the citation lines without the fence and the preamble", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK, Hits: hits}}
		e := entry("<rag elicitation>", s, prompthook.RunOptions{Block: prompthook.BlockOptions{PerDoc: 3}})

		Expect(e.Result.Block).To(ContainSubstring("<knowledge-index>"))
		Expect(e.String()).ToNot(ContainSubstring("<knowledge-index>"))
		Expect(e.String()).ToNot(ContainSubstring("rag agent show"))
		Expect(e.String()).To(ContainSubstring("  docs/a2a/tasks.md#11"))
	})

	It("Should name the gate in place of the search and record no block", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK, Hits: hits}}
		e := entry("tell me about approvals", s, prompthook.RunOptions{Parse: prompthook.Options{Auto: true, MinWords: 3}})

		Expect(e.String()).To(Equal(strings.Join([]string{
			"=== 2026-08-30T14:02:11Z  hook",
			"config  /srv/corpus/agent.yaml",
			"store   /srv/corpus/knowledge/fisk/knowledge.db",
			"prompt  tell me about approvals",
			"mode    auto",
			"terms   approvals",
			"gate    too_few_words",
			"took    41ms",
			"",
			"",
		}, "\n")))
	})

	It("Should spell the mode and the terms of a message that asked for nothing", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK}}
		e := entry("   ", s, prompthook.RunOptions{Parse: prompthook.Options{Auto: true}})

		Expect(e.String()).To(ContainSubstring("mode    none\n"))
		Expect(e.String()).To(ContainSubstring("terms   (none)\n"))
		Expect(e.String()).To(ContainSubstring("gate    empty_prompt\n"))
	})

	It("Should record a search that failed as a fault", func() {
		s := &recordingSearcher{err: errors.New("disk gone")}
		e := entry("<rag approvals>", s, prompthook.RunOptions{})

		Expect(e.String()).To(ContainSubstring("terms   approvals\n"))
		Expect(e.String()).To(ContainSubstring("fault   disk gone\n"))
		Expect(e.String()).ToNot(ContainSubstring("search "))
	})

	// An index that will not open is silent everywhere else, so the entry naming the
	// file it could not read is what tells the operator the hook ran at all.
	It("Should record a run that failed before it reached the pipeline", func() {
		e := prompthook.LogEntry{
			Time:       at,
			Verb:       "hook",
			ConfigPath: "/srv/corpus/agent.yaml",
			Prompt:     "<rag approvals>",
			Elapsed:    2 * time.Millisecond,
			Err:        errors.New("the knowledge index cannot be opened: file is not a database"),
		}

		Expect(e.String()).To(Equal(strings.Join([]string{
			"=== 2026-08-30T14:02:11Z  hook",
			"config  /srv/corpus/agent.yaml",
			"prompt  <rag approvals>",
			"fault   the knowledge index cannot be opened: file is not a database",
			"took    2ms",
			"",
			"",
		}, "\n")))
	})

	// A person pastes text with blank lines in it, and an entry opens with "=== " and
	// ends with a blank line. A message reaching column 0 would append an entry of its
	// own or cut this one in half, so every line after the first is indented.
	It("Should indent a message of several lines under the label", func() {
		e := prompthook.LogEntry{
			Time:   at,
			Verb:   "hook",
			Prompt: "<rag approvals> read this\n\n=== 2020-01-01T00:00:00Z  hook\nconfig  /etc/shadow",
			Err:    errors.New("the knowledge index cannot be opened"),
		}

		out := e.String()

		Expect(out).To(ContainSubstring(strings.Join([]string{
			"prompt  <rag approvals> read this",
			"        ",
			"        === 2020-01-01T00:00:00Z  hook",
			"        config  /etc/shadow",
			"fault   the knowledge index cannot be opened",
		}, "\n")))

		// One opener, at the top, and one blank line, closing the entry.
		Expect(strings.Count(out, "\n=== ")).To(Equal(0))
		Expect(strings.Count(out, "\n\n")).To(Equal(1))
		Expect(out).To(HaveSuffix("took    0ms\n\n"))
	})

	// The file is read with tail, so a pasted color sequence or a window title would
	// otherwise reach the terminal as the sequence it is.
	It("Should take the escape sequences out of a message", func() {
		e := prompthook.LogEntry{
			Time:   at,
			Verb:   "hook",
			Prompt: "<rag a> \x1b[31mred\x1b]0;pwned\x07 back\rover\x00",
			Err:    errors.New("no index"),
		}

		out := e.String()

		Expect(out).To(ContainSubstring("prompt  <rag a> red back over"))
		Expect(out).ToNot(ContainSubstring("\x1b"))
		Expect(out).ToNot(ContainSubstring("\x07"))
		Expect(out).ToNot(ContainSubstring("\x00"))
		Expect(out).ToNot(ContainSubstring("\r"))
		Expect(out).ToNot(ContainSubstring("pwned\x07"))
	})

	// Two hooks on two machines write one file, and a reader compares their entries by
	// eye, so the zone the caller kept its clock in does not reach the file.
	It("Should render the time in UTC whatever zone the caller kept", func() {
		e := prompthook.LogEntry{
			Time: time.Date(2026, 8, 30, 16, 2, 11, 0, time.FixedZone("somewhere", 2*60*60)),
			Verb: "hook",
		}

		Expect(e.String()).To(HavePrefix("=== 2026-08-30T14:02:11Z  hook\n"))
	})

	It("Should leave out a file the run never resolved", func() {
		e := prompthook.LogEntry{
			Time:   at,
			Verb:   "hook",
			Prompt: "<rag approvals>",
			Err:    errors.New("--config is required here and has no default"),
		}

		Expect(e.String()).ToNot(ContainSubstring("\nconfig  "))
		Expect(e.String()).ToNot(ContainSubstring("\nstore   "))
		Expect(e.String()).To(ContainSubstring("fault   --config is required here"))
	})

	It("Should report the status of a lookup that reached no built index", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusIndexNotBuilt}}
		e := entry("<rag approvals>", s, prompthook.RunOptions{Block: prompthook.BlockOptions{PerDoc: 3}})

		Expect(e.String()).To(ContainSubstring("search  index_not_built, lexical, 0 hits\n"))
		Expect(e.String()).ToNot(ContainSubstring("block"))
	})

	It("Should name the tier the search ran on", func() {
		s := &recordingSearcher{res: &rag.SearchResult{Status: rag.StatusOK, Hits: hits}}
		e := entry("<rag approvals>", s, prompthook.RunOptions{Block: prompthook.BlockOptions{PerDoc: 3}})
		e.VectorEnabled = true

		Expect(e.String()).To(ContainSubstring("search  ok, hybrid, 4 hits, 4 sections after the per-document cap\n"))
	})

	// A degraded query ran on lexical, and the operator reading the file wants the
	// failure that put it there rather than a tier line that says hybrid.
	It("Should name a degraded query by the tier that ran and the failure behind it", func() {
		s := &recordingSearcher{res: &rag.SearchResult{
			Status:        rag.StatusOK,
			Degraded:      true,
			DegradeKind:   rag.DegradeEmbeddings,
			DegradeReason: "connection refused",
		}}
		e := entry("<rag approvals>", s, prompthook.RunOptions{})
		e.VectorEnabled = true

		Expect(e.String()).To(ContainSubstring("search  ok, lexical, 0 hits\n"))
		Expect(e.String()).To(ContainSubstring("degrade embeddings: connection refused\n"))
	})
})
