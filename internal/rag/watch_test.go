//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

var _ = Describe("Watcher (lexical tier)", func() {
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

		writeDoc(docsD, "one.md", "# One\n\nfirst document body\n")
		writeDoc(docsD, "two.md", "# Two\n\nsecond document body\n")
	})

	documents := func() int {
		st, err := statsFor(cfg)
		Expect(err).ToNot(HaveOccurred())

		return st.Documents
	}

	It("rejects a debounce below the floor", func() {
		_, err := NewWatcher(cfg, WatchOptions{Roots: []string{docsD}, Debounce: time.Millisecond}, WatchReporter{})
		Expect(err).To(MatchError(ContainSubstring("debounce must be at least")))
	})

	It("warns about and skips a missing path instead of failing", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var (
			mu    sync.Mutex
			warns []string
		)
		started := make(chan int, 1)
		w, err := NewWatcher(cfg, WatchOptions{
			Roots:     []string{docsD, filepath.Join(tmp, "missing.md")},
			Debounce:  100 * time.Millisecond,
			Reconcile: true,
		}, WatchReporter{
			OnStart: func(dirs int) { started <- dirs },
			OnWarn:  func(msg string) { mu.Lock(); warns = append(warns, msg); mu.Unlock() },
		})
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()

		go func() {
			defer GinkgoRecover()
			_ = w.Run(ctx)
		}()

		Eventually(started, "5s").Should(Receive())
		Expect(documents()).To(Equal(2))

		mu.Lock()
		defer mu.Unlock()
		Expect(warns).To(ContainElement(ContainSubstring("missing.md")))
	})

	It("fails only when no configured path exists", func() {
		w, err := NewWatcher(cfg, WatchOptions{
			Roots:    []string{filepath.Join(tmp, "nope")},
			Debounce: 100 * time.Millisecond,
		}, WatchReporter{})
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()

		Expect(w.Run(context.Background())).To(MatchError(ContainSubstring("none of the configured knowledge paths exist")))
	})

	It("indexes the corpus on start, then picks up added and removed files", func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		started := make(chan int, 1)
		w, err := NewWatcher(cfg, WatchOptions{
			Roots:     []string{docsD},
			Debounce:  100 * time.Millisecond,
			Reconcile: true,
		}, WatchReporter{
			OnStart: func(dirs int) { started <- dirs },
		})
		Expect(err).ToNot(HaveOccurred())
		defer w.Close()

		runErr := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			runErr <- w.Run(ctx)
		}()

		// The initial pass runs and watches are in place before OnStart fires.
		Eventually(started, "5s").Should(Receive(BeNumerically(">=", 1)))
		Expect(documents()).To(Equal(2))

		// A new file is indexed after the debounce settles.
		writeDoc(docsD, "three.md", "# Three\n\nthird document body\n")
		Eventually(documents, "5s", "100ms").Should(Equal(3))

		// Removing a file drops its document via the per-event delete path.
		Expect(os.Remove(filepath.Join(docsD, "two.md"))).To(Succeed())
		Eventually(documents, "5s", "100ms").Should(Equal(2))

		cancel()
		Eventually(runErr, "5s").Should(Receive(BeNil()))
	})
})
