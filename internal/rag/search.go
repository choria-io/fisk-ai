//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// rrfK is the Reciprocal Rank Fusion constant; 60 is the standard default.
	rrfK = 60

	// searchFanout is how many candidates each retriever over-fetches before fusion,
	// so a chunk ranked well by one retriever but deep in the other still gets its
	// boost before the final truncation to top_k.
	searchFanout = 50

	// maxFTSTerms clamps how many terms a query contributes to the MATCH expression,
	// so a pathological many-word query cannot become an expensive lexical scan.
	maxFTSTerms = 40

	// minFTSTermRunes drops one-character terms, which add noise, not recall.
	minFTSTermRunes = 2
)

// SearchStatus classifies a search outcome the caller reports to the model or the
// operator without treating it as an error.
type SearchStatus string

const (
	// StatusOK means hits were returned (possibly zero rows for a valid query).
	StatusOK SearchStatus = "ok"
	// StatusIndexNotBuilt means no index file exists yet.
	StatusIndexNotBuilt SearchStatus = "index_not_built"
	// StatusIndexEmpty means the index exists but holds no chunks, or the query
	// reduced to no searchable terms.
	StatusIndexEmpty SearchStatus = "index_empty"
)

// Hit is a citation-ready search result. Citation is the canonical
// <relpath>#<ordinal> token, identical across the tool and every CLI surface;
// HeadingPath is the human-readable breadcrumb shown alongside it.
type Hit struct {
	ChunkID     int64
	Citation    string
	DocPath     string
	Ordinal     int
	HeadingPath string
	Content     string
}

// SearchResult carries the ranked hits plus the status and degradation the caller
// surfaces so a silent lexical fallback is never mistaken for "vectors did not
// help". Degraded is true when the vector tier was configured but this query fell
// back to lexical because the embeddings server could not be reached.
type SearchResult struct {
	Hits          []Hit
	Status        SearchStatus
	Degraded      bool
	DegradeReason string
}

// result is the lightweight (id, score) pair used during fusion; only ranks matter.
type result struct {
	chunkID int64
	score   float64
}

// Search runs the lexical tier and, when the vector tier is on, fuses it with the
// vector tier via RRF. Soft outcomes (no index, empty index) are reported in the
// result's Status rather than as errors. A transient embeddings outage degrades to
// lexical with Degraded set; a genuine index/config mismatch (dimension) or a DB
// error is returned as an error.
func (s *Store) Search(ctx context.Context, query string, requestedTopK int) (*SearchResult, error) {
	if s.db == nil {
		return &SearchResult{Status: StatusIndexNotBuilt}, nil
	}

	var chunkCount int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&chunkCount)
	if err != nil {
		return nil, fmt.Errorf("counting chunks: %w", err)
	}
	if chunkCount == 0 {
		return &SearchResult{Status: StatusIndexEmpty}, nil
	}

	match := ftsQuery(query)
	if match == "" {
		return &SearchResult{Status: StatusIndexEmpty}, nil
	}

	topK := s.effectiveTopK(requestedTopK)

	lex, err := s.ftsSearch(ctx, match, searchFanout)
	if err != nil {
		return nil, err
	}

	res := &SearchResult{Status: StatusOK}
	fused := lex

	if s.emb != nil {
		qv, derr := s.embedQueryVector(ctx, query)
		switch {
		case errors.Is(derr, ErrDimensionMismatch):
			return nil, derr
		case derr != nil:
			// A transient outage degrades to lexical rather than failing the query,
			// but the reason is surfaced so a persistent outage is visible.
			res.Degraded = true
			res.DegradeReason = derr.Error()
		default:
			vec, verr := s.vecSearch(ctx, qv, searchFanout)
			if verr != nil {
				return nil, verr
			}
			fused = rrf([][]result{lex, vec})
		}
	}

	hits, err := s.hydrate(ctx, truncate(fused, topK))
	if err != nil {
		return nil, err
	}
	res.Hits = hits

	return res, nil
}

// effectiveTopK resolves the count for one search: the requested value when
// positive, else the store default, clamped to the hard ceiling. SQLite LIMIT
// never errors when it exceeds the row count, so no count-aware clamp is needed.
func (s *Store) effectiveTopK(requested int) int {
	k := s.topK
	if requested > 0 {
		k = requested
	}
	if k > topKCeiling {
		return topKCeiling
	}
	if k < 1 {
		return 1
	}

	return k
}

// embedQueryVector embeds the query and normalizes it, first checking the live
// model's dimension against the pinned manifest. A dimension mismatch is a real
// index/config disagreement returned as ErrDimensionMismatch (not degraded); a
// network failure is returned as-is so the caller degrades to lexical.
func (s *Store) embedQueryVector(ctx context.Context, query string) ([]float32, error) {
	meta, err := s.readMeta(ctx)
	if err != nil {
		return nil, err
	}

	dim, err := s.emb.Dim(ctx)
	if err != nil {
		return nil, err
	}
	if dim != meta.Dimension {
		return nil, fmt.Errorf("%w: model %q now emits dimension %d but the index was built at %d; run 'fisk-ai knowledge index --reindex'", ErrDimensionMismatch, s.emb.Model(), dim, meta.Dimension)
	}

	qv, err := s.emb.EmbedQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	return normalize(qv), nil
}

