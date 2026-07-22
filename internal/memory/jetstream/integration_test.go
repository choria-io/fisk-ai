//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package jetstream

import (
	"context"
	"strings"
	"time"

	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/memory"
)

// runJetStream starts an embedded JetStream-enabled NATS server on a random port
// and returns a client connection. Both are torn down when the spec ends. The
// Describe is labeled Integration so the unit suite (ginkgo --skip Integration)
// does not run it.
func runJetStream() *nats.Conn {
	GinkgoHelper()

	ns, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: GinkgoT().TempDir()})
	Expect(err).NotTo(HaveOccurred())

	go ns.Start()
	Expect(ns.ReadyForConnections(10 * time.Second)).To(BeTrue())
	DeferCleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(nc.Close)

	return nc
}

var _ = Describe("Integration: jetstream memory", func() {
	var (
		ctx context.Context
		nc  *nats.Conn
		js  jetstream.JetStream
	)

	newStoreFor := func(identity, options string) (memory.Store, error) {
		return newStore(memory.RuntimeEnv{Nats: nc}, identity, []byte(options))
	}

	createBucket := func(cfg jetstream.KeyValueConfig) {
		GinkgoHelper()
		_, err := js.CreateKeyValue(ctx, cfg)
		Expect(err).ToNot(HaveOccurred())
	}

	BeforeEach(func() {
		ctx = context.Background()
		nc = runJetStream()
		var err error
		js, err = jetstream.New(nc)
		Expect(err).ToNot(HaveOccurred())
	})

	Describe("binding", func() {
		It("Should fail when the bucket does not exist", func() {
			_, err := newStoreFor("agent", `{"bucket":"missing"}`)
			Expect(err).To(MatchError(ContainSubstring("does not exist")))
		})

		It("Should reject a bucket with a TTL", func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "ttl", TTL: time.Hour})
			_, err := newStoreFor("agent", `{"bucket":"ttl"}`)
			Expect(err).To(MatchError(ContainSubstring("TTL")))
		})

		It("Should reject a bucket whose max value size is below the entry limit", func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "small", MaxValueSize: 1024})
			_, err := newStoreFor("agent", `{"bucket":"small"}`)
			Expect(err).To(MatchError(ContainSubstring("max value size")))
		})

		It("Should reject a bucket sized to the content cap but not a full serialized entry", func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "exact", MaxValueSize: memory.MaxContentBytes})
			_, err := newStoreFor("agent", `{"bucket":"exact"}`)
			Expect(err).To(MatchError(ContainSubstring("max value size")))
		})

		It("Should accept a bucket sized to a full serialized entry", func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "full", MaxValueSize: memory.MaxEntryBytes})
			_, err := newStoreFor("agent", `{"bucket":"full"}`)
			Expect(err).ToNot(HaveOccurred())
		})

		It("Should write a max-size memory against a bucket sized to a full entry", func() {
			// The bug: a bucket sized to MaxContentBytes passes the guard but rejects a
			// full-size write, because the stored value is the body plus its frontmatter
			// header. A bucket sized to MaxEntryBytes (what the guard now requires) must
			// hold a body at the content cap.
			createBucket(jetstream.KeyValueConfig{Bucket: "full", History: 1, MaxValueSize: memory.MaxEntryBytes})
			s, err := newStoreFor("agent", `{"bucket":"full"}`)
			Expect(err).ToNot(HaveOccurred())

			body := strings.Repeat("x", memory.MaxContentBytes)
			Expect(s.Write(ctx, "big", "d", body, false)).To(Succeed())

			_, content, err := s.Read(ctx, "big")
			Expect(err).ToNot(HaveOccurred())
			Expect(content).To(Equal(body))
		})
	})

	Describe("CRUD", func() {
		var store memory.Store

		create := func(key, description, content string) error {
			return store.Write(ctx, key, description, content, false)
		}

		BeforeEach(func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "mem", History: 1})
			var err error
			store, err = newStoreFor("agent", `{"bucket":"mem"}`)
			Expect(err).ToNot(HaveOccurred())
		})

		It("Should create and read a memory", func() {
			Expect(create("k", "desc", "body")).To(Succeed())

			desc, content, err := store.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(desc).To(Equal("desc"))
			Expect(content).To(Equal("body"))
		})

		It("Should return ErrExists creating a duplicate", func() {
			Expect(create("k", "d", "b")).To(Succeed())
			Expect(create("k", "d", "b")).To(MatchError(memory.ErrExists))
		})

		It("Should overwrite with overwrite true", func() {
			Expect(create("k", "d", "b")).To(Succeed())
			Expect(store.Write(ctx, "k", "d2", "b2", true)).To(Succeed())

			desc, content, err := store.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(desc).To(Equal("d2"))
			Expect(content).To(Equal("b2"))
		})

		It("Should return ErrNotExist reading an absent key", func() {
			_, _, err := store.Read(ctx, "nope")
			Expect(err).To(MatchError(memory.ErrNotExist))
		})

		It("Should delete idempotently and report whether the key existed", func() {
			Expect(create("k", "d", "b")).To(Succeed())

			existed, err := store.Delete(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(existed).To(BeTrue())

			existed, err = store.Delete(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(existed).To(BeFalse())
		})

		It("Should allow re-creating a deleted key", func() {
			Expect(create("k", "d", "b")).To(Succeed())
			_, err := store.Delete(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(create("k", "d2", "b2")).To(Succeed())
		})

		It("Should list entries sorted by key with their descriptions", func() {
			Expect(create("b", "second", "x")).To(Succeed())
			Expect(create("a", "first", "y")).To(Succeed())

			items, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(items).To(HaveLen(2))
			Expect(items[0].Key).To(Equal("a"))
			Expect(items[0].Description).To(Equal("first"))
			Expect(items[1].Key).To(Equal("b"))
		})

		It("Should return an empty list for an empty store", func() {
			items, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(items).To(BeEmpty())
		})

		It("Should exclude a deleted key from List", func() {
			Expect(create("a", "first", "x")).To(Succeed())
			Expect(create("b", "second", "y")).To(Succeed())
			Expect(create("c", "third", "z")).To(Succeed())

			_, err := store.Delete(ctx, "b")
			Expect(err).ToNot(HaveOccurred())

			items, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			keys := []string{}
			for _, it := range items {
				keys = append(keys, it.Key)
			}
			Expect(keys).To(Equal([]string{"a", "c"}))
		})
	})

	Describe("prefixing", func() {
		BeforeEach(func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "shared", History: 1})
		})

		It("Should isolate two agents by their default identity prefix", func() {
			a, err := newStoreFor("agent-a", `{"bucket":"shared"}`)
			Expect(err).ToNot(HaveOccurred())
			b, err := newStoreFor("agent-b", `{"bucket":"shared"}`)
			Expect(err).ToNot(HaveOccurred())

			Expect(a.Write(ctx, "k", "from a", "x", false)).To(Succeed())

			_, _, err = b.Read(ctx, "k")
			Expect(err).To(MatchError(memory.ErrNotExist))

			items, err := b.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(items).To(BeEmpty())

			items, err = a.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(items).To(HaveLen(1))
			Expect(items[0].Key).To(Equal("k"))
		})

		It("Should share a keyspace when agents set the same explicit prefix", func() {
			a, err := newStoreFor("agent-a", `{"bucket":"shared","prefix":"team"}`)
			Expect(err).ToNot(HaveOccurred())
			b, err := newStoreFor("agent-b", `{"bucket":"shared","prefix":"team"}`)
			Expect(err).ToNot(HaveOccurred())

			Expect(a.Write(ctx, "k", "shared", "x", false)).To(Succeed())

			desc, _, err := b.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(desc).To(Equal("shared"))
		})

		It("Should store keys flat under an explicit empty prefix", func() {
			s, err := newStoreFor("agent-a", `{"bucket":"shared","prefix":""}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(s.Write(ctx, "flatkey", "d", "b", false)).To(Succeed())

			// The raw KV key is the memory key verbatim, with no namespace prefix.
			kv, err := js.KeyValue(ctx, "shared")
			Expect(err).ToNot(HaveOccurred())
			_, err = kv.Get(ctx, "flatkey")
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("read-before-update", func() {
		var store memory.Store

		// A fresh store shares the same bucket and default prefix but starts with no
		// read receipts, standing in for a later run that has not read the key.
		freshStore := func() memory.Store {
			GinkgoHelper()
			s, err := newStoreFor("agent", `{"bucket":"mem"}`)
			Expect(err).ToNot(HaveOccurred())
			return s
		}

		BeforeEach(func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "mem", History: 1})
			store = freshStore()
			Expect(store.Write(ctx, "k", "d", "b", false)).To(Succeed())
		})

		It("Should refuse an overwrite of a key not read in this run", func() {
			err := freshStore().Write(ctx, "k", "d2", "b2", true)
			Expect(err).To(MatchError(memory.ErrStale))
		})

		It("Should allow an overwrite after reading the current value", func() {
			s := freshStore()
			_, _, err := s.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(s.Write(ctx, "k", "d2", "b2", true)).To(Succeed())
		})

		It("Should allow overwriting a key it just created without a read", func() {
			// store created "k" in BeforeEach, so it already holds authority for it.
			Expect(store.Write(ctx, "k", "d2", "b2", true)).To(Succeed())
		})

		It("Should not grant overwrite authority from List", func() {
			s := freshStore()
			items, err := s.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(items).ToNot(BeEmpty())

			err = s.Write(ctx, "k", "d2", "b2", true)
			Expect(err).To(MatchError(memory.ErrStale))
		})

		It("Should refuse an overwrite when the key changed since it was read", func() {
			s := freshStore()
			_, _, err := s.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())

			// Another writer bumps the revision out of band, under the default prefix.
			kv, err := js.KeyValue(ctx, "mem")
			Expect(err).ToNot(HaveOccurred())
			_, err = kv.Put(ctx, "agent.k", []byte("changed"))
			Expect(err).ToNot(HaveOccurred())

			err = s.Write(ctx, "k", "d2", "b2", true)
			Expect(err).To(MatchError(memory.ErrStale))
		})

		It("Should allow repeated overwrites in a run after a single read", func() {
			s := freshStore()
			_, _, err := s.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())

			Expect(s.Write(ctx, "k", "d2", "b2", true)).To(Succeed())
			// No re-read: the successful write carried the new revision forward.
			Expect(s.Write(ctx, "k", "d3", "b3", true)).To(Succeed())
		})

		It("Should require a read again after deleting and re-creating", func() {
			s := freshStore()
			_, _, err := s.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())

			existed, err := s.Delete(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(existed).To(BeTrue())

			// Re-create grants fresh authority, so the following overwrite succeeds.
			Expect(s.Write(ctx, "k", "d2", "b2", false)).To(Succeed())
			Expect(s.Write(ctx, "k", "d3", "b3", true)).To(Succeed())
		})
	})

	Describe("no_require_read_before_update", func() {
		It("Should allow a blind overwrite when the guard is off", func() {
			createBucket(jetstream.KeyValueConfig{Bucket: "mem", History: 1})

			seed, err := newStoreFor("agent", `{"bucket":"mem"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(seed.Write(ctx, "k", "d", "b", false)).To(Succeed())

			blind, err := newStoreFor("agent", `{"bucket":"mem","no_require_read_before_update":true}`)
			Expect(err).ToNot(HaveOccurred())
			// No read of "k", but the guard is off, so the overwrite still lands.
			Expect(blind.Write(ctx, "k", "d2", "b2", true)).To(Succeed())

			desc, _, err := blind.Read(ctx, "k")
			Expect(err).ToNot(HaveOccurred())
			Expect(desc).To(Equal("d2"))
		})
	})
})
