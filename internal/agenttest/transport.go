//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agenttest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/choria-io/fisk-ai/internal/a2a"
)

// FakeTransport is an a2a.Transport for tests: it answers discovery with a fixed
// agent card and every direct tool call with a fixed reply, over no wire, so a run
// can import and invoke remote tools through an injected transport with no broker
// reachable. It records how many round trips it served, so a test can assert the run
// went through the injected transport (and, since the fake never dials, that Run did
// not dial either). It is one of the separate-package fakes proving each injectable
// seam is implementable from outside its own package, and it is safe for the
// concurrent use runs sharing one transport make of it.
type FakeTransport struct {
	mu         sync.Mutex
	card       a2a.AgentCard
	toolOutput string
	toolIsErr  bool
	roundTrips int
	closeCalls int
	serveCalls int
}

// FakeTransport implements a2a.Transport; the assertion is the separate-package
// interface audit, failing to compile if the seam stops being implementable from
// outside its own package.
var _ a2a.Transport = (*FakeTransport)(nil)

// NewFakeTransport returns a transport that answers discovery with card. Tool calls
// answer with a success reply carrying "ok"; use SetToolReply to change it.
func NewFakeTransport(tb testing.TB, card a2a.AgentCard) *FakeTransport {
	tb.Helper()
	return &FakeTransport{card: card, toolOutput: "ok"}
}

// SetToolReply sets what every direct tool call answers with.
func (t *FakeTransport) SetToolReply(output string, isError bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.toolOutput = output
	t.toolIsErr = isError
}

// RoundTrips reports how many requests the transport answered, across discovery and
// tool calls.
func (t *FakeTransport) RoundTrips() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.roundTrips
}

// Closed reports whether Close was called. A borrowed transport must not be closed
// by Run, so a test asserts this stays false.
func (t *FakeTransport) Closed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closeCalls > 0
}

// RoundTrip implements a2a.Transport by answering discovery and tool requests from
// its fixed card and reply, echoing the request's correlation tags so the reply
// passes the engine's schema validation.
func (t *FakeTransport) RoundTrip(_ context.Context, agent string, op a2a.RouteHint, body []byte) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.roundTrips++

	var reqHdr a2a.Header
	err := json.Unmarshal(body, &reqHdr)
	if err != nil {
		return nil, fmt.Errorf("agenttest: FakeTransport could not decode request header: %w", err)
	}

	switch op {
	case a2a.OpDiscovery:
		reply := a2a.NewDiscoveryReply(t.card.Name, t.card.Version)
		reply.AgentCard = t.card
		t.stamp(&reply.Header, &reqHdr, agent)
		return json.Marshal(reply)
	case a2a.OpTool:
		reply := a2a.NewToolReply(t.toolOutput, t.toolIsErr)
		t.stamp(&reply.Header, &reqHdr, agent)
		return json.Marshal(reply)
	default:
		return nil, fmt.Errorf("agenttest: FakeTransport got unexpected op %v", op)
	}
}

// Serve implements a2a.Transport. The fake is a client transport only; Run never
// serves through the injected transport, so a call here is recorded for a test to
// catch rather than answered.
func (t *FakeTransport) Serve(a2a.RouteHint, a2a.Handler) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.serveCalls++
	return nil
}

// Describe implements a2a.Transport with no address lines.
func (t *FakeTransport) Describe(string) []a2a.DescLine { return nil }

// Close implements a2a.Transport. Run never closes a borrowed transport, so this is
// recorded for a test to assert it was not reached rather than releasing anything.
func (t *FakeTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeCalls++
	return nil
}

// stamp fills a reply header so it echoes the request it answers and validates
// against the message schema: a fresh id, the request's correlation and conversation
// tags, this agent as sender, and the original sender as recipient.
func (t *FakeTransport) stamp(h *a2a.Header, req *a2a.Header, sender string) {
	h.ID = a2a.NewID()
	h.Request = req.Request
	h.Conversation = req.Conversation
	h.Time = time.Now().UTC()
	h.Sender = a2a.Identity{Name: sender}
	if req.Sender.Name != "" {
		h.Recipient = &a2a.Identity{Name: req.Sender.Name}
	}
}
