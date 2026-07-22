//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubStore is a no-op Store used to prove the registry dispatches to a backend
// factory; the file backend has its own package and tests.
type stubStore struct{}

func (stubStore) Create(string, MetaRecord) (Journal, error) { return nil, nil }
func (stubStore) Open(string) (Journal, error)               { return nil, nil }
func (stubStore) Load(string) (*RunState, error)             { return nil, nil }
func (stubStore) List() ([]RunInfo, error)                   { return nil, nil }
func (stubStore) Delete(string) error                        { return nil }

// The fake backend is registered once for the whole test binary so New has
// something to dispatch to without linking a real backend in. Register panics on a
// duplicate, so registration must happen exactly once, which an init gives.
var lastFakeOptions string

func init() {
	Register("faketest", func(_ RuntimeEnv, options json.RawMessage) (Store, error) {
		lastFakeOptions = string(options)
		return stubStore{}, nil
	})
}

var _ = Describe("Register", func() {
	It("Should list every registered backend, sorted", func() {
		Expect(Backends()).To(ContainElement("faketest"))
	})

	It("Should panic on an empty name", func() {
		Expect(func() { Register("", func(RuntimeEnv, json.RawMessage) (Store, error) { return stubStore{}, nil }) }).To(Panic())
	})

	It("Should panic on a nil factory", func() {
		Expect(func() { Register("nilfactory", nil) }).To(Panic())
	})

	It("Should panic on a duplicate registration", func() {
		f := func(RuntimeEnv, json.RawMessage) (Store, error) { return stubStore{}, nil }
		Register("duplicate.throwaway", f)
		Expect(func() { Register("duplicate.throwaway", f) }).To(Panic())
	})
})

var _ = Describe("New", func() {
	It("Should dispatch to the registered backend with the options", func() {
		store, err := New("faketest", json.RawMessage(`{"any":"thing"}`), "")
		Expect(err).ToNot(HaveOccurred())
		Expect(store).To(BeAssignableToTypeOf(stubStore{}))
		Expect(lastFakeOptions).To(Equal(`{"any":"thing"}`))
	})

	It("Should reject an unknown backend and list the known ones", func() {
		_, err := New("redis", nil, "")
		Expect(err).To(MatchError(ContainSubstring("unknown session backend")))
		Expect(err).To(MatchError(ContainSubstring("faketest")))
	})
})
