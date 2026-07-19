//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/segmentio/ksuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRunState(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RunState")
}

func newID() string {
	return ksuid.New().String()
}

func assistantWithTools(iter int64, ids ...string) *AssistantRecord {
	content := []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock("working")}
	for _, id := range ids {
		content = append(content, anthropic.NewToolUseBlock(id, map[string]any{"x": 1}, "shell"))
	}
	return &AssistantRecord{
		Iteration: iter,
		Message:   anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: content},
		InTokens:  10,
		OutTokens: 5,
	}
}

func toolResult(id string) *ToolResultRecord {
	return &ToolResultRecord{ToolUseID: id, Result: anthropic.NewToolResultBlock(id, "ok", false)}
}

func userRecord(text string) *UserRecord {
	return &UserRecord{Message: anthropic.NewUserMessage(anthropic.NewTextBlock(text))}
}

func assistantText(iter int64, stop, text string) *AssistantRecord {
	return &AssistantRecord{
		Iteration:  iter,
		StopReason: stop,
		Message:    anthropic.NewAssistantMessage(anthropic.NewTextBlock(text)),
		InTokens:   3,
		OutTokens:  4,
	}
}

// userTexts returns the concatenated text blocks of every user message in the folded
// conversation, so a test can assert the reconstructed follow-ups without depending on
// block ordering within a message.
func userTexts(rs *RunState) []string {
	var out []string
	for _, msg := range rs.Messages {
		if msg.Role != anthropic.MessageParamRoleUser {
			continue
		}
		var text string
		for _, block := range msg.Content {
			if block.OfText != nil {
				text += block.OfText.Text
			}
		}
		out = append(out, text)
	}
	return out
}

