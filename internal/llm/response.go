//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package llm

// StopReason is the neutral reason a model turn ended. The values mirror the
// vocabulary Anthropic reports and the a2a.StopReason seed; a provider codec maps
// its own strings onto these.
type StopReason string

const (
	// StopEndTurn is a natural end to the turn with a final answer.
	StopEndTurn StopReason = "end_turn"
	// StopMaxTokens means the output token cap was hit; the turn may be truncated
	// and any trailing tool_use is not safe to execute.
	StopMaxTokens StopReason = "max_tokens"
	// StopToolUse means the model asked to run one or more tools.
	StopToolUse StopReason = "tool_use"
	// StopPauseTurn means the model paused a long-running turn it intends to
	// continue on the next call.
	StopPauseTurn StopReason = "pause_turn"
	// StopRefusal means the model declined to answer.
	StopRefusal StopReason = "refusal"
	// StopStopSequence means a configured stop sequence was reached.
	StopStopSequence StopReason = "stop_sequence"
)

// Usage is the token accounting for one call, split into the tiers the agent
// meters: uncached input, output, and the two prompt-cache input tiers.
type Usage struct {
	In          int64
	Out         int64
	CacheRead   int64
	CacheCreate int64
}

// Response is a model's reply to a call: the assistant turn's content, why it
// stopped, and what it cost.
type Response struct {
	Content    []ContentBlock
	StopReason StopReason
	Usage      Usage
}
