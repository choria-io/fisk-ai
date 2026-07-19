//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// maxCommandLineRunes caps the length of a resolved command line shown to the
// operator for confirmation. It is larger than maxPromptRunes because a real
// command with several flags is legitimately longer than a one-line question,
// and truncating it could hide the very arguments the operator is approving.
const maxCommandLineRunes = 2000

// SanitizeCommandLine makes a resolved command line safe to print to the
// operator's terminal. The command path is fixed, but its argument values come
// from the model, so the assembled line is untrusted display text: it is stripped
// of terminal escape sequences and control characters so a model-supplied value
// cannot rewrite or spoof what the operator sees, and capped at
// maxCommandLineRunes.
func SanitizeCommandLine(s string) string {
	return SanitizeForTerminal(s, maxCommandLineRunes)
}

// ConfirmGate enforces confirmation tags in the agent loop: a tool carrying
// ai:confirm or any operator-configured confirm tag must be approved before it
// runs. An "allow for the session" answer is remembered by tool name for the rest
// of the run, so a tool the operator has blessed is not asked about again,
// regardless of its arguments; the approval covers that one command, not every
// tool that happens to share its triggering tag.
//
// ConfirmGate is not safe for concurrent use. It is created once per run and used
// only from the single-goroutine agent loop; it must never be wired into the
// concurrent MCP path, which has no local operator and where confirmation is
// instead requested from the calling client through elicitation.
type ConfirmGate struct {
	// allow holds tool names the operator approved for the rest of the session.
	allow map[string]bool
	// prompter renders the approval request and reports the operator's choice. All
	// prompt and trace rendering lives behind it, so nothing writes to the raw
	// terminal while a full-screen Prompter owns the screen. The gate keeps the
	// default-deny policy; the prompter only reports a choice or an error.
	prompter toolkit.Prompter
}

// NewConfirmGate returns a ConfirmGate with an empty session allow list that puts
// its approval prompts to the given Prompter.
func NewConfirmGate(prompter toolkit.Prompter) *ConfirmGate {
	return &ConfirmGate{allow: map[string]bool{}, prompter: prompter}
}

// Approve decides whether a confirm-tagged command may run, putting the approval
// request to the prompter when needed. toolName is the cache
// key for a session-wide approval, commandPath is the human-readable command
// (e.g. "stream rm") used in messages, display is the sanitized command line
// shown in the approval prompt, and tag is the command tag that gated it (e.g.
// "ai:confirm" or "impact:rw"), named in the prompt so the operator sees why
// approval is being asked. The command's trace line is emitted by the caller for
// every tool that runs, so the gate itself renders nothing on approval.
//
// It returns true with an empty reason when the command may run. It returns false
// with a reason when it may not: the operator declined, or no interactive
// terminal was attached to ask one. A false result is authoritative; the reason
// is surfaced to the model via ConfirmDeniedResult.
func (g *ConfirmGate) Approve(ctx context.Context, toolName, commandPath, display, tag string) (bool, string) {
	if g.allow[toolName] {
		return true, ""
	}

	// Default-deny lives here at the caller, not in the prompter: with no terminal
	// there is no operator to ask, and a run canceled before the operator could
	// answer must resolve to a denial rather than block or run. Both are checked
	// before any prompt is shown so the prompter is never reached in a state where
	// its answer could not be trusted.
	if !StdinIsTerminal() {
		return false, NoTerminalReason
	}
	if err := ctx.Err(); err != nil {
		return false, fmt.Sprintf("the run ended before the operator could approve this command: %v; this decision is final, do not retry", err)
	}

	choice, err := g.prompter.ApproveCommand(ctx, toolkit.GateRequest{Command: commandPath, Display: display, Tag: tag})
	if err != nil {
		return false, fmt.Sprintf("the operator did not permit this command: %v; this decision is final, do not retry", err)
	}

	switch choice {
	case toolkit.ConfirmAlways:
		g.allow[toolName] = true
		return true, ""
	case toolkit.ConfirmOnce:
		return true, ""
	default:
		return false, "the operator declined to permit this command; this decision is final, do not retry"
	}
}

// confirmDeniedOutcome is the JSON result returned to the model when a
// confirm-tagged command was not permitted to run. It mirrors the human-in-the-
// loop outcomes: a normal (non-error) result the model should reason about, not a
// tool failure to route around.
type confirmDeniedOutcome struct {
	// Allowed is always false here; the command did not run.
	Allowed bool `json:"allowed"`
	// Reason explains why, so the model knows whether the operator declined or no
	// operator could be reached.
	Reason string `json:"reason,omitempty"`
}

// ConfirmDeniedResult builds the tool_result content block for a confirm-tagged
// command the gate did not permit. It is a non-error result so the model treats
// the refusal as authoritative rather than as a failure to work around.
func ConfirmDeniedResult(useID, reason string) anthropic.ContentBlockParamUnion {
	data, err := json.Marshal(confirmDeniedOutcome{Reason: reason})
	if err != nil {
		return anthropic.NewToolResultBlock(useID, `{"allowed":false}`, false)
	}

	return anthropic.NewToolResultBlock(useID, string(data), false)
}
