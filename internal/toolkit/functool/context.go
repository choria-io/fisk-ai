//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package functool

import "github.com/choria-io/fisk-ai/internal/toolkit"

// CallContext is the per-call surface a Handler runs against. It exposes the two
// per-run dependencies a function tool may need, the operator Prompter and the
// working directory, without handing the handler the raw toolkit.ExecDeps, so the
// handler surface stays independent of the toolkit's internal wiring.
type CallContext struct {
	workDir  string
	prompter toolkit.Prompter
}

// WorkDir is the per-run working directory a tool should use for relative-path work,
// so concurrent runs sharing one process do not collide. It is empty when the caller
// supplied none, which inherits the process working directory. It confines nothing.
func (c *CallContext) WorkDir() string { return c.workDir }

// Prompter is the operator interaction path. It is never nil: when no prompter was
// wired (a non-agent caller, or a path with no operator) it is
// toolkit.DefaultDenyPrompter, so a handler that prompts fails closed rather than
// panicking.
func (c *CallContext) Prompter() toolkit.Prompter {
	if c.prompter == nil {
		return toolkit.DefaultDenyPrompter()
	}

	return c.prompter
}
