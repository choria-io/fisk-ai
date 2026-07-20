//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package llm is the provider-neutral domain model for a model conversation. It
// exists so the agent loop, the persisted journal, the renderers and the tool
// kinds speak one representation of a message that no single provider's SDK
// types leak through. Per-provider codecs (see internal/llm/anthropic) translate
// this model to and from a concrete wire format at the edges.
//
// The model names the block kinds every provider shares (text, thinking, tool
// use, tool result) and carries anything else a provider emits as an opaque
// ProviderBlock, so a server-side block the neutral model does not name still
// survives a round-trip verbatim.
package llm

import "encoding/json"

// Role is the author of a message.
type Role string

const (
	// RoleUser is a message from the operator or a batch of tool results.
	RoleUser Role = "user"
	// RoleAssistant is a message from the model.
	RoleAssistant Role = "assistant"
)

// Message is one turn in a conversation.
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// ContentBlock is a discriminated union of the block kinds a turn can carry.
// Exactly one field is set. Text, Thinking, ToolUse and ToolResult are the
// neutral kinds every provider shares; Provider is the opaque escape hatch for
// server-side blocks the neutral model does not name (an Anthropic
// tool_search_tool_result, a web_search_tool_result, a redacted_thinking block).
type ContentBlock struct {
	Text       *TextBlock       `json:"text,omitempty"`
	Thinking   *ThinkingBlock   `json:"thinking,omitempty"`
	ToolUse    *ToolUseBlock    `json:"tool_use,omitempty"`
	ToolResult *ToolResultBlock `json:"tool_result,omitempty"`
	Provider   *ProviderBlock   `json:"provider,omitempty"`
}

// TextBlock is plain text, either the model's prose or an operator's prompt.
type TextBlock struct {
	Text string `json:"text"`
}

// ThinkingBlock is a reasoning turn. Text is the human-readable (summarized)
// reasoning. Signature is the opaque provider payload that must be echoed back
// verbatim for the provider to accept the turn on a later call: an Anthropic
// signature, an OpenAI encrypted_content. It is bytes, not a string, because the
// neutral model never inspects or renders it; it only preserves it.
type ThinkingBlock struct {
	Text      string `json:"text,omitempty"`
	Signature []byte `json:"signature,omitempty"`
}

// ToolUseBlock is the model asking to run a tool. Input is the raw JSON arguments
// so it is preserved byte-for-byte without a schema-shaped intermediate.
type ToolUseBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// ToolResultBlock is the outcome of a single tool call, answering the ToolUse
// with the matching ID. Content is the textual result; IsError marks a result
// the model should treat as a failure.
type ToolResultBlock struct {
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// ProviderBlock preserves a provider-specific block the neutral model does not
// name. Kind is the provider's own discriminator (an Anthropic block "type").
// Raw is the block's faithful JSON as the provider's codec produced it, stored so
// the block survives the neutral round-trip and can be reconstructed for a later
// call without the neutral model having to understand it.
type ProviderBlock struct {
	Kind string          `json:"kind"`
	Raw  json.RawMessage `json:"raw"`
}

// ToolDef is a provider-neutral tool definition. The per-provider codec renders
// it to the provider's tool format. DeferLoading asks for the tool to be hidden
// behind server-side tool search, leaving the model to discover it through the
// provider's search tool rather than receiving it up front. How a codec spells
// that on the wire is its own concern; false is the default in every format
// seen so far, so emitting it and omitting it are equivalent.
type ToolDef struct {
	Name         string
	Description  string
	InputSchema  map[string]any
	DeferLoading bool
}
