//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// These live in the external agent_test package alongside the examples: they drive
// only agent's exported API, asserting the precedence rule for the injectable memory
// and session seams. Each injects a store while the config also names a non-default
// (jetstream) backend and runs with no broker reachable, so the failure it catches is
// the conflict error, not "connecting to NATS". Because the dial gating runs before
// the conflict checks and skips dialing for an injected seam, that these fail on the
// conflict rather than the dial is also the memory and session skip-dial proof.
package agent_test

import (
	"context"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/agenttest"
)

// TestInjection_MemoryStoreConflict asserts injecting a memory store while the config
// also selects a non-default memory backend fails at run start with the conflict
// error. The jetstream backend would otherwise force a dial; that the run instead
// reaches and returns the conflict, with no broker reachable, shows the dial was
// skipped for the injected store.
func TestInjection_MemoryStoreConflict(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	cfg := agenttest.Config(t, app)
	cfg.Harness.Memory = &config.MemoryConfig{Enabled: true, Backend: "jetstream"}

	_, err := agent.Run(context.Background(), agent.Options{
		Config:      cfg,
		ConfigFile:  "agent.yaml",
		Prompt:      []string{"go"},
		Provider:    agenttest.NewScriptedProvider(t, agenttest.TextResponse("done")),
		MemoryStore: agenttest.NewFakeMemoryStore(t),
	}, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))

	g.Expect(err).To(HaveOccurred())
	g.Expect(err).To(MatchError(ContainSubstring("Options.MemoryStore was injected")))
	g.Expect(err).To(MatchError(ContainSubstring("harness.memory.backend")))
	g.Expect(err.Error()).NotTo(ContainSubstring("connecting to NATS"))
}

// TestInjection_SessionStoreConflict asserts the same for the session seam: an
// injected session store plus a non-default session backend fails at run start with
// the conflict error rather than a NATS error, proving the session skip-dial gating.
func TestInjection_SessionStoreConflict(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	cfg := agenttest.Config(t, app)
	cfg.Harness.Sessions = &config.SessionConfig{Backend: "jetstream"}

	_, err := agent.Run(context.Background(), agent.Options{
		Config:       cfg,
		ConfigFile:   "agent.yaml",
		Prompt:       []string{"go"},
		Provider:     agenttest.NewScriptedProvider(t, agenttest.TextResponse("done")),
		Checkpoint:   agent.Checkpoint{Enabled: true},
		SessionStore: agenttest.NewFakeSessionStore(t),
	}, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))

	g.Expect(err).To(HaveOccurred())
	g.Expect(err).To(MatchError(ContainSubstring("Options.SessionStore was injected")))
	g.Expect(err).To(MatchError(ContainSubstring("harness.sessions.backend")))
	g.Expect(err.Error()).NotTo(ContainSubstring("connecting to NATS"))
}
