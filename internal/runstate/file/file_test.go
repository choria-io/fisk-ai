//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package file

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/segmentio/ksuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/runstate"
)

func newID() string {
	return ksuid.New().String()
}

func assistantWithTools(iter int64, ids ...string) *runstate.AssistantRecord {
	content := []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("working")}
	for _, id := range ids {
		content = append(content, anthropic.NewToolUseBlock(id, map[string]any{"x": 1}, "shell"))
	}
	return &runstate.AssistantRecord{
		Iteration: iter,
		Message:   anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: content},
		InTokens:  10,
		OutTokens: 5,
	}
}

func toolResult(id string) *runstate.ToolResultRecord {
	return &runstate.ToolResultRecord{ToolUseID: id, Result: anthropic.NewToolResultBlock(id, "ok", false)}
}

var _ = Describe("FileStore", func() {
	var store *FileStore

	BeforeEach(func() {
		s, err := NewFileStore(GinkgoT().TempDir())
		Expect(err).NotTo(HaveOccurred())
		store = s
	})

	newMeta := func(id string) runstate.MetaRecord {
		return runstate.MetaRecord{Version: runstate.Version, RunID: id, Prompt: "hello", Fingerprint: runstate.Fingerprint{Model: "claude-opus-4-8"}}
	}

	It("creates, appends, and folds back a run", func() {
		id := newID()
		j, err := store.Create(id, newMeta(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(j.Append(2, runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")})).To(Succeed())
		Expect(j.Append(3, runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: toolResult("tu_1")})).To(Succeed())
		Expect(j.Close()).To(Succeed())

		rs, err := store.Load(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(rs.RunID).To(Equal(id))
		Expect(rs.Messages).To(HaveLen(3))
		Expect(rs.NextIteration).To(Equal(int64(1)))
	})

	It("refuses to create a run that already exists", func() {
		id := newID()
		j, err := store.Create(id, newMeta(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(j.Close()).To(Succeed())

		_, err = store.Create(id, newMeta(id))
		Expect(err).To(MatchError(runstate.ErrExists))
	})

	It("treats a duplicate seq as an idempotent no-op and rejects gaps", func() {
		id := newID()
		j, err := store.Create(id, newMeta(id))
		Expect(err).NotTo(HaveOccurred())
		defer j.Close()

		Expect(j.Append(2, runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")})).To(Succeed())
		// Re-append the same seq (crash-retry): no error, no duplicate line.
		Expect(j.Append(2, runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")})).To(Succeed())
		recs, err := j.Records()
		Expect(err).NotTo(HaveOccurred())
		Expect(recs).To(HaveLen(2))
		// A seq that skips ahead is a gap.
		Expect(j.Append(5, runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: toolResult("tu_1")})).To(MatchError(runstate.ErrSeqGap))
	})

	It("drops a torn final line but keeps complete records", func() {
		id := newID()
		j, err := store.Create(id, newMeta(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(j.Append(2, runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")})).To(Succeed())
		Expect(j.Close()).To(Succeed())

		// Simulate a crash mid-write: append a truncated, unterminated line.
		f, err := os.OpenFile(store.journalPath(id), os.O_WRONLY|os.O_APPEND, 0o600)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.WriteString(`{"seq":3,"protocol":"io.choria.fisk-ai.v1.session.tool_res`)
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())

		rs, err := store.Load(id)
		Expect(err).NotTo(HaveOccurred())
		Expect(rs.Counters.LlmCalls).To(Equal(int64(1)))
	})

	It("errors on interior corruption", func() {
		id := newID()
		j, err := store.Create(id, newMeta(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(j.Close()).To(Succeed())

		f, err := os.OpenFile(store.journalPath(id), os.O_WRONLY|os.O_APPEND, 0o600)
		Expect(err).NotTo(HaveOccurred())
		_, err = f.WriteString("not json at all\n{\"seq\":3,\"protocol\":\"io.choria.fisk-ai.v1.session.terminal\",\"terminal\":{\"reason\":\"completed\"}}\n")
		Expect(err).NotTo(HaveOccurred())
		Expect(f.Close()).To(Succeed())

		_, err = store.Load(id)
		Expect(err).To(MatchError(runstate.ErrCorrupt))
	})

	It("rejects unsafe run ids (path traversal)", func() {
		_, err := store.Load("../../etc/passwd")
		Expect(err).To(MatchError(runstate.ErrInvalidID))
		_, err = store.Create("../evil", newMeta("../evil"))
		Expect(err).To(MatchError(runstate.ErrInvalidID))
	})

	It("lists and deletes runs", func() {
		id := newID()
		j, err := store.Create(id, newMeta(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(j.Close()).To(Succeed())

		infos, err := store.List()
		Expect(err).NotTo(HaveOccurred())
		Expect(infos).To(HaveLen(1))
		Expect(infos[0].RunID).To(Equal(id))
		Expect(infos[0].Model).To(Equal("claude-opus-4-8"))

		Expect(store.Delete(id)).To(Succeed())
		_, err = store.Load(id)
		Expect(err).To(MatchError(runstate.ErrNotFound))
	})

	It("does not leak a sensitive prompt into the fingerprint on disk", func() {
		id := newID()
		meta := newMeta(id)
		meta.Fingerprint.SystemHash = runstate.HashHex([]byte("TOP-SECRET-INSTRUCTIONS"))
		j, err := store.Create(id, meta)
		Expect(err).NotTo(HaveOccurred())
		Expect(j.Close()).To(Succeed())

		data, err := os.ReadFile(store.journalPath(id))
		Expect(err).NotTo(HaveOccurred())
		Expect(bytes.Contains(data, []byte("TOP-SECRET-INSTRUCTIONS"))).To(BeFalse())
	})
})

var _ = Describe("newStore", func() {
	It("defaults an empty directory to the core XDG default, not the working directory", func() {
		def, err := runstate.DefaultDir()
		Expect(err).NotTo(HaveOccurred())

		s, err := newStore(nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.(*FileStore).dir).To(Equal(def))
		Expect(filepath.IsAbs(s.(*FileStore).dir)).To(BeTrue())
	})

	It("uses the directory option when set", func() {
		dir := GinkgoT().TempDir()
		s, err := newStore(json.RawMessage(`{"directory":"` + dir + `"}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.(*FileStore).dir).To(Equal(dir))
	})

	It("rejects an unknown option key", func() {
		_, err := newStore(json.RawMessage(`{"bogus":1}`))
		Expect(err).To(MatchError(ContainSubstring("invalid file session options")))
	})

	It("is registered under the file backend name", func() {
		Expect(runstate.Backends()).To(ContainElement(runstate.BackendFile))
	})
})
