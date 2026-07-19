//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package file

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/memory"
)

var _ = Describe("fileStore", func() {
	var (
		ctx   context.Context
		dir   string
		store memory.Store
	)

	// create writes a new memory with the create-guard (overwrite off), the common
	// case in these specs.
	create := func(key, description, content string) error {
		return store.Write(ctx, key, description, content, false)
	}

	BeforeEach(func() {
		ctx = context.Background()
		dir = GinkgoT().TempDir()
		var err error
		store, err = newFileStore(filepath.Join(dir, "mem"))
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should create the backing directory", func() {
		info, err := os.Stat(filepath.Join(dir, "mem"))
		Expect(err).ToNot(HaveOccurred())
		Expect(info.IsDir()).To(BeTrue())
	})

	It("Should create then read a memory", func() {
		Expect(create("build.notes", "how the build works", "make it so")).To(Succeed())

		desc, content, err := store.Read(ctx, "build.notes")
		Expect(err).ToNot(HaveOccurred())
		Expect(desc).To(Equal("how the build works"))
		Expect(content).To(Equal("make it so"))
	})

	It("Should refuse to create over an existing key and leave it untouched", func() {
		Expect(create("k", "first", "one")).To(Succeed())

		Expect(create("k", "second", "two")).To(MatchError(memory.ErrExists))

		_, content, _ := store.Read(ctx, "k")
		Expect(content).To(Equal("one"))
	})

	It("Should replace an existing key when overwrite is set", func() {
		Expect(create("k", "first", "one")).To(Succeed())
		Expect(store.Write(ctx, "k", "second", "two", true)).To(Succeed())

		desc, content, _ := store.Read(ctx, "k")
		Expect(desc).To(Equal("second"))
		Expect(content).To(Equal("two"))
	})

	It("Should create with overwrite set when the key is absent", func() {
		Expect(store.Write(ctx, "fresh", "d", "c", true)).To(Succeed())
		_, content, err := store.Read(ctx, "fresh")
		Expect(err).ToNot(HaveOccurred())
		Expect(content).To(Equal("c"))
	})

	It("Should report ErrNotExist reading an absent key", func() {
		_, _, err := store.Read(ctx, "nope")
		Expect(err).To(MatchError(memory.ErrNotExist))
	})

	It("Should delete idempotently", func() {
		Expect(create("k", "d", "c")).To(Succeed())

		existed, err := store.Delete(ctx, "k")
		Expect(err).ToNot(HaveOccurred())
		Expect(existed).To(BeTrue())

		existed, err = store.Delete(ctx, "k")
		Expect(err).ToNot(HaveOccurred())
		Expect(existed).To(BeFalse())
	})

	It("Should collapse a multi-line description to one line end to end", func() {
		Expect(create("k", "line one\nline two\t indented", "c")).To(Succeed())
		desc, _, _ := store.Read(ctx, "k")
		Expect(desc).To(Equal("line one line two indented"))
	})

	It("Should reject an invalid key before any file access", func() {
		Expect(create("bad/key", "d", "c")).To(HaveOccurred())
		_, _, err := store.Read(ctx, "bad/key")
		Expect(err).To(HaveOccurred())
	})

	Describe("List", func() {
		It("Should return entries sorted by key with their descriptions", func() {
			Expect(create("b", "second", "x")).To(Succeed())
			Expect(create("a", "first", "y")).To(Succeed())

			entries, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(entries).To(Equal([]memory.Item{
				{Key: "a", Description: "first"},
				{Key: "b", Description: "second"},
			}))
		})

		It("Should be empty for a fresh store", func() {
			entries, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(entries).To(BeEmpty())
		})

		It("Should ignore files whose name is not a valid key and non-md files", func() {
			fs := store.(*fileStore)
			Expect(os.WriteFile(filepath.Join(fs.dir, "notes.txt"), []byte("x"), 0o600)).To(Succeed())
			Expect(os.WriteFile(filepath.Join(fs.dir, ".hidden.md"), []byte("x"), 0o600)).To(Succeed())
			Expect(create("real", "d", "c")).To(Succeed())

			entries, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(entries).To(HaveLen(1))
			Expect(entries[0].Key).To(Equal("real"))
		})
	})

	Describe("symlink safety", func() {
		var evilTarget string

		BeforeEach(func() {
			fs := store.(*fileStore)
			evilTarget = filepath.Join(dir, "secret")
			Expect(os.WriteFile(evilTarget, []byte("top secret"), 0o600)).To(Succeed())
			Expect(os.Symlink(evilTarget, filepath.Join(fs.dir, "evil.md"))).To(Succeed())
		})

		It("Should not follow a symlink on read", func() {
			_, _, err := store.Read(ctx, "evil")
			Expect(err).To(HaveOccurred())
		})

		It("Should not list a symlinked entry", func() {
			entries, err := store.List(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(entries).To(BeEmpty())
		})
	})
})

var _ = Describe("New", func() {
	memCfg := func(options string) *config.Config {
		m := &config.MemoryConfig{Enabled: true, Backend: memory.BackendFile}
		if options != "" {
			m.Options = []byte(options)
		}
		return &config.Config{Identity: "agent", Harness: config.HarnessConfig{Memory: m}}
	}

	It("Should build a file store and default the directory under the identity", func() {
		dir := GinkgoT().TempDir()
		GinkgoT().Chdir(dir)

		store, err := memory.New(memCfg(""))
		Expect(err).ToNot(HaveOccurred())
		Expect(store).ToNot(BeNil())

		_, err = os.Stat(filepath.Join(dir, "memory", "agent"))
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should honor an explicit directory in the options block", func() {
		dir := GinkgoT().TempDir()
		target := filepath.Join(dir, "custom")

		_, err := memory.New(memCfg(`{"directory":` + quote(target) + `}`))
		Expect(err).ToNot(HaveOccurred())

		_, err = os.Stat(target)
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should reject an unknown option key", func() {
		_, err := memory.New(memCfg(`{"nonesuch":"x"}`))
		Expect(err).To(MatchError(ContainSubstring("invalid file memory options")))
	})
})

// quote renders s as a JSON string literal for building an options block inline.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
