//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
