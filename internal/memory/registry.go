//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"encoding/json"
	"sort"
	"sync"
)

// RuntimeEnv carries the per-run environment a backend may need beyond its own
// options. A backend uses what applies to it and ignores the rest: a directory
// backend rebases a relative store path under StoreDir, a backend that keeps its
// data elsewhere (a database, a key-value bucket) ignores it. It is a struct rather
// than a bare argument so a later per-run value is a new field, not another change
// across every backend.
type RuntimeEnv struct {
	// StoreDir is the base directory a directory backend resolves a relative or
	// default store path under, so runs sharing one process can place their stores
	// deterministically. Empty resolves as before (relative to the process working
	// directory); an absolute configured directory ignores it.
	StoreDir string
}

// Factory constructs a Store for a backend from the per-run environment, the agent
// identity, and the raw per-backend options block (harness.memory.options, verbatim;
// empty when unset). It is registered under a backend name with Register.
//
// An implementation must:
//   - be safe for concurrent use by independent processes sharing a backing
//     store, as the Store contract requires;
//   - validate and normalize every write with ValidateWrite before storing, and
//     enforce the shared entry cap with CheckCapacity on the create path;
//   - return ErrExists from Write when overwrite is false and the key exists, and
//     ErrNotExist from Read when the key is absent;
//   - make Delete idempotent, and return List sorted by key;
//   - decode its options block strictly (reject unknown keys) so an operator's
//     mistyped option fails at run start, and surface a construction failure
//     (bad options, an unwritable backing store) as an error rather than deferring
//     it to the first tool call.
type Factory func(env RuntimeEnv, identity string, options json.RawMessage) (Store, error)

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register adds a backend factory under name. It is meant to be called from a
// backend package's init, so a program links a backend in simply by importing
// its package. It panics on an empty name, a nil factory, or a duplicate
// registration: each is a programming error resolved at compile time, mirroring
// database/sql.Register. Do not call it outside init.
func Register(name string, factory Factory) {
	if name == "" {
		panic("memory: Register called with an empty backend name")
	}
	if factory == nil {
		panic("memory: Register called with a nil factory for backend " + name)
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, dup := registry[name]; dup {
		panic("memory: Register called twice for backend " + name)
	}

	registry[name] = factory
}

// Backends returns the names of every backend linked into this build, sorted. A
// caller can show it so an operator sees which backends are available without
// triggering an unknown-backend error.
func Backends() []string {
	registryMu.Lock()
	defer registryMu.Unlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// lookup returns the factory registered under name.
func lookup(name string) (Factory, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()

	factory, ok := registry[name]

	return factory, ok
}
