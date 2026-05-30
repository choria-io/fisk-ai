//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package memory provides a small pluggable key/value store the agent uses to
// keep durable notes across runs. A key maps to a markdown value that carries a
// one-line description in YAML frontmatter; the description feeds the memory
// index and the memory_list tool. The store is deliberately minimal: a flat
// keyspace whose keys are legal both as NATS KV keys and as filenames, so a
// value written by the file backend can migrate unchanged to a future JetStream
// KV backend. The file backend is the only implementation today.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/choria-io/fisk-ai/config"
)

// Backend names for the memory store, selected by harness.memory.backend.
const (
	// BackendFile stores each memory as a markdown file under a directory.
	BackendFile = "file"
)

const (
	// defaultDirectory is the base directory the file backend uses when
	// harness.memory.options.directory is unset. The agent identity is appended
	// so two agents run from the same working directory do not share a namespace
	// unless the operator points them at the same explicit directory.
	defaultDirectory = "memory"

	// maxKeyRunes caps a key's length so key+".md" stays within a filename limit
	// on every supported filesystem. The charset is ASCII, so runes equal bytes.
	maxKeyRunes = 200

	// maxDescriptionRunes caps a stored description. Descriptions are one-line
	// summaries shown in the index and memory_list, not prose.
	maxDescriptionRunes = 500

	// maxContentBytes caps a memory value's body, keeping a single entry within
	// what a future JetStream KV backend accepts and bounding file size.
	maxContentBytes = 64 * 1024

	// maxEntries caps how many memories a store may hold, bounding both the index
	// injected into the system prompt and a runaway model that writes without end.
	maxEntries = 1024
)

// ErrExists is returned by Write when overwrite is false and the key is already
// present, so the create-guard reports a collision the model can reason about
// rather than silently clobbering an existing memory.
var ErrExists = errors.New("memory key already exists")

// ErrNotExist is returned by Read when the key is not present.
var ErrNotExist = errors.New("memory key does not exist")

// Item is a single memory as surfaced by List: the key and its one-line
// description. The body is fetched separately with Read.
type Item struct {
	Key         string
	Description string
}

// Store is the pluggable memory backend. Implementations must be safe for
// concurrent use by independent processes sharing a backing store; the file
// backend achieves this with atomic create and replace. Keys are validated by
// the implementation against ValidateKey before any backing access.
type Store interface {
	// List returns every stored memory as key and description, sorted by key. It
	// reads each value to recover its description, so its cost grows with the
	// number of entries; that is acceptable at the volumes this store targets.
	List(ctx context.Context) ([]Item, error)
	// Read returns the description and body of key, or ErrNotExist if absent.
	Read(ctx context.Context, key string) (description, content string, err error)
	// Write stores content under key with the given description. With overwrite
	// false it creates the key and returns ErrExists if it already exists; with
	// overwrite true it replaces any existing value. Both paths are atomic to a
	// concurrent reader.
	Write(ctx context.Context, key, description, content string, overwrite bool) error
	// Delete removes key. It is idempotent: deleting an absent key is not an
	// error, and existed reports whether a value was actually removed.
	Delete(ctx context.Context, key string) (existed bool, err error)
}

// New builds the memory store described by cfg. It returns an error for an
// unknown backend or malformed backend options, so an operator's mistake surfaces
// at run start rather than on the first tool call.
func New(cfg *config.Config) (Store, error) {
	switch cfg.MemoryBackend() {
	case BackendFile:
		opts, err := decodeFileOptions(cfg.MemoryRawOptions())
		if err != nil {
			return nil, err
		}

		dir := opts.Directory
		if dir == "" {
			dir = filepath.Join(defaultDirectory, cfg.Identity)
		}

		return newFileStore(dir)
	default:
		return nil, fmt.Errorf("unknown memory backend %q: the only supported backend is %q", cfg.MemoryBackend(), BackendFile)
	}
}

// fileOptions is the typed shape of harness.memory.options for the file backend.
type fileOptions struct {
	// Directory is where memory files live. It is resolved relative to the
	// working directory when not absolute, and defaults to memory/<identity>.
	Directory string `json:"directory"`
}

// decodeFileOptions strictly decodes the backend options. The options arrive as
// canonical JSON (config parses with UseJSONUnmarshaler), so a stdlib decoder
// with DisallowUnknownFields catches a mistyped option key the same way the YAML
// layer catches a mistyped top-level key.
func decodeFileOptions(raw json.RawMessage) (fileOptions, error) {
	var opts fileOptions
	if len(raw) == 0 {
		return opts, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&opts); err != nil {
		return opts, fmt.Errorf("invalid file memory options: %w", err)
	}

	return opts, nil
}
