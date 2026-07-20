//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/util"
)

// maxQueryChars caps the length of a single query sent to the embeddings server. A
// real search query is short; a pathological multi-kilobyte query would only waste
// the embedding round-trip (and could be a cheap way for an MCP client to load the
// endpoint), so it is truncated to a generous bound well past any genuine query.
const maxQueryChars = 8192

const (
	// embedBatchSize is how many inputs are sent per embeddings request during
	// ingest. A server that rejects the batch is retried with smaller batches, so
	// this is a throughput target, not a hard limit.
	embedBatchSize = 64

	// maxEmbedInputBytes rejects a single oversized input before it is sent, so a
	// pathological chunk cannot blow up the request or the server.
	maxEmbedInputBytes = 128 * 1024

	// maxEmbedResponseBytes bounds the response body read from the embeddings
	// server, so a hostile or broken server cannot exhaust memory.
	maxEmbedResponseBytes = 64 << 20
)

// Embedder is the tier-2 seam: a source of embedding vectors for documents and
// queries. It is an interface so unit tests mock it and never touch the network;
// the only production implementation is the OpenAI-compatible client below. The
// rag package L2-normalizes every returned vector itself, so an implementation
// returns the model's raw vectors.
type Embedder interface {
	// Model returns the configured model name, pinned in the manifest.
	Model() string
	// QueryPrefix and DocumentPrefix return the configured prefixes, part of the
	// pinned vector identity: changing either forces a reindex.
	QueryPrefix() string
	DocumentPrefix() string
	// Dim probes and returns the model's embedding dimension, caching it after the
	// first success. It is called before any table create or corpus embedding so a
	// dimension mismatch is caught upfront.
	Dim(ctx context.Context) (int, error)
	// EmbedQuery returns the raw vector for a query, with the query prefix applied.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
	// EmbedDocuments returns the raw vectors for documents, aligned one-to-one with
	// docs, with the document prefix applied. The caller guarantees non-empty text.
	EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error)
}

// Document is a chunk to embed: its heading fills the document prefix's {title}
// placeholder, and text is the chunk body.
type Document struct {
	Title string
	Text  string
}

// buildEmbedder constructs the embedder for cfg, or returns nil when the vector
// tier is off (lexical-only). It validates the embeddings block here so a
// misconfiguration fails at Open, before the agent loop, without contacting the
// server: base_url and model must be set, and a non-loopback base_url must use
// https so a query is never sent in cleartext to a remote host.
func buildEmbedder(cfg *config.Config) (Embedder, error) {
	if !cfg.RAGVectorEnabled() {
		return nil, nil
	}

	ec := cfg.Harness.RAG.Embeddings
	if strings.TrimSpace(ec.BaseURL) == "" {
		return nil, fmt.Errorf("knowledge.embeddings.base_url is required when the embeddings block is present")
	}
	if strings.TrimSpace(ec.Model) == "" {
		return nil, fmt.Errorf("knowledge.embeddings.model is required when the embeddings block is present")
	}

	if err := util.ValidateBaseURL("knowledge.embeddings.base_url", ec.BaseURL); err != nil {
		return nil, err
	}

	var apiKey string
	if ec.APIKeyEnv != "" {
		apiKey = os.Getenv(ec.APIKeyEnv)
	}

	return &openAIEmbedder{
		baseURL:     strings.TrimRight(ec.BaseURL, "/"),
		model:       ec.Model,
		apiKey:      apiKey,
		queryPrefix: ec.QueryPrefix,
		docPrefix:   ec.DocumentPrefix,
		client:      &http.Client{Timeout: ec.TimeoutParsed},
	}, nil
}

// openAIEmbedder talks to a local OpenAI-compatible /v1/embeddings endpoint. The
// dimension is probed lazily on first use and cached; vectors are returned raw and
// normalized by the rag package.
type openAIEmbedder struct {
	baseURL     string
	model       string
	apiKey      string
	queryPrefix string
	docPrefix   string
	client      *http.Client

	// dimMu guards the lazily-probed dimension cache. A single openAIEmbedder is
	// shared across concurrent readers (the MCP server serves knowledge_search to
	// more than one in-flight call), so the cache must not be raced.
	dimMu sync.Mutex
	dim   int
}

func (e *openAIEmbedder) Model() string          { return e.model }
func (e *openAIEmbedder) QueryPrefix() string    { return e.queryPrefix }
func (e *openAIEmbedder) DocumentPrefix() string { return e.docPrefix }

// Dim probes the model's dimension once (embedding a short neutral input) and
// caches it. An empty or error-shaped response is a failure, never a pin, so a
// broken server does not fix a bogus dimension in the manifest.
func (e *openAIEmbedder) Dim(ctx context.Context) (int, error) {
	e.dimMu.Lock()
	defer e.dimMu.Unlock()

	if e.dim != 0 {
		return e.dim, nil
	}

	vecs, err := e.embedBatch(ctx, []string{"dimension probe"})
	if err != nil {
		return 0, err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return 0, fmt.Errorf("embeddings server at %s returned an empty dimension probe for model %q", e.baseURL, e.model)
	}
	e.dim = len(vecs[0])

	return e.dim, nil
}

