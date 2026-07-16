//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

// embedRequest and embedItem mirror the OpenAI embeddings request/response shapes
// the fake server speaks.
type embedRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// fakeServer builds an httptest server whose handler is provided by the test.
func fakeServer(handler func(w http.ResponseWriter, req embedRequest)) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		Expect(json.NewDecoder(r.Body).Decode(&req)).To(Succeed())
		handler(w, req)
	}))
}

// writeVectors writes a well-formed response with one vector per input, in the
// given index order, each vector a distinct constant so the mapping is checkable.
func writeVectors(w http.ResponseWriter, indices []int) {
	type item struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	}
	var data []item
	for _, idx := range indices {
		data = append(data, item{Embedding: []float32{float32(idx) + 1, 0.5}, Index: idx})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func newEmbedder(url string) *openAIEmbedder {
	return &openAIEmbedder{baseURL: url, model: "test-model", client: &http.Client{Timeout: 5 * time.Second}}
}

var _ = Describe("Embedding client", func() {
	ctx := context.Background()

	Describe("base_url validation", func() {
		It("accepts https and loopback http, rejects non-loopback http and bad schemes", func() {
			Expect(validateEmbedURL("https://example.com/v1")).To(Succeed())
			Expect(validateEmbedURL("http://127.0.0.1:1234/v1")).To(Succeed())
			Expect(validateEmbedURL("http://localhost:1234/v1")).To(Succeed())
			Expect(validateEmbedURL("http://[::1]:1234/v1")).To(Succeed())
			Expect(validateEmbedURL("http://example.com/v1")).ToNot(Succeed())
			Expect(validateEmbedURL("ftp://example.com")).ToNot(Succeed())
		})
	})

	Describe("buildEmbedder", func() {
		It("returns nil when the vector tier is off", func() {
			cfg := &config.Config{Identity: "t", Harness: config.HarnessConfig{RAG: &config.RAGConfig{Enabled: true}}}
			emb, err := buildEmbedder(cfg)
			Expect(err).ToNot(HaveOccurred())
			Expect(emb).To(BeNil())
		})

		It("rejects a non-loopback http base_url via config", func() {
			cfg := &config.Config{Identity: "t", Harness: config.HarnessConfig{RAG: &config.RAGConfig{
				Enabled:    true,
				Embeddings: &config.RAGEmbeddingsConfig{BaseURL: "http://example.com/v1", Model: "m", TimeoutParsed: time.Second},
			}}}
			_, err := buildEmbedder(cfg)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("index-field mapping", func() {
		It("maps vectors by the response index, not array position", func() {
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) {
				// Return the objects in reverse order but with correct index fields.
				idx := make([]int, len(req.Input))
				for i := range req.Input {
					idx[len(req.Input)-1-i] = i
				}
				writeVectors(w, idx)
			})
			defer srv.Close()

			vecs, err := newEmbedder(srv.URL).embedBatch(ctx, []string{"a", "b", "c"})
			Expect(err).ToNot(HaveOccurred())
			Expect(vecs[0][0]).To(Equal(float32(1))) // index 0 -> value idx+1 = 1
			Expect(vecs[1][0]).To(Equal(float32(2)))
			Expect(vecs[2][0]).To(Equal(float32(3)))
		})

		It("fails the batch on a duplicated index", func() {
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) { writeVectors(w, []int{0, 0}) })
			defer srv.Close()
			_, err := newEmbedder(srv.URL).embedBatch(ctx, []string{"a", "b"})
			Expect(err).To(MatchError(ContainSubstring("duplicate index")))
		})

		It("fails the batch on a count mismatch", func() {
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) { writeVectors(w, []int{0}) })
			defer srv.Close()
			_, err := newEmbedder(srv.URL).embedBatch(ctx, []string{"a", "b"})
			Expect(err).To(MatchError(ContainSubstring("2 inputs")))
		})

		It("treats an error-shaped 200 as a failure", func() {
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "model not loaded"}})
			})
			defer srv.Close()
			_, err := newEmbedder(srv.URL).embedBatch(ctx, []string{"a"})
			Expect(err).To(MatchError(ContainSubstring("model not loaded")))
		})

		It("rejects an empty input before sending", func() {
			_, err := newEmbedder("http://127.0.0.1:1").embedBatch(ctx, []string{"  "})
			Expect(err).To(MatchError(ContainSubstring("empty")))
		})
	})

	Describe("batch fallback", func() {
		It("falls back to smaller batches when the server rejects a multi-input batch", func() {
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) {
				if len(req.Input) > 1 {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte("batch too large"))
					return
				}
				writeVectors(w, []int{0})
			})
			defer srv.Close()

			vecs, err := newEmbedder(srv.URL).EmbedDocuments(ctx, []Document{{Text: "a"}, {Text: "b"}, {Text: "c"}})
			Expect(err).ToNot(HaveOccurred())
			Expect(vecs).To(HaveLen(3))
		})
	})

	Describe("dimension probe", func() {
		It("probes once and caches the dimension", func() {
			calls := 0
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) {
				calls++
				writeVectors(w, []int{0})
			})
			defer srv.Close()

			e := newEmbedder(srv.URL)
			dim, err := e.Dim(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(dim).To(Equal(2))
			_, _ = e.Dim(ctx)
			Expect(calls).To(Equal(1), "the dimension is cached after the first probe")
		})

		It("probes the dimension safely under concurrent callers", func() {
			srv := fakeServer(func(w http.ResponseWriter, req embedRequest) {
				writeVectors(w, []int{0})
			})
			defer srv.Close()

			e := newEmbedder(srv.URL)

			const n = 16
			var wg sync.WaitGroup
			dims := make([]int, n)
			errs := make([]error, n)
			wg.Add(n)
			for i := 0; i < n; i++ {
				go func(i int) {
					defer wg.Done()
					dims[i], errs[i] = e.Dim(ctx)
				}(i)
			}
			wg.Wait()

			for i := 0; i < n; i++ {
				Expect(errs[i]).ToNot(HaveOccurred())
				Expect(dims[i]).To(Equal(2))
			}
		})
	})
})
