//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"sort"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/memory"
)

// fakeStore is an in-memory Store for exercising the memory tool handlers without
// touching the filesystem.
type fakeStore struct {
	values       map[string]fakeValue
	forceListErr error
}

type fakeValue struct {
	description string
	content     string
}

func newFakeStore() *fakeStore { return &fakeStore{values: map[string]fakeValue{}} }

func (f *fakeStore) List(context.Context) ([]memory.Item, error) {
	if f.forceListErr != nil {
		return nil, f.forceListErr
	}
	var items []memory.Item
	for k, v := range f.values {
		items = append(items, memory.Item{Key: k, Description: v.description})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items, nil
}

func (f *fakeStore) Read(_ context.Context, key string) (string, string, error) {
	v, ok := f.values[key]
	if !ok {
		return "", "", memory.ErrNotExist
	}
	return v.description, v.content, nil
}

func (f *fakeStore) Write(_ context.Context, key, description, content string, overwrite bool) error {
	if _, ok := f.values[key]; ok && !overwrite {
		return memory.ErrExists
	}
	f.values[key] = fakeValue{description: description, content: content}
	return nil
}

func (f *fakeStore) Delete(_ context.Context, key string) (bool, error) {
	_, ok := f.values[key]
	delete(f.values, key)
	return ok, nil
}

var _ = Describe("Memory tools", func() {
	ctx := context.Background()
	enabled := &config.Config{Harness: config.HarnessConfig{Memory: &config.MemoryConfig{Enabled: true}}}

	var store *fakeStore
	var tools map[string]*BuiltinTool

	BeforeEach(func() {
		store = newFakeStore()
		tools = map[string]*BuiltinTool{}
		for _, t := range MemoryTools(enabled, store) {
			tools[t.Name()] = t
		}
	})

	call := func(name, input string) (map[string]any, error) {
		GinkgoHelper()
		out, err := tools[name].handler(ctx, json.RawMessage(input), nil)
		if err != nil {
			return nil, err
		}
		var decoded map[string]any
		Expect(json.Unmarshal([]byte(out), &decoded)).To(Succeed())
		return decoded, nil
	}

	Describe("MemoryTools", func() {
		It("Should offer the four tools only when enabled", func() {
			Expect(MemoryTools(&config.Config{}, store)).To(BeEmpty())

			var names []string
			for _, t := range MemoryTools(enabled, store) {
				names = append(names, t.Name())
			}
			Expect(names).To(ConsistOf("memory_list", "memory_read", "memory_write", "memory_delete"))
		})
	})

	Describe("memory_write", func() {
		It("Should create a new memory", func() {
			out, err := call("memory_write", `{"key":"k","description":"d","content":"c"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["written"]).To(BeTrue())
			Expect(store.values["k"]).To(Equal(fakeValue{description: "d", content: "c"}))
		})

		It("Should refuse to overwrite by default and name the existing description", func() {
			Expect(store.Write(ctx, "k", "existing summary", "old", false)).To(Succeed())

			out, err := call("memory_write", `{"key":"k","description":"d","content":"new"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["written"]).To(BeFalse())
			Expect(out["reason"]).To(ContainSubstring("existing summary"))
			Expect(out["reason"]).To(ContainSubstring("overwrite true"))
			Expect(store.values["k"].content).To(Equal("old"))
		})

		It("Should replace when overwrite is set", func() {
			Expect(store.Write(ctx, "k", "old", "old", false)).To(Succeed())

			out, err := call("memory_write", `{"key":"k","description":"d","content":"new","overwrite":true}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["written"]).To(BeTrue())
			Expect(store.values["k"].content).To(Equal("new"))
		})
	})

	Describe("memory_read", func() {
		It("Should return the description and content of an existing memory", func() {
			Expect(store.Write(ctx, "k", "d", "c", false)).To(Succeed())

			out, err := call("memory_read", `{"key":"k"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["found"]).To(BeTrue())
			Expect(out["description"]).To(Equal("d"))
			Expect(out["content"]).To(Equal("c"))
		})

		It("Should report a miss without erroring", func() {
			out, err := call("memory_read", `{"key":"gone"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["found"]).To(BeFalse())
			Expect(out["reason"]).To(ContainSubstring("no memory is stored"))
			Expect(out).ToNot(HaveKey("content"))
		})

		It("Should always include content on a hit, even when empty", func() {
			Expect(store.Write(ctx, "k", "d", "", false)).To(Succeed())

			out, err := call("memory_read", `{"key":"k"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["found"]).To(BeTrue())
			Expect(out).To(HaveKey("content"))
			Expect(out["content"]).To(Equal(""))
		})
	})

	Describe("memory_list", func() {
		It("Should list stored memories sorted by key", func() {
			Expect(store.Write(ctx, "b", "second", "x", false)).To(Succeed())
			Expect(store.Write(ctx, "a", "first", "y", false)).To(Succeed())

			out, err := call("memory_list", `{}`)
			Expect(err).ToNot(HaveOccurred())
			memories := out["memories"].([]any)
			Expect(memories).To(HaveLen(2))
			Expect(memories[0].(map[string]any)["key"]).To(Equal("a"))
			Expect(memories[1].(map[string]any)["key"]).To(Equal("b"))
		})
	})

	Describe("memory_delete", func() {
		It("Should report deleted true when a memory was removed", func() {
			Expect(store.Write(ctx, "k", "d", "c", false)).To(Succeed())

			out, err := call("memory_delete", `{"key":"k"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["deleted"]).To(BeTrue())
		})

		It("Should report deleted false for an absent key without erroring", func() {
			out, err := call("memory_delete", `{"key":"gone"}`)
			Expect(err).ToNot(HaveOccurred())
			Expect(out["deleted"]).To(BeFalse())
		})
	})

	Describe("call tracing", func() {
		It("Should mark memory tools as traced, unlike the self-rendering HITL tools", func() {
			for _, t := range MemoryTools(enabled, store) {
				Expect(t.Traced()).To(BeTrue(), "%s should be traced", t.Name())
			}
			for _, t := range HITLTools(&config.Config{Harness: config.HarnessConfig{HumanInTheLoop: &config.HumanInTheLoopConfig{Enabled: true}}}) {
				Expect(t.Traced()).To(BeFalse(), "%s should not be traced", t.Name())
			}
		})

		It("Should render a call line naming the tool and the key", func() {
			Expect(tools["memory_write"].TraceLine(json.RawMessage(`{"key":"build.notes","description":"d","content":"c"}`))).To(Equal("memory_write build.notes"))
			Expect(tools["memory_read"].TraceLine(json.RawMessage(`{"key":"build.notes"}`))).To(Equal("memory_read build.notes"))
			Expect(tools["memory_list"].TraceLine(json.RawMessage(`{}`))).To(Equal("memory_list"))
		})

		It("Should fall back to the bare name when no key is given", func() {
			Expect(tools["memory_read"].TraceLine(json.RawMessage(`{}`))).To(Equal("memory_read"))
		})

	})

	Describe("nil store guard", func() {
		It("Should error rather than panic when a handler is invoked with no store", func() {
			listWithoutStore := MemoryTools(enabled, nil)[0]
			_, err := listWithoutStore.handler(ctx, json.RawMessage(`{}`), nil)
			Expect(err).To(MatchError(ContainSubstring("not configured")))
		})
	})

	Describe("MemoryIndexBlock", func() {
		It("Should render entries inside delimiters and note when empty", func() {
			Expect(MemoryIndexBlock(nil)).To(ContainSubstring("(none stored yet)"))

			block := MemoryIndexBlock([]memory.Item{{Key: "build", Description: "how it builds"}})
			Expect(block).To(ContainSubstring("<memory-index>"))
			Expect(block).To(ContainSubstring("- build: how it builds"))
			Expect(block).To(ContainSubstring("</memory-index>"))
		})

		It("Should sanitize a description that carries control characters", func() {
			block := MemoryIndexBlock([]memory.Item{{Key: "k", Description: "line one\nline two\x1b[31m"}})
			Expect(block).ToNot(ContainSubstring("\x1b"))
			Expect(block).ToNot(ContainSubstring("\n\n"))
		})
	})
})
