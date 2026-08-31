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

	"github.com/choria-io/fisk-ai/internal/telemetry"
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

	// MinTermRunes is minFTSTermRunes for callers that have to explain the drop to a
	// reader. A term below it is never queried, and under a completeness contract
	// that has to be said out loud rather than left as a silent discard.
	MinTermRunes = minFTSTermRunes

	// bm25Weights are the per-column BM25 weights for chunks_fts, in column order:
	// body then heading_path. The heading is weighted 2.0 because a section title is
	// often the most search-relevant phrase in a chunk, and because it restores the
	// ranking the index had while the breadcrumb was stored folded into the body and
	// so counted twice. Measured against the ranking recorded before that change.
	bm25Weights = "1.0, 2.0"

	// ftsSearchQuery ranks chunks by BM25 for a prepared MATCH expression. The
	// weights are concatenated at compile time rather than formatted in at runtime,
	// so the statement is a constant and no value can reach the query text; the match
	// expression and the limit are bound. Both bm25() calls must carry the same
	// weights or the ORDER BY and the returned score disagree, which is why the
	// weights are named once.
	ftsSearchQuery = `SELECT rowid, bm25(chunks_fts, ` + bm25Weights + `) FROM chunks_fts ` +
		`WHERE chunks_fts MATCH ? ORDER BY bm25(chunks_fts, ` + bm25Weights + `) LIMIT ?`
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

	// DocTitle is the document's first heading, the value the indexer wrote to
	// documents.title. It is empty for a document that holds no heading at all.
	// The first chunk of a document has an empty HeadingPath, so on such a hit
	// DocTitle is the only thing besides the path that names what was found.
	DocTitle string

	// MappedCitation is how this chunk is cited outside the corpus, rendered from
	// the configured citation rules: a URL where the rules publish one, and whatever
	// else a rule renders otherwise. It is the Citation token itself when no rule
	// matched the document path, so a surface can print it without asking whether
	// any rule is configured.
	MappedCitation string

	// Mapped reports whether a rule produced MappedCitation. It cannot be derived
	// from MappedCitation, since a rule may render a path unchanged, and a surface
	// needs it to leave a column blank or to count the documents no rule reaches.
	Mapped bool
}

// DegradeKind classifies why a hybrid query fell back to the lexical tier. It exists
// because "the embeddings server was unreachable" is not true of every fallback: the
// pinned index metadata is read on the same path and fails the same way, and the two
// have unrelated fixes. It is derived from which step failed rather than from the
// error's text, so nothing an error message carries takes part in the decision.
type DegradeKind string

const (
	// DegradeNone means the query did not degrade.
	DegradeNone DegradeKind = ""
	// DegradeEmbeddings means the embeddings server failed to answer usefully.
	DegradeEmbeddings DegradeKind = "embeddings"
	// DegradeTimeout means an embeddings request ran out of time.
	DegradeTimeout DegradeKind = "timeout"
	// DegradeCanceled means the query's context was canceled while embedding.
	DegradeCanceled DegradeKind = "canceled"
	// DegradeIndexMeta means the index's own pinned metadata could not be read, which is
	// this store failing rather than the embeddings server.
	DegradeIndexMeta DegradeKind = "index_meta"
)

