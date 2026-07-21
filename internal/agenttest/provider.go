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

	"github.com/choria-io/fisk-ai/internal/llm"
)

// ScriptedProvider is an llm.Provider that returns a fixed queue of responses in
// order, one per Call, so a test drives the agent loop deterministically without a
// live backend. It is handed to a run through agent.Options.Provider, so nothing is
// registered in the global llm registry and each run owns its own provider with no
// shared state to isolate. A Call past the end of the script returns an error, which
// surfaces as a failed run the test's completion assertion catches.
type ScriptedProvider struct {
	caps llm.Caps

	mu        sync.Mutex
	responses []*llm.Response
	idx       int
	requests  []llm.Request
}

// NewScriptedProvider builds a provider that answers successive calls with the given
// responses. Its declared capabilities default to an anthropic-shaped provider with
// tool search; override them with SetCapabilities before the run when a spec needs
// different behavior.
func NewScriptedProvider(tb testing.TB, responses ...*llm.Response) *ScriptedProvider {
	tb.Helper()

	for i, r := range responses {
		if r == nil {
			tb.Fatalf("agenttest: NewScriptedProvider response %d is nil", i)
		}
	}

	return &ScriptedProvider{
		caps:      llm.Caps{Provider: "anthropic", SupportsToolSearch: true},
		responses: responses,
	}
}

// SetCapabilities overrides the capabilities the provider declares, for a spec that
// exercises capability-dependent behavior (tool-search degradation, a provider name
// a checkpoint fingerprint pins).
func (p *ScriptedProvider) SetCapabilities(caps llm.Caps) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.caps = caps
}

// Call records the request and returns the next scripted response.
func (p *ScriptedProvider) Call(_ context.Context, req llm.Request) (*llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requests = append(p.requests, req)
	if p.idx >= len(p.responses) {
		return nil, fmt.Errorf("agenttest: ScriptedProvider exhausted: call %d exceeds %d scripted responses", p.idx+1, len(p.responses))
	}

	resp := p.responses[p.idx]
	p.idx++

	return resp, nil
}

// Capabilities reports the declared capability set.
func (p *ScriptedProvider) Capabilities() llm.Caps {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.caps
}

// Requests returns a copy of every request the provider was called with, in order,
// so a spec can assert what the loop sent (the tools offered, the messages built
// from tool results).
func (p *ScriptedProvider) Requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := make([]llm.Request, len(p.requests))
	copy(out, p.requests)

	return out
}

// TextResponse is a terminal assistant turn carrying a single text block: the
// simplest completing reply, ending the run with ReasonCompleted.
func TextResponse(text string) *llm.Response {
	return &llm.Response{
		StopReason: llm.StopEndTurn,
		Content:    []llm.ContentBlock{{Text: &llm.TextBlock{Text: text}}},
	}
}

// ToolUseResponse is an assistant turn asking to run one tool: the loop executes it,
// feeds the result back as a user turn, and calls the provider again for the next
// scripted response.
func ToolUseResponse(id, name string, input json.RawMessage) *llm.Response {
	return &llm.Response{
		StopReason: llm.StopToolUse,
		Content:    []llm.ContentBlock{{ToolUse: &llm.ToolUseBlock{ID: id, Name: name, Input: input}}},
	}
}
