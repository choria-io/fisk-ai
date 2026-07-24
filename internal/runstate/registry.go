//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/nats-io/nats.go"
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
//     Open and Load when it is absent, and ErrLocked when the run is already locked
//     (by another process or a concurrent run of the same id in this process);
//   - decode its options block strictly (reject unknown keys) so an operator's
//     mistyped option fails at run start, and surface a construction failure (bad
//     options, an unwritable backing store) as an error rather than deferring it to
//     the first operation.
type Factory func(env RuntimeEnv, options json.RawMessage) (Store, error)

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

// RuntimeEnv carries the per-run environment a backend may need beyond its own
// options, mirroring memory.RuntimeEnv. A backend uses what applies to it and
// ignores the rest.
type RuntimeEnv struct {
	// StoreDir is the run-store base. When set, the file backend roots journals under
	// StoreDir/runs (still absolute, still never the working directory) so a run's state
	// sits alongside its memory and knowledge; a resume must then be given the same
	// StoreDir. Empty keeps the XDG default, the CLI's behavior, and a resume needs no
	// StoreDir. A relative configured directory is rebased under it; an absolute one is
	// honored verbatim.
	StoreDir string

	// Nats is the shared core NATS connection a connection-backed backend borrows,
	// or nil when none was provisioned. It is borrowed: a backend uses it but must
	// never close it, since the caller owns its lifecycle. A backend that needs it
	// (the jetstream backend) fails construction loudly when it is nil rather than
	// dereferencing it; a backend that keeps its data locally (the file backend)
	// ignores it.
	Nats *nats.Conn
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
		panic("runstate: Register called with an empty backend name")
	}
	if factory == nil {
		panic("runstate: Register called with a nil factory for backend " + name)
	}

	reg := registration{factory: factory}
	for _, opt := range opts {
		opt(&reg)
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, dup := registry[name]; dup {
		panic("runstate: Register called twice for backend " + name)
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

// NeedsNats reports whether the named session backend needs a NATS connection
// provisioned on RuntimeEnv.Nats. The host calls it to decide whether to establish a
// connection before building the store, without naming any backend: the requirement
// is declared at registration with RequiresNats. It returns false for an unknown
// backend, leaving New to surface the unknown backend.
func NeedsNats(backend string) bool {
	reg, ok := lookup(backend)

	return ok && reg.needsNats
}

// lookup returns the registration for name.
func lookup(name string) (registration, bool) {
	registryMu.Lock()
	defer registryMu.Unlock()

	reg, ok := registry[name]

	return reg, ok
}
