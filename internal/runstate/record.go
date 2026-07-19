//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package runstate persists an agent run as an append-only sequence of events so
// it can be suspended and resumed later, possibly in a fresh process.
//
// A run is journaled as records (see Record): a single Meta record, then an
// Assistant record after every LLM call and a ToolResult record as each tool
// completes, a User record for each interactive follow-up in a chat run, and
// finally a Terminal record. Folding the records back into a RunState (see Fold)
// reconstructs the conversation, the counters and the resume position. The record
// stream maps one-to-one onto the durability journal today and onto the A2A event
// stream later, so the same model backs local resume and remote streaming.
package runstate

import (
	"encoding/json"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

// Version is the current on-disk record schema version, stamped into the Meta
// record. Fold rejects a snapshot written by a newer version rather than
// best-effort parsing it, but reads any older version it still understands.
//
// v2 added the User record (a free-standing interactive user turn) and the
// Interactive meta flag. A v2 reader folds a v1 journal unchanged (neither is
// present); a v1 reader rejects a v2 journal with ErrVersion.
const Version = 2

// Protocol is the schema id of a Record, carried in the record body so a single
// stored record is self describing: given just one record you can find its
// origin schema, independent of the subject it arrived on or the store it was
// read from. The ids share the a2a product namespace under a ".session" segment
// so they never collide with the a2a wire protocols.
type Protocol string

// protocolNamespace is the a2a product namespace (a2a.ProtocolNamespace) under a
// ".session" segment. It is spelled out here rather than imported so the storage
// layer does not depend on the a2a package, which now carries the anthropic SDK
// via its remote-tool code; it must track a2a.ProtocolNamespace if that changes.
const protocolNamespace = "io.choria.fisk-ai.v1.session"

const (
	// MetaProtocol frames the run: version, id, fingerprint and the initial
	// prompt. It is always the first record.
	MetaProtocol Protocol = protocolNamespace + ".meta"
	// AssistantProtocol records one assistant turn (the result of one LLM call).
	AssistantProtocol Protocol = protocolNamespace + ".assistant"
	// UserProtocol records a free-standing interactive user turn: a chat follow-up
	// the operator typed at an input boundary, which has no other origin in the loop
	// (the initial prompt lives in Meta, tool results in ToolResult). Present only in
	// an interactive (--chat) run.
	UserProtocol Protocol = protocolNamespace + ".user"
	// ToolResultProtocol records the result of a single tool invocation, written
	// as the tool completes so a crash loses at most one tool.
	ToolResultProtocol Protocol = protocolNamespace + ".tool_result"
	// TerminalProtocol records why the run ended (or that it was suspended).
	TerminalProtocol Protocol = protocolNamespace + ".terminal"
)

// Record is one journal entry. Exactly one of the payload pointers is set,
// selected by Protocol. Seq is the monotonic event id, the ordering authority
// (mirroring a2a.Header.Sequence); it starts at 1 on the Meta record.
type Record struct {
	Seq      uint64   `json:"seq"`
	Protocol Protocol `json:"protocol"`

	Meta       *MetaRecord       `json:"meta,omitempty"`
	Assistant  *AssistantRecord  `json:"assistant,omitempty"`
	User       *UserRecord       `json:"user,omitempty"`
	ToolResult *ToolResultRecord `json:"tool_result,omitempty"`
	Terminal   *TerminalRecord   `json:"terminal,omitempty"`
}

// MetaRecord frames a run. It carries no secrets: the fingerprint holds only a
// hash of the system prompt, never the prompt or credentials.
type MetaRecord struct {
	Version     int         `json:"version"`
	RunID       string      `json:"run_id"`
	Created     time.Time   `json:"created"`
	Fingerprint Fingerprint `json:"fingerprint"`
	// Prompt is the initial user prompt, from which Messages[0] is rebuilt.
	Prompt string `json:"prompt"`
	// Interactive marks a run started in chat (--chat) mode, so a resume knows to
	// reopen the input bar rather than making a fresh LLM call at a completed
	// boundary. Absent (false) on a one-shot or batch checkpoint run.
	Interactive bool `json:"interactive,omitempty"`
}

// AssistantRecord is one assistant turn, exactly as it was appended to the
// conversation (resp.ToParam()), so thinking blocks with signatures and
// server-side tool blocks are preserved verbatim for resume.
type AssistantRecord struct {
	Iteration  int64                  `json:"iteration"`
	Message    anthropic.MessageParam `json:"message"`
	StopReason string                 `json:"stop_reason,omitempty"`
	InTokens   int64                  `json:"in_tokens"`
	OutTokens  int64                  `json:"out_tokens"`
	// CacheReadTokens and CacheCreateTokens are the response's prompt-cache input
	// tiers, split out from InTokens (the uncached remainder). Additive and omitempty
	// so no schema version bump is needed: a pre-caching journal omits them and folds
	// as zero, which is correct (caching was off, so there were none).
	CacheReadTokens   int64 `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens int64 `json:"cache_create_tokens,omitempty"`
}

// UnmarshalJSON decodes an assistant record and then repairs any server-side tool
// search result the SDK cannot round-trip. The record is otherwise decoded with the
// standard rules; only Message is post-processed.
//
// A tool_search_tool_result block that reports a successful search carries its matched
// tool_references in a nested union (ToolSearchToolResultBlockParamContentUnion).
// anthropic-sdk-go v1.56.0 marshals that union correctly but never registers it with
// its JSON decoder the way it registers the sibling ToolResultBlockParamContentUnion,
// so decoding drops the tool_references (which the API marks required) and mis-selects
// the error variant of the union. Resending such a turn on resume is then rejected with
// a 400 (messages.N.content.M.tool_search_tool_result.content...). The journal wrote the
// block out correctly, so we re-read the tool_references from the raw JSON and rebuild
// the union. Remove this once the SDK registers the union (anthropics/anthropic-sdk-go
// PR 338 proposes a related fix but was still unmerged as of July 2026).
func (r *AssistantRecord) UnmarshalJSON(data []byte) error {
	// shadow strips the method set so the nested json.Unmarshal does the ordinary
	// struct decode rather than recursing back into this method.
	type shadow AssistantRecord
	var s shadow
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}
	*r = AssistantRecord(s)

	return repairToolSearchResults(&r.Message, data)
}

// repairToolSearchResults restores the tool_references dropped from every successful
// tool_search_tool_result block in msg. raw is the assistant record JSON the message was
// decoded from; its message.content array holds each block's original, correct JSON.
// Blocks are matched to their raw counterpart by position, which the decoder preserves
// (it corrupts the block in place, it does not drop or reorder blocks). An error-variant
// block already round-trips, so only the success variant (tool_search_tool_search_result)
// is rebuilt.
func repairToolSearchResults(msg *anthropic.MessageParam, raw []byte) error {
	var envelope struct {
		Message struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	err := json.Unmarshal(raw, &envelope)
	if err != nil {
		return err
	}

	for i := range msg.Content {
		block := msg.Content[i].OfToolSearchToolResult
		if block == nil {
			continue
		}
		if i >= len(envelope.Message.Content) {
			break
		}

		var rawBlock struct {
			Content struct {
				Type           string                              `json:"type"`
				ToolReferences []anthropic.ToolReferenceBlockParam `json:"tool_references"`
			} `json:"content"`
		}
		err = json.Unmarshal(envelope.Message.Content[i], &rawBlock)
		if err != nil {
			return err
		}
		if rawBlock.Content.Type != "tool_search_tool_search_result" {
			continue
		}

		// Rebuild the whole content union: the decoder left the error variant set and
		// the success variant nil, so patching a field in place is not enough. Type is
		// left as its zero value, which marshals to "tool_search_tool_search_result"
		// via the SDK's default tag, matching how resp.ToParam() builds the block.
		block.Content = anthropic.ToolSearchToolResultBlockParamContentUnion{
			OfRequestToolSearchToolSearchResultBlock: &anthropic.ToolSearchToolSearchResultBlockParam{
				ToolReferences: rawBlock.Content.ToolReferences,
			},
		}
	}

	return nil
}

// UserRecord is a free-standing interactive user turn (a chat follow-up). Message
// carries only the newly typed block(s), never a merged view of a preceding user
// turn: when a follow-up folds into a dangling user message at runtime (the prior
// turn errored before replying), the journal still records the follow-up on its own
// and Fold reconstructs the fold by merging consecutive user messages. Recording the
// merged message instead would double the tool_result blocks Fold already appended.
type UserRecord struct {
	Message anthropic.MessageParam `json:"message"`
}

// ToolResultRecord is the result of a single tool call, keyed by the tool_use id
// it answers. Remote marks a call dispatched to another agent over A2A.
type ToolResultRecord struct {
	ToolUseID string                           `json:"tool_use_id"`
	Result    anthropic.ContentBlockParamUnion `json:"result"`
	Remote    bool                             `json:"remote,omitempty"`
}

// TerminalReason explains why a run stopped.
type TerminalReason string

const (
	// ReasonCompleted means the agent returned a final answer.
	ReasonCompleted TerminalReason = "completed"
	// ReasonSuspended means the run was checkpointed and exited to be resumed.
	ReasonSuspended TerminalReason = "suspended"
	// ReasonError means the run ended on an error.
	ReasonError TerminalReason = "error"
	// ReasonBudget means the token budget was exhausted.
	ReasonBudget TerminalReason = "budget"
	// ReasonMaxIterations means the iteration cap was reached.
	ReasonMaxIterations TerminalReason = "max_iterations"
)

// TerminalRecord records the end of a run.
type TerminalRecord struct {
	Reason  TerminalReason `json:"reason"`
	Message string         `json:"message,omitempty"`
}
