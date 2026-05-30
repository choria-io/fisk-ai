//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package a2anats is the NATS binding for the fisk-ai a2a protocol. It carries
// the request-reply messages used to import and export tools between agents:
// discovery (an agent describes itself) and direct tool invocation. The streaming
// task flow is not part of this binding yet.
//
// Subjects are routing only. The server never infers a message's meaning from the
// subject it arrived on; every message is self-describing through its
// Header.Protocol id and is dispatched on that. Each subject does, however, carry
// a single, fixed message type, enforced by its handler: a subject rejects any
// message that is not the one it is contracted to carry. That contract exists for
// NATS permission seams (granting discovery without granting tool execution) and
// to detect misbehaving clients; it is an artifact of this binding and is not
// relied on by the protocol layer, which is why wrapping the same bodies in the
// Choria Protocol later (with its own subject space) needs no change here.
package a2anats

import (
	"errors"
	"fmt"

	"github.com/choria-io/fisk-ai/a2a"
)

// SubjectPrefix namespaces every a2a NATS subject. It sits inside the existing
// Choria subject space.
const SubjectPrefix = "choria.fisk-ai"

var (
	// ErrAgentUnavailable indicates no agent answered on the expected subject
	// (no responder, or the request deadline elapsed).
	ErrAgentUnavailable = errors.New("remote agent unavailable")
	// ErrToolImport indicates a remote agent answered discovery but its tools
	// could not be imported (an unusable reply, an invalid schema).
	ErrToolImport = errors.New("remote tool import failed")
	// ErrProtocolMismatch indicates a message arrived on a subject that does not
	// carry its protocol id, or a reply was not the expected type.
	ErrProtocolMismatch = errors.New("unexpected message protocol")
)

// DiscoverySubject is the subject an agent with the given identity answers
// discovery requests on. It carries only discovery.request messages.
func DiscoverySubject(identity string) string {
	return fmt.Sprintf("%s.discovery.%s", SubjectPrefix, identity)
}

// ToolSubject is the subject an agent with the given identity answers tool
// invocation requests on. It carries only tool.request messages.
func ToolSubject(identity string) string {
	return fmt.Sprintf("%s.tool.%s", SubjectPrefix, identity)
}

// maxMessageSize bounds a single a2a body on the wire, kept under the NATS
// default 1 MiB max payload with room for transport framing. It caps both a
// discovery reply (a large command tree with per-tool schemas) and a tool reply.
const maxMessageSize = 768 * 1024

// expectProtocol decodes a raw body, confirms its protocol id is the one the
// receiving subject is contracted to carry, and returns the decoded message. A
// mismatch (a tool request arriving on the discovery subject, a malformed body)
// is reported as an error so the handler can fail closed rather than guess; this
// is the per-subject type contract, not an inference of meaning from the subject.
func expectProtocol(data []byte, want string) (any, error) {
	msg, err := a2a.Decode(data)
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
func headerOf(msg any) *a2a.Header {
	switch m := msg.(type) {
	case *a2a.Request:
		return &m.Header
	case *a2a.Event:
		return &m.Header
	case *a2a.Result:
		return &m.Header
	case *a2a.ErrorMessage:
		return &m.Header
	case *a2a.Cancel:
		return &m.Header
	case *a2a.Ack:
		return &m.Header
	case *a2a.ToolRequest:
		return &m.Header
	case *a2a.ToolReply:
		return &m.Header
	case *a2a.DiscoveryRequest:
		return &m.Header
	case *a2a.DiscoveryReply:
		return &m.Header
	default:
		return nil
	}
}
