//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"fmt"
)

// BlockType is the type discriminator of an event content block.
type BlockType string

const (
	BlockThinking   BlockType = "thinking"
	BlockText       BlockType = "text"
	BlockToolCall   BlockType = "tool_call"
	BlockToolResult BlockType = "tool_result"
	BlockAgentCall  BlockType = "agent_call"
	BlockStatus     BlockType = "status"
)

// BlockContent is the content of a single event block. The concrete types are
// ThinkingBlock, TextBlock, ToolCallBlock, ToolResultBlock, AgentCallBlock and
// StatusBlock. The Go type is the single source of truth for the block type; the
// "type" field is added on the wire by Block during marshaling.
type BlockContent interface {
	blockType() BlockType
}

// ThinkingBlock is reasoning output. Signature is opaque and provider defined;
// it is for display and audit only and is never replayed into a model across the
// agent boundary.
type ThinkingBlock struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

func (ThinkingBlock) blockType() BlockType { return BlockThinking }

// TextBlock is answer text.
type TextBlock struct {
	Text string `json:"text"`
}

func (TextBlock) blockType() BlockType { return BlockText }

// ToolCallBlock is the agent invoking one of its own tools.
type ToolCallBlock struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

func (ToolCallBlock) blockType() BlockType { return BlockToolCall }

// ToolResultBlock is the result of a ToolCallBlock, identified by CallID and
// carrying the shared ToolResult outcome.
type ToolResultBlock struct {
	CallID string `json:"call_id"`
	ToolResult
}

func (ToolResultBlock) blockType() BlockType { return BlockToolResult }

// AgentCallBlock is the agent invoking another agent, distinct from a local
// tool call. Task is the request id of the spawned sub-task; its stream is
// correlated via Header.Parent.
type AgentCallBlock struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Task string `json:"task"`
}

func (AgentCallBlock) blockType() BlockType { return BlockAgentCall }

// StatusBlock reports progress. Iteration is 1-based; a zero value is omitted.
type StatusBlock struct {
	Iteration int    `json:"iteration,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Usage     *Usage `json:"usage,omitempty"`
}

func (StatusBlock) blockType() BlockType { return BlockStatus }

// Block wraps a single BlockContent for transport. It marshals to a flat JSON
// object carrying the variant's fields plus a "type" discriminator, and decodes
// back to the matching concrete type.
type Block struct {
	content BlockContent
}

// NewBlock wraps any BlockContent.
func NewBlock(content BlockContent) Block { return Block{content: content} }

// NewThinkingBlock builds a thinking Block.
func NewThinkingBlock(text string) Block { return NewBlock(ThinkingBlock{Text: text}) }

// NewTextBlock builds a text Block.
func NewTextBlock(text string) Block { return NewBlock(TextBlock{Text: text}) }

// NewToolCallBlock builds a tool_call Block.
func NewToolCallBlock(id, name string, input json.RawMessage) Block {
	return NewBlock(ToolCallBlock{ID: id, Name: name, Input: input})
}

// NewToolResultBlock builds a tool_result Block.
func NewToolResultBlock(callID, output string, isError bool) Block {
	return NewBlock(ToolResultBlock{CallID: callID, ToolResult: ToolResult{Output: output, IsError: isError}})
}

// AsAny returns the concrete BlockContent for use in a type switch, or nil if
// the Block is empty.
func (b Block) AsAny() any {
	if b.content == nil {
		return nil
	}

	return b.content
}

// Content returns the wrapped BlockContent, or nil if the Block is empty.
func (b Block) Content() BlockContent { return b.content }

// Type returns the block type, or the empty string if the Block is empty.
func (b Block) Type() BlockType {
	if b.content == nil {
		return ""
	}

	return b.content.blockType()
}

// MarshalJSON renders the block as its content fields plus a "type"
// discriminator.
func (b Block) MarshalJSON() ([]byte, error) {
	if b.content == nil {
		return nil, fmt.Errorf("%w: block has no content", ErrInvalidMessage)
	}

	raw, err := json.Marshal(b.content)
	if err != nil {
		return nil, err
	}

	fields := map[string]json.RawMessage{}
	err = json.Unmarshal(raw, &fields)
	if err != nil {
		return nil, err
	}

	kind, err := json.Marshal(b.content.blockType())
	if err != nil {
		return nil, err
	}
	fields["type"] = kind

	return json.Marshal(fields)
}

// UnmarshalJSON decodes the block into the concrete type named by its "type"
// discriminator. It returns ErrUnknownBlockType for an unrecognized type.
func (b *Block) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type BlockType `json:"type"`
	}

	err := json.Unmarshal(data, &probe)
	if err != nil {
		return err
	}

	var content BlockContent

	switch probe.Type {
	case BlockThinking:
		var v ThinkingBlock
		err = json.Unmarshal(data, &v)
		content = v
	case BlockText:
		var v TextBlock
		err = json.Unmarshal(data, &v)
		content = v
	case BlockToolCall:
		var v ToolCallBlock
		err = json.Unmarshal(data, &v)
		content = v
	case BlockToolResult:
		var v ToolResultBlock
		err = json.Unmarshal(data, &v)
		content = v
	case BlockAgentCall:
		var v AgentCallBlock
		err = json.Unmarshal(data, &v)
		content = v
	case BlockStatus:
		var v StatusBlock
		err = json.Unmarshal(data, &v)
		content = v
	default:
		return fmt.Errorf("%w: %q", ErrUnknownBlockType, probe.Type)
	}

	if err != nil {
		return err
	}

	b.content = content

	return nil
}
