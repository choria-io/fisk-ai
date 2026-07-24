//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agenttest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/choria-io/fisk-ai/internal/runstate"
)

// FakeSessionStore is an in-memory runstate.Store for tests: it keeps each run's
// journal in a map guarded by a mutex, so a checkpoint+resume pair can be handed one
// store through agent.Options.SessionStore without a file backend or a NATS
// connection. Because the store lives only in this instance, a resume finds its
// session only if the injected store was actually borrowed across both runs, which
// is what the shared-session example asserts. It is one of the separate-package
// fakes proving each injectable seam is implementable from outside its own package,
// and it is safe for the concurrent use runs sharing one store make of it.
type FakeSessionStore struct {
	mu   sync.Mutex
	runs map[string]*fakeJournal
}

// FakeSessionStore implements runstate.Store and fakeJournal implements
// runstate.Journal; the assertions are the separate-package interface audit,
// failing to compile if the seam stops being implementable from outside its own
// package.
var (
	_ runstate.Store   = (*FakeSessionStore)(nil)
	_ runstate.Journal = (*fakeJournal)(nil)
)

// NewFakeSessionStore returns an empty in-memory session store.
func NewFakeSessionStore(tb testing.TB) *FakeSessionStore {
	tb.Helper()
	return &FakeSessionStore{runs: map[string]*fakeJournal{}}
}

// Create implements runstate.Store.
func (s *FakeSessionStore) Create(id string, meta runstate.MetaRecord) (runstate.Journal, error) {
	err := runstate.ValidateID(id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.runs[id]; ok {
		return nil, fmt.Errorf("%w: %q", runstate.ErrExists, id)
	}

	j := &fakeJournal{held: true}
	// The Meta record is seq 1 and frames the run, mirroring the file backend.
	err = j.append(1, runstate.Record{Seq: 1, Protocol: runstate.MetaProtocol, Meta: &meta})
	if err != nil {
		return nil, err
	}
	s.runs[id] = j

	return j, nil
}

// Open implements runstate.Store.
func (s *FakeSessionStore) Open(id string) (runstate.Journal, error) {
	err := runstate.ValidateID(id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	j, ok := s.runs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", runstate.ErrNotFound, id)
	}
	if !j.acquire() {
		return nil, runstate.ErrLocked
	}

	return j, nil
}

// Load implements runstate.Store.
func (s *FakeSessionStore) Load(id string) (*runstate.RunState, error) {
	err := runstate.ValidateID(id)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	j, ok := s.runs[id]
	s.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", runstate.ErrNotFound, id)
	}

	return runstate.Fold(j.snapshot())
}

// List implements runstate.Store.
func (s *FakeSessionStore) List() ([]runstate.RunInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]runstate.RunInfo, 0, len(s.runs))
	for id, j := range s.runs {
		rs, err := runstate.Fold(j.snapshot())
		if err != nil {
			return nil, err
		}
		info := runstate.RunInfo{RunID: id, Prompt: rs.Prompt}
		if rs.Terminal != nil {
			info.Terminal = rs.Terminal.Reason
		}
		out = append(out, info)
	}

	return out, nil
}

// Delete implements runstate.Store.
func (s *FakeSessionStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)

	return nil
}

// fakeJournal is one run's in-memory append-only record log. held models the file
// backend's per-run lock so a second Open of a still-open run fails with ErrLocked.
type fakeJournal struct {
	mu      sync.Mutex
	held    bool
	records []runstate.Record
	lastSeq uint64
}

// acquire takes the lock, reporting false when it is already held.
func (j *fakeJournal) acquire() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.held {
		return false
	}
	j.held = true

	return true
}

// Append implements runstate.Journal.
func (j *fakeJournal) Append(seq uint64, rec runstate.Record) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	return j.append(seq, rec)
}

// append is the unlocked core, also called from Create while the journal is not yet
// published and so cannot be contended. It uses the same seq accounting as the file
// backend: runstate.CheckAppend folds a duplicate and rejects a gap, and the record's
// Seq is stamped from the seq argument so a fold sees a strictly increasing sequence.
func (j *fakeJournal) append(seq uint64, rec runstate.Record) error {
	skip, err := runstate.CheckAppend(j.lastSeq, seq)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}
	rec.Seq = seq
	j.records = append(j.records, rec)
	j.lastSeq = seq

	return nil
}

// Records implements runstate.Journal.
func (j *fakeJournal) Records() ([]runstate.Record, error) {
	return j.snapshot(), nil
}

// LastSeq implements runstate.Journal.
func (j *fakeJournal) LastSeq() uint64 {
	j.mu.Lock()
	defer j.mu.Unlock()

	return j.lastSeq
}

// Close implements runstate.Journal, releasing the per-run lock.
func (j *fakeJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.held = false

	return nil
}

// snapshot returns a copy of the records under lock, for Load and Records.
func (j *fakeJournal) snapshot() []runstate.Record {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]runstate.Record, len(j.records))
	copy(out, j.records)

	return out
}