var _ = Describe("runstate", func() {
	Describe("MessageParam JSON round-trip (golden, S1 tripwire)", func() {
		It("preserves every block variant byte-identically", func() {
			assistant := anthropic.MessageParam{
				Role: anthropic.MessageParamRoleAssistant,
				Content: []anthropic.ContentBlockParamUnion{
					anthropic.NewThinkingBlock("sig-opaque-123", "reasoning"),
					anthropic.NewRedactedThinkingBlock("redacted-xyz"),
					anthropic.NewTextBlock("narration"),
					anthropic.NewServerToolUseBlock("srv_1", map[string]any{"query": "choria"}, anthropic.ServerToolUseBlockParamNameWebSearch),
					{OfWebSearchToolResult: &anthropic.WebSearchToolResultBlockParam{
						ToolUseID: "srv_1",
						Content: anthropic.WebSearchToolResultBlockParamContentUnion{
							OfWebSearchToolResultBlockItem: []anthropic.WebSearchResultBlockParam{{
								EncryptedContent: "enc-1", Title: "Choria", URL: "https://choria.io",
							}},
						},
					}},
					anthropic.NewToolUseBlock("tu_1", map[string]any{"path": "/tmp"}, "shell"),
				},
			}
			messages := []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("do it")),
				assistant,
				anthropic.NewUserMessage(anthropic.NewToolResultBlock("tu_1", "done", false)),
			}

			a, err := json.Marshal(messages)
			Expect(err).NotTo(HaveOccurred())

			var round []anthropic.MessageParam
			Expect(json.Unmarshal(a, &round)).To(Succeed())

			b, err := json.Marshal(round)
			Expect(err).NotTo(HaveOccurred())
			Expect(b).To(Equal(a), "round-trip must be byte-identical")

			for _, want := range []string{"sig-opaque-123", "redacted-xyz", "srv_1", "enc-1", "server_tool_use", "web_search_tool_result"} {
				Expect(bytes.Contains(b, []byte(want))).To(BeTrue(), "field %q must survive", want)
			}
		})
	})

	Describe("tool_search_tool_result repair on decode", func() {
		// A successful tool_search block, as resp.ToParam() appends it to the assistant
		// turn: a nested union whose tool_references the SDK v1.56.0 decoder drops.
		toolSearchBlock := func(useID string, tools ...string) anthropic.ContentBlockParamUnion {
			refs := make([]anthropic.ToolReferenceBlockParam, 0, len(tools))
			for _, name := range tools {
				refs = append(refs, anthropic.ToolReferenceBlockParam{ToolName: name})
			}
			return anthropic.ContentBlockParamUnion{OfToolSearchToolResult: &anthropic.ToolSearchToolResultBlockParam{
				ToolUseID: useID,
				Content: anthropic.ToolSearchToolResultBlockParamContentUnion{
					OfRequestToolSearchToolSearchResultBlock: &anthropic.ToolSearchToolSearchResultBlockParam{ToolReferences: refs},
				},
			}}
		}

		It("restores the tool_references a bare SDK decode drops", func() {
			// The block sits between other block kinds so the position match is exercised.
			rec := &AssistantRecord{
				Iteration: 3,
				Message: anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: []anthropic.ContentBlockParamUnion{
					anthropic.NewTextBlock("searching for tools"),
					anthropic.NewServerToolUseBlock("srv_1", map[string]any{"query": "read files"}, "tool_search"),
					toolSearchBlock("srv_1", "Read", "Grep", "Edit"),
					anthropic.NewToolUseBlock("tu_1", map[string]any{"path": "/tmp"}, "shell"),
				}},
			}

			data, err := json.Marshal(rec)
			Expect(err).NotTo(HaveOccurred())

			// A plain SDK decode loses the references (mis-selecting the error variant):
			// this is the corruption the record's repair exists to undo.
			var bare struct {
				Message anthropic.MessageParam `json:"message"`
			}
			Expect(json.Unmarshal(data, &bare)).To(Succeed())
			Expect(bare.Message.Content[2].OfToolSearchToolResult.Content.OfRequestToolSearchToolSearchResultBlock).To(BeNil())

			var back AssistantRecord
			Expect(json.Unmarshal(data, &back)).To(Succeed())

			block := back.Message.Content[2].OfToolSearchToolResult
			Expect(block).NotTo(BeNil())
			Expect(block.Content.OfRequestToolSearchToolSearchResultBlock).NotTo(BeNil())
			names := make([]string, 0)
			for _, ref := range block.Content.OfRequestToolSearchToolSearchResultBlock.ToolReferences {
				names = append(names, ref.ToolName)
			}
			Expect(names).To(Equal([]string{"Read", "Grep", "Edit"}))

			// The repaired record must re-marshal byte-identically to the original, so the
			// turn resent on resume matches what the model produced.
			remarshalled, err := json.Marshal(&back)
			Expect(err).NotTo(HaveOccurred())
			Expect(remarshalled).To(Equal(data))
		})

		It("leaves an error-variant tool_search block unchanged", func() {
			rec := &AssistantRecord{
				Iteration: 1,
				Message: anthropic.MessageParam{Role: anthropic.MessageParamRoleAssistant, Content: []anthropic.ContentBlockParamUnion{
					{OfToolSearchToolResult: &anthropic.ToolSearchToolResultBlockParam{
						ToolUseID: "srv_9",
						Content: anthropic.ToolSearchToolResultBlockParamContentUnion{
							OfRequestToolSearchToolResultError: &anthropic.ToolSearchToolResultErrorParam{
								ErrorCode: anthropic.ToolSearchToolResultErrorCodeUnavailable,
							},
						},
					}},
				}},
			}

			data, err := json.Marshal(rec)
			Expect(err).NotTo(HaveOccurred())

			var back AssistantRecord
			Expect(json.Unmarshal(data, &back)).To(Succeed())

			remarshalled, err := json.Marshal(&back)
			Expect(err).NotTo(HaveOccurred())
			Expect(remarshalled).To(Equal(data))
		})
	})

	Describe("Fold", func() {
		meta := func() Record {
			return Record{Seq: 1, Protocol: MetaProtocol, Meta: &MetaRecord{
				Version: Version, RunID: newID(), Prompt: "start here",
				Fingerprint: Fingerprint{Model: "claude-opus-4-8"},
			}}
		}

		It("rebuilds the initial prompt as the first user message", func() {
			rs, err := Fold([]Record{meta()})
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Messages).To(HaveLen(1))
			Expect(rs.Pending).To(BeNil())
			Expect(rs.NextIteration).To(Equal(int64(0)))
		})

		It("commits a complete turn and derives counters", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Pending).To(BeNil())
			// user(prompt), assistant, user(results)
			Expect(rs.Messages).To(HaveLen(3))
			Expect(rs.NextIteration).To(Equal(int64(1)))
			Expect(rs.Counters.LlmCalls).To(Equal(int64(1)))
			Expect(rs.Counters.ToolCalls).To(Equal(int64(1)))
			Expect(rs.Counters.InTokens).To(Equal(int64(10)))
			Expect(rs.Counters.OutTokens).To(Equal(int64(5)))
		})

		It("sums the cache token split across assistant records", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: &AssistantRecord{
					Iteration: 0, Message: anthropic.NewAssistantMessage(anthropic.NewTextBlock("a")),
					InTokens: 10, OutTokens: 5, CacheReadTokens: 100, CacheCreateTokens: 40,
				}},
				{Seq: 3, Protocol: AssistantProtocol, Assistant: &AssistantRecord{
					Iteration: 1, Message: anthropic.NewAssistantMessage(anthropic.NewTextBlock("b")),
					InTokens: 2, OutTokens: 3, CacheReadTokens: 200,
				}},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Counters.CacheReadTokens).To(Equal(int64(300)))
			Expect(rs.Counters.CacheCreateTokens).To(Equal(int64(40)))
		})

		It("folds a pre-caching record (no cache fields) as zero", func() {
			// A journal written before prompt caching omits the cache fields; they read as
			// zero, which is correct since caching was off and there were none.
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantText(0, "end_turn", "done")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Counters.CacheReadTokens).To(BeZero())
			Expect(rs.Counters.CacheCreateTokens).To(BeZero())
		})

		It("leaves an unanswered tool batch as a pending turn", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1", "tu_2")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Pending).NotTo(BeNil())
			Expect(rs.Pending.Answered).To(HaveKeyWithValue("tu_1", true))
			Expect(rs.Pending.Answered).NotTo(HaveKey("tu_2"))
			Expect(rs.Pending.Results).To(HaveLen(1))
			// The in-flight assistant turn is not committed to Messages.
			Expect(rs.Messages).To(HaveLen(1))
			Expect(rs.NextIteration).To(Equal(int64(1)))
			Expect(unansweredToolUses(rs.Pending.Assistant, rs.Pending.Answered)).To(Equal([]string{"tu_2"}))
		})

		It("surfaces a trailing paused turn's stop reason and commits it", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: &AssistantRecord{
					Iteration:  0,
					StopReason: "pause_turn",
					Message:    anthropic.NewAssistantMessage(anthropic.NewTextBlock("searching")),
				}},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Pending).To(BeNil())
			Expect(rs.LastStopReason).To(Equal("pause_turn"))
			Expect(rs.Messages).To(HaveLen(2))
		})

		It("records terminal state and reports completion", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: &AssistantRecord{Iteration: 0, Message: anthropic.NewAssistantMessage(anthropic.NewTextBlock("final"))}},
				{Seq: 3, Protocol: TerminalProtocol, Terminal: &TerminalRecord{Reason: ReasonCompleted}},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Completed()).To(BeTrue())
		})

		It("rejects an unsupported version", func() {
			r := meta()
			r.Meta.Version = Version + 1
			_, err := Fold([]Record{r})
			Expect(err).To(MatchError(ErrVersion))
		})

		It("rejects records that do not start with meta", func() {
			_, err := Fold([]Record{{Seq: 1, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")}})
			Expect(err).To(MatchError(ErrNoMeta))
		})

		It("rejects a non-increasing seq", func() {
			recs := []Record{meta(), {Seq: 1, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")}}
			_, err := Fold(recs)
			Expect(err).To(MatchError(ErrCorrupt))
		})

		It("restores the interactive flag from meta", func() {
			r := meta()
			r.Meta.Interactive = true
			rs, err := Fold([]Record{r})
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Interactive).To(BeTrue())
		})

		It("appends an interactive follow-up as a new user turn after a completed answer", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantText(0, "end_turn", "first answer")},
				{Seq: 3, Protocol: UserProtocol, User: userRecord("second question")},
				{Seq: 4, Protocol: AssistantProtocol, Assistant: assistantText(1, "end_turn", "second answer")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			// user(prompt), assistant, user(follow-up), assistant
			Expect(rs.Messages).To(HaveLen(4))
			Expect(userTexts(rs)).To(Equal([]string{"start here", "second question"}))
			Expect(rs.NextIteration).To(Equal(int64(2)))
			Expect(rs.Counters.LlmCalls).To(Equal(int64(2)))
		})

		It("folds a follow-up into a trailing tool-results turn, mirroring the post-error runtime fold", func() {
			// A turn calls a tool (answered), then the next LLM call errored before a
			// reply, so the conversation rests on a dangling user(results) turn and the
			// follow-up must merge into it rather than open a second user turn in a row.
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
				{Seq: 4, Protocol: UserProtocol, User: userRecord("carry on")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			// user(prompt), assistant(tool call), user(results + follow-up merged)
			Expect(rs.Messages).To(HaveLen(3))
			last := rs.Messages[2]
			Expect(last.Role).To(Equal(anthropic.MessageParamRoleUser))
			// The merged user turn carries both the tool result and the follow-up text.
			Expect(userTexts(rs)).To(Equal([]string{"start here", "carry on"}))
			hasResult := false
			for _, b := range last.Content {
				if b.OfToolResult != nil {
					hasResult = true
				}
			}
			Expect(hasResult).To(BeTrue(), "the tool result must survive alongside the follow-up")
		})

		It("merges consecutive user records into one turn", func() {
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
				{Seq: 4, Protocol: UserProtocol, User: userRecord("one")},
				{Seq: 5, Protocol: UserProtocol, User: userRecord("two")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Messages).To(HaveLen(3))
			Expect(userTexts(rs)).To(Equal([]string{"start here", "onetwo"}))
		})

		It("preserves the resume position and stop reason when the journal ends on a follow-up", func() {
			// submit follow-up -> LLM call fails -> operator leaves: the journal's last
			// structural record is a user turn, but NextIteration must still point past the
			// last assistant so a resumed turn does not reuse an iteration index.
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantText(0, "end_turn", "answer")},
				{Seq: 3, Protocol: UserProtocol, User: userRecord("next")},
				{Seq: 4, Protocol: TerminalProtocol, Terminal: &TerminalRecord{Reason: ReasonSuspended}},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.NextIteration).To(Equal(int64(1)))
			Expect(rs.LastStopReason).To(Equal("end_turn"))
			Expect(rs.Pending).To(BeNil())
			Expect(rs.Completed()).To(BeFalse())
			Expect(userTexts(rs)).To(Equal([]string{"start here", "next"}))
		})

		It("reconstructs an identical conversation whether follow-ups were merged at runtime or journaled separately", func() {
			// Two follow-ups journaled as separate user records after a dangling results
			// turn must fold to the same Messages as if the runtime had merged them.
			separate := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
				{Seq: 4, Protocol: UserProtocol, User: userRecord("part one ")},
				{Seq: 5, Protocol: UserProtocol, User: userRecord("part two")},
			}
			merged := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantWithTools(0, "tu_1")},
				{Seq: 3, Protocol: ToolResultProtocol, ToolResult: toolResult("tu_1")},
				{Seq: 4, Protocol: UserProtocol, User: userRecord("part one part two")},
			}
			a, err := Fold(separate)
			Expect(err).NotTo(HaveOccurred())
			b, err := Fold(merged)
			Expect(err).NotTo(HaveOccurred())
			Expect(userTexts(a)).To(Equal(userTexts(b)))
			Expect(a.Messages).To(HaveLen(len(b.Messages)))
		})

		It("resumes a chat across a suspend terminal record", func() {
			// The normal resumed-chat shape: a suspended session, then more turns appended
			// after resume. The final terminal wins for completion, and the follow-up added
			// after the suspend is part of the conversation.
			recs := []Record{
				meta(),
				{Seq: 2, Protocol: AssistantProtocol, Assistant: assistantText(0, "end_turn", "answer one")},
				{Seq: 3, Protocol: TerminalProtocol, Terminal: &TerminalRecord{Reason: ReasonSuspended}},
				{Seq: 4, Protocol: UserProtocol, User: userRecord("again")},
				{Seq: 5, Protocol: AssistantProtocol, Assistant: assistantText(1, "end_turn", "answer two")},
			}
			rs, err := Fold(recs)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Completed()).To(BeFalse())
			Expect(rs.NextIteration).To(Equal(int64(2)))
			Expect(userTexts(rs)).To(Equal([]string{"start here", "again"}))
		})
	})

	Describe("Fingerprint", func() {
		It("reports an actionable field-level diff", func() {
			a := Fingerprint{Model: "claude-opus-4-7", SystemHash: "h1", MaxTokens: 100}
			b := Fingerprint{Model: "claude-opus-4-8", SystemHash: "h2", MaxTokens: 100}
			Expect(a.Equal(b)).To(BeFalse())
			Expect(a.Diff(b)).To(ConsistOf("model: claude-opus-4-7 -> claude-opus-4-8", "system prompt: changed"))
		})

		It("never stores the raw system prompt", func() {
			secret := "SENSITIVE-SYSTEM-PROMPT-TEXT"
			fp := Fingerprint{Model: "m", SystemHash: HashHex([]byte(secret))}
			data, err := json.Marshal(fp)
			Expect(err).NotTo(HaveOccurred())
			Expect(bytes.Contains(data, []byte(secret))).To(BeFalse())
		})
	})
})
