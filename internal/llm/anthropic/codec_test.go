//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"bytes"
	"encoding/json"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
)

func TestAnthropicCodec(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/LLM/Anthropic")
}

// roundTrip converts a message to the neutral model and back, returning the
// re-encoded Anthropic message.
func roundTrip(mp sdk.MessageParam) sdk.MessageParam {
	neutral, err := MessageToNeutral(mp)
	Expect(err).NotTo(HaveOccurred())

	back, err := MessageToAnthropic(neutral)
	Expect(err).NotTo(HaveOccurred())

	return back
}

// toolSearchBlock is a successful tool_search_tool_result as resp.ToParam()
// appends it: a nested union whose tool_references the SDK decoder drops.
func toolSearchBlock(useID string, tools ...string) sdk.ContentBlockParamUnion {
	refs := make([]sdk.ToolReferenceBlockParam, 0, len(tools))
	for _, name := range tools {
		refs = append(refs, sdk.ToolReferenceBlockParam{ToolName: name})
	}
	return sdk.ContentBlockParamUnion{OfToolSearchToolResult: &sdk.ToolSearchToolResultBlockParam{
		ToolUseID: useID,
		Content: sdk.ToolSearchToolResultBlockParamContentUnion{
			OfRequestToolSearchToolSearchResultBlock: &sdk.ToolSearchToolSearchResultBlockParam{ToolReferences: refs},
		},
	}}
}

