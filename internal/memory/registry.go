//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/nats-io/nats.go"
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

	// Nats is the shared core NATS connection a connection-backed backend borrows,
	// or nil when none was provisioned. It is borrowed: a backend uses it but must
	// never close it, since the caller owns its lifecycle. A backend that needs it
	// (the jetstream backend) fails construction loudly when it is nil rather than
	// dereferencing it; a backend that keeps its data locally ignores it.
	Nats *nats.Conn
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

// RegisterOption declares a backend's requirements at registration, so the host can
// resolve them before construction without naming the backend. A backend that keeps
// its data locally needs none.
type RegisterOption func(*registration)

// RequiresNats declares that a backend needs a NATS connection on RuntimeEnv.Nats.
// The host provisions one before building the store (see NeedsNats), so a
// connection-backed backend is named nowhere outside its own registration.
func RequiresNats() RegisterOption {
	return func(r *registration) { r.needsNats = true }
}

// registration is a backend's factory plus the requirements it declared.
type registration struct {
	factory   Factory
	needsNats bool
}

var (
	registryMu sync.Mutex
	registry   = map[string]registration{}
)

// Register adds a backend factory under name, with any requirements it declares
// (RequiresNats). It is meant to be called from a backend package's init, so a
// program links a backend in simply by importing its package. It panics on an empty
// name, a nil factory, or a duplicate registration: each is a programming error
// resolved at compile time, mirroring database/sql.Register. Do not call it outside
// init.
func Register(name string, factory Factory, opts ...RegisterOption) {
	if name == "" {
		panic("memory: Register called with an empty backend name")
	}
	if factory == nil {
		panic("memory: Register called with a nil factory for backend " + name)
	}

	reg := registration{factory: factory}
	for _, opt := range opts {
		opt(&reg)
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, dup := registry[name]; dup {
		panic("memory: Register called twice for backend " + name)
	}

	registry[name] = reg
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

// lookup returns the registration for name.
func lookup(name string) (registration, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()

	reg, ok := registry[name]

	return reg, ok
}
