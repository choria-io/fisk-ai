//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/choria-io/fisk-ai/internal/telemetry"
)

// EnumerateStatus classifies an enumerate outcome the caller reports without
// treating it as an error. It is a separate enum from SearchStatus rather than a
// reuse of it: SearchStatus folds "the index holds nothing" and "the query reduced
// to nothing" into one member, and those are the two states this feature exists to
// tell apart. A malformed query is an error rather than a status, because it
// carries a fix and there is nothing to report about the index.
type EnumerateStatus string

const (
	// EnumOK means the set is complete and was computed. It may be empty, and an
	// empty ok result is the answer this feature exists to give.
	EnumOK EnumerateStatus = "ok"

	// EnumIndexNotBuilt means no index file exists yet.
	EnumIndexNotBuilt EnumerateStatus = "index_not_built"

	// EnumCorpusEmpty means the index exists and holds no documents, so a zero says
	// nothing about any document.
	EnumCorpusEmpty EnumerateStatus = "corpus_empty"

	// EnumQueryEmpty means every term was dropped before anything was queried, so a
	// zero here is about the query rather than the index.
	EnumQueryEmpty EnumerateStatus = "query_empty"
)

// EnumerateTierLine is the tier banner every enumerate surface prints, overriding
// the canonical one. The canonical line names the retrieval tiers and is false
// here: enumeration never fuses vectors and never ranks, so a hybrid banner over a
// complete set would describe a query that did not run.
const EnumerateTierLine = "tier: lexical (FTS5) - matching is complete, never ranked"

// EnumerateSort selects the order matched documents are returned in, which has to
// be resolved before any budget is applied or a truncated set is an arbitrary one.
type EnumerateSort string

const (
	// SortByMatches orders by matching chunks, most first, then by path. It answers
	// "which of these is most about the query".
	SortByMatches EnumerateSort = "matches"

	// SortByPath orders by path alone, which is what makes two runs diffable.
	SortByPath EnumerateSort = "path"
)

// EnumerateOptions controls one enumerate call.
type EnumerateOptions struct {
	// Limit caps the documents returned; zero returns the complete set. The total is
	// reported either way, so a budget never hides the size of the answer.
	Limit int

	// Sort orders the set before Limit is applied. Empty means SortByMatches.
	Sort EnumerateSort

	// MinBodyMatches drops documents with fewer than this many matching body chunks.
	// It is the aboutness lever: a document that mentions a word once is not about
	// it. Documents filtered out are not counted in Matched, since the filter is part
	// of the question that was asked.
	MinBodyMatches int
}

// MatchedDoc is one document in the matched set. It carries no title because no
// surface renders one here: match answers which documents hold the terms, and the
// path is the answer. Hit.DocTitle carries the title on the search path, where a
// chunk with no heading has nothing else to identify it by.
type MatchedDoc struct {
	Path     string
	Citation string
	// BodyMatches and HeadingMatches count the chunks of this document that match in
	// each column. They are separate because a naive single count inverts: the
	// breadcrumb used to be indexed inside the body, so a three-chunk document headed
	// "Deprecation Policy" whose body never mentions it scored 3, beating a document
	// that genuinely discusses it and scored 1.
	BodyMatches    int
	HeadingMatches int
	TotalChunks    int

	// MappedCitation is how this document is cited outside the corpus, rendered from
	// the configured citation rules: a URL where the rules publish one, and whatever
	// else a rule renders otherwise. It is the Citation token itself when no rule
	// matched the path. It cites the document at its first matching chunk:
	// ${ordinal} is filled, and ${heading} and ${anchor} render empty because
	// enumeration answers which documents hold a term and never loads a section.
	//
	// A listing built on Sources renders the same document with
	// CitationMapper.RenderDocument; see there for how the two differ.
	MappedCitation string

	// Mapped reports whether a rule produced MappedCitation, which MappedCitation
	// alone does not say: a rule may render a path unchanged.
	Mapped bool
}

// TermReport is one query term and what the index holds for it. Literal is the
// count from the unstemmed index, and is what lets a narrow or empty result name
// the forms that are actually present rather than leaving the author to guess.
type TermReport struct {
	Surface string // as typed
	Stem    string // as the index stores it, from SQLite's own tokenizer
	Docs    int    // documents matching the stem
	Literal int    // documents containing the surface form as written
	Dropped bool   // below the minimum term length, so never queried

	// Related are other words in the index that share this term's stem, most common
	// first, so a count larger than the literal one can say what made up the
	// difference instead of leaving it as a discrepancy. It is populated only when
	// there is a difference to explain.
	Related []string
}

