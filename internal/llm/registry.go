//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package llm

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config carries the neutral settings a Factory needs to build a Provider. APIKey
// and BaseURL address the backend, Timeout bounds a single call, and Middlewares
// are the cross-cutting request hooks the caller assembled (request trace, HTTP
// debug dump). Every field is neutral, so provider resolution never forces the
// caller to name a vendor package.
type Config struct {
	APIKey      string
	BaseURL     string
	Timeout     time.Duration
	Middlewares []Middleware
}

// Factory constructs a Provider from neutral Config. It is registered under a
// provider name with Register and is meant to be called from a provider package's
// init, so a program links a provider in simply by importing its package. A
// construction failure (a backend that cannot be reached, a bad option) is
// returned as an error so an operator's mistake surfaces at run start.
type Factory func(cfg Config) (Provider, error)

// registration is a provider's registry entry: how to build it, and the names of
// the environment variables that carry its credentials.
type registration struct {
	factory            Factory
	credentialEnvNames []string
}

var (
	registryMu sync.Mutex
	registry   = map[string]registration{}
	credsOnce  sync.Once
	credsCache []string
)

// Register adds a provider under name: its factory, and credentialEnvNames, the
// environment variables the provider's SDK reads to authenticate the agent's own
// API calls. Those names are stripped from the environment of any tool subprocess
// whose command line the model chooses (see internal/toolkit/fisk), so a tool can
// never read the agent's credentials. This is why the llm registry carries more
// than the runstate/a2a/memory ones: a registered provider also contributes to a
// security boundary elsewhere in the tree, so declaring its secrets is not optional
// and is a required argument here rather than an omittable field.
//
// credentialEnvNames must list every SECRET-BEARING variable the provider's default
// credential chain reads (API keys, bearer or identity tokens, signing keys, any
// header variable that can carry an Authorization value). It must NOT list selector
// variables that only point at on-disk credentials (a profile name, a config-dir
// path): those hold no secret, are guarded by file permissions, and stripping them
// buys nothing a tool cannot rediscover. Pass an empty slice only for a provider
// that genuinely reads no credential from the environment.
//
// It panics on an empty name, a nil factory, or a duplicate registration: each is a
// programming error resolved at compile time, mirroring database/sql.Register. Do
// not call it outside init.
func Register(name string, factory Factory, credentialEnvNames []string) {
	if name == "" {
		panic("llm: Register called with an empty provider name")
	}
	if factory == nil {
		panic("llm: Register called with a nil factory for provider " + name)
	}

	registryMu.Lock()
	defer registryMu.Unlock()

	if _, dup := registry[name]; dup {
		panic("llm: Register called twice for provider " + name)
	}

	registry[name] = registration{factory: factory, credentialEnvNames: credentialEnvNames}
}

// Providers returns the names of every provider linked into this build, sorted. A
// caller can show it so an operator sees which providers are available without
// triggering an unknown-provider error.
func Providers() []string {
	registryMu.Lock()
	defer registryMu.Unlock()

	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// CredentialEnvNames returns the sorted, deduplicated union of the credential
// environment variable names declared by every provider linked into this build. A
// caller strips them from the environment of a subprocess whose command line the
// model chooses (see internal/toolkit/fisk), so no linked provider's secrets can
// leak to a tool, regardless of which provider is active. Names are trimmed and
// empties dropped, matching config.CredentialEnvNames, which supplies the
// operator-named half of the same strip. The union is computed once: the registry
// is populated only from provider init and never mutated after.
func CredentialEnvNames() []string {
	credsOnce.Do(func() {
		registryMu.Lock()
		defer registryMu.Unlock()

		lists := make([][]string, 0, len(registry))
		for _, reg := range registry {
			lists = append(lists, reg.credentialEnvNames)
		}
		credsCache = mergeEnvNames(lists)
	})

	return credsCache
}

// mergeEnvNames returns the sorted, deduplicated, whitespace-trimmed union of the
// given name lists, dropping empties. It is pure so the merge rules can be tested
// without registry state, and matches config.CredentialEnvNames's trim-and-dedup
// behavior so both halves of the credential strip compose consistently.
func mergeEnvNames(lists [][]string) []string {
	var out []string
	for _, list := range lists {
		for _, n := range list {
			n = strings.TrimSpace(n)
			if n == "" || slices.Contains(out, n) {
				continue
			}
			out = append(out, n)
		}
	}
	sort.Strings(out)

	return out
}

// NewProvider resolves the named provider from the registry and constructs it from
// cfg. It returns an error for an unknown provider (most often because its package
// was not imported into this build; the error lists the providers that are linked
// in) or a construction failure, so an operator's mistake surfaces up front rather
// than on the first call.
func NewProvider(name string, cfg Config) (Provider, error) {
	registryMu.Lock()
	reg, ok := registry[name]
	registryMu.Unlock()

	if !ok {
		return nil, fmt.Errorf("unknown llm provider %q: known providers are %v", name, Providers())
	}

	return reg.factory(cfg)
}
