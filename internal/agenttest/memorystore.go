//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agenttest

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/choria-io/fisk-ai/internal/memory"
)

// FakeMemoryStore is an in-memory memory.Store for tests: it holds each memory in a
// map guarded by a mutex, so a run can be handed a store through
// agent.Options.MemoryStore without a file backend or a NATS connection. It is one
// of the separate-package fakes that prove each injectable seam is implementable
// from outside its own package using only exported identifiers, and it is safe for
// the concurrent use runs sharing one store make of it.
type FakeMemoryStore struct {
	mu    sync.Mutex
	items map[string]fakeMemory
}

// fakeMemory is one stored entry, description plus body.
type fakeMemory struct {
	description string
	content     string
}

// FakeMemoryStore implements memory.Store; the assertion is the separate-package
// interface audit, failing to compile if the seam stops being implementable from
// outside its own package.
var _ memory.Store = (*FakeMemoryStore)(nil)

// NewFakeMemoryStore returns an empty in-memory store.
func NewFakeMemoryStore(tb testing.TB) *FakeMemoryStore {
	tb.Helper()
	return &FakeMemoryStore{items: map[string]fakeMemory{}}
}

// List implements memory.Store.
func (s *FakeMemoryStore) List(context.Context) ([]memory.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]memory.Item, 0, len(s.items))
	for k, v := range s.items {
		out = append(out, memory.Item{Key: k, Description: v.description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })

	return out, nil
}

// Read implements memory.Store.
func (s *FakeMemoryStore) Read(_ context.Context, key string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	m, ok := s.items[key]
	if !ok {
		return "", "", memory.ErrNotExist
	}

	return m.description, m.content, nil
}

// Write implements memory.Store.
func (s *FakeMemoryStore) Write(_ context.Context, key, description, content string, overwrite bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[key]; ok && !overwrite {
		return memory.ErrExists
	}
	s.items[key] = fakeMemory{description: description, content: content}

	return nil
}

// Delete implements memory.Store.
func (s *FakeMemoryStore) Delete(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[key]; !ok {
		return false, nil
	}
	delete(s.items, key)

	return true, nil
}