// EnumerateResult is the complete answer to one enumerate call.
type EnumerateResult struct {
	Docs             []MatchedDoc
	Compiled         string
	Terms            []TermReport
	Matched          int // before any limit
	Returned         int
	Truncated        bool
	IndexedDocuments int
	Status           EnumerateStatus
}

// Enumerate returns every indexed document matching query, as a set rather than a
// ranking: the predicate is MATCH and no score or cutoff takes part, so an empty
// result means the documents do not contain the terms rather than that nothing
// scored well. That is the whole point of the command, and it is why composition
// happens here in Go instead of inside one MATCH expression.
//
// FTS5 booleans evaluate within a single row, which is one chunk, so
// '"retention" AND "policy"' misses a document holding one word in each of two
// chunks. Each term is therefore run as its own document-set query and the sets are
// intersected and subtracted here. Only document ids are held, never rows, so the
// full set is computed exactly and the limit applies to hydration alone.
//
// Its span covers all nine returns through a deferred Finish over the named returns,
// which is safe only because every failing path returns a nil result explicitly. Reading
// the local instead would be the trap this shape exists to avoid: the result is
// initialized to a successful status before anything runs, so a database failure would
// export a span reporting a completed enumeration alongside its own error.
func (s *Store) Enumerate(ctx context.Context, query string, opts EnumerateOptions) (res *EnumerateResult, err error) {
	ctx, span := telemetry.ProviderFromContext(ctx).StartEnumerate(ctx, telemetry.EnumerateInfo{
		Limit:          opts.Limit,
		MinBodyMatches: opts.MinBodyMatches,
	})

	// The class for the one failure that is not the store's fault. It is named here
	// rather than recognized from the error, because a compiled-query failure carries no
	// sentinel and there is exactly one place it can come from.
	var class telemetry.ErrorClass
	defer func() { span.Finish(enumerateOutcome(res, err, class)) }()

	res = &EnumerateResult{Docs: []MatchedDoc{}, Terms: []TermReport{}, Status: EnumOK}

	// The agent opens a read-only store over a nonexistent file on an ordinary first
	// run, so this is the common path rather than an edge case.
	if s.db == nil {
		res.Status = EnumIndexNotBuilt
		return res, nil
	}

	compiled, err := compileEnumerateQuery(query)
	if err != nil {
		class = telemetry.ClassInvalidQuery
		return nil, err
	}
	res.Compiled = compiled.Compiled()

	res.IndexedDocuments, err = scanCount(ctx, s.db, `SELECT count(*) FROM documents`)
	if err != nil {
		return nil, fmt.Errorf("counting documents: %w", err)
	}
	if res.IndexedDocuments == 0 {
		res.Status = EnumCorpusEmpty
		return res, nil
	}

	res.Terms, err = s.termReports(ctx, compiled)
	if err != nil {
		return nil, err
	}

	// Every term was dropped, so nothing was queried. Reporting this as an empty
	// match would be a complete answer to a question nobody asked.
	if len(compiled.Positive) == 0 {
		res.Status = EnumQueryEmpty
		return res, nil
	}

	ids, err := s.enumerateDocumentSet(ctx, compiled)
	if err != nil {
		return nil, err
	}

	docs, err := s.describeMatches(ctx, ids, compiled)
	if err != nil {
		return nil, err
	}

	if opts.MinBodyMatches > 0 {
		kept := docs[:0]
		for _, d := range docs {
			if d.BodyMatches >= opts.MinBodyMatches {
				kept = append(kept, d)
			}
		}
		docs = kept
	}

	sortMatchedDocs(docs, opts.Sort)

	res.Matched = len(docs)
	if opts.Limit > 0 && len(docs) > opts.Limit {
		docs = docs[:opts.Limit]
		res.Truncated = true
	}
	res.Docs = docs
	res.Returned = len(docs)

	return res, nil
}

// enumerateOutcome builds the span outcome from what the enumeration returned. A nil
// result is a failure whatever err says, because every failing return abandons it.
//
// The corpus size is reported as absent rather than zero when no index existed, since
// the count was never taken there and a zero would read as an empty corpus, which is a
// different answer with a different fix.
func enumerateOutcome(res *EnumerateResult, err error, class telemetry.ErrorClass) telemetry.EnumerateOutcome {
	if res == nil {
		return telemetry.EnumerateOutcome{Class: enumerateErrorClass(err, class), Failed: true}
	}

	out := telemetry.EnumerateOutcome{
		Status:    string(res.Status),
		Matched:   res.Matched,
		Documents: res.Returned,
		Truncated: res.Truncated,
	}
	if res.Status != EnumIndexNotBuilt {
		out.IndexedDocuments = &res.IndexedDocuments
	}

	return out
}

