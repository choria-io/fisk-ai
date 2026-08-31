//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"

	"github.com/choria-io/fisk-ai/internal/rag"
)

// The machine renderings of knowledge search and knowledge match, for callers
// that script the CLI rather than read it. They are types of their own rather
// than the rag result structs with tags added, so the wire shape stays a decision
// made here instead of a side effect of a library struct gaining a field.
//
// Nothing on this path is sanitized for a terminal. Sanitizing rewrites the text
// the corpus holds, which is right for a screen and wrong for a parser, and the
// encoder escapes the control characters that make raw text dangerous to print. A
// consumer that puts these values on a terminal sanitizes them there.

// ragSearchHitJSON is one ranked chunk.
type ragSearchHitJSON struct {
	Citation    string `json:"citation"`
	DocPath     string `json:"doc_path"`
	DocTitle    string `json:"doc_title,omitempty"`
	Ordinal     int    `json:"ordinal"`
	HeadingPath string `json:"heading_path,omitempty"`

	// MappedCitation is present only when a citation rule matched it. Unmapped it
	// holds the Citation token itself, and repeating that under a second key
	// invites a consumer to treat a corpus path as a published address. Mapped
	// says which case this is without the consumer inferring it from absence.
	MappedCitation string `json:"mapped_citation,omitempty"`
	Mapped         bool   `json:"mapped"`

	// Content is the whole chunk and appears only under --full. The screen
	// rendering truncates to a snippet, which reads well and would be a lie in a
	// field named content, so this format carries the chunk or carries nothing.
	Content string `json:"content,omitempty"`
}

// ragSearchJSON is one knowledge search run.
type ragSearchJSON struct {
	Query  string `json:"query"`
	Status string `json:"status"`

	// VectorEnabled reports whether the hybrid tier is configured and Degraded
	// whether this query fell back to lexical anyway. Neither implies the other: a
	// store with no embeddings is lexical without having degraded, which is why
	// the tier cannot be read off Degraded alone.
	VectorEnabled bool   `json:"vector_enabled"`
	Degraded      bool   `json:"degraded"`
	DegradeKind   string `json:"degrade_kind,omitempty"`
	DegradeReason string `json:"degrade_reason,omitempty"`

	Hits []ragSearchHitJSON `json:"hits"`
}

// ragMatchDocJSON is one matched document.
type ragMatchDocJSON struct {
	Path     string `json:"path"`
	Citation string `json:"citation"`

	MappedCitation string `json:"mapped_citation,omitempty"`
	Mapped         bool   `json:"mapped"`

	BodyMatches    int `json:"body_matches"`
	HeadingMatches int `json:"heading_matches"`
	TotalChunks    int `json:"total_chunks"`
}

// ragMatchTermJSON is what the index holds for one query term. It is always
// present rather than gated behind --explain: a parser does not pay for a field
// it ignores, and a count that cannot be explained is the thing these numbers
// exist to prevent.
type ragMatchTermJSON struct {
	Surface   string   `json:"surface"`
	Stem      string   `json:"stem"`
	Documents int      `json:"documents"`
	Literal   int      `json:"literal"`
	Dropped   bool     `json:"dropped"`
	Related   []string `json:"related,omitempty"`
}

// ragMatchJSON is one knowledge match run.
type ragMatchJSON struct {
	Query    string `json:"query"`
	Compiled string `json:"compiled,omitempty"`
	Status   string `json:"status"`

	Matched          int  `json:"matched"`
	Returned         int  `json:"returned"`
	Truncated        bool `json:"truncated"`
	IndexedDocuments int  `json:"indexed_documents"`

	Documents []ragMatchDocJSON  `json:"documents"`
	Terms     []ragMatchTermJSON `json:"terms"`
}

// newRAGSearchJSON builds the search rendering. full decides whether chunk bodies
// are carried at all.
func newRAGSearchJSON(query string, res *rag.SearchResult, vectorEnabled bool, full bool) *ragSearchJSON {
	out := &ragSearchJSON{
		Query:         query,
		Status:        string(res.Status),
		VectorEnabled: vectorEnabled,
		Degraded:      res.Degraded,
		DegradeKind:   string(res.DegradeKind),
		DegradeReason: res.DegradeReason,
		Hits:          []ragSearchHitJSON{},
	}

	for _, h := range res.Hits {
		hit := ragSearchHitJSON{
			Citation:    h.Citation,
			DocPath:     h.DocPath,
			DocTitle:    h.DocTitle,
			Ordinal:     h.Ordinal,
			HeadingPath: h.HeadingPath,
			Mapped:      h.Mapped,
		}
		if h.Mapped {
			hit.MappedCitation = h.MappedCitation
		}
		if full {
			hit.Content = h.Content
		}

		out.Hits = append(out.Hits, hit)
	}

	return out
}

// newRAGMatchJSON builds the match rendering.
func newRAGMatchJSON(query string, res *rag.EnumerateResult) *ragMatchJSON {
	out := &ragMatchJSON{
		Query:            query,
		Compiled:         res.Compiled,
		Status:           string(res.Status),
		Matched:          res.Matched,
		Returned:         res.Returned,
		Truncated:        res.Truncated,
		IndexedDocuments: res.IndexedDocuments,
		Documents:        []ragMatchDocJSON{},
		Terms:            []ragMatchTermJSON{},
	}

	for _, d := range res.Docs {
		doc := ragMatchDocJSON{
			Path:           d.Path,
			Citation:       d.Citation,
			Mapped:         d.Mapped,
			BodyMatches:    d.BodyMatches,
			HeadingMatches: d.HeadingMatches,
			TotalChunks:    d.TotalChunks,
		}
		if d.Mapped {
			doc.MappedCitation = d.MappedCitation
		}

		out.Documents = append(out.Documents, doc)
	}

	for _, t := range res.Terms {
		out.Terms = append(out.Terms, ragMatchTermJSON{
			Surface:   t.Surface,
			Stem:      t.Stem,
			Documents: t.Docs,
			Literal:   t.Literal,
			Dropped:   t.Dropped,
			Related:   t.Related,
		})
	}

	return out
}

// writeRAGJSON writes one object to stdout. It is one object per run rather than a
// stream of them so that a write cut short fails to parse instead of parsing as a
// shorter answer.
func writeRAGJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	// Nothing here is destined for a script tag, and heading paths are full of the
	// angle brackets that separate a breadcrumb. Escaped, every one of them reads as
	// > to anyone who looks at the output.
	enc.SetEscapeHTML(false)

	return enc.Encode(v)
}
