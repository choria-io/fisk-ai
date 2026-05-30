//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package a2a defines the fisk-ai agent-to-agent (A2A) protocol message types.
//
// The protocol is transport agnostic: every message is a single, flat JSON
// object that carries all of its own framing in the body (see Header), so a
// captured message is fully self describing outside of any transport. The first
// transport binding is NATS JetStream, where a reply set is correlated by the
// Header.Request id and ordered by Header.Sequence; later bindings wrap the same
// body inside the Choria Protocol.
//
// Message bodies are versioned by their protocol id, e.g.
// "io.choria.fisk-ai.v1.request". The matching JSON schemas live under
// a2a/schemas/io.choria.fisk-ai.v1.
package a2a

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/segmentio/ksuid"
)

// ProtocolNamespace is the namespace and version shared by every v1 message id.
const ProtocolNamespace = "io.choria.fisk-ai.v1"

// Protocol ids, used as the value of the Header.Protocol field and as the
// discriminator consumed by Decode.
const (
	RequestProtocol = ProtocolNamespace + ".request"
	EventProtocol   = ProtocolNamespace + ".event"
	ResultProtocol  = ProtocolNamespace + ".result"
	ErrorProtocol   = ProtocolNamespace + ".error"
	CancelProtocol  = ProtocolNamespace + ".cancel"
	AckProtocol     = ProtocolNamespace + ".ack"

	// ToolRequestProtocol and ToolReplyProtocol carry a direct tool invocation
	// (request-reply), used to import or export tools between agents without
	// engaging the agentic loop.
	ToolRequestProtocol = ProtocolNamespace + ".tool.request"
	ToolReplyProtocol   = ProtocolNamespace + ".tool.reply"

	// DiscoveryRequestProtocol and DiscoveryReplyProtocol let an agent describe
	// itself (name, version, exposed tools) to others.
	DiscoveryRequestProtocol = ProtocolNamespace + ".discovery.request"
	DiscoveryReplyProtocol   = ProtocolNamespace + ".discovery.reply"
)

var (
	// ErrUnknownProtocol is returned by Decode for an unrecognized protocol id.
	ErrUnknownProtocol = errors.New("unknown protocol")
	// ErrUnknownBlockType is returned when decoding an event block with an
	// unrecognized type discriminator.
	ErrUnknownBlockType = errors.New("unknown block type")
	// ErrInvalidMessage is returned when a message cannot be represented on the
	// wire, e.g. an event block with no content.
	ErrInvalidMessage = errors.New("invalid message")
)

// NewID returns a new KSUID string, used to mint message, request and
// conversation ids. KSUIDs are k-sorted and unique; ordering within a reply set
// still relies on Header.Sequence, not on the id.
func NewID() string {
	return ksuid.New().String()
}

// Decode parses a raw message body and returns the concrete message type as a
// pointer (*Request, *Event, *Result, *ErrorMessage, *Cancel, *Ack, *ToolRequest,
// *ToolReply, *DiscoveryRequest or *DiscoveryReply), chosen by its protocol id.
// It returns ErrUnknownProtocol for an unrecognized id.
func Decode(data []byte) (any, error) {
	var probe struct {
		Protocol string `json:"protocol"`
	}

	err := json.Unmarshal(data, &probe)
	if err != nil {
		return nil, err
	}

	switch probe.Protocol {
	case RequestProtocol:
		return decodeInto(data, &Request{})
	case EventProtocol:
		return decodeInto(data, &Event{})
	case ResultProtocol:
		return decodeInto(data, &Result{})
	case ErrorProtocol:
		return decodeInto(data, &ErrorMessage{})
	case CancelProtocol:
		return decodeInto(data, &Cancel{})
	case AckProtocol:
		return decodeInto(data, &Ack{})
	case ToolRequestProtocol:
		return decodeInto(data, &ToolRequest{})
	case ToolReplyProtocol:
		return decodeInto(data, &ToolReply{})
	case DiscoveryRequestProtocol:
		return decodeInto(data, &DiscoveryRequest{})
	case DiscoveryReplyProtocol:
		return decodeInto(data, &DiscoveryReply{})
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProtocol, probe.Protocol)
	}
}

func decodeInto[T any](data []byte, msg *T) (*T, error) {
	err := json.Unmarshal(data, msg)
	if err != nil {
		return nil, err
	}

	return msg, nil
}