func (e *openAIEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	if len(text) > maxQueryChars {
		text = text[:maxQueryChars]
		// Back off any partial trailing rune so the truncation never emits invalid
		// UTF-8, which embedBatch would reject.
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}

	vecs, err := e.embedBatch(ctx, []string{e.queryPrefix + text})
	if err != nil {
		return nil, err
	}
	if len(vecs) != 1 {
		return nil, fmt.Errorf("embeddings server returned %d vectors for one query input", len(vecs))
	}

	return vecs[0], nil
}

// EmbedDocuments embeds docs in batches, falling back to smaller batches (down to
// one-at-a-time) when a server rejects a batch size, without reordering. The
// returned vectors align one-to-one with docs.
func (e *openAIEmbedder) EmbedDocuments(ctx context.Context, docs []Document) ([][]float32, error) {
	inputs := make([]string, len(docs))
	for i, d := range docs {
		inputs[i] = e.documentInput(d)
	}

	out := make([][]float32, 0, len(inputs))
	for start := 0; start < len(inputs); start += embedBatchSize {
		end := min(start+embedBatchSize, len(inputs))
		vecs, err := e.embedWithFallback(ctx, inputs[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}

	return out, nil
}

// documentInput builds the prefixed input for a document chunk, filling the
// {title} placeholder from the chunk's heading.
func (e *openAIEmbedder) documentInput(d Document) string {
	title := d.Title
	if title == "" {
		title = "none"
	}

	return strings.ReplaceAll(e.docPrefix, "{title}", title) + d.Text
}

// embedWithFallback embeds a batch, and on failure splits it and retries each half
// (preserving order) down to single inputs, so a server that caps the batch size
// still succeeds. A single input that still fails returns the error.
func (e *openAIEmbedder) embedWithFallback(ctx context.Context, inputs []string) ([][]float32, error) {
	vecs, err := e.embedBatch(ctx, inputs)
	if err == nil {
		return vecs, nil
	}
	if len(inputs) == 1 {
		return nil, err
	}

	mid := len(inputs) / 2
	left, err := e.embedWithFallback(ctx, inputs[:mid])
	if err != nil {
		return nil, err
	}
	right, err := e.embedWithFallback(ctx, inputs[mid:])
	if err != nil {
		return nil, err
	}

	return append(left, right...), nil
}

// embedResponse is the OpenAI embeddings response shape. index is authoritative
// for placement: some servers return objects out of order.
type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// embedBatch sends one embeddings request for inputs and returns the vectors
// mapped strictly by each object's index field. It validates every input first
// (non-empty, valid UTF-8, within the size cap) and asserts the returned index set
// exactly equals the sent set, failing the whole batch on any gap, duplicate, or
// count mismatch so a vector never lands on the wrong chunk.
func (e *openAIEmbedder) embedBatch(ctx context.Context, inputs []string) ([][]float32, error) {
	for i, in := range inputs {
		if strings.TrimSpace(in) == "" {
			return nil, fmt.Errorf("embedding input %d is empty", i)
		}
		if !utf8.ValidString(in) {
			return nil, fmt.Errorf("embedding input %d is not valid UTF-8", i)
		}
		if len(in) > maxEmbedInputBytes {
			return nil, fmt.Errorf("embedding input %d is too large: %d bytes, limit is %d", i, len(in), maxEmbedInputBytes)
		}
	}

	body, err := json.Marshal(map[string]any{"input": inputs, "model": e.model})
	if err != nil {
		return nil, fmt.Errorf("encoding embeddings request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building embeddings request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("contacting embeddings server at %s: %w", e.baseURL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxEmbedResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("reading embeddings response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings server at %s returned %s: %s", e.baseURL, resp.Status, strings.TrimSpace(string(raw)))
	}

	var parsed embedResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decoding embeddings response: %w", err)
	}
	// Some servers signal an error inside a 200 body; treat that as a failure so a
	// broken response is never mistaken for a valid (empty) result.
	if parsed.Error != nil && parsed.Error.Message != "" {
		return nil, fmt.Errorf("embeddings server at %s reported: %s", e.baseURL, parsed.Error.Message)
	}
	if len(parsed.Data) != len(inputs) {
		return nil, fmt.Errorf("embeddings server returned %d vectors for %d inputs", len(parsed.Data), len(inputs))
	}

	out := make([][]float32, len(inputs))
	seen := make([]bool, len(inputs))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(inputs) {
			return nil, fmt.Errorf("embeddings server returned out-of-range index %d for %d inputs", d.Index, len(inputs))
		}
		if seen[d.Index] {
			return nil, fmt.Errorf("embeddings server returned duplicate index %d", d.Index)
		}
		if len(d.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings server returned an empty vector at index %d", d.Index)
		}
		seen[d.Index] = true
		out[d.Index] = d.Embedding
	}

	return out, nil
}
