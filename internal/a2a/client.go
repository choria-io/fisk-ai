//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"fmt"
)

// Client performs a2a request-reply interactions over a Transport: discovering a
// remote agent and invoking its tools directly. It is the consumer side of the
// protocol, used to import remote tools. All message validation (outgoing request,
// incoming reply size cap and schema) lives here in the engine; the Transport only
// moves bytes.
type Client struct {
	transport Transport
	sender    string
	validator *Validator
}

// NewClient wraps a Transport as a Client. sender is this agent's identity, set as
// the Header.Sender on outgoing requests. The Transport is borrowed: the caller
// established it (and the Provider behind it) and closes them.
func NewClient(transport Transport, sender string) (*Client, error) {
	validator, err := NewValidator()
	if err != nil {
		return nil, fmt.Errorf("building message validator: %w", err)
	}

	return &Client{transport: transport, sender: sender, validator: validator}, nil
}

// Discover asks the named agent to describe itself and returns its agent card.
// ErrAgentUnavailable is returned when no agent answers.
func (c *Client) Discover(ctx context.Context, agent string) (*AgentCard, error) {
	req := NewDiscoveryRequest()
	stampRequest(&req.Header, c.sender, agent)

	reply, err := c.roundTrip(ctx, agent, OpDiscovery, req, DiscoveryReplyProtocol)
	if err != nil {
		return nil, err
	}

	dr, ok := reply.(*DiscoveryReply)
	if !ok {
		return nil, fmt.Errorf("%w: discovery reply had unexpected type %T", ErrProtocolMismatch, reply)
	}

	return &dr.AgentCard, nil
}

// InvokeTool calls a single tool on the named agent and returns its reply. A
// failed or denied call is reported in-band on the ToolReply (IsError set), not
// as a Go error; a Go error means the call could not be made or answered.
func (c *Client) InvokeTool(ctx context.Context, agent, tool string, input json.RawMessage) (*ToolReply, error) {
	req := NewToolRequest(tool, normalizeInput(input))
	stampRequest(&req.Header, c.sender, agent)

	reply, err := c.roundTrip(ctx, agent, OpTool, req, ToolReplyProtocol)
	if err != nil {
		return nil, err
	}

	tr, ok := reply.(*ToolReply)
	if !ok {
		return nil, fmt.Errorf("%w: tool reply had unexpected type %T", ErrProtocolMismatch, reply)
	}

	return tr, nil
}

// roundTrip validates and sends a request over the transport, then returns the
// reply decoded once it passes the size cap, the schema, and the expected protocol
// id. A missing responder or an elapsed deadline is surfaced by the transport as
// ErrAgentUnavailable; an unusable reply is ErrToolImport.
func (c *Client) roundTrip(ctx context.Context, agent string, op RouteHint, req any, wantReply string) (any, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	err = c.validator.Validate(data)
	if err != nil {
		return nil, fmt.Errorf("invalid outgoing request: %w", err)
	}

	reply, err := c.transport.RoundTrip(ctx, agent, op, data)
	if err != nil {
		return nil, err
	}

	if len(reply) > maxMessageSize {
		return nil, fmt.Errorf("%w: reply exceeds %d bytes", ErrToolImport, maxMessageSize)
	}

	err = c.validator.Validate(reply)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid reply: %w", ErrToolImport, err)
	}

	return expectProtocol(reply, wantReply)
}

// normalizeInput drops an empty or explicit-null tool input. The tool.request
// schema requires input to be a JSON object when present, while the model may
// emit null or nothing for a no-argument tool; omitting it keeps such a request
// valid, and the server treats an absent input as an empty object.
func normalizeInput(input json.RawMessage) json.RawMessage {
	if len(input) == 0 || string(input) == "null" {
		return nil
	}

	return input
}
