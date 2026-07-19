//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2anats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/choria-io/fisk-ai/a2a"
	"github.com/choria-io/fisk-ai/internal/conns"
)

// defaultRequestTimeout bounds a discovery or tool request when the caller's
// context carries no deadline of its own. It keeps a dead or wedged remote from
// hanging a run indefinitely.
const defaultRequestTimeout = 30 * time.Second

// Client performs a2a request-reply interactions over NATS: discovering a remote
// agent and invoking its tools directly. It is the consumer side of the binding,
// used to import remote tools.
type Client struct {
	nc        *nats.Conn
	sender    string
	timeout   time.Duration
	validator *a2a.Validator
}

// NewClientFromProvider wraps the shared NATS connection carried by p as a
// Client. It borrows the connection: the Provider's owner establishes and closes
// it, so the Client never does. sender is this agent's identity, used as the
// Header.Sender on outgoing requests. It returns an error when p carries no NATS
// connection, so a misconfigured wiring fails loudly rather than dereferencing a
// nil connection.
func NewClientFromProvider(p *conns.Provider, sender string, timeout time.Duration) (*Client, error) {
	nc := p.Nats()
	if nc == nil {
		return nil, fmt.Errorf("a2a NATS client requires a NATS connection but none was provisioned")
	}

	return NewClient(nc, sender, timeout)
}

// NewClient wraps an existing NATS connection as a Client. It never takes
// ownership of the connection; tests use it to drive the client against a
// connection they manage, and NewClientFromProvider uses it for the shared one.
func NewClient(nc *nats.Conn, sender string, timeout time.Duration) (*Client, error) {
	validator, err := a2a.NewValidator()
	if err != nil {
		return nil, fmt.Errorf("building message validator: %w", err)
	}

	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}

	return &Client{nc: nc, sender: sender, timeout: timeout, validator: validator}, nil
}

// Discover asks the named agent to describe itself and returns its agent card.
// ErrAgentUnavailable is returned when no agent answers.
func (c *Client) Discover(ctx context.Context, agent string) (*a2a.AgentCard, error) {
	req := a2a.NewDiscoveryRequest()
	stampRequest(&req.Header, c.sender, agent)

	reply, err := c.roundTrip(ctx, DiscoverySubject(agent), req, a2a.DiscoveryReplyProtocol)
	if err != nil {
		return nil, err
	}

	dr, ok := reply.(*a2a.DiscoveryReply)
	if !ok {
		return nil, fmt.Errorf("%w: discovery reply had unexpected type %T", ErrProtocolMismatch, reply)
	}

	return &dr.AgentCard, nil
}

// InvokeTool calls a single tool on the named agent and returns its reply. A
// failed or denied call is reported in-band on the ToolReply (IsError set), not
// as a Go error; a Go error means the call could not be made or answered.
func (c *Client) InvokeTool(ctx context.Context, agent, tool string, input json.RawMessage) (*a2a.ToolReply, error) {
	req := a2a.NewToolRequest(tool, normalizeInput(input))
	stampRequest(&req.Header, c.sender, agent)

	reply, err := c.roundTrip(ctx, ToolSubject(agent), req, a2a.ToolReplyProtocol)
	if err != nil {
		return nil, err
	}

	tr, ok := reply.(*a2a.ToolReply)
	if !ok {
		return nil, fmt.Errorf("%w: tool reply had unexpected type %T", ErrProtocolMismatch, reply)
	}

	return tr, nil
}

// roundTrip validates and publishes a request, waits for the reply, and returns
// it decoded once it matches the expected protocol id. A missing responder or an
// elapsed deadline is reported as ErrAgentUnavailable.
func (c *Client) roundTrip(ctx context.Context, subject string, req any, wantReply string) (any, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	err = c.validator.Validate(data)
	if err != nil {
		return nil, fmt.Errorf("invalid outgoing request: %w", err)
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancelFn context.CancelFunc
		ctx, cancelFn = context.WithTimeout(ctx, c.timeout)
		defer cancelFn()
	}

	msg, err := c.nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: no reply on %q: %w", ErrAgentUnavailable, subject, err)
		}
		return nil, fmt.Errorf("requesting %q: %w", subject, err)
	}

	if len(msg.Data) > maxMessageSize {
		return nil, fmt.Errorf("%w: reply on %q exceeds %d bytes", ErrToolImport, subject, maxMessageSize)
	}

	err = c.validator.Validate(msg.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid reply on %q: %w", ErrToolImport, subject, err)
	}

	return expectProtocol(msg.Data, wantReply)
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
