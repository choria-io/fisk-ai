//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"encoding/json"
	"sort"
	"sync"
)

// Factory constructs a Store for a backend from the raw per-backend options block
// (the session config's options, verbatim; empty when unset). It is registered
// under a backend name with Register.
//
// Unlike the memory registry, no identity is passed: sessions are not namespaced
// by identity. They live in one store (the file backend's single XDG directory)
// so a resume finds its run regardless of which identity is active. A backend that
// wants a namespace (a JetStream subject prefix or stream) takes it from its
// options block, the same place the file backend takes its directory.
//
// An implementation must:
//   - be safe for concurrent use by independent processes sharing a backing store,
//     as the Store and Journal contracts require;
//   - validate every run id with ValidateID before it is used as a key or path
//     component, and enforce the append contract with CheckAppend so the seq rules
//     cannot drift between backends;
//   - return ErrExists from Create when the id is already present, ErrNotFound from
//     Open and Load when it is absent, and ErrLocked when another process holds the
//     run;
//   - decode its options block strictly (reject unknown keys) so an operator's
//     mistyped option fails at run start, and surface a construction failure (bad
//     options, an unwritable backing store) as an error rather than deferring it to
//     the first operation.
type Factory func(options json.RawMessage) (Store, error)

var (
	registryMu sync.Mutex
	registry   = map[string]Factory{}
)

// Register adds a backend factory under name. It is meant to be called from a
// backend package's init, so a program links a backend in simply by importing its
// package. It panics on an empty name, a nil factory, or a duplicate registration:
// each is a programming error resolved at compile time, mirroring
// database/sql.Register. Do not call it outside init.
func Register(name string, factory Factory) {
	if name == "" {
		panic("runstate: Register called with an empty backend name")
	}
	if factory == nil {
		panic("runstate: Register called with a nil factory for backend " + name)
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, dup := registry[name]; dup {
		panic("runstate: Register called twice for backend " + name)
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
