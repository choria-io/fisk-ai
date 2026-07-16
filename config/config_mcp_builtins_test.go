// Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
// SPDX-License-Identifier: Apache-2.0

package config

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("MCP builtins allowlist", func() {
	build := func(builtins []string, ragEnabled bool) *Config {
		cfg := &Config{
			Expose: &ExposeConfig{Agent: &AgentExpose{MCP: &ExposedMCPConfig{Port: 8080, Builtins: builtins}}},
		}
		if ragEnabled {
			cfg.Harness.RAG = &RAGConfig{Enabled: true}
		}

		return cfg
	}

	It("accepts knowledge_search when knowledge is enabled, trimming and de-duplicating", func() {
		cfg := build([]string{"knowledge_search", " knowledge_search "}, true)
		Expect(cfg.prepare()).To(Succeed())
		Expect(cfg.MCPBuiltins()).To(Equal([]string{"knowledge_search"}))
		Expect(cfg.MCPExposesKnowledgeSearch()).To(BeTrue())
	})

	It("rejects a real but unexposable built-in, naming the exposable set", func() {
		cfg := build([]string{"ask_human_confirm"}, true)
		err := cfg.prepare()
		Expect(err).To(MatchError(ContainSubstring("cannot be exposed over MCP")))
		Expect(err).To(MatchError(ContainSubstring("knowledge_search")))
	})

	It("rejects an unknown built-in name", func() {
		cfg := build([]string{"frobnicate"}, true)
		Expect(cfg.prepare()).To(MatchError(ContainSubstring("cannot be exposed over MCP")))
	})

	It("rejects knowledge_search when knowledge is not enabled", func() {
		cfg := build([]string{"knowledge_search"}, false)
		Expect(cfg.prepare()).To(MatchError(ContainSubstring("knowledge is not enabled")))
	})

	It("is a no-op with no builtins listed", func() {
		cfg := build(nil, false)
		Expect(cfg.prepare()).To(Succeed())
		Expect(cfg.MCPExposesKnowledgeSearch()).To(BeFalse())
	})
})
