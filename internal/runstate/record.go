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
	"time"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// Version is the on-disk record format version, stamped into the Meta record.
// Fold accepts only this exact version: the record format is provider-neutral
// (llm.Message), and the earlier Anthropic-wire format does not round-trip
// through the neutral records, so any other version is rejected rather than
// silently mis-folded.
const Version = 3

// Protocol is the schema id of a Record, carried in the record body so a single
// stored record is self describing: given just one record you can find its
// origin schema, independent of the subject it arrived on or the store it was
// read from. The ids share the a2a product namespace under a ".session" segment
// so they never collide with the a2a wire protocols.
type Protocol string

// protocolNamespace is the a2a product namespace (a2a.ProtocolNamespace) under a
// ".session" segment. It is spelled out here rather than imported so the storage
// layer does not depend on the a2a package; it must track a2a.ProtocolNamespace
// if that changes.
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

// AssistantRecord is one assistant turn in the neutral model, so thinking blocks
// with signatures and provider server-side blocks are preserved verbatim for
// resume regardless of which provider produced them.
type AssistantRecord struct {
	Iteration  int64       `json:"iteration"`
	Message    llm.Message `json:"message"`
	StopReason string      `json:"stop_reason,omitempty"`
	InTokens   int64       `json:"in_tokens"`
	OutTokens  int64       `json:"out_tokens"`
	// CacheReadTokens and CacheCreateTokens are the response's prompt-cache input
	// tiers, split out from InTokens (the uncached remainder). Additive and omitempty
	// so no schema version bump is needed: a pre-caching journal omits them and folds
	// as zero, which is correct (caching was off, so there were none).
	CacheReadTokens   int64 `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens int64 `json:"cache_create_tokens,omitempty"`
}

// UserRecord is a free-standing interactive user turn (a chat follow-up). Message
// carries only the newly typed block(s), never a merged view of a preceding user
// turn: when a follow-up folds into a dangling user message at runtime (the prior
// turn errored before replying), the journal still records the follow-up on its own
// and Fold reconstructs the fold by merging consecutive user messages. Recording the
// merged message instead would double the tool_result blocks Fold already appended.
type UserRecord struct {
	Message llm.Message `json:"message"`
}

// ToolResultRecord is the result of a single tool call, keyed by the tool_use id
// it answers. Remote marks a call dispatched to another agent over A2A.
type ToolResultRecord struct {
	ToolUseID string              `json:"tool_use_id"`
	Result    llm.ToolResultBlock `json:"result"`
	Remote    bool                `json:"remote,omitempty"`
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
