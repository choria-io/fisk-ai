//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package conns is the single home for connection establishment and access. A
// connection is established once and handed to every backend through a Provider,
// so backends do not each dial their own. Today the a2a client and server consume
// it; the memory and session stores will follow the same way. A Choria connection
// manager already exposes Nats(), so it can back a Provider without adaptation.
package conns

import (
	"fmt"

	"github.com/nats-io/jsm.go/natscontext"
	"github.com/nats-io/nats.go"
)

// Provider gives a backend access to the shared connections by kind. A backend
// uses only the kind it needs and treats a nil result as "this kind was not
// provisioned", failing loudly rather than dereferencing it. A connection the
// Provider established (Connect) is owned and released by Close; a connection
// handed in (WithNats) is borrowed and must never be closed through the Provider.
type Provider struct {
	nats    *nats.Conn
	ownNats bool
}

// Option provisions a connection kind on a Provider. WithNats is the only kind
// today; a Choria kind will be added the same way, so a backend that needs it
// can ask for it without changing the backends that only need NATS.
type Option func(*Provider)

// WithNats provisions a borrowed core NATS connection: the caller retains
// ownership and the Provider's Close leaves it open.
func WithNats(nc *nats.Conn) Option {
	return func(p *Provider) {
		p.nats = nc
		p.ownNats = false
	}
}

// New builds a Provider from the given options.
func New(opts ...Option) *Provider {
	p := &Provider{}
	for _, opt := range opts {
		opt(p)
	}

	return p
}

// Connect establishes the shared core NATS connection from the named context and
// returns a Provider that owns it. connName identifies the connection to the
// server as "fisk-ai <connName>". The Provider owns the connection, so the caller
// must Close it when done.
func Connect(contextName, connName string) (*Provider, error) {
	nc, err := natscontext.Connect(contextName, nats.Name(fmt.Sprintf("fisk-ai %s", connName)))
	if err != nil {
		return nil, fmt.Errorf("connecting to NATS context %q: %w", contextName, err)
	}

	return &Provider{nats: nc, ownNats: true}, nil
}

// Nats returns the shared core NATS connection, or nil when no NATS connection
// was provisioned. The connection is shared: callers must never close it
// directly; release it through the owning Provider's Close.
func (p *Provider) Nats() *nats.Conn {
	if p == nil {
		return nil
	}

	return p.nats
}

// Close releases the connections the Provider established and owns, leaving any
// borrowed connection (provisioned with WithNats) open. It is safe to call more
// than once and on a nil Provider.
func (p *Provider) Close() {
	if p == nil {
		return
	}

	if p.ownNats && p.nats != nil {
		p.nats.Close()
		p.nats = nil
	}
}
