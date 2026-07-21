//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agenttest

import (
	"testing"

	"github.com/choria-io/fisk-ai/config"
)

// Config builds a minimal *config.Config for a run against app: the application
// path, a placeholder model, and a small iteration budget so a misbehaving script
// cannot loop indefinitely. Options tune it per feature. Each call returns a fresh
// config, so concurrent runs never share a pointer.
func Config(tb testing.TB, app *FakeApp, opts ...ConfigOption) *config.Config {
	tb.Helper()

	cfg := &config.Config{ApplicationPath: app.Path, Identity: "agent"}
	cfg.LLM.Model = "test-model"
	cfg.LLM.Budget.MaxIterations = 20

	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// ConfigOption tunes a Config for a particular setup.
type ConfigOption func(*config.Config)

// WithMaxIterations sets the loop's iteration cap, the number of model turns a
// run may take before it ends with the max-iterations outcome.
func WithMaxIterations(n int64) ConfigOption {
	return func(c *config.Config) { c.LLM.Budget.MaxIterations = n }
}

// WithMaxTokens sets the run's token budget, the cumulative processed-token cap that
// ends a run with the budget outcome once crossed.
func WithMaxTokens(n int64) ConfigOption {
	return func(c *config.Config) { c.LLM.Budget.MaxTokens = n }
}

// WithRAG enables the knowledge (RAG) feature with its index directory left at the
// default, so a run's StoreDir rebases it. The lexical tier needs no embeddings
// server, so the store opens without one.
func WithRAG() ConfigOption {
	return func(c *config.Config) { c.Harness.RAG = &config.RAGConfig{Enabled: true} }
}

// WithMemory enables the file memory backend with its directory left at the default,
// so a run's StoreDir rebases it; the store directory is created at run start.
func WithMemory() ConfigOption {
	return func(c *config.Config) { c.Harness.Memory = &config.MemoryConfig{Enabled: true} }
}

// WithHITL enables the human-in-the-loop built-in tools (ask_human_confirm, _select,
// _input), which route to the run's prompter.
func WithHITL() ConfigOption {
	return func(c *config.Config) {
		c.Harness.HumanInTheLoop = &config.HumanInTheLoopConfig{Enabled: true}
	}
}
