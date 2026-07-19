//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"errors"
	"fmt"
)

var (
	// ErrAgentUnavailable indicates no agent answered the request (no responder,
	// or the request deadline elapsed). A transport returns it from RoundTrip.
	ErrAgentUnavailable = errors.New("remote agent unavailable")
	// ErrToolImport indicates a remote agent answered but its reply could not be
	// used (a reply over the size cap, an invalid or unexpected body).
	ErrToolImport = errors.New("remote tool import failed")
	// ErrProtocolMismatch indicates a decoded message did not carry the protocol id
	// the receiving path is contracted for, or a reply was not the expected type.
	ErrProtocolMismatch = errors.New("unexpected message protocol")
)

// maxMessageSize bounds a single a2a body on the wire. It is enforced in the
// engine on both inbound handler bodies and round-trip replies, before any decode
// or allocation. It is kept under the NATS default 1 MiB max payload with room for
// transport framing, and caps both a discovery reply (a large command tree with
// per-tool schemas) and a tool reply.
const maxMessageSize = 768 * 1024

// expectProtocol decodes a raw body, confirms its protocol id is the one the
// receiving path is contracted to carry, and returns the decoded message. A
// mismatch (a tool request arriving where a discovery request is expected, a
// malformed body) is reported as an error so the caller can fail closed rather
// than guess; this is the per-path type contract, not an inference of meaning from
// the transport.
func expectProtocol(data []byte, want string) (any, error) {
	msg, err := Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProtocolMismatch, err)
	}

	hdr := headerOf(msg)
	if hdr == nil {
		return nil, fmt.Errorf("%w: message carries no header", ErrProtocolMismatch)
	}
	if hdr.Protocol != want {
		return nil, fmt.Errorf("%w: got %q, want %q", ErrProtocolMismatch, hdr.Protocol, want)
	}

	return msg, nil
}

// headerOf returns the embedded Header of any decoded a2a message, or nil if the
// value is not one of the known message types.
func headerOf(msg any) *Header {
	switch m := msg.(type) {
	case *Request:
		return &m.Header
	case *Event:
		return &m.Header
	case *Result:
		return &m.Header
	case *ErrorMessage:
		return &m.Header
	case *Cancel:
		return &m.Header
	case *Ack:
		return &m.Header
	case *ToolRequest:
		return &m.Header
	case *ToolReply:
		return &m.Header
	case *DiscoveryRequest:
		return &m.Header
	case *DiscoveryReply:
		return &m.Header
	default:
		return nil
	}
}