// enumerateErrorClass gives the context cases precedence over the class the call site
// named, then falls back to the store. A canceled run reaches this through the same
// database calls a broken index does.
func enumerateErrorClass(err error, named telemetry.ErrorClass) telemetry.ErrorClass {
	class, ok := telemetry.ClassifyContext(err)
	if ok {
		return class
	}
	if named.Set() {
		return named
	}

	return telemetry.ClassStore
}

// enumerateDocumentSet runs one document-set query per term and composes them:
// positives intersect, negatives subtract. It stops early once the set is empty,
// since nothing can re-enter it.
func (s *Store) enumerateDocumentSet(ctx context.Context, q *enumQuery) (map[int64]bool, error) {
	var set map[int64]bool

	for i, t := range q.Positive {
		ids, err := s.documentsMatching(ctx, ftsTablePorter, t.match())
		if err != nil {
			return nil, err
		}
		if i == 0 {
			set = ids
			continue
		}
		for id := range set {
			if !ids[id] {
				delete(set, id)
			}
		}
		if len(set) == 0 {
			return set, nil
		}
	}

	for _, t := range q.Negative {
		if len(set) == 0 {
			return set, nil
		}
		ids, err := s.documentsMatching(ctx, ftsTablePorter, t.match())
		if err != nil {
			return nil, err
		}
		for id := range ids {
			delete(set, id)
		}
	}

	return set, nil
}

// ftsTablePorter and ftsTableExact name the two full-text tables. They are
// constants selected by this package and never derived from input, because a table
// name cannot be bound as a parameter.
const (
	ftsTablePorter = "chunks_fts"
	ftsTableExact  = "chunks_fts_exact"
)

// documentsMatching returns the set of document ids with at least one chunk
// matching the expression in the named table.
func (s *Store) documentsMatching(ctx context.Context, table, match string) (map[int64]bool, error) {
	q := fmt.Sprintf(`SELECT DISTINCT c.document_id FROM %s f JOIN chunks c ON c.id = f.rowid WHERE %[1]s MATCH ?`, table)

	rows, err := s.db.QueryContext(ctx, q, match)
	if err != nil {
		return nil, fmt.Errorf("matching documents: %w", err)
	}
	defer rows.Close()

	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("matching documents: %w", err)
		}
		out[id] = true
	}

	return out, rows.Err()
}

// describeMatches turns a document id set into the reported rows. The per-column
// counts and the first matching ordinal come from three grouped queries over the
// whole index rather than one query per document, so the cost does not grow with
// the size of the matched set.
func (s *Store) describeMatches(ctx context.Context, ids map[int64]bool, q *enumQuery) ([]MatchedDoc, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	bodyCounts, err := s.chunkCounts(ctx, columnScopedExpr(q.Positive, enumColumnBody))
	if err != nil {
		return nil, err
	}
	headingCounts, err := s.chunkCounts(ctx, columnScopedExpr(q.Positive, enumColumnHeading))
	if err != nil {
		return nil, err
	}
	firstOrdinals, err := s.firstMatchingOrdinals(ctx, anyTermExpr(q.Positive))
	if err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.path, count(c.id) FROM documents d
		JOIN chunks c ON c.document_id = d.id GROUP BY d.id, d.path`)
	if err != nil {
		return nil, fmt.Errorf("describing matches: %w", err)
	}
	defer rows.Close()

	var out []MatchedDoc
	for rows.Next() {
		var (
			id    int64
			path  string
			total int
		)
		if err := rows.Scan(&id, &path, &total); err != nil {
			return nil, fmt.Errorf("describing matches: %w", err)
		}
		if !ids[id] {
			continue
		}

		mappedCitation, mapped := s.citations.Render(path, firstOrdinals[id], "")

		out = append(out, MatchedDoc{
			Path:           path,
			Citation:       Citation(path, firstOrdinals[id]),
			BodyMatches:    bodyCounts[id],
			HeadingMatches: headingCounts[id],
			TotalChunks:    total,
			MappedCitation: mappedCitation,
			Mapped:         mapped,
		})
	}

	return out, rows.Err()
}

// chunkCounts counts matching chunks per document for one expression. An empty
// expression means no term is scoped to that column, which is not an error: it is a
// count of zero for every document.
func (s *Store) chunkCounts(ctx context.Context, match string) (map[int64]int, error) {
	out := map[int64]int{}
	if match == "" {
		return out, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT c.document_id, count(*) FROM chunks_fts f JOIN chunks c ON c.id = f.rowid
		 WHERE chunks_fts MATCH ? GROUP BY c.document_id`, match)
	if err != nil {
		return nil, fmt.Errorf("counting matching chunks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id int64
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("counting matching chunks: %w", err)
		}
		out[id] = n
	}

	return out, rows.Err()
}

