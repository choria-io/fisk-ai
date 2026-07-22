//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package memory provides a small pluggable key/value store the agent uses to
// keep durable notes across runs. A key maps to a value carrying a one-line
// description; the description feeds the memory index and the memory_list tool.
// The store is deliberately minimal: a flat keyspace whose keys are legal both
// as NATS KV keys and as filenames, so a value written by one backend can
// migrate unchanged to another.
//
// A backend is a Store implementation registered under a name with Register,
// usually from the backend package's init so a program links a backend in by
// importing it. New looks the configured backend up in that registry, so adding
// a backend touches no code here. The file backend lives in the file subpackage
// and is the only implementation today.
package memory

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/choria-io/fisk-ai/config"
)

// Backend names for the memory store, selected by harness.memory.backend.
const (
	// BackendFile stores each memory as a markdown file under a directory. It is
	// the name the file subpackage registers under and the config default.
	BackendFile = "file"

	// BackendJetStream stores each memory in a NATS JetStream KV bucket. It is the
	// name the jetstream subpackage registers under, and it requires a NATS
	// connection on the RuntimeEnv.
	BackendJetStream = "jetstream"
)

const (
	// maxKeyRunes caps a key's length so key+".md" stays within a filename limit
	// on every supported filesystem. The charset is ASCII, so runes equal bytes.
	maxKeyRunes = 200

	// maxDescriptionRunes caps a stored description. Descriptions are one-line
	// summaries shown in the index and memory_list, not prose.
	maxDescriptionRunes = 500

	// maxDescriptionBytes bounds a normalized description's byte length:
	// maxDescriptionRunes runes at up to utf8.UTFMax bytes each.
	maxDescriptionBytes = maxDescriptionRunes * utf8.UTFMax
)

// MaxContentBytes caps a memory value's body, keeping a single entry within what
// the JetStream KV backend accepts and bounding file size. It is exported so a
// backend can size its backing store (a KV bucket's max value size) against it and
// a status surface can show the limit.
const MaxContentBytes = 64 * 1024

// MaxEntries caps how many memories a store may hold, bounding both the index
// injected into the system prompt and a runaway model that writes without end. It
// is exported so a status surface can show the limit.
const MaxEntries = 1024

// maxEntryOverhead upper-bounds the frontmatter Serialize prepends to the content:
// the two "---" delimiter lines and the YAML "description:" line, whose value YAML
// may double-quote and escape (at most doubling maxDescriptionBytes), plus a fixed
// margin for the delimiters and key. It is an over-estimate, not the exact size.
const maxEntryOverhead = 2*maxDescriptionBytes + 64

// MaxEntryBytes is the largest a serialized memory value can be: content at the
// MaxContentBytes cap plus the frontmatter Serialize adds. A backend whose backing
// store caps a value's size (a KV bucket's max value size) must size it against
// this, not MaxContentBytes, because the stored value carries the description in a
// YAML header ahead of the body; sizing to MaxContentBytes would reject a full-size
// entry at write time. It is exported so a backend can size its store against it.
const MaxEntryBytes = MaxContentBytes + maxEntryOverhead

// ErrExists is returned by Write when overwrite is false and the key is already
// present, so the create-guard reports a collision the model can reason about
// rather than silently clobbering an existing memory.
var ErrExists = errors.New("memory key already exists")

// ErrNotExist is returned by Read when the key is not present.
var ErrNotExist = errors.New("memory key does not exist")

// ErrStale is returned by Write with overwrite true when the backend enforces
// read-before-update and the key was not read in this run, or has changed since it
// was read. It lets the model reason about a lost-update conflict (read the current
// value and retry) rather than silently clobbering a concurrent change. Only a
// backend that can check this atomically returns it; the file backend does not.
var ErrStale = errors.New("memory changed since it was read")

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

// New builds the memory store described by cfg. It looks the configured backend up
// in the registry and hands its factory the per-run environment, the agent identity,
// and the raw per-backend options block. env carries the per-run values a backend
// may need (a directory base, a borrowed NATS connection); it is taken whole so a
// backend that later needs a new per-run value adds a field to RuntimeEnv rather
// than changing this signature and every caller. It returns an error for an unknown
// backend or malformed backend options, so an operator's mistake surfaces at run
// start rather than on the first tool call. An unknown backend most often means the
// backend package was not imported into this build; the error lists the backends
// that are linked in.
func New(cfg *config.Config, env RuntimeEnv) (Store, error) {
	backend := cfg.MemoryBackend()

	reg, ok := lookup(backend)
	if !ok {
		return nil, fmt.Errorf("unknown memory backend %q: known backends are %v", backend, Backends())
	}

	return reg.factory(env, cfg.Identity, cfg.MemoryRawOptions())
}

// NeedsNats reports whether the memory backend selected by cfg needs a NATS
// connection provisioned on RuntimeEnv.Nats. The host calls it to decide whether to
// establish a connection before building the store, without naming any backend: the
// requirement is declared at registration with RequiresNats. It returns false when
// memory is disabled or the backend is unknown, leaving New to surface the unknown
// backend.
func NeedsNats(cfg *config.Config) bool {
	if !cfg.MemoryEnabled() {
		return false
	}

	reg, ok := lookup(cfg.MemoryBackend())

	return ok && reg.needsNats
}
