//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

// lexicalConfig builds a lexical-only (no embeddings) config whose store lives
// under dir.
func lexicalConfig(dir string) *config.Config {
	return &config.Config{
		Identity: "test",
		Harness: config.HarnessConfig{
			RAG: &config.RAGConfig{Enabled: true, Directory: dir},
		},
	}
}

// writeDoc writes a document under root, creating parent directories.
func writeDoc(root, rel, content string) string {
	path := filepath.Join(root, rel)
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	Expect(os.WriteFile(path, []byte(content), 0o644)).To(Succeed())

	return path
}

var _ = Describe("Store (lexical tier)", func() {
	ctx := context.Background()

	var (
		tmp    string
		storeD string
		docsD  string
		cfg    *config.Config
	)

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		storeD = filepath.Join(tmp, "knowledge")
		docsD = filepath.Join(tmp, "docs")
		cfg = lexicalConfig(storeD)

		writeDoc(docsD, "backpressure.md", "# Design\n\n## Backpressure\n\nThe queue applies backpressure when the buffer is full so producers slow down.\n")
		writeDoc(docsD, "auth.md", "# Authentication\n\nTokens are validated against the issuer before any request proceeds.\n")
	})

	index := func() *IndexStats {
		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()
		stats, err := w.Index(ctx, []string{docsD}, IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())
		return stats
	}

	It("builds a lexical index and retrieves the on-topic section", func() {
		stats := index()
		Expect(stats.Added).To(Equal(2))
		Expect(stats.Chunks).To(BeNumerically(">=", 2))

		r, err := Open(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer r.Close()
		Expect(r.Built()).To(BeTrue())

		res, err := r.Search(ctx, "how does backpressure work", 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Status).To(Equal(StatusOK))
		Expect(res.Hits).ToNot(BeEmpty())
		Expect(res.Hits[0].DocPath).To(ContainSubstring("backpressure.md"))
		Expect(res.Hits[0].Citation).To(MatchRegexp(`backpressure\.md#\d+`))
	})

	It("reports index_not_built before any index exists", func() {
		r, err := Open(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer r.Close()
		Expect(r.Built()).To(BeFalse())

		res, err := r.Search(ctx, "anything", 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Status).To(Equal(StatusIndexNotBuilt))
	})

	It("skips unchanged files and reconciles deletions on a full-root walk", func() {
		index()

		// Remove one file, re-index the whole root: it should be reconciled away.
		Expect(os.Remove(filepath.Join(docsD, "auth.md"))).To(Succeed())

		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		stats, err := w.Index(ctx, []string{docsD}, IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())
		Expect(stats.Removed).To(Equal(1))
		Expect(stats.Skipped).To(Equal(1)) // backpressure.md unchanged
		w.Close()

		r, err := Open(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer r.Close()
		res, err := r.Search(ctx, "authentication tokens issuer", 5)
		Expect(err).ToNot(HaveOccurred())
		for _, h := range res.Hits {
			Expect(h.DocPath).ToNot(ContainSubstring("auth.md"))
		}
	})

	It("does not reconcile-delete on a subpath (non-reconciling) walk", func() {
		index()
		Expect(os.Remove(filepath.Join(docsD, "auth.md"))).To(Succeed())

		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		stats, err := w.Index(ctx, []string{docsD}, IndexOptions{Reconcile: false})
		Expect(err).ToNot(HaveOccurred())
		Expect(stats.Removed).To(Equal(0))
		w.Close()

		st, err := statsFor(cfg)
		Expect(err).ToNot(HaveOccurred())
		Expect(st.Documents).To(Equal(2)) // auth.md still indexed despite deletion
	})

	It("refuses a second concurrent writer with the advisory lock", func() {
		w1, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer w1.Close()

		_, err = OpenWriter(cfg)
		Expect(err).To(MatchError(ErrLocked))
	})

	It("keeps the DB file private (0600) after a write", func() {
		index()
		fi, err := os.Stat(filepath.Join(storeD, dbFileName))
		Expect(err).ToNot(HaveOccurred())
		Expect(fi.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	// A mode=ro reader must read correctly while a separate writer commits, and see
	// each committed snapshot without torn state. This is the multi-process WAL
	// validation gate exercised in-process (separate connections behave identically
	// to separate processes under SQLite WAL).
	It("serves a read-only reader concurrently with a live writer", func() {
		index() // establishes the file + WAL

		reader, err := Open(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer reader.Close()

		writer, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer writer.Close()

		// The reader sees the current committed state.
		res, err := reader.Search(ctx, "backpressure buffer", 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Hits).ToNot(BeEmpty())

		// The writer commits a brand-new document while the reader stays open.
		writeDoc(docsD, "sharding.md", "# Sharding\n\nKeys are hashed to shards for horizontal scale.\n")
		_, err = writer.Index(ctx, []string{docsD}, IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())

		// A fresh per-query read transaction sees the new commit; no torn state.
		res, err = reader.Search(ctx, "sharding horizontal scale keys", 5)
		Expect(err).ToNot(HaveOccurred())
		found := false
		for _, h := range res.Hits {
			if filepath.Base(h.DocPath) == "sharding.md" {
				found = true
			}
		}
		Expect(found).To(BeTrue())
	})
})

var _ = Describe("Store rm and reset", func() {
	ctx := context.Background()

	var (
		tmp    string
		storeD string
		docsD  string
		cfg    *config.Config
	)

	BeforeEach(func() {
		tmp = GinkgoT().TempDir()
		storeD = filepath.Join(tmp, "knowledge")
		docsD = filepath.Join(tmp, "docs")
		cfg = lexicalConfig(storeD)

		writeDoc(docsD, "backpressure.md", "# Design\n\n## Backpressure\n\nThe queue applies backpressure when the buffer is full so producers slow down.\n")
		writeDoc(docsD, "auth.md", "# Authentication\n\nTokens are validated against the issuer before any request proceeds.\n")

		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()
		_, err = w.Index(ctx, []string{docsD}, IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())
	})

	It("reports StoreExists only once an index file is present", func() {
		empty := lexicalConfig(filepath.Join(tmp, "empty"))
		exists, err := StoreExists(empty)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeFalse())

		exists, err = StoreExists(cfg)
		Expect(err).ToNot(HaveOccurred())
		Expect(exists).To(BeTrue())
	})

	It("removes a known document and reports it, leaving others intact", func() {
		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()

		removed, err := w.DeleteDocument(ctx, filepath.Join(docsD, "auth.md"))
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(BeTrue())

		st, err := w.Stats(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(st.Documents).To(Equal(1))

		res, err := w.Search(ctx, "authentication tokens issuer", 5)
		Expect(err).ToNot(HaveOccurred())
		for _, h := range res.Hits {
			Expect(h.DocPath).ToNot(ContainSubstring("auth.md"))
		}
	})

	It("reports a miss for an unknown document without erroring", func() {
		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()

		removed, err := w.DeleteDocument(ctx, filepath.Join(docsD, "does-not-exist.md"))
		Expect(err).ToNot(HaveOccurred())
		Expect(removed).To(BeFalse())

		st, err := w.Stats(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(st.Documents).To(Equal(2))
	})

	It("wipes all data on Reset, leaving a clean empty index", func() {
		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())

		Expect(w.Reset(ctx)).To(Succeed())

		st, err := w.Stats(ctx)
		Expect(err).ToNot(HaveOccurred())
		Expect(st.Documents).To(Equal(0))
		Expect(st.Chunks).To(Equal(0))
		w.Close()

		// The file remains and a fresh search reports an empty (not unbuilt) index.
		r, err := Open(cfg)
		Expect(err).ToNot(HaveOccurred())
		defer r.Close()
		res, err := r.Search(ctx, "backpressure buffer", 5)
		Expect(err).ToNot(HaveOccurred())
		Expect(res.Status).To(Equal(StatusIndexEmpty))
	})

	It("returns freed pages to the OS on Reset (auto_vacuum=FULL)", func() {
		dbPath := filepath.Join(storeD, dbFileName)

		// Grow the index well past its initial size so a failure to compact is
		// unmistakable, then checkpoint so the pages land in the main file.
		w, err := OpenWriter(cfg)
		Expect(err).ToNot(HaveOccurred())
		for i := range 200 {
			writeDoc(docsD, fmt.Sprintf("bulk/doc%d.md", i), "# Doc\n\n"+strings.Repeat("padding content for the index ", 200)+"\n")
		}
		_, err = w.Index(ctx, []string{docsD}, IndexOptions{Reconcile: true})
		Expect(err).ToNot(HaveOccurred())
		_, err = w.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		Expect(err).ToNot(HaveOccurred())

		full, err := os.Stat(dbPath)
		Expect(err).ToNot(HaveOccurred())

		Expect(w.Reset(ctx)).To(Succeed())
		_, err = w.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		Expect(err).ToNot(HaveOccurred())
		w.Close()

		after, err := os.Stat(dbPath)
		Expect(err).ToNot(HaveOccurred())
		Expect(after.Size()).To(BeNumerically("<", full.Size()/2))
	})
})

// statsFor opens a read-only store and returns its stats.
func statsFor(cfg *config.Config) (*Stats, error) {
	r, err := Open(cfg)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	return r.Stats(context.Background())
}
