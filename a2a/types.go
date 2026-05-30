//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

// StopReason is the neutral reason a task finished, carried by Result and
// ErrorMessage.
type StopReason string

const (
	StopEndTurn         StopReason = "end_turn"
	StopMaxTokens       StopReason = "max_tokens"
	StopRefusal         StopReason = "refusal"
	StopCanceled        StopReason = "canceled"
	StopError           StopReason = "error"
	StopBudgetExhausted StopReason = "budget_exhausted"
)

// Usage reports token accounting. The fields mirror the agent's own accounting
// of input and output tokens.
type Usage struct {
	InputTokens  int64 `json:"input_tokens,omitempty"`
	OutputTokens int64 `json:"output_tokens,omitempty"`
}

// Budget bounds how much an agent may spend serving a request. The receiver's
// local configuration is the ceiling; a request may only lower a limit.
type Budget struct {
	MaxTokens     int64  `json:"max_tokens,omitempty"`
	MaxIterations int64  `json:"max_iterations,omitempty"`
	CallTimeout   string `json:"call_timeout,omitempty"`
}

// ExecResult is optional command metadata attached to a tool result when the
// tool was a shell command. It is absent for non-shell tools.
type ExecResult struct {
	Command   string `json:"command,omitempty"`
	ExitCode  int    `json:"exit_code,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ToolResult is the outcome of a tool invocation. It is shared by the streamed
// ToolResultBlock and the direct ToolReply so a tool result has one shape
// regardless of how it is delivered. IsError reports a harness failure (the tool
// did not run cleanly), distinct from a non-zero command exit reported in Exec.
type ToolResult struct {
	IsError bool        `json:"is_error,omitempty"`
	Output  string      `json:"output,omitempty"`
	Exec    *ExecResult `json:"exec,omitempty"`
}
