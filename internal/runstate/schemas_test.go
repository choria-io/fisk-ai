//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
)

var _ = Describe("Validator", func() {
	var v *Validator

	BeforeEach(func() {
		var err error
		v, err = NewValidator()
		Expect(err).ToNot(HaveOccurred())
	})

	metaRecord := func() Record {
		return Record{
			Seq:      1,
			Protocol: MetaProtocol,
			Meta: &MetaRecord{
				Version:     Version,
				RunID:       "run-1",
				Created:     time.Now().UTC(),
				Fingerprint: Fingerprint{Model: "claude-opus-4-8", SystemHash: "abc", ToolsHash: "def", ThinkingMode: "on", MaxTokens: 1000, MaxIterations: 5},
				Prompt:      "do the thing",
			},
		}
	}

	Describe("valid records", func() {
		It("Should accept every record type produced by a run", func() {
			records := []Record{
				metaRecord(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
				{Seq: 4, Protocol: UserProtocol, User: userRecord("a follow-up")},
				{Seq: 5, Protocol: TerminalProtocol, Terminal: &TerminalRecord{Reason: ReasonCompleted, Message: "done"}},
			}

			for _, rec := range records {
				Expect(v.ValidateRecord(rec)).To(Succeed(), "protocol %s", rec.Protocol)
			}
		})

		It("Should accept a terminal record with no message", func() {
			Expect(v.ValidateRecord(Record{Seq: 2, Protocol: TerminalProtocol, Terminal: &TerminalRecord{Reason: ReasonSuspended}})).To(Succeed())
		})
	})

	Describe("invalid records", func() {
		It("Should reject an unknown protocol id", func() {
			data, err := json.Marshal(Record{Seq: 2, Protocol: "io.choria.fisk-ai.v1.session.bogus", Terminal: &TerminalRecord{Reason: ReasonCompleted}})
			Expect(err).ToNot(HaveOccurred())
			Expect(v.Validate(data)).To(MatchError(ErrNoSchema))
		})

		It("Should reject a record whose payload does not match its protocol", func() {
			data := tamperRecord(metaRecord(), func(m map[string]any) {
				delete(m, "meta")
			})
			Expect(v.Validate(data)).ToNot(Succeed())
		})

		It("Should reject a meta record missing a required field", func() {
			data := tamperRecord(metaRecord(), func(m map[string]any) {
				delete(m["meta"].(map[string]any), "run_id")
			})
			Expect(v.Validate(data)).ToNot(Succeed())
		})

		It("Should reject a stray payload key for the protocol", func() {
			data := tamperRecord(metaRecord(), func(m map[string]any) {
				m["terminal"] = map[string]any{"reason": "completed"}
			})
			Expect(v.Validate(data)).ToNot(Succeed())
		})

		It("Should reject a terminal record with an unknown reason", func() {
			data := tamperRecord(Record{Seq: 2, Protocol: TerminalProtocol, Terminal: &TerminalRecord{Reason: ReasonCompleted}}, func(m map[string]any) {
				m["terminal"].(map[string]any)["reason"] = "exploded"
			})
			Expect(v.Validate(data)).ToNot(Succeed())
		})
	})
})

// tamperRecord round-trips a record through a map so a test can mutate it before
// re-validating.
func tamperRecord(rec Record, mut func(map[string]any)) []byte {
	data, err := json.Marshal(rec)
	Expect(err).ToNot(HaveOccurred())

	var m map[string]any
	Expect(json.Unmarshal(data, &m)).To(Succeed())
	mut(m)

	out, err := json.Marshal(m)
	Expect(err).ToNot(HaveOccurred())

	return out
}

var _ = Describe("assistant record message", func() {
	It("Should validate an assistant message stored verbatim", func() {
		v, err := NewValidator()
		Expect(err).ToNot(HaveOccurred())

		asst := &AssistantRecord{
			Iteration: 0,
			Message:   llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: "hello"}}}},
			InTokens:  1,
			OutTokens: 2,
		}
		Expect(v.ValidateRecord(Record{Seq: 2, Protocol: AssistantProtocol, Assistant: asst})).To(Succeed())
	})

	It("Should validate an assistant record carrying the cache token split", func() {
		v, err := NewValidator()
		Expect(err).ToNot(HaveOccurred())

		asst := &AssistantRecord{
			Iteration:         0,
			Message:           llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: "hello"}}}},
			InTokens:          1,
			OutTokens:         2,
			CacheReadTokens:   100,
			CacheCreateTokens: 40,
		}
		Expect(v.ValidateRecord(Record{Seq: 2, Protocol: AssistantProtocol, Assistant: asst})).To(Succeed())
	})
})