var _ = Describe("Anthropic codec", func() {
	Describe("message round-trip (golden, S1 tripwire)", func() {
		It("preserves every block variant byte-identically through the neutral model", func() {
			assistant := sdk.MessageParam{
				Role: sdk.MessageParamRoleAssistant,
				Content: []sdk.ContentBlockParamUnion{
					sdk.NewThinkingBlock("sig-opaque-123", "reasoning"),
					sdk.NewRedactedThinkingBlock("redacted-xyz"),
					sdk.NewTextBlock("narration"),
					sdk.NewServerToolUseBlock("srv_1", map[string]any{"query": "choria"}, sdk.ServerToolUseBlockParamNameWebSearch),
					{OfWebSearchToolResult: &sdk.WebSearchToolResultBlockParam{
						ToolUseID: "srv_1",
						Content: sdk.WebSearchToolResultBlockParamContentUnion{
							OfWebSearchToolResultBlockItem: []sdk.WebSearchResultBlockParam{{
								EncryptedContent: "enc-1", Title: "Choria", URL: "https://choria.io",
							}},
						},
					}},
					sdk.NewToolUseBlock("tu_1", map[string]any{"path": "/tmp"}, "shell"),
				},
			}
			messages := []sdk.MessageParam{
				sdk.NewUserMessage(sdk.NewTextBlock("do it")),
				assistant,
				sdk.NewUserMessage(sdk.NewToolResultBlock("tu_1", "done", false)),
			}

			for _, mp := range messages {
				want, err := json.Marshal(mp)
				Expect(err).NotTo(HaveOccurred())

				got, err := json.Marshal(roundTrip(mp))
				Expect(err).NotTo(HaveOccurred())

				Expect(got).To(Equal(want), "round-trip must be byte-identical")
			}

			// The opaque fields must survive inside the neutral form, not just the
			// re-encoding: signatures and encrypted server content are load-bearing.
			neutral, err := MessageToNeutral(assistant)
			Expect(err).NotTo(HaveOccurred())
			blob, err := json.Marshal(neutral)
			Expect(err).NotTo(HaveOccurred())
			for _, want := range []string{"redacted-xyz", "srv_1", "enc-1", "server_tool_use", "web_search_tool_result"} {
				Expect(bytes.Contains(blob, []byte(want))).To(BeTrue(), "field %q must survive in the neutral form", want)
			}
		})

		It("carries the thinking signature as an opaque neutral field", func() {
			mp := sdk.MessageParam{Role: sdk.MessageParamRoleAssistant, Content: []sdk.ContentBlockParamUnion{
				sdk.NewThinkingBlock("sig-opaque-123", "reasoning"),
			}}

			neutral, err := MessageToNeutral(mp)
			Expect(err).NotTo(HaveOccurred())
			Expect(neutral.Content).To(HaveLen(1))
			Expect(neutral.Content[0].Thinking).NotTo(BeNil())
			Expect(neutral.Content[0].Thinking.Text).To(Equal("reasoning"))
			Expect(string(neutral.Content[0].Thinking.Signature)).To(Equal("sig-opaque-123"))
		})
	})

	Describe("named block decomposition", func() {
		It("maps text, tool_use and tool_result to neutral kinds", func() {
			mp := sdk.MessageParam{Role: sdk.MessageParamRoleAssistant, Content: []sdk.ContentBlockParamUnion{
				sdk.NewTextBlock("hello"),
				sdk.NewToolUseBlock("tu_1", map[string]any{"path": "/tmp"}, "shell"),
			}}

			neutral, err := MessageToNeutral(mp)
			Expect(err).NotTo(HaveOccurred())
			Expect(neutral.Content[0].Text.Text).To(Equal("hello"))
			Expect(neutral.Content[1].ToolUse.ID).To(Equal("tu_1"))
			Expect(neutral.Content[1].ToolUse.Name).To(Equal("shell"))
			Expect(string(neutral.Content[1].ToolUse.Input)).To(Equal(`{"path":"/tmp"}`))

			result := sdk.NewUserMessage(sdk.NewToolResultBlock("tu_1", "the output", true))
			rn, err := MessageToNeutral(result)
			Expect(err).NotTo(HaveOccurred())
			Expect(rn.Role).To(Equal(llm.RoleUser))
			Expect(rn.Content[0].ToolResult).NotTo(BeNil())
			Expect(rn.Content[0].ToolResult.ToolUseID).To(Equal("tu_1"))
			Expect(rn.Content[0].ToolResult.Content).To(Equal("the output"))
			Expect(rn.Content[0].ToolResult.IsError).To(BeTrue())
		})
	})

	Describe("tool_search_tool_result preservation", func() {
		It("restores the tool_references a bare SDK decode drops", func() {
			mp := sdk.MessageParam{Role: sdk.MessageParamRoleAssistant, Content: []sdk.ContentBlockParamUnion{
				sdk.NewTextBlock("searching for tools"),
				sdk.NewServerToolUseBlock("srv_1", map[string]any{"query": "read files"}, "tool_search"),
				toolSearchBlock("srv_1", "Read", "Grep", "Edit"),
				sdk.NewToolUseBlock("tu_1", map[string]any{"path": "/tmp"}, "shell"),
			}}

			want, err := json.Marshal(mp)
			Expect(err).NotTo(HaveOccurred())

			neutral, err := MessageToNeutral(mp)
			Expect(err).NotTo(HaveOccurred())

			// It is carried opaquely, and the references are inside the preserved JSON.
			pb := neutral.Content[2].Provider
			Expect(pb).NotTo(BeNil())
			Expect(pb.Kind).To(Equal("tool_search_tool_result"))
			Expect(bytes.Contains(pb.Raw, []byte("Grep"))).To(BeTrue())

			back, err := MessageToAnthropic(neutral)
			Expect(err).NotTo(HaveOccurred())

			// The reconstructed union must carry the references again, not the
			// mis-selected error variant a bare decode leaves behind.
			block := back.Content[2].OfToolSearchToolResult
			Expect(block).NotTo(BeNil())
			Expect(block.Content.OfRequestToolSearchToolSearchResultBlock).NotTo(BeNil())
			names := make([]string, 0)
			for _, ref := range block.Content.OfRequestToolSearchToolSearchResultBlock.ToolReferences {
				names = append(names, ref.ToolName)
			}
			Expect(names).To(Equal([]string{"Read", "Grep", "Edit"}))

			got, err := json.Marshal(back)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want), "resent turn must match what the model produced")
		})

		It("leaves an error-variant tool_search block unchanged", func() {
			errBlock := sdk.ContentBlockParamUnion{OfToolSearchToolResult: &sdk.ToolSearchToolResultBlockParam{
				ToolUseID: "srv_1",
				Content: sdk.ToolSearchToolResultBlockParamContentUnion{
					OfRequestToolSearchToolResultError: &sdk.ToolSearchToolResultErrorParam{
						ErrorCode: "unavailable",
					},
				},
			}}
			mp := sdk.MessageParam{Role: sdk.MessageParamRoleAssistant, Content: []sdk.ContentBlockParamUnion{errBlock}}

			want, err := json.Marshal(mp)
			Expect(err).NotTo(HaveOccurred())

			got, err := json.Marshal(roundTrip(mp))
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(want))
		})
	})

	Describe("ResponseToNeutral", func() {
		It("maps stop reason and usage tiers", func() {
			// Decoded from JSON so the SDK response union carries the internal state
			// ToParam relies on, exactly as a real API response would.
			raw := `{
				"role": "assistant",
				"stop_reason": "max_tokens",
				"content": [{"type": "text", "text": "partial"}],
				"usage": {
					"input_tokens": 100,
					"output_tokens": 8,
					"cache_read_input_tokens": 40,
					"cache_creation_input_tokens": 12
				}
			}`
			var msg sdk.Message
			Expect(json.Unmarshal([]byte(raw), &msg)).To(Succeed())

			resp, err := ResponseToNeutral(&msg)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.StopReason).To(Equal(llm.StopMaxTokens))
			Expect(resp.Usage).To(Equal(llm.Usage{In: 100, Out: 8, CacheRead: 40, CacheCreate: 12}))
			Expect(resp.Content[0].Text.Text).To(Equal("partial"))
		})
	})
})
