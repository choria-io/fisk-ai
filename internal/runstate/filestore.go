//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// idPattern constrains a run id to a safe, single path component. It is also a
// valid NATS subject token, so the same ids carry over to the JetStream backend.
// Operator-chosen names and machine-generated KSUIDs both satisfy it.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// FileStore stores each run as a JSON-lines journal (<id>.json) under a
// directory, guarded by a per-run lock file (<id>.lock). Conversation and tool
// IO are sensitive, so the directory is 0700 and journals are 0600.
type FileStore struct {
	dir string
}

// DefaultDir returns the default run store directory, honoring XDG_STATE_HOME and
// falling back to ~/.local/state. Runs are never stored in the working
// directory, where they would leak into repositories.
func DefaultDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}

	return filepath.Join(base, "fisk-ai", "runs"), nil
}

// OpenStore returns a FileStore rooted at dir, or at DefaultDir() when dir is
// empty. It is the single resolution point shared by the run and session
// commands, so an operator's --state-dir (or its absence) resolves identically
// across them.
func OpenStore(dir string) (*FileStore, error) {
	if dir == "" {
		d, err := DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}

	return NewFileStore(dir)
}

// NewFileStore returns a FileStore rooted at dir, creating it 0700 if needed.
func NewFileStore(dir string) (*FileStore, error) {
	err := os.MkdirAll(dir, 0o700)
	if err != nil {
		return nil, fmt.Errorf("creating run store %q: %w", dir, err)
	}

	return &FileStore{dir: dir}, nil
}

func validateID(id string) error {
	if len(id) > 128 || !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %q (use letters, digits, '-' or '_')", ErrInvalidID, id)
	}

	return nil
}

func (s *FileStore) journalPath(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *FileStore) lockPath(id string) string {
	return filepath.Join(s.dir, id+".lock")
}

// Create implements Store.
func (s *FileStore) Create(id string, meta MetaRecord) (Journal, error) {
	err := validateID(id)
	if err != nil {
		return nil, err
	}

	_, err = os.Stat(s.journalPath(id))
	if err == nil {
		return nil, fmt.Errorf("%w: %q", ErrExists, id)
	}
	if !os.IsNotExist(err) {
		return nil, err
	}

	j, err := s.openJournal(id, true)
	if err != nil {
		return nil, err
	}

	err = j.Append(1, Record{Seq: 1, Protocol: MetaProtocol, Meta: &meta})
	if err != nil {
		j.Close()
		return nil, err
	}

	return j, nil
}

// Open implements Store.
func (s *FileStore) Open(id string) (Journal, error) {
	err := validateID(id)
	if err != nil {
		return nil, err
	}

	_, err = os.Stat(s.journalPath(id))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}

	return s.openJournal(id, false)
}

func (s *FileStore) openJournal(id string, created bool) (*fileJournal, error) {
	lock, err := acquireLock(s.lockPath(id))
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(s.journalPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		lock.release()
		return nil, err
	}

	j := &fileJournal{path: s.journalPath(id), f: f, lock: lock, dirCreated: created, dir: s.dir}

	last, err := lastSeq(s.journalPath(id))
	if err != nil {
		j.Close()
		return nil, err
	}
	j.lastSeq = last

	return j, nil
}

// Load implements Store.
func (s *FileStore) Load(id string) (*RunState, error) {
	err := validateID(id)
	if err != nil {
		return nil, err
	}

	recs, err := readRecords(s.journalPath(id))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}

	return Fold(recs)
}

// List implements Store.
func (s *FileStore) List() ([]RunInfo, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}

	var out []RunInfo
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		id := name[:len(name)-len(".json")]
		if validateID(id) != nil {
			continue
		}

		recs, err := readRecords(s.journalPath(id))
		if err != nil || len(recs) == 0 || recs[0].Meta == nil {
			continue
		}
		rs, err := Fold(recs)
		if err != nil {
			continue
		}

		info := RunInfo{RunID: rs.RunID, Created: recs[0].Meta.Created, Model: rs.Fingerprint.Model, Prompt: rs.Prompt}
		if fi, err := e.Info(); err == nil {
			info.Updated = fi.ModTime()
		}
		if rs.Terminal != nil {
			info.Terminal = rs.Terminal.Reason
		}
		out = append(out, info)
	}

	return out, nil
}

// Delete implements Store.
func (s *FileStore) Delete(id string) error {
	err := validateID(id)
	if err != nil {
		return err
	}

	err = os.Remove(s.journalPath(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	err = os.Remove(s.lockPath(id))
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return nil
}

// fileJournal is the JSON-lines journal handle returned by FileStore.
type fileJournal struct {
	path       string
	dir        string
	f          *os.File
	lock       *fileLock
	lastSeq    uint64
	dirCreated bool
}

// Append implements Journal.
func (j *fileJournal) Append(seq uint64, rec Record) error {
	if seq <= j.lastSeq {
		// Already recorded (a crash-retry of the most recent event). Idempotent.
		return nil
	}
	if seq > j.lastSeq+1 {
		return fmt.Errorf("%w: seq %d skips ahead of %d", ErrSeqGap, seq, j.lastSeq)
	}
	rec.Seq = seq

	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshaling record: %w", err)
	}
	line = append(line, '\n')

	_, err = j.f.Write(line)
	if err != nil {
		return fmt.Errorf("writing record: %w", err)
	}
	err = j.f.Sync()
	if err != nil {
		return fmt.Errorf("syncing record: %w", err)
	}

	// On the first write of a newly created journal, fsync the directory so the
	// new file entry itself survives a crash.
	if j.dirCreated {
		err = syncDir(j.dir)
		if err != nil {
			return fmt.Errorf("syncing store directory: %w", err)
		}
		j.dirCreated = false
	}

	j.lastSeq = seq

	return nil
}

// Records implements Journal.
func (j *fileJournal) Records() ([]Record, error) {
	return readRecords(j.path)
}

// LastSeq implements Journal.
func (j *fileJournal) LastSeq() uint64 {
	return j.lastSeq
}

// Close implements Journal.
func (j *fileJournal) Close() error {
	err := j.f.Close()
	j.lock.release()

	return err
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()

	return d.Sync()
}

// readRecords parses a JSON-lines journal, dropping an unparsable final line as a
// torn tail (only the last append can be torn on an append-only, fsynced file)
// while treating interior parse failures as corruption.
func readRecords(path string) ([]Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(data, []byte{'\n'})
	// A well-formed file ends in a newline, so the final split element is empty.
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}

	out := make([]Record, 0, len(lines))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var rec Record
		err := json.Unmarshal(line, &rec)
		if err != nil {
			if i == len(lines)-1 {
				// Torn tail from a crash mid-write: drop it and resume from the
				// last complete record.
				break
			}
			return nil, fmt.Errorf("%w: line %d: %w", ErrCorrupt, i+1, err)
		}
		out = append(out, rec)
	}

	return out, nil
}

func lastSeq(path string) (uint64, error) {
	recs, err := readRecords(path)
	if err != nil {
		return 0, err
	}
	if len(recs) == 0 {
		return 0, nil
	}

	return recs[len(recs)-1].Seq, nil
}
