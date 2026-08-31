//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook

import (
	"context"
	"errors"

	"github.com/choria-io/fisk-ai/internal/rag"
)

// ErrNoSearchResult is returned when a Searcher answers with neither a result nor an
// error. rag.Store never does, and an implementation that does has a defect Run
// cannot paper over: a nil result is not an empty one, and reading it as one would
// report an index that ranked nothing.
var ErrNoSearchResult = errors.New("the search returned no result and no error")

// Searcher is the index Run queries. rag.Store satisfies it, and a caller with its
// own retrieval in front of the store passes that instead.
type Searcher interface {
	Search(ctx context.Context, query string, topK int) (*rag.SearchResult, error)
}

// RunOptions are what Run needs besides the message.
type RunOptions struct {
	// Parse is handed to Parse, which decides whether the message asks for a lookup
	// and which terms it asks with.
	Parse Options

	// TopK is how many chunks the search returns. rag.Store clamps it at its own
	// ceiling of twenty.
	TopK int

	// Block is handed to Block, except for its Mode: Run sets that from the decision,
	// since the caller learns the mode from the same call that renders the block.
	Block BlockOptions
}

// Result is what Run made of one message.
type Result struct {
	// Decision is what Parse made of the message, carrying the mode, the terms and
	// the gate that stopped a lookup.
	Decision Decision

	// Search is what the index returned, and is nil when a gate stopped the lookup.
	// It carries the status and the degradation, so a caller reports a missing index
	// and a lexical fallback in the terms rag states them.
	Search *rag.SearchResult

	// Block is the rendered block, and is empty when no lookup ran, when the status
	// was not rag.StatusOK and when nothing ranked.
	Block string
}

// Run takes a message from Parse through the index to a rendered block, which is
// the whole of what a prompt hook does with one message. A caller drives it for a
// message a person typed and again for a query the same person is tuning, so the two
// agree on every gate.
//
// A decision carrying a Skip returns before the search, with Result.Search nil. A
// status other than rag.StatusOK renders no block, whatever hits came with it: the
// status says the lookup did not reach a built index holding the terms, and hits
// alongside one are not an answer to offer a model. A search error is returned as one.
func Run(ctx context.Context, s Searcher, prompt string, opts RunOptions) (Result, error) {
	d := Parse(prompt, opts.Parse)

	res := Result{Decision: d}
	if d.Query == "" {
		return res, nil
	}

	sr, err := s.Search(ctx, d.Query, opts.TopK)
	if err != nil {
		return res, err
	}
	if sr == nil {
		return res, ErrNoSearchResult
	}

	res.Search = sr
	if sr.Status != rag.StatusOK {
		return res, nil
	}

	blockOpts := opts.Block
	blockOpts.Mode = d.Mode
	res.Block = Block(sr.Hits, blockOpts)

	return res, nil
}

// Outcome names what Run made of a message, folding the gate, the search status and
// the ranking into one value. A caller reports one of these; the words it reports
// them in are the caller's, since a logfile, a terminal and a hook each want their
// own.
type Outcome string

const (
	// OutcomeBlock is a run that rendered a block.
	OutcomeBlock Outcome = "block"

	// OutcomeSkipped is a run a gate stopped before the search. Decision.Skip names
	// the gate.
	OutcomeSkipped Outcome = "skipped"

	// OutcomeIndexNotBuilt is a lookup against an index nobody has built yet.
	OutcomeIndexNotBuilt Outcome = "index_not_built"

	// OutcomeIndexEmpty is a lookup against an index holding no chunks, or one whose
	// terms the index cannot be queried for.
	OutcomeIndexEmpty Outcome = "index_empty"

	// OutcomeNoHits is a lookup that reached the index and ranked nothing.
	OutcomeNoHits Outcome = "no_hits"

	// OutcomeNoSearch is a run whose search never answered, which Run reports as an
	// error. A Result that Run returned with a nil error never carries it.
	OutcomeNoSearch Outcome = "no_search"
)

// Outcome classifies the run, so a caller reports it without reading three fields and
// rebuilding the same rules. Every outcome but OutcomeBlock rendered no block.
func (r Result) Outcome() Outcome {
	switch {
	case r.Decision.Skip != SkipNone:
		return OutcomeSkipped

	case r.Search == nil:
		return OutcomeNoSearch

	case r.Search.Status == rag.StatusIndexNotBuilt:
		return OutcomeIndexNotBuilt

	case r.Search.Status == rag.StatusIndexEmpty:
		return OutcomeIndexEmpty

	case r.Block == "":
		return OutcomeNoHits

	default:
		return OutcomeBlock
	}
}
