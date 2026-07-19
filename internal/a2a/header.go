//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import "time"

// Identity names an agent. Name is the logical identity (it maps to the agent's
// configured identity) and Instance optionally identifies one running instance
// of that named agent.
type Identity struct {
	Name     string `json:"name"`
	Instance string `json:"instance,omitempty"`
}

// Header carries the framing fields shared by every message. It is embedded into
// each message type so the fields marshal flat into the body, keeping a captured
// message self describing without the transport.
type Header struct {
	// Protocol is the message protocol id, e.g. RequestProtocol.
	Protocol string `json:"protocol"`
	// ID uniquely identifies this message.
	ID string `json:"id"`
	// Request is the correlation tag of the originating request; every reply in
	// the set echoes it. On a request message it equals ID.
	Request string `json:"request"`
	// Conversation is stable across multiple requests in a session.
	Conversation string `json:"conversation"`
	// Parent is the request that spawned this one, for multi-hop A->B->C. It is
	// empty for a top level request.
	Parent string `json:"parent,omitempty"`
	// Sequence is the per-request, gap-free, monotonic ordering authority. It is
	// never reused across a restart and is authoritative over Time for ordering.
	Sequence uint64 `json:"sequence"`
	// Time is advisory and for audit only; it is not an ordering authority.
	Time time.Time `json:"time"`
	// Sender identifies the agent that produced the message.
	Sender Identity `json:"sender"`
	// Recipient optionally identifies the intended agent.
	Recipient *Identity `json:"recipient,omitempty"`
	// MustUnderstand, when true, requires a receiver that does not understand the
	// protocol id to fail closed rather than ignore the message.
	MustUnderstand bool `json:"must_understand,omitempty"`
}
