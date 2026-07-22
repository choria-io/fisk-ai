//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
)

// stubStore is a no-op Store used to prove the registry dispatches to a backend
// factory; the file backend has its own package and tests.
type stubStore struct{}

func (stubStore) List(context.Context) ([]Item, error)                 { return nil, nil }
func (stubStore) Read(context.Context, string) (string, string, error) { return "", "", ErrNotExist }
func (stubStore) Write(context.Context, string, string, string, bool) error {
	return nil
}
func (stubStore) Delete(context.Context, string) (bool, error) { return false, nil }

// The fake backend is registered once for the whole test binary so New has
// something to dispatch to without linking a real backend in. Register panics on
// a duplicate, so registration must happen exactly once, which an init gives.
var lastFake struct {
	identity string
	options  string
}

func init() {
	Register("faketest", func(_ RuntimeEnv, identity string, options json.RawMessage) (Store, error) {
		lastFake.identity = identity
		lastFake.options = string(options)
		return stubStore{}, nil
	})
}

var _ = Describe("Register", func() {
	It("Should list every registered backend, sorted", func() {
		Expect(Backends()).To(ContainElement("faketest"))
	})

	It("Should panic on an empty name", func() {
		Expect(func() {
			Register("", func(RuntimeEnv, string, json.RawMessage) (Store, error) { return stubStore{}, nil })
		}).To(Panic())
	})

	It("Should panic on a nil factory", func() {
		Expect(func() { Register("nilfactory", nil) }).To(Panic())
	})

	It("Should panic on a duplicate registration", func() {
		f := func(RuntimeEnv, string, json.RawMessage) (Store, error) { return stubStore{}, nil }
		Register("duplicate.throwaway", f)
		Expect(func() { Register("duplicate.throwaway", f) }).To(Panic())
	})
})

var _ = Describe("New", func() {
	memCfg := func(backend, options string) *config.Config {
		m := &config.MemoryConfig{Enabled: true, Backend: backend}
		if options != "" {
			m.Options = []byte(options)
		}
		return &config.Config{Identity: "agent", Harness: config.HarnessConfig{Memory: m}}
	}

	It("Should dispatch to the registered backend with the identity and options", func() {
		store, err := New(memCfg("faketest", `{"any":"thing"}`), "")
		Expect(err).ToNot(HaveOccurred())
		Expect(store).To(BeAssignableToTypeOf(stubStore{}))
		Expect(lastFake.identity).To(Equal("agent"))
		Expect(lastFake.options).To(Equal(`{"any":"thing"}`))
	})

	It("Should reject an unknown backend and list the known ones", func() {
		_, err := New(memCfg("redis", ""), "")
		Expect(err).To(MatchError(ContainSubstring("unknown memory backend")))
		Expect(err).To(MatchError(ContainSubstring("faketest")))
	})
})
