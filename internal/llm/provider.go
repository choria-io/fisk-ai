//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package llm

import "context"

// Provider is one model backend. It turns a neutral Request into a neutral
// Response, translating to and from its own wire format internally, and reports
// the capabilities that shape how a request must be built for it. A Provider is
// the single seam the agent loop calls: it is the only place a concrete SDK is
// spoken on the request path.
type Provider interface {
	// Call issues one model request and returns the assistant turn, why it stopped,
	// and what it cost. It owns the wire call end to end, including any per-call
	// timeout, so the caller hands it a neutral value and gets a neutral value back.
	Call(ctx context.Context, req Request) (*Response, error)

	// Capabilities reports the provider's declared capabilities. They are declared,
	// not discovered: neither Anthropic nor OpenAI expose capability flags at runtime,
	// so a provider states them from static knowledge of its backend.
	Capabilities() Caps
}

// Caps is a provider's declared capability set. It is deliberately small; it grows
// as a second provider makes a real capability difference concrete rather than
// predicted.
type Caps struct {
	// Provider is the neutral provider id, the value stamped into the run fingerprint
	// so a resume against a different provider is refused.
	Provider string

	// SupportsToolSearch reports whether the provider offers server-side tool search
	// (deferred tool loading). A provider without it must send every tool directly.
	SupportsToolSearch bool

	// MaxOutputTokens is the provider's ceiling on a single response, or 0 when it is
	// not known or not enforced.
	MaxOutputTokens int64
}
