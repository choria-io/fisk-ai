//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

var _ = Describe("knowledge agent", func() {
	ctx := context.Background()

	// Four sections, so a range can cover three of them and stop inside the third.
	const guide = `# Guide

The guide covers the transport and the cache.

## Framing

Every frame carries a length prefix.

## Backpressure

The queue slows producers when the buffer fills.

## Cache

Entries expire after an hour.
`

	// No directory key, so the index lands in knowledge/<identity> relative to
	// whatever directory the command runs in. An absolute directory here would
	// resolve from anywhere and hide the change of directory the group turns on.
	const agentYAML = `identity: corpus
harness:
  knowledge:
    enabled: true
    paths:
      - docs
`

	// A configuration that parses and then fails the enabled check, which is a
	// failure the group reaches only after it has changed directory.
	const disabledYAML = `identity: corpus
harness:
  knowledge:
    enabled: false
`

	// A configuration naming a document as the store directory, which the group
	// reaches one step later still: the parse passes and rag.Open fails.
	const notADirYAML = `identity: corpus
harness:
  knowledge:
    enabled: true
    directory: docs/guide.md
`

	var (
		tmp    string
		corpus string
		cfg    *config.Config
	)

	// inCorpus runs fn in the corpus directory and puts the process back where it
	// was. Ginkgo runs the specs of one suite in a single process, so a spec that
	// walks away from its working directory takes every later spec with it.
	inCorpus := func(fn func()) {
		GinkgoHelper()

		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(corpus)).To(Succeed())
		defer func() {
			Expect(os.Chdir(prior)).To(Succeed())
		}()

		fn()
	}

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		corpus = filepath.Join(tmp, "corpus")

		Expect(os.MkdirAll(filepath.Join(corpus, "docs"), 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(corpus, "docs", "guide.md"), []byte(guide), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(corpus, "agent.yaml"), []byte(agentYAML), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(corpus, "disabled.yaml"), []byte(disabledYAML), 0o644)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(corpus, "notadir.yaml"), []byte(notADirYAML), 0o644)).To(Succeed())

		// Indexed from the corpus the way an operator indexes it, which stores the
		// documents as docs/guide.md and puts the index under the corpus.
		inCorpus(func() {
			var err error

			cfg, err = config.ParseConfigFileForMode("agent.yaml", config.ModeMCP)
			Expect(err).ToNot(HaveOccurred())

			w, err := rag.OpenWriter(cfg, "")
			Expect(err).ToNot(HaveOccurred())
			_, err = w.Index(ctx, []string{"docs"}, rag.IndexOptions{Reconcile: true})
			Expect(err).ToNot(HaveOccurred())
			Expect(w.Close()).To(Succeed())
		})

		// Every spec runs from a directory holding neither the configuration nor the
		// index, which is where Claude runs these verbs from.
		prior, err := os.Getwd()
		Expect(err).ToNot(HaveOccurred())
		Expect(os.Chdir(tmp)).To(Succeed())
		DeferCleanup(func() {
			Expect(os.Chdir(prior)).To(Succeed())
		})
	})

	// run parses args as the fisk binary does and returns what the action printed.
	run := func(args ...string) (string, error) {
		GinkgoHelper()

		app := fisk.New("fisk", "Fisk AI Toolkit")
		registerRAGCommand(app)

		r, w, err := os.Pipe()
		Expect(err).ToNot(HaveOccurred())

		prior := os.Stdout
		os.Stdout = w

		_, runErr := app.Parse(args)

		os.Stdout = prior
		Expect(w.Close()).To(Succeed())

		out, err := io.ReadAll(r)
		Expect(err).ToNot(HaveOccurred())
		Expect(r.Close()).To(Succeed())

		return string(out), runErr
	}

	// showFromCorpus renders one citation with the process already in the corpus, so
	// a spec can drive showKnowledgeChunks without the command around it. The store
	// is opened and read in the same directory: the reader pool retires an idle
	// connection and opens another from the same relative path, so a query made
	// elsewhere looks for the index under that directory instead.
	showFromCorpus := func(citation string) (string, error) {
		GinkgoHelper()

		var (
			out     bytes.Buffer
			showErr error
		)

		inCorpus(func() {
			relPath, first, last, err := prompthook.ParseCitation(citation)
			Expect(err).ToNot(HaveOccurred())

			store, err := rag.Open(cfg, "")
			Expect(err).ToNot(HaveOccurred())
			defer store.Close()

			showErr = showKnowledgeChunks(ctx, &out, store, relPath, first, last)
		})

		return out.String(), showErr
	}

	// dropChunk deletes one ordinal from the indexed document, leaving the ordinals
	// on either side of it in place. No reindex produces that: index.go drops every
	// chunk of a document and writes the new ones back at 0..n-1, so a citation
	// written before a reindex runs off a shortened document rather than into a gap.
	// The spec using it drives the same branch as a shortened document and proves
	// the walk stops at the absent ordinal rather than skipping over it, which a
	// document with nothing after the gap cannot show.
	dropChunk := func(ordinal int) {
		GinkgoHelper()

		db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", rag.StorePath(cfg, corpus)))
		Expect(err).ToNot(HaveOccurred())
		defer db.Close()

		_, err = db.Exec(`DELETE FROM chunks WHERE ordinal = ? AND document_id = (SELECT id FROM documents WHERE path = 'docs/guide.md')`, ordinal)
		Expect(err).ToNot(HaveOccurred())
	}

	Describe("the config flag", func() {
		It("refuses to run without one and says the flag picks the working directory", func() {
			_, err := run("knowledge", "agent", "show", "docs/guide.md#1")

			Expect(err).To(MatchError(ContainSubstring("--config is required here and has no default")))
			Expect(err).To(MatchError(ContainSubstring("runs in the directory holding the configuration file")))
			Expect(err).To(MatchError(ContainSubstring("knowledge.directory")))
		})

		It("resolves a relative config and its index from an unrelated working directory", func() {
			before, err := os.Getwd()
			Expect(err).ToNot(HaveOccurred())

			out, err := run("knowledge", "agent", "show", "--config", filepath.Join("corpus", "agent.yaml"), "docs/guide.md#1")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Every frame carries a length prefix."))

			after, err := os.Getwd()
			Expect(err).ToNot(HaveOccurred())
			Expect(after).To(Equal(before), "the verb puts the process back where it started")
		})

		// Both of these fail after the change of directory. A verb that returned one
		// of them without restoring would leave every later command in the corpus.
		DescribeTable("returns to the starting directory when it fails after changing into it",
			func(configName string, message string) {
				before, err := os.Getwd()
				Expect(err).ToNot(HaveOccurred())

				_, err = run("knowledge", "agent", "show", "--config", filepath.Join("corpus", configName), "docs/guide.md#1")
				Expect(err).To(MatchError(ContainSubstring(message)))

				after, err := os.Getwd()
				Expect(err).ToNot(HaveOccurred())
				Expect(after).To(Equal(before))
			},
			Entry("the configuration is rejected", "disabled.yaml", "knowledge is not enabled"),
			Entry("the index cannot be opened", "notadir.yaml", "not a directory"),
		)
	})

	Describe("show", func() {
		It("prints one chunk with its heading and no citation line", func() {
			out, err := showFromCorpus("docs/guide.md#1")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(Equal("# Guide > Framing\n\nEvery frame carries a length prefix.\n"))
		})

		// Through the command rather than the renderer, so the change of directory
		// carries a whole range and not only the first chunk of one.
		It("prints every chunk of a range in ordinal order, naming each", func() {
			out, err := run("knowledge", "agent", "show", "--config", filepath.Join("corpus", "agent.yaml"), "docs/guide.md#1-3")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(Equal(strings.Join([]string{
				"docs/guide.md#1",
				"# Guide > Framing",
				"",
				"Every frame carries a length prefix.",
				"",
				"docs/guide.md#2",
				"# Guide > Backpressure",
				"",
				"The queue slows producers when the buffer fills.",
				"",
				"docs/guide.md#3",
				"# Guide > Cache",
				"",
				"Entries expire after an hour.",
				"",
			}, "\n")))
		})

		It("says once where the document ends when the range runs past it", func() {
			out, err := showFromCorpus("docs/guide.md#2-6")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(HaveSuffix("Entries expire after an hour.\n\ndocs/guide.md has no chunk 4; the range stops there\n"))
			Expect(strings.Count(out, "has no chunk")).To(Equal(1))
		})

		It("stops at an absent ordinal rather than printing what follows it", func() {
			dropChunk(2)

			out, err := showFromCorpus("docs/guide.md#1-3")

			Expect(err).ToNot(HaveOccurred())
			Expect(out).To(ContainSubstring("Every frame carries a length prefix."))
			Expect(out).To(HaveSuffix("docs/guide.md has no chunk 2; the range stops there\n"))
			Expect(out).ToNot(ContainSubstring("Entries expire after an hour."), "the ordinals after the absent one are not printed")
		})

		It("errors and prints nothing when the one chunk asked for is gone", func() {
			dropChunk(1)

			out, err := showFromCorpus("docs/guide.md#1")

			Expect(out).To(BeEmpty())
			Expect(err).To(MatchError(ContainSubstring(`no chunk found for citation "docs/guide.md#1"`)))
		})

		It("errors and prints nothing when a range opens on a chunk that is gone", func() {
			dropChunk(1)

			out, err := showFromCorpus("docs/guide.md#1-3")

			Expect(out).To(BeEmpty())
			Expect(err).To(MatchError(ContainSubstring(`no chunk found for citation "docs/guide.md#1-3"`)))
		})

		// The read costs one query per ordinal, so a mistyped nine-digit ordinal runs
		// for hours and writes gigabytes. The argument is a token a model types.
		It("refuses a range wider than the cap, naming both numbers", func() {
			_, err := showFromCorpus(fmt.Sprintf("docs/guide.md#0-%d", knowledgeAgentMaxSpan))

			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("may cover %d chunks", knowledgeAgentMaxSpan))))
			Expect(err).To(MatchError(ContainSubstring(fmt.Sprintf("covers %d", knowledgeAgentMaxSpan+1))))

			_, err = showFromCorpus(fmt.Sprintf("docs/guide.md#0-%d", knowledgeAgentMaxSpan-1))
			Expect(err).ToNot(HaveOccurred(), "a range of exactly the cap is allowed")
		})
	})
})
