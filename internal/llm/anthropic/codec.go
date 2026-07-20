//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package anthropic is the Anthropic codec for the neutral llm model: it
// translates between llm.Message / llm.Response and the anthropic-sdk-go wire
// types. Once the migration completes this is the only package that imports the
// Anthropic SDK.
package anthropic

import (
	"encoding/json"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// MessageToNeutral converts an Anthropic message into the neutral model. Named
// block kinds decompose into their neutral form; every other block is preserved
// verbatim as a ProviderBlock so nothing is lost.
func MessageToNeutral(mp sdk.MessageParam) (llm.Message, error) {
	out := llm.Message{Role: llm.Role(mp.Role)}

	for i, block := range mp.Content {
		nb, err := blockToNeutral(block)
		if err != nil {
			return llm.Message{}, fmt.Errorf("content block %d: %w", i, err)
		}
		out.Content = append(out.Content, nb)
	}

	return out, nil
}

// MessageToAnthropic converts a neutral message back into an Anthropic message.
func MessageToAnthropic(m llm.Message) (sdk.MessageParam, error) {
	out := sdk.MessageParam{Role: sdk.MessageParamRole(m.Role)}

	for i, block := range m.Content {
		ab, err := blockToAnthropic(block)
		if err != nil {
			return sdk.MessageParam{}, fmt.Errorf("content block %d: %w", i, err)
		}
		out.Content = append(out.Content, ab)
	}

	return out, nil
}

// ResponseToNeutral converts an Anthropic response into the neutral model. The
// assistant content is folded through the same block codec as a stored message
// (via ToParam) so a response and a journaled turn share one representation.
func ResponseToNeutral(msg *sdk.Message) (llm.Response, error) {
	content, err := MessageToNeutral(msg.ToParam())
	if err != nil {
		return llm.Response{}, err
	}

	return llm.Response{
		Content:    content.Content,
		StopReason: stopReasonToNeutral(msg.StopReason),
		Usage: llm.Usage{
			In:          msg.Usage.InputTokens,
			Out:         msg.Usage.OutputTokens,
			CacheRead:   msg.Usage.CacheReadInputTokens,
			CacheCreate: msg.Usage.CacheCreationInputTokens,
		},
	}, nil
}

// blockToNeutral maps a single Anthropic content block to its neutral form.
func blockToNeutral(block sdk.ContentBlockParamUnion) (llm.ContentBlock, error) {
	switch {
	case block.OfText != nil:
		return llm.ContentBlock{Text: &llm.TextBlock{Text: block.OfText.Text}}, nil

	case block.OfThinking != nil:
		return llm.ContentBlock{Thinking: &llm.ThinkingBlock{
			Text:      block.OfThinking.Thinking,
			Signature: []byte(block.OfThinking.Signature),
		}}, nil

	case block.OfToolUse != nil:
		input, err := json.Marshal(block.OfToolUse.Input)
		if err != nil {
			return llm.ContentBlock{}, fmt.Errorf("tool_use input: %w", err)
		}
		return llm.ContentBlock{ToolUse: &llm.ToolUseBlock{
			ID:    block.OfToolUse.ID,
			Name:  block.OfToolUse.Name,
			Input: input,
		}}, nil

	case block.OfToolResult != nil && isPlainTextResult(block.OfToolResult):
		return llm.ContentBlock{ToolResult: &llm.ToolResultBlock{
			ToolUseID: block.OfToolResult.ToolUseID,
			Content:   block.OfToolResult.Content[0].OfText.Text,
			IsError:   block.OfToolResult.IsError.Or(false),
		}}, nil
	}

	return providerBlockToNeutral(block)
}

// providerBlockToNeutral preserves a block the neutral model does not name by
// storing its faithful JSON. Marshaling an Anthropic block is lossless (only the
// SDK's decoder has a round-trip gap, repaired in blockToAnthropic), so Raw is
// always a true copy of the original block.
func providerBlockToNeutral(block sdk.ContentBlockParamUnion) (llm.ContentBlock, error) {
	raw, err := json.Marshal(block)
	if err != nil {
		return llm.ContentBlock{}, fmt.Errorf("marshaling provider block: %w", err)
	}

	// The discriminator is read from the marshaled JSON, not GetType(): the SDK
	// leaves each block's Type field at its zero value and fills the default only
	// on marshal, so GetType() reports an empty string for these server-side blocks.
	var disc struct {
		Type string `json:"type"`
	}
	err = json.Unmarshal(raw, &disc)
	if err != nil {
		return llm.ContentBlock{}, fmt.Errorf("reading provider block type: %w", err)
	}
	if disc.Type == "" {
		return llm.ContentBlock{}, fmt.Errorf("provider block has no type discriminator")
	}

	return llm.ContentBlock{Provider: &llm.ProviderBlock{Kind: disc.Type, Raw: raw}}, nil
}

// blockToAnthropic maps a single neutral content block back to an Anthropic block.
func blockToAnthropic(block llm.ContentBlock) (sdk.ContentBlockParamUnion, error) {
	switch {
	case block.Text != nil:
		return sdk.NewTextBlock(block.Text.Text), nil

	case block.Thinking != nil:
		return sdk.NewThinkingBlock(string(block.Thinking.Signature), block.Thinking.Text), nil

	case block.ToolUse != nil:
		return sdk.NewToolUseBlock(block.ToolUse.ID, json.RawMessage(block.ToolUse.Input), block.ToolUse.Name), nil

	case block.ToolResult != nil:
		return sdk.NewToolResultBlock(block.ToolResult.ToolUseID, block.ToolResult.Content, block.ToolResult.IsError), nil

	case block.Provider != nil:
		return providerBlockToAnthropic(block.Provider)
	}

	return sdk.ContentBlockParamUnion{}, fmt.Errorf("content block has no variant set")
}

// providerBlockToAnthropic reconstructs an Anthropic block from its preserved
// JSON. It then repairs the one block the SDK decoder cannot round-trip: a
// successful tool_search_tool_result, whose required tool_references the decoder
// drops while mis-selecting the error variant of the content union. Marshaling
// wrote them out correctly, so they are re-read from Raw and the union rebuilt.
func providerBlockToAnthropic(pb *llm.ProviderBlock) (sdk.ContentBlockParamUnion, error) {
	var block sdk.ContentBlockParamUnion
	err := json.Unmarshal(pb.Raw, &block)
	if err != nil {
		return sdk.ContentBlockParamUnion{}, fmt.Errorf("provider block %q: %w", pb.Kind, err)
	}

	err = repairToolSearchResult(&block, pb.Raw)
	if err != nil {
		return sdk.ContentBlockParamUnion{}, fmt.Errorf("provider block %q: %w", pb.Kind, err)
	}

	return block, nil
}

// repairToolSearchResult restores the tool_references dropped from a successful
// tool_search_tool_result block. raw is the block's original, correct JSON. An
// error-variant block already round-trips, so only the success variant is rebuilt.
func repairToolSearchResult(block *sdk.ContentBlockParamUnion, raw json.RawMessage) error {
	if block.OfToolSearchToolResult == nil {
		return nil
	}

	var rawBlock struct {
		Content struct {
			Type           string                        `json:"type"`
			ToolReferences []sdk.ToolReferenceBlockParam `json:"tool_references"`
		} `json:"content"`
	}
	err := json.Unmarshal(raw, &rawBlock)
	if err != nil {
		return err
	}
	if rawBlock.Content.Type != "tool_search_tool_search_result" {
		return nil
	}

	// Rebuild the whole content union: the decoder left the error variant set and
	// the success variant nil, so patching a field in place is not enough. Type is
	// left as its zero value, which marshals to "tool_search_tool_search_result"
	// via the SDK's default tag, matching how the block was originally produced.
	block.OfToolSearchToolResult.Content = sdk.ToolSearchToolResultBlockParamContentUnion{
		OfRequestToolSearchToolSearchResultBlock: &sdk.ToolSearchToolSearchResultBlockParam{
			ToolReferences: rawBlock.Content.ToolReferences,
		},
	}

	return nil
}

// isPlainTextResult reports whether a tool_result carries exactly one text block,
// the shape every tool kind in this codebase produces. Anything else (image
// content, multiple blocks) is preserved verbatim as a ProviderBlock instead.
func isPlainTextResult(r *sdk.ToolResultBlockParam) bool {
	return len(r.Content) == 1 && r.Content[0].OfText != nil
}

// stopReasonToNeutral maps an Anthropic stop reason onto the neutral vocabulary.
// The values coincide, so an unrecognized reason passes through unchanged rather
// than being lost.
func stopReasonToNeutral(r sdk.StopReason) llm.StopReason {
	return llm.StopReason(r)
}
