//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

// Kind identifies which provider supplies a tool: the wrapped application, the
// harness itself, another agent, or the embedding caller. It is the accounting axis
// and the value behind the machine-readable kind= log token, kept deliberately
// distinct from Presentation, which is the visibility axis (how a call is shown and
// suppressed). A single provider can present in more than one way: a Builtin tool
// self-renders for the human-in-the-loop tools and is traced for the memory and
// knowledge tools. Accounting therefore keys off Kind and suppression off
// Presentation, and neither is ever derived from the other.
type Kind int

const (
	// KindUnknown is the zero value and the safe sentinel: a tool that never declared
	// a provider, or a call to a name that is not in the registry, is accounted here.
	// It surfaces as kind=unknown rather than silently masquerading as a real
	// provider, so a forgotten assignment is visible in the accounting.
	KindUnknown Kind = iota
	// KindApplication is a tool of the wrapped application: a fisk command tool.
	KindApplication
	// KindBuiltin is a tool the harness provides itself, in-process: the
	// human-in-the-loop, memory, and knowledge tools. Their differing presentation is
	// a Presentation concern, not a Kind: they are all one provider.
	KindBuiltin
	// KindRemote is a tool served by another agent over a2a.
	KindRemote
	// KindCustom is a tool supplied by the embedding caller through the run's
	// custom tools.
	KindCustom
)

// String returns the stable, lowercase, machine-readable token for a Kind, used for
// the kind= log token and as the key of the per-kind accounting map in the JSON
// trace. The tokens are part of the log and trace contract, so they are stable
// across releases. An unrecognized value formats as the KindUnknown token so a new
// Kind added without a token here is visible rather than blank.
func (k Kind) String() string {
	switch k {
	case KindApplication:
		return "application"
	case KindBuiltin:
		return "builtin"
	case KindRemote:
		return "remote"
	case KindCustom:
		return "custom"
	default:
		return "unknown"
	}
}
