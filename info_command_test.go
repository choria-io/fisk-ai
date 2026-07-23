//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/ui/columns"

	"github.com/choria-io/fisk-ai/config"
)

var _ = Describe("toolSearchStatus", func() {
	It("Should report tool search enabled for the default provider", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		Expect(toolSearchStatus(cfg)).To(ContainSubstring("enabled"))
	})

	It("Should report the operator-disabled cause when no_tool_search is set", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.LLM.NoToolSearch = true
		Expect(toolSearchStatus(cfg)).To(Equal("disabled (no_tool_search)"))
	})

	It("Should report an unavailable provider that is not linked into the build", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "gpt-5"
		cfg.LLM.Provider = "openai"
		Expect(toolSearchStatus(cfg)).To(ContainSubstring(`provider "openai" is not available`))
	})
})

var _ = Describe("printSessionsSection", func() {
	render := func(cfg *config.Config) string {
		c := columns.New()
		printSessionsSection(c, cfg)

		return c.String()
	}

	It("Should omit the section for an MCP-only config with no model", func() {
		cfg := &config.Config{}
		cfg.Harness.Sessions = &config.SessionConfig{Backend: "jetstream"}
		Expect(render(cfg)).ToNot(ContainSubstring("Sessions"))
	})

	It("Should show the file backend and its configured directory", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.Harness.Sessions = config.SessionConfigFromStateDir("/tmp/runs")

		out := render(cfg)
		Expect(out).To(ContainSubstring("Sessions"))
		Expect(out).To(ContainSubstring("file"))
		Expect(out).To(ContainSubstring("/tmp/runs"))
	})

	It("Should show the XDG default when the file backend has no directory", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"

		Expect(render(cfg)).To(ContainSubstring("(XDG default)"))
	})

	It("Should show the jetstream stream, context, and the derived-prefix note", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.NatsContext = "prod"
		cfg.Harness.Sessions = &config.SessionConfig{
			Backend: "jetstream",
			Options: json.RawMessage(`{"stream":"FISKSESSIONS"}`),
		}

		out := render(cfg)
		Expect(out).To(ContainSubstring("jetstream"))
		Expect(out).To(ContainSubstring("FISKSESSIONS"))
		Expect(out).To(ContainSubstring("prod"))
		Expect(out).To(ContainSubstring("derived from the stream"))
	})

	It("Should show (default) as the jetstream context when none is configured", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "claude-sonnet-5"
		cfg.Harness.Sessions = &config.SessionConfig{
			Backend: "jetstream",
			Options: json.RawMessage(`{"stream":"FISKSESSIONS"}`),
		}

		Expect(render(cfg)).To(ContainSubstring("(default)"))
	})
})
