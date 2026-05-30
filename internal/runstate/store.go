//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"errors"
	"time"
)

var (
	// ErrNotFound is returned when a run id has no stored journal.
	ErrNotFound = errors.New("run not found")
	// ErrExists is returned by Create when a journal for the id already exists.
	ErrExists = errors.New("run already exists")
	// ErrInvalidID is returned when a run id is not a valid KSUID; it must be
	// validated before it is ever used as a path component.
	ErrInvalidID = errors.New("invalid run id")
	// ErrLocked is returned when another process holds the run's lock.
	ErrLocked = errors.New("run is locked by another process")
	// ErrSeqGap is returned by Append when a seq skips ahead of the journal.
	ErrSeqGap = errors.New("record seq gap")
)

// RunInfo is a summary of a stored run, for listing.
type RunInfo struct {
	RunID   string
	Created time.Time
	Updated time.Time
	Model   string
	Prompt  string
	// Terminal is the reason the run ended, or empty if it is still open (was
	// suspended or crashed).
	Terminal TerminalReason
}

// Journal is an append-only record log for a single run. Append is idempotent on
// a duplicate seq so a crash-retry of the most recent event is a no-op. A Journal
// is not safe for concurrent use; the Store guards each run with a lock.
type Journal interface {
	// Append writes rec at seq. A seq equal to or below the last written seq is
	// treated as an already-recorded duplicate and ignored; a seq more than one
	// beyond the last is an ErrSeqGap.
	Append(seq uint64, rec Record) error
	// Records returns every record in order, dropping an unparsable final line
	// (a torn tail from a crash mid-write) but erroring on interior corruption.
	Records() ([]Record, error)
	// LastSeq returns the highest seq written, so a resuming writer continues the
	// sequence rather than colliding with existing records.
	LastSeq() uint64
	// Close releases the journal and its lock.
	Close() error
}

// Store persists run journals. The file implementation is used today; a
// JetStream stream (subject <prefix>.<run>.<seq>, MaxMsgsPerSubject=1 with
// discard-new-per-subject for an unbounded dedup window) is the intended second
// backend, hence the append-oriented, seq-keyed interface.
type Store interface {
	// Create starts a new run, writing meta as seq 1, and returns the locked
	// journal. It fails with ErrExists if the id is already present.
	Create(id string, meta MetaRecord) (Journal, error)
	// Open locks an existing run's journal for appending (resume). It fails with
	// ErrNotFound if the id is unknown.
	Open(id string) (Journal, error)
	// Load reads and folds a run without locking it, for inspection and listing.
	Load(id string) (*RunState, error)
	// List summarizes all stored runs.
	List() ([]RunInfo, error)
	// Delete removes a run's journal and lock.
	Delete(id string) error
}