// SearchResult carries the ranked hits plus the status and degradation the caller
// surfaces so a silent lexical fallback is never mistaken for "vectors did not
// help". Degraded is true when the vector tier was configured but this query fell
// back to lexical; DegradeKind says which failure caused it and DegradeReason carries
// the underlying text for a local surface to print.
type SearchResult struct {
	Hits          []Hit
	Status        SearchStatus
	Degraded      bool
	DegradeKind   DegradeKind
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
// The span it opens covers every one of the nine ways this returns, through a deferred
// Finish over the named returns. That is safe here only because every failing path
// returns a nil result explicitly: the deferred read then sees nil and reports a
// failure, where reading a partially assembled local would report the status the result
// was initialized with. The two values the result does not carry, the corpus size and
// the tier that actually ran, are held in locals filled in as they are learned, so a
// return before either is known reports neither rather than reporting a default.
//
// It reassigns ctx, which is what makes the embeddings request a child of this span.
func (s *Store) Search(ctx context.Context, query string, requestedTopK int) (res *SearchResult, err error) {
	topK := s.effectiveTopK(requestedTopK)

	ctx, span := telemetry.ProviderFromContext(ctx).StartSearch(ctx, telemetry.SearchInfo{
		Hybrid: s.emb != nil,
		TopK:   topK,
	})

	var indexedChunks *int
	var effectiveTier string
	defer func() { span.Finish(ctx, searchOutcome(res, err, indexedChunks, effectiveTier)) }()

	if s.db == nil {
		return &SearchResult{Status: StatusIndexNotBuilt}, nil
	}

	var chunkCount int
	err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM chunks`).Scan(&chunkCount)
	if err != nil {
		return nil, fmt.Errorf("counting chunks: %w", err)
	}
	indexedChunks = &chunkCount
	if chunkCount == 0 {
		return &SearchResult{Status: StatusIndexEmpty}, nil
	}

	match := ftsQuery(query)
	if match == "" {
		return &SearchResult{Status: StatusIndexEmpty}, nil
	}

	lex, err := s.ftsSearch(ctx, match, searchFanout)
	if err != nil {
		return nil, err
	}
	effectiveTier = telemetry.TierLexical

	res = &SearchResult{Status: StatusOK}
	fused := lex

	if s.emb != nil {
		qv, kind, derr := s.embedQueryVector(ctx, query)
		switch {
		case errors.Is(derr, ErrDimensionMismatch), errors.Is(derr, ErrModelMismatch):
			return nil, derr
		case derr != nil:
			// A transient outage degrades to lexical rather than failing the query,
			// but the reason is surfaced so a persistent outage is visible.
			res.Degraded = true
			res.DegradeKind = kind
			res.DegradeReason = derr.Error()
		default:
			vec, verr := s.vecSearch(ctx, qv, searchFanout)
			if verr != nil {
				return nil, verr
			}
			fused = rrf([][]result{lex, vec})
			effectiveTier = telemetry.TierHybrid
		}
	}

	hits, err := s.hydrate(ctx, truncate(fused, topK))
	if err != nil {
		return nil, err
	}
	res.Hits = hits

	return res, nil
}

// searchOutcome builds the span outcome from what the search returned.
//
// A nil result is a failure whatever err says, because every failing return abandons it.
func searchOutcome(res *SearchResult, err error, indexedChunks *int, effectiveTier string) telemetry.SearchOutcome {
	if res == nil {
		return telemetry.SearchOutcome{
			IndexedChunks: indexedChunks,
			EffectiveTier: effectiveTier,
			Class:         searchErrorClass(err),
			Failed:        true,
		}
	}

	out := telemetry.SearchOutcome{
		Status:        string(res.Status),
		EffectiveTier: effectiveTier,
		Sections:      len(res.Hits),
		IndexedChunks: indexedChunks,
		Degraded:      res.Degraded,
	}
	if res.Degraded {
		out.Degrade = degradeReason(res.DegradeKind)
	}

	return out
}

// searchErrorClass names the class for a failed search. The context cases come first:
// a canceled run reaches this through the same database calls a broken index does, and
// reporting a Ctrl-C as a store failure would be wrong on the most common one.
func searchErrorClass(err error) telemetry.ErrorClass {
	class, ok := telemetry.ClassifyContext(err)
	if ok {
		return class
	}
	if errors.Is(err, ErrDimensionMismatch) || errors.Is(err, ErrModelMismatch) {
		return telemetry.ClassConfig
	}

	return telemetry.ClassStore
}

// degradeReason maps this package's degrade kind onto the telemetry vocabulary.
//
// The two lists exist separately because the telemetry package imports nothing from this
// tree, so neither can name the other's values. The spec that guards them iterates this
// package's kinds and asserts each maps to a distinct value rather than restating either
// list, since a third hand-written copy would agree with both and catch nothing.
func degradeReason(k DegradeKind) telemetry.DegradeReason {
	switch k {
	case DegradeEmbeddings:
		return telemetry.DegradeEmbeddings
	case DegradeTimeout:
		return telemetry.DegradeTimeout
	case DegradeCanceled:
		return telemetry.DegradeCanceled
	case DegradeIndexMeta:
		return telemetry.DegradeIndexMeta
	default:
		return telemetry.DegradeOther
	}
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
//
// It also returns the kind of degradation a failure amounts to. The kind comes from
// which of the three steps failed, never from the error's text: the errors here carry
// the embeddings endpoint and fragments of a server's response body, and the kind is
// reported on a span that leaves the process.
func (s *Store) embedQueryVector(ctx context.Context, query string) ([]float32, DegradeKind, error) {
	meta, err := s.readMeta(ctx)
	if err != nil {
		return nil, degradeKind(err, DegradeIndexMeta), err
	}

	dim, err := s.emb.Dim(ctx)
	if err != nil {
		return nil, degradeKind(err, DegradeEmbeddings), err
	}
	if dim != meta.Dimension {
		return nil, DegradeNone, fmt.Errorf("%w: model %q now emits dimension %d but the index was built at %d; run 'fisk-ai knowledge index --reindex'", ErrDimensionMismatch, s.emb.Model(), dim, meta.Dimension)
	}

	qv, err := s.emb.EmbedQuery(ctx, query)
	if err != nil {
		return nil, degradeKind(err, DegradeEmbeddings), err
	}

	return normalize(qv), DegradeNone, nil
}

// degradeKind classifies a failure at step, giving the context cases precedence.
//
// The precedence matters and the common failure is what settles it: the embeddings
// client carries its own timeout, so a hung server produces an error that is both an
// embeddings failure and a deadline. "The server is slow" and "the server is down" are
// the two things an operator looks for, so the deadline wins and the step is the
// fallback.
func degradeKind(err error, step DegradeKind) DegradeKind {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return DegradeTimeout
	case errors.Is(err, context.Canceled):
		return DegradeCanceled
	default:
		return step
	}
}

// ftsSearch returns chunk ids ranked by BM25 for the prepared MATCH expression.
//
// The column weights are the deliberate replacement for an implicit one. While the
// breadcrumb was folded into the indexed body, heading tokens were counted twice
// and headings were weighted without anyone choosing to; storing the two apart
// removed that, and bm25Weights puts it back where it can be seen and changed.
func (s *Store) ftsSearch(ctx context.Context, match string, limit int) ([]result, error) {
	rows, err := s.db.QueryContext(ctx, ftsSearchQuery, match, limit)
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
		`SELECT c.id, d.path, d.title, c.heading_path, c.ordinal, c.body
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
		if err := rows.Scan(&h.ChunkID, &h.DocPath, &h.DocTitle, &h.HeadingPath, &h.Ordinal, &h.Content); err != nil {
			return nil, fmt.Errorf("hydrating results: %w", err)
		}
		h.Citation = Citation(h.DocPath, h.Ordinal)
		h.MappedCitation, h.Mapped = s.citations.Render(h.DocPath, h.Ordinal, h.HeadingPath)
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
// lexical. It names the kind of failure rather than asserting one: the index metadata
// is read on the same path as the embeddings call and fails the same way, so a fixed
// "embeddings unreachable" told an operator to go and check a server that was fine.
func DegradedTierLine(kind DegradeKind, reason string) string {
	return fmt.Sprintf("tier: hybrid -> DEGRADED to lexical (%s: %s)", degradeSummary(kind), reason)
}

// degradeSummary is the short phrase each degrade kind is described by, shared between
// the tier banner and the note the knowledge tool returns to the model so the two
// surfaces cannot come to disagree about what happened.
func degradeSummary(kind DegradeKind) string {
	switch kind {
	case DegradeIndexMeta:
		return "index metadata unreadable"
	case DegradeTimeout:
		return "embeddings server timed out"
	case DegradeCanceled:
		return "canceled"
	default:
		return "embeddings unreachable"
	}
}

// DegradeNote is the sentence a caller shows to explain a degraded query, including
// the fix where there is one to name.
func DegradeNote(kind DegradeKind) string {
	switch kind {
	case DegradeIndexMeta:
		return "the knowledge index metadata could not be read, so this query used the lexical tier only; run: fisk-ai knowledge doctor"
	case DegradeTimeout:
		return "the embeddings server did not respond in time, so this query used the lexical tier only"
	case DegradeCanceled:
		return "embedding the query was canceled, so this query used the lexical tier only"
	default:
		return "the embeddings server was unreachable, so this query used the lexical tier only"
	}
}

// scanCount is a small helper for count(*) queries used by the CLI stats/doctor.
func scanCount(ctx context.Context, db *sql.DB, query string, args ...any) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, err
	}

	return n, nil
}