// ftsSearch returns chunk ids ranked by BM25 for the prepared MATCH expression.
func (s *Store) ftsSearch(ctx context.Context, match string, limit int) ([]result, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT rowid, bm25(chunks_fts) FROM chunks_fts WHERE chunks_fts MATCH ? ORDER BY bm25(chunks_fts) LIMIT ?`,
		match, limit)
	if err != nil {
		return nil, fmt.Errorf("lexical search: %w", err)
	}
	defer rows.Close()

	var out []result
	for rows.Next() {
		var r result
		if err := rows.Scan(&r.chunkID, &r.score); err != nil {
			return nil, fmt.Errorf("lexical search: %w", err)
		}
		out = append(out, r)
	}

	return out, rows.Err()
}

// vecSearch returns chunk ids ranked by ascending L2 distance from the normalized
// query vector.
func (s *Store) vecSearch(ctx context.Context, qv []float32, limit int) ([]result, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT chunk_id, distance FROM chunks_vec WHERE embedding MATCH ? ORDER BY distance LIMIT ?`,
		vecJSON(qv), limit)
	if err != nil {
		return nil, fmt.Errorf("vector search: %w", err)
	}
	defer rows.Close()

	var out []result
	for rows.Next() {
		var r result
		if err := rows.Scan(&r.chunkID, &r.score); err != nil {
			return nil, fmt.Errorf("vector search: %w", err)
		}
		out = append(out, r)
	}

	return out, rows.Err()
}

// hydrate turns fused (chunkID) rows into citation-ready Hits with one join,
// preserving the fused order. A row that vanished between fusion and hydration
// (concurrent reindex) is skipped rather than failing the search.
func (s *Store) hydrate(ctx context.Context, ranked []result) ([]Hit, error) {
	if len(ranked) == 0 {
		return nil, nil
	}

	ids := make([]any, len(ranked))
	placeholders := make([]string, len(ranked))
	for i, r := range ranked {
		ids[i] = r.chunkID
		placeholders[i] = "?"
	}

	q := fmt.Sprintf(
		`SELECT c.id, d.path, c.heading_path, c.ordinal, c.content
		 FROM chunks c JOIN documents d ON d.id = c.document_id
		 WHERE c.id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, q, ids...)
	if err != nil {
		return nil, fmt.Errorf("hydrating results: %w", err)
	}
	defer rows.Close()

	byID := map[int64]Hit{}
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ChunkID, &h.DocPath, &h.HeadingPath, &h.Ordinal, &h.Content); err != nil {
			return nil, fmt.Errorf("hydrating results: %w", err)
		}
		h.Citation = Citation(h.DocPath, h.Ordinal)
		byID[h.ChunkID] = h
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("hydrating results: %w", err)
	}

	out := make([]Hit, 0, len(ranked))
	for _, r := range ranked {
		if h, ok := byID[r.chunkID]; ok {
			out = append(out, h)
		}
	}

	return out, nil
}

// Citation renders the canonical <relpath>#<ordinal> token emitted by the tool and
// every CLI surface and accepted verbatim by knowledge show.
func Citation(relPath string, ordinal int) string {
	return fmt.Sprintf("%s#%d", relPath, ordinal)
}

// rrf fuses ranked lists on rank (not score) so incompatible score scales (BM25
// vs vector distance) never need normalizing, with a deterministic chunkID
// tie-break.
func rrf(lists [][]result) []result {
	fused := map[int64]float64{}
	for _, list := range lists {
		for rank, r := range list {
			fused[r.chunkID] += 1.0 / float64(rrfK+rank+1)
		}
	}

	out := make([]result, 0, len(fused))
	for id, score := range fused {
		out = append(out, result{chunkID: id, score: score})
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].score != out[b].score {
			return out[a].score > out[b].score
		}
		return out[a].chunkID < out[b].chunkID
	})

	return out
}

// truncate returns at most n results.
func truncate(rs []result, n int) []result {
	if n < len(rs) {
		return rs[:n]
	}

	return rs
}

// ftsQuery reduces free text to OR-ed, quoted terms for FTS5 MATCH. OR favors
// recall in the lexical tier (the vector tier supplies precision); quoting keeps
// arbitrary punctuation from tripping FTS5's query syntax, and any embedded double
// quote is doubled so a term can never break out of the string into MATCH syntax.
// The term count is clamped so a pathological query is not a cheap DoS.
func ftsQuery(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	var terms []string
	for _, f := range fields {
		if utf8.RuneCountInString(f) < minFTSTermRunes {
			continue
		}
		terms = append(terms, `"`+strings.ReplaceAll(f, `"`, `""`)+`"`)
		if len(terms) >= maxFTSTerms {
			break
		}
	}

	return strings.Join(terms, " OR ")
}

// TierLine renders the canonical one-line tier banner shown on every surface, so
// it is never ambiguous which tier is active. When the vector tier is configured
// it reads the pinned model and dimension from the manifest. A degraded runtime
// state (a per-query embeddings outage) is rendered by DegradedTierLine from a
// SearchResult, not here.
func (s *Store) TierLine(ctx context.Context) (string, error) {
	if s.emb == nil {
		return "tier: lexical (FTS5) - no embeddings configured", nil
	}

	dim := 0
	if s.db != nil {
		meta, err := s.readMeta(ctx)
		if err != nil {
			return "", err
		}
		dim = meta.Dimension
	}

	return fmt.Sprintf("tier: hybrid (FTS5 + vectors, RRF) - model=%s dim=%d", s.emb.Model(), dim), nil
}

// DegradedTierLine renders the degraded banner when a hybrid query fell back to
// lexical because the embeddings server was unreachable.
func DegradedTierLine(reason string) string {
	return fmt.Sprintf("tier: hybrid -> DEGRADED to lexical (embeddings unreachable: %s)", reason)
}

// scanCount is a small helper for count(*) queries used by the CLI stats/doctor.
func scanCount(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}

	return n, nil
}
