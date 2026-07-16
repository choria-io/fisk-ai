//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/util"
)

// lexicalKnowledgeBuiltins builds a real lexical knowledge_search built-in backed
// by a small on-disk index, so a dispatch exercises the actual tool end to end.
func lexicalKnowledgeBuiltins(ctx context.Context) []*util.BuiltinTool {
	GinkgoHelper()

	storeDir := GinkgoT().TempDir()
	docsDir := GinkgoT().TempDir()
	Expect(os.WriteFile(filepath.Join(docsDir, "a.md"),
		[]byte("# Backpressure\n\nThe queue applies backpressure when the buffer is full.\n"), 0o644)).To(Succeed())

	cfg := &config.Config{
		Identity: "test",
		Harness:  config.HarnessConfig{RAG: &config.RAGConfig{Enabled: true, Directory: storeDir}},
	}

	w, err := rag.OpenWriter(cfg)
	Expect(err).NotTo(HaveOccurred())
	_, err = w.Index(ctx, []string{docsDir}, rag.IndexOptions{Reconcile: true})
	Expect(err).NotTo(HaveOccurred())
	Expect(w.Close()).To(Succeed())

	store, err := rag.Open(cfg)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { store.Close() })

	return util.RAGTools(cfg, store)
}

var _ = Describe("BuildServer built-ins", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("serves a built-in-only server and returns its JSON result verbatim", func() {
		builtins := lexicalKnowledgeBuiltins(ctx)
		srv, registered := BuildServer(nil, Options{Name: "app", Version: "v1", LogOutput: io.Discard, Builtins: builtins})
		Expect(registered).To(ConsistOf("knowledge_search"))

		cs := connect(ctx, srv)
		defer cs.Close()

		text, isError := callText(ctx, cs, "knowledge_search", map[string]any{"query": "backpressure"})
		Expect(isError).To(BeFalse())

		// The handler already returns JSON, so the result must be that object, not a
		// double-encoded JSON string.
		var out struct {
			Tier    string `json:"tier"`
			Status  string `json:"status"`
			Results []struct {
				Citation string `json:"citation"`
				Content  string `json:"content"`
			} `json:"results"`
		}
		Expect(json.Unmarshal([]byte(text), &out)).To(Succeed())
		Expect(out.Tier).To(ContainSubstring("lexical"))
		Expect(out.Status).To(Equal("ok"))
		Expect(out.Results).NotTo(BeEmpty())
		Expect(out.Results[0].Citation).To(ContainSubstring("a.md#"))
	})

	It("skips a built-in whose name a wrapped command already exposes", func() {
		app := fisk.New("app", "an app")
		k := app.Command("knowledge", "a group")
		k.Command("search", "a command") // introspects to the tool name knowledge_search
		cmdTools := toolsFor(app)

		var logs bytes.Buffer
		_, registered := BuildServer(cmdTools, Options{Name: "app", Version: "v1", LogOutput: &logs, Builtins: lexicalKnowledgeBuiltins(ctx)})

		// The command tool wins; the built-in is skipped, so the name registers once.
		count := 0
		for _, n := range registered {
			if n == "knowledge_search" {
				count++
			}
		}
		Expect(count).To(Equal(1))
		Expect(logs.String()).To(ContainSubstring("a wrapped command already exposes that name"))
	})

	It("dispatches concurrent calls to one read-only store", func() {
		builtins := lexicalKnowledgeBuiltins(ctx)
		srv, _ := BuildServer(nil, Options{Name: "app", Version: "v1", LogOutput: io.Discard, Builtins: builtins})

		cs := connect(ctx, srv)
		defer cs.Close()

		done := make(chan bool, 8)
		for i := 0; i < 8; i++ {
			go func() {
				defer GinkgoRecover()
				_, isError := callText(ctx, cs, "knowledge_search", map[string]any{"query": "backpressure buffer"})
				done <- isError
			}()
		}
		for i := 0; i < 8; i++ {
			Expect(<-done).To(BeFalse())
		}
	})
})
