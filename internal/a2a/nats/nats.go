//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package nats is the NATS transport binding for the fisk-ai a2a protocol. It
// carries the request-reply messages the a2a engine uses to import and export
// tools between agents: discovery (an agent describes itself) and direct tool
// invocation. The streaming task flow is not part of this binding yet.
//
// Subjects are routing only. The engine never infers a message's meaning from the
// subject it arrived on; every message is self-describing through its
// Header.Protocol id and is dispatched on that. Each subject does, however, carry a
// single, fixed message type: discovery and tool invocation ride separate subjects
// so a NATS permission seam can grant discovery without granting tool execution.
// That separation is an artifact of this binding and is not relied on by the
// protocol layer, which is why wrapping the same bodies in the Choria Protocol
// later (with its own subject space) needs no change to the engine.
//
// The package registers itself as the "nats" a2a transport in init, so a program
// links it in with a blank import.
package nats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/choria-io/fisk-ai/internal/a2a"
	"github.com/choria-io/fisk-ai/internal/conns"
)

func init() {
	a2a.RegisterTransport("nats", newTransport)
}

// SubjectPrefix namespaces every a2a NATS subject. It sits inside the existing
// Choria subject space.
const SubjectPrefix = "choria.fisk-ai"

// defaultRequestTimeout bounds a discovery or tool request when neither the
// caller's context nor the transport configuration carries a deadline. It keeps a
// dead or wedged remote from hanging a run indefinitely.
const defaultRequestTimeout = 30 * time.Second

// microServiceVersion is the SemVer stamped on the micro service registration.
// This is service metadata only; the agent card carries the agent's real,
// free-form version, which the engine builds. A fixed value keeps the transport
// out of the agent-versioning business.
const microServiceVersion = "0.0.0"

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

// options is the nats-specific transport options block. It has no fields yet; it
// exists so the factory can reject any unknown option strictly, surfacing an
// operator's mistake at construction.
type options struct{}

// Transport implements a2a.Transport over core NATS request-reply. It borrows the
// NATS connection from the shared Provider (it never closes it) and, on the serving
// side, registers a micro service whose endpoints map the discovery and tool
// subjects onto a2a handlers.
type Transport struct {
	nc       *nats.Conn
	identity string
	timeout  time.Duration
	svc      micro.Service
}

// newTransport is the registered factory. It borrows the Provider's NATS
// connection and returns an error when none was provisioned, so a misconfigured
// wiring fails loudly rather than dereferencing a nil connection.
func newTransport(p *conns.Provider, cfg a2a.TransportConfig) (a2a.Transport, error) {
	nc := p.Nats()
	if nc == nil {
		return nil, fmt.Errorf("a2a NATS transport requires a NATS connection but none was provisioned")
	}

	if len(cfg.Options) > 0 {
		dec := json.NewDecoder(bytes.NewReader(cfg.Options))
		dec.DisallowUnknownFields()
		var o options
		err := dec.Decode(&o)
		if err != nil {
			return nil, fmt.Errorf("decoding nats transport options: %w", err)
		}
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}

	return &Transport{nc: nc, identity: cfg.Identity, timeout: timeout}, nil
}

// RoundTrip publishes body on the subject for op against agent and returns the raw
// reply. A missing responder or an elapsed deadline is reported as
// a2a.ErrAgentUnavailable. The reply is returned undecoded; the engine size-caps
// and validates it.
func (t *Transport) RoundTrip(ctx context.Context, agent string, op a2a.RouteHint, body []byte) ([]byte, error) {
	subject, err := t.subject(agent, op)
	if err != nil {
		return nil, err
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.timeout)
		defer cancel()
	}

	msg, err := t.nc.RequestWithContext(ctx, subject, body)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: no reply on %q: %w", a2a.ErrAgentUnavailable, subject, err)
		}
		return nil, fmt.Errorf("requesting %q: %w", subject, err)
	}

	return msg.Data, nil
}

// Serve registers h as a micro endpoint on the subject for op under this
// transport's own identity. micro invokes the endpoint synchronously on its
// per-subscription goroutine, so when h blocks (the engine acquiring its
// semaphore) intake is back-pressured. The micro service is created on first use.
func (t *Transport) Serve(op a2a.RouteHint, h a2a.Handler) error {
	subject, err := t.subject(t.identity, op)
	if err != nil {
		return err
	}

	svc, err := t.service()
	if err != nil {
		return err
	}

	handler := micro.HandlerFunc(func(req micro.Request) {
		h(context.Background(), req.Data(), replier{req: req})
	})

	err = svc.AddEndpoint(endpointName(op), handler, micro.WithEndpointSubject(subject))
	if err != nil {
		return fmt.Errorf("registering %s endpoint: %w", endpointName(op), err)
	}

	return nil
}

// Describe returns the discovery and tool subjects the identity is reached on, for
// CLI display.
func (t *Transport) Describe(identity string) []a2a.DescLine {
	return []a2a.DescLine{
		{Label: "Discovery", Value: DiscoverySubject(identity)},
		{Label: "Tools", Value: ToolSubject(identity)},
	}
}

// Close stops the micro service and its subscriptions. It leaves the borrowed NATS
// connection open; the Provider that established it owns its lifecycle.
func (t *Transport) Close() error {
	if t.svc != nil {
		return t.svc.Stop()
	}

	return nil
}

// service lazily registers the micro service that backs the serving endpoints. It
// is created only when the transport is used to serve, so a client-only transport
// registers nothing.
func (t *Transport) service() (micro.Service, error) {
	if t.svc != nil {
		return t.svc, nil
	}

	svc, err := micro.AddService(t.nc, micro.Config{
		Name:        t.identity,
		Version:     microServiceVersion,
		Description: "fisk-ai a2a tool server",
		QueueGroup:  t.identity,
	})
	if err != nil {
		return nil, fmt.Errorf("registering a2a service: %w", err)
	}

	t.svc = svc

	return svc, nil
}

// subject maps a route hint to the NATS subject for the given identity.
func (t *Transport) subject(identity string, op a2a.RouteHint) (string, error) {
	switch op {
	case a2a.OpDiscovery:
		return DiscoverySubject(identity), nil
	case a2a.OpTool:
		return ToolSubject(identity), nil
	default:
		return "", fmt.Errorf("unknown a2a route hint %d", op)
	}
}

// endpointName is the micro endpoint name for a route hint.
func endpointName(op a2a.RouteHint) string {
	if op == a2a.OpTool {
		return "tool"
	}

	return "discovery"
}

// replier adapts a micro.Request's reply side to a2a.Replier. It targets only the
// reply inbox micro supplied for the request and stays valid after the handler
// returns, so the engine's worker goroutine can answer.
type replier struct {
	req micro.Request
}

func (r replier) Respond(body []byte) error {
	return r.req.Respond(body)
}

func (r replier) Error(code, description string) error {
	return r.req.Error(code, description, nil)
}
