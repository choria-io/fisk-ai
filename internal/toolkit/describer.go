//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import "encoding/json"

// Presentation is how a tool's call is shown to the operator and accounted by the
// runner. A tool declares its own through Describe, so the runner traces and
// accounts a call without importing the tool's package or switching on its concrete
// type.
type Presentation int

const (
	// PresentCommand is an application command tool: its call is traced with the
	// resolved command line (Display) and a middle-elided short form (DisplayShort).
	PresentCommand Presentation = iota
	// PresentRemote is a tool served by another agent: its call is traced by naming
	// that agent (Agent) and is accounted as a remote call.
	PresentRemote
	// PresentSelfRendered is an in-process tool that renders its own operator
	// interaction (the human-in-the-loop tools), so its call and result are not
	// traced except under verbose, to avoid duplicating what the tool itself shows.
	PresentSelfRendered
	// PresentTraced is an in-process tool that has no operator interaction of its own
	// (the memory and knowledge tools), so it is traced like a command: Display holds
	// its call line and its result is shown.
	PresentTraced
)

// CallInfo is a tool's own description of one call: how to present it and which
// per-run dependencies it needs. The runner obtains it through Describe instead of
// switching on the concrete tool type, so a new tool kind carries its own
// presentation and dependency needs rather than teaching the runner about itself.
type CallInfo struct {
	// Present is how the call is shown and accounted for visibility (renderer
	// suppression and call-text).
	Present Presentation
	// Kind is the provider of the tool, the accounting axis and the value behind the
	// kind= log token. It is independent of Present: a built-in may present as
	// self-rendered or traced yet is one Kind. The zero value KindUnknown is the safe
	// sentinel for a tool that does not declare a provider.
	Kind Kind
	// Display is the full one-line call trace, already sanitized for terminal
	// display; an empty string suppresses the line.
	Display string
	// DisplayShort is an abbreviated trace with long argument values middle-elided,
	// already sanitized; an empty string means fall back to Display. Only a command
	// tool produces one.
	DisplayShort string
	// Agent is the identity of the remote agent serving the call, set only when
	// Present is PresentRemote. It may be empty when the serving agent is not named,
	// so it is never itself the signal that a call is remote.
	Agent string
	// NeedsPrompter asks the runner to supply the operator Prompter in ExecDeps.
	NeedsPrompter bool
	// NeedsWorkDir asks the runner to supply the per-run WorkDir in ExecDeps.
	NeedsWorkDir bool
}

// Describer is implemented by a tool that describes its own presentation and per-run
// dependency needs, so the runner traces and accounts a call of any kind uniformly,
// without a concrete-type switch. A tool that does not implement it is run and
// traced by name alone, with no dependencies and not as a remote call.
type Describer interface {
	// Describe returns the presentation and dependency needs for a call with the
	// given input. It must not run the tool or mutate state; it is called to build
	// the call trace before execution.
	Describe(input json.RawMessage) CallInfo
}
