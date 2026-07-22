//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package file is the file-backed memory backend: one markdown file per key
// under a directory, each carrying its one-line description in YAML frontmatter.
// Importing this package registers the backend under memory.BackendFile, so the
// program links it in by importing it (usually for its side effect). It holds no
// exported API beyond that registration.
package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/choria-io/fisk-ai/internal/memory"
)

func init() {
	memory.Register(memory.BackendFile, newStore)
}

const (
	// defaultDirectory is the base directory used when the directory option is
	// unset. The agent identity is appended so two agents run from the same
	// working directory do not share a namespace unless the operator points them
	// at the same explicit directory.
	defaultDirectory = "memory"

	// fileExtension is appended to a key to form its filename, so the on-disk name
	// is the key verbatim and an operator can read the directory directly.
	fileExtension = ".md"

	// tempPattern names the temporary file a write stages before atomically linking
	// or renaming it into place. The leading dot keeps it out of List, which only
	// considers names ending in fileExtension whose stem is a valid key.
	tempPattern = ".memtmp-*"
)

// options is the typed shape of harness.memory.options for the file backend.
type options struct {
	// Directory is where memory files live. It is resolved relative to the
	// working directory when not absolute, and defaults to memory/<identity>.
	Directory string `json:"directory"`
}

// newStore is the memory.Factory for the file backend: it decodes the options
// block, resolves the directory, and opens the store. A construction failure
// (bad options, an unwritable directory) surfaces here at run start.
//
// The directory is the configured one, else the default memory/<identity>. A
// relative result is rebased under env.StoreDir when the caller set one, so runs
// sharing a process place their stores deterministically; an absolute configured
// directory is honored verbatim and ignores StoreDir, and an empty StoreDir keeps
// today's process-working-directory behavior.
func newStore(env memory.RuntimeEnv, identity string, raw json.RawMessage) (memory.Store, error) {
	opts, err := memory.DecodeOptions[options](raw, "file memory")
	if err != nil {
		return nil, err
	}

	dir := opts.Directory
	if dir == "" {
		dir = filepath.Join(defaultDirectory, identity)
	}
	if env.StoreDir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(env.StoreDir, dir)
	}

	return newFileStore(dir)
}

// fileStore is the file-backed Store: one markdown file per key under dir.
type fileStore struct {
	dir string
}

// newFileStore creates the backing directory if needed and returns a store over
// it. The directory is private since a memory may hold operator notes.
func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating memory directory %q: %w", dir, err)
	}

	return &fileStore{dir: dir}, nil
}

// path returns the filename for key. The key is validated by the caller, so it
// carries no separator and cannot escape dir.
func (s *fileStore) path(key string) string {
	return filepath.Join(s.dir, key+fileExtension)
}

func (s *fileStore) Read(_ context.Context, key string) (string, string, error) {
	if err := memory.ValidateKey(key); err != nil {
		return "", "", err
	}

	data, err := s.readFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", memory.ErrNotExist
	}
	if err != nil {
		return "", "", err
	}

	description, content := memory.Parse(data)

	return description, content, nil
}

func (s *fileStore) Write(_ context.Context, key, description, content string, overwrite bool) error {
	description, err := memory.ValidateWrite(key, description, content)
	if err != nil {
		return err
	}

	data, err := memory.Serialize(description, content)
	if err != nil {
		return err
	}

	if !overwrite {
		count, err := s.count()
		if err != nil {
			return err
		}
		if err := memory.CheckCapacity(count); err != nil {
			return err
		}
	}

	return s.writeAtomic(s.path(key), data, overwrite)
}

func (s *fileStore) Delete(_ context.Context, key string) (bool, error) {
	if err := memory.ValidateKey(key); err != nil {
		return false, err
	}

	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("deleting memory %q: %w", key, err)
	}

	return true, nil
}

func (s *fileStore) List(_ context.Context) ([]memory.Item, error) {
	names, err := s.keyFiles()
	if err != nil {
		return nil, err
	}

	entries := make([]memory.Item, 0, len(names))
	for _, key := range names {
		data, err := s.readFile(s.path(key))
		if err != nil {
			// A file that races with a concurrent delete, or is unreadable, is
			// skipped rather than failing the whole listing.
			continue
		}
		description, _ := memory.Parse(data)
		entries = append(entries, memory.Item{Key: key, Description: description})
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })

	return entries, nil
}

// keyFiles returns the keys of every valid memory file in the directory, filtering
// out temp files, subdirectories, and any name whose stem is not a valid key.
func (s *fileStore) keyFiles() ([]string, error) {
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reading memory directory %q: %w", s.dir, err)
	}

	var keys []string
	for _, de := range dirEntries {
		if !de.Type().IsRegular() {
			continue
		}
		name := de.Name()
		if !strings.HasSuffix(name, fileExtension) {
			continue
		}
		key := strings.TrimSuffix(name, fileExtension)
		if memory.ValidateKey(key) != nil {
			continue
		}
		keys = append(keys, key)
	}

	return keys, nil
}

// count reports how many valid memory files the directory holds, for the
// create-time entry cap.
func (s *fileStore) count() (int, error) {
	keys, err := s.keyFiles()
	if err != nil {
		return 0, err
	}

	return len(keys), nil
}

// readFile reads a memory file without following a symlink and rejecting any
// non-regular file, so a symlink or device planted in the directory cannot make
// the store return the contents of an unrelated file to the model.
func (s *fileStore) readFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|openNoFollow, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("memory file %q is not a regular file", path)
	}

	return os.ReadFile(f.Name())
}

// writeAtomic stages data in a temp file in the same directory and moves it into
// place so a concurrent reader never observes a half-written file. Create
// (overwrite false) uses a hard link, which fails if the name already exists,
// giving the create-guard atomically; replace (overwrite true) uses rename,
// which atomically supersedes any existing value.
func (s *fileStore) writeAtomic(path string, data []byte, overwrite bool) error {
	tmp, err := os.CreateTemp(s.dir, tempPattern)
	if err != nil {
		return fmt.Errorf("staging memory write: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("writing memory value: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing memory value: %w", err)
	}

	if overwrite {
		if err := os.Rename(tmpName, path); err != nil {
			return fmt.Errorf("replacing memory value: %w", err)
		}
		return nil
	}

	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return memory.ErrExists
		}
		return fmt.Errorf("creating memory value: %w", err)
	}

	return nil
}
