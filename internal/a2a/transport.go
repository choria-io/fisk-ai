//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import "context"

// RouteHint tells a transport which of an agent's request-reply paths a message
// belongs to, so it can route it (e.g. a NATS transport picks the subject). It is
// ROUTING ONLY: the meaning of a message is always dispatched from its
// Header.Protocol id by the engine, never inferred from this hint. A transport may
// use it to keep the paths on separate channels as a permission seam, but must not
// treat it as the message's type.
type RouteHint int

const (
	// OpDiscovery is the path an agent answers discovery requests on.
	OpDiscovery RouteHint = iota
	// OpTool is the path an agent answers direct tool invocation requests on.
	OpTool
)

// Transport is the pluggable binding the a2a engine rides on. One implementation
// exists per wire binding (NATS today, Choria services later); it is selected from
// the registry by name and constructed from a shared conns.Provider. The engine
// owns all message validation and back-pressure; a Transport only moves bytes and
// keeps the routing paths separate.
type Transport interface {
	// RoundTrip sends body to agent on the op path and returns the raw reply. It
	// returns ErrAgentUnavailable when no agent answers or the deadline elapses. The
	// engine validates and size-caps the reply; the transport must not decode it.
	RoundTrip(ctx context.Context, agent string, op RouteHint, body []byte) ([]byte, error)
	// Serve registers h as the handler for inbound messages on the op path for this
	// transport's own identity. The transport must invoke h synchronously on its
	// per-path serving goroutine so the engine's semaphore, acquired inside h, back-
	// pressures intake. It may be called once per op.
	Serve(op RouteHint, h Handler) error
	// Describe returns transport-neutral {label, value} lines describing how the
	// named identity is reached, for display by the CLI (e.g. the NATS subjects).
	Describe(identity string) []DescLine
	// Close releases the transport's own resources (e.g. its service registration).
	// It does not close the shared conns.Provider, which the caller owns.
	Close() error
}

// Handler processes one inbound message body and answers through reply. It is
// invoked synchronously on the transport's serving goroutine; the engine may
// acquire a semaphore and spawn a worker inside it. reply stays valid for use from
// that worker after Handler returns.
type Handler func(ctx context.Context, body []byte, reply Replier)

// Replier is the reply side of one inbound request, the transport-neutral form of
// a NATS micro request's reply. It targets only the reply inbox the transport
// supplied for this request, never an identity taken from the message body, and is
// single-shot: exactly one of Respond or Error is called per request. It stays
// valid for use from a worker goroutine spawned by the handler after the handler
// returns.
type Replier interface {
	// Respond sends a successful reply body.
	Respond(body []byte) error
	// Error reports a transport-level handler failure with a code and description,
	// distinct from an in-band application error carried in a normal reply body.
	Error(code, description string) error
}

// DescLine is one {label, value} row describing how an identity is reached, for
// CLI display. It is transport-neutral: a NATS transport fills it with subjects, a
// later transport with whatever addresses it.
type DescLine struct {
	Label string
	Value string
}
