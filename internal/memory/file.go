//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// fileExtension is appended to a key to form its filename, so the on-disk name
// is the key verbatim and an operator can read the directory directly.
const fileExtension = ".md"

// tempPattern names the temporary file a write stages before atomically linking
// or renaming it into place. The leading dot keeps it out of List, which only
// considers names ending in fileExtension whose stem is a valid key.
const tempPattern = ".memtmp-*"

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
	if err := ValidateKey(key); err != nil {
		return "", "", err
	}

	data, err := s.readFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", ErrNotExist
	}
	if err != nil {
		return "", "", err
	}

	description, content := parse(data)

	return description, content, nil
}

func (s *fileStore) Write(_ context.Context, key, description, content string, overwrite bool) error {
	if err := ValidateKey(key); err != nil {
		return err
	}

	description = normalizeDescription(description)
	if description == "" {
		return fmt.Errorf("a memory description must not be empty")
	}
	if len(content) > maxContentBytes {
		return fmt.Errorf("memory content is too large: %d bytes, limit is %d", len(content), maxContentBytes)
	}

	data, err := serialize(description, content)
	if err != nil {
		return err
	}

	if !overwrite {
		count, err := s.count()
		if err != nil {
			return err
		}
		if count >= maxEntries {
			return fmt.Errorf("memory is full: %d entries, limit is %d; delete an entry before creating another", count, maxEntries)
		}
	}

	return s.writeAtomic(s.path(key), data, overwrite)
}

func (s *fileStore) Delete(_ context.Context, key string) (bool, error) {
	if err := ValidateKey(key); err != nil {
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

func (s *fileStore) List(_ context.Context) ([]Item, error) {
	names, err := s.keyFiles()
	if err != nil {
		return nil, err
	}

	entries := make([]Item, 0, len(names))
	for _, key := range names {
		data, err := s.readFile(s.path(key))
		if err != nil {
			// A file that races with a concurrent delete, or is unreadable, is
			// skipped rather than failing the whole listing.
			continue
		}
		description, _ := parse(data)
		entries = append(entries, Item{Key: key, Description: description})
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
		if ValidateKey(key) != nil {
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
			return ErrExists
		}
		return fmt.Errorf("creating memory value: %w", err)
	}

	return nil
}

// normalizeDescription flattens a description to a single trimmed line and caps
// its length: control characters (including newlines and tabs) become spaces,
// runs of whitespace collapse to one, so the value is a clean single-key
// frontmatter header and a clean single line in the index.
func normalizeDescription(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")

	if utf8.RuneCountInString(s) > maxDescriptionRunes {
		s = string([]rune(s)[:maxDescriptionRunes])
	}

	return s
}
