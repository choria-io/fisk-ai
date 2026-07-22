//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	"context"
	"encoding/json"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// ExecDeps carries the per-run dependencies a tool may need to run a model
// tool_use. A kind that needs none ignores it; only the human-in-the-loop
// built-ins use the Prompter and only local command tools use the WorkDir.
type ExecDeps struct {
	// Prompter is the operator interaction path a built-in tool uses to reach a
	// person. It is nil for tools that never prompt.
	Prompter Prompter

	// WorkDir is the directory a local command tool runs in, so concurrent runs
	// sharing one process do not collide on relative-path writes. Empty inherits the
	// process working directory. It sets the child's directory only and confines
	// nothing.
	WorkDir string
}

// Tool is the model-facing contract every tool kind satisfies, however it runs:
// a local fisk command, an in-process built-in, or a tool invoked on a remote
// agent. The runner dispatches uniformly over this interface; kind-specific
// policy (confirmation, missing-argument checks, per-kind tracing) is exposed
// through narrow capability interfaces the runner consults, never folded in here.
type Tool interface {
	// Name is the model-facing tool name; it is unique within a run and is the key
	// the runner dispatches on.
	Name() string
	// Description is the model-facing description.
	Description() string
	// InputSchema is the JSON schema advertised to the model.
	InputSchema() map[string]any
	// Definition renders the tool as a provider-neutral definition. deferLoading asks
	// for the tool to be hidden behind tool search; a kind may decline it (a
	// built-in is never deferred, a tool tagged ai:no_defer opts out).
	Definition(deferLoading bool) llm.ToolDef
	// ExecuteUse runs the tool for a model tool_use block and returns the matching
	// tool result. A harness failure is reported as an error result; an outcome the
	// model should reason about (including a non-zero exit) is a normal result, so
	// the model sees every kind the same way.
	ExecuteUse(ctx context.Context, use llm.ToolUseBlock, deps ExecDeps) llm.ToolResultBlock
}

// Confirmable is implemented by the tool kinds that can require operator
// confirmation before running. Only local command tools do; the runner consults it
// to decide whether to gate a call and to render the approval prompt, then drives
// the confirm gate itself so the gate's per-run state stays out of the tool.
// Keeping the whole contract here lets the runner drive the gate without knowing
// the concrete tool type.
type Confirmable interface {
	// NeedsConfirm reports whether a call must be approved, given the operator's
	// extra confirm tags on top of the always-on ai:confirm.
	NeedsConfirm(extraTags []string) bool
	// ConfirmTrigger names the tag that gated the call, for the prompt.
	ConfirmTrigger(extraTags []string) string
	// Command is the bare command the call runs, shown in the approval prompt.
	Command() string
	// TraceLine is the full command line for these arguments, shown in the prompt
	// so the operator approves exactly what will run.
	TraceLine(input json.RawMessage) string
}

// ArgumentValidator is implemented by the tool kinds that can pre-validate a
// model's input against required parameters before running. Only local command
// tools do; the runner rejects a structurally invalid call before the confirm gate
// and before execution, so the operator is never asked to approve an incomplete
// call and nothing runs that would fail only on its own exit.
type ArgumentValidator interface {
	// MissingRequired returns the required parameters absent from input, or nil
	// when the call is complete.
	MissingRequired(input json.RawMessage) []string
	// MissingRequiredMessage is the result returned to the model naming the missing
	// parameters so it can correct and retry.
	MissingRequiredMessage(missing []string) string
}