// firstMatchingOrdinals returns the lowest matching chunk ordinal per document, so
// a citation points at a chunk that actually matched rather than at the top of the
// file.
func (s *Store) firstMatchingOrdinals(ctx context.Context, match string) (map[int64]int, error) {
	out := map[int64]int{}
	if match == "" {
		return out, nil
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT c.document_id, min(c.ordinal) FROM chunks_fts f JOIN chunks c ON c.id = f.rowid
		 WHERE chunks_fts MATCH ? GROUP BY c.document_id`, match)
	if err != nil {
		return nil, fmt.Errorf("locating matches: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id      int64
			ordinal int
		)
		if err := rows.Scan(&id, &ordinal); err != nil {
			return nil, fmt.Errorf("locating matches: %w", err)
		}
		out[id] = ordinal
	}

	return out, rows.Err()
}

// columnScopedExpr builds the OR of the terms that can match in one column, scoped
// to it. Terms scoped to the other column are left out, since they are not claims
// about this one.
//
// The OR is deliberate and is not the matching predicate. Matching is the
// intersection computed in Go; this is a per-document statistic, and a document
// holding one term in one chunk and another in the next must report both chunks
// rather than the zero an AND would give it.
func columnScopedExpr(terms []enumTerm, column string) string {
	var parts []string
	for _, t := range terms {
		if t.Column != "" && t.Column != column {
			continue
		}
		parts = append(parts, enumTerm{Surface: t.Surface}.match())
	}
	if len(parts) == 0 {
		return ""
	}

	return column + ":(" + strings.Join(parts, " OR ") + ")"
}

// anyTermExpr builds the OR of every positive term, keeping each term's own column
// scope, which is what "this chunk took part in the match" means.
func anyTermExpr(terms []enumTerm) string {
	var parts []string
	for _, t := range terms {
		parts = append(parts, t.match())
	}
	if len(parts) == 0 {
		return ""
	}

	return "(" + strings.Join(parts, " OR ") + ")"
}

// sortMatchedDocs orders the set before any limit applies. Both orders end in path,
// so the result is deterministic and two runs of the same query are diffable.
func sortMatchedDocs(docs []MatchedDoc, order EnumerateSort) {
	sort.Slice(docs, func(a, b int) bool {
		if order != SortByPath {
			ta := docs[a].BodyMatches + docs[a].HeadingMatches
			tb := docs[b].BodyMatches + docs[b].HeadingMatches
			if ta != tb {
				return ta > tb
			}
		}

		return docs[a].Path < docs[b].Path
	})
}

// termReports describes each term against both indexes: how many documents hold it
// in any form, and how many hold it as written. The gap between the two is what
// stops a stemmed count reading as a literal one, and what lets an empty result say
// which forms the index does have.
func (s *Store) termReports(ctx context.Context, q *enumQuery) ([]TermReport, error) {
	out := make([]TermReport, 0, len(q.Positive)+len(q.Negative)+len(q.Dropped))

	terms := make([]enumTerm, 0, len(q.Positive)+len(q.Negative))
	terms = append(terms, q.Positive...)
	terms = append(terms, q.Negative...)

	surfaces := make([]string, 0, len(terms))
	for _, t := range terms {
		surfaces = append(surfaces, t.Surface)
	}
	stems := stemSurfaces(ctx, surfaces)

	for _, t := range terms {
		docs, err := s.documentsMatching(ctx, ftsTablePorter, t.match())
		if err != nil {
			return nil, err
		}
		literal, err := s.documentsMatching(ctx, ftsTableExact, t.match())
		if err != nil {
			return nil, err
		}

		report := TermReport{
			Surface: t.Surface,
			Stem:    stems[t.Surface],
			Docs:    len(docs),
			Literal: len(literal),
		}

		// Only when the two counts disagree: with nothing to explain, the scan is
		// cost for an empty answer.
		if report.Docs > report.Literal && report.Stem != "" {
			report.Related, err = s.relatedForms(ctx, report.Surface, report.Stem)
			if err != nil {
				return nil, err
			}
		}

		out = append(out, report)
	}

	for _, d := range q.Dropped {
		out = append(out, TermReport{Surface: d, Dropped: true})
	}

	return out, nil
}

// maxRelatedForms caps how many other forms of a word are named. The point is to
// show what a stemmed count reached, not to dump a vocabulary.
const maxRelatedForms = 5

// relatedForms returns the words in the index that share stem with surface, most
// common first and excluding surface itself.
//
// Candidates are drawn from the unstemmed vocabulary by stem prefix rather than by
// stemming the whole vocabulary, which would be thousands of words per call. That
// bounds it to a range scan and a handful of stem lookups, at the cost of missing a
// form whose stem is not its own prefix, which Porter can produce by restoring a
// final 'e'. The miss is in an explanation of a count, never in the count or the
// matched set, so an incomplete list here cannot make an answer wrong.
func (s *Store) relatedForms(ctx context.Context, surface, stem string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT term, doc FROM chunks_vocab WHERE term >= ? AND term < ? ORDER BY doc DESC`,
		stem, prefixUpperBound(stem))
	if err != nil {
		return nil, fmt.Errorf("reading index vocabulary: %w", err)
	}
	defer rows.Close()

	var candidates []string
	for rows.Next() {
		var (
			term string
			doc  int
		)
		if err := rows.Scan(&term, &doc); err != nil {
			return nil, fmt.Errorf("reading index vocabulary: %w", err)
		}
		if !strings.EqualFold(term, surface) {
			candidates = append(candidates, term)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// A shared prefix is not a shared stem: "deprecation" and "deprecates" both
	// start with "deprec", and so does "depreciate", which is a different word.
	stems := stemSurfaces(ctx, candidates)

	var out []string
	for _, c := range candidates {
		if stems[c] != stem {
			continue
		}
		out = append(out, c)
		if len(out) == maxRelatedForms {
			break
		}
	}

	return out, nil
}

// prefixUpperBound returns the exclusive upper bound of a prefix range scan, by
// incrementing the last rune of the prefix.
func prefixUpperBound(prefix string) string {
	if prefix == "" {
		return ""
	}

	runes := []rune(prefix)
	runes[len(runes)-1]++

	return string(runes)
}

// stemSurfaces returns the stem the index stores for each surface form, computed by
// SQLite's own porter tokenizer in a scratch in-memory database rather than by a
// second implementation of the algorithm, which would drift from the index it is
// meant to describe.
//
// It cannot use the index's own connection: the reader is query-only and cannot
// create a table, and the writer's tables hold the corpus. A failure here degrades
// to an empty stem rather than failing the query, since the stem is an explanation
// and the answer does not depend on it.
func stemSurfaces(ctx context.Context, surfaces []string) map[string]string {
	out := map[string]string{}
	if len(surfaces) == 0 {
		return out
	}

	db, err := sql.Open("sqlite", "file:enumerate-stems?mode=memory&cache=private")
	if err != nil {
		return out
	}
	defer db.Close()

	for _, stmt := range []string{
		`CREATE VIRTUAL TABLE terms USING fts5(surface, tokenize='porter unicode61')`,
		`CREATE VIRTUAL TABLE terms_vocab USING fts5vocab('terms', 'instance')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return out
		}
	}

	for i, surface := range surfaces {
		if _, err := db.ExecContext(ctx, `INSERT INTO terms(rowid, surface) VALUES(?, ?)`, i, surface); err != nil {
			return out
		}
	}

	// One surface may tokenize to several terms (a phrase, or a hyphenated word), so
	// the stems are reassembled in offset order into the form the index would match.
	rows, err := db.QueryContext(ctx, `SELECT doc, term FROM terms_vocab ORDER BY doc, offset`)
	if err != nil {
		return out
	}
	defer rows.Close()

	parts := map[int][]string{}
	for rows.Next() {
		var (
			doc  int
			term string
		)
		if err := rows.Scan(&doc, &term); err != nil {
			return out
		}
		parts[doc] = append(parts[doc], term)
	}
	if rows.Err() != nil {
		return out
	}

	for i, surface := range surfaces {
		if stem, ok := parts[i]; ok {
			out[surface] = strings.Join(stem, " ")
		}
	}

	return out
}
