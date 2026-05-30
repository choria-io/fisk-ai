//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RunStats", func() {
	Describe("summaryLine", func() {
		It("labels a completed run and omits the session when unset", func() {
			s := &RunStats{Model: "claude-opus-4-8", LlmCalls: 2, ToolCalls: 1}
			line := s.summaryLine(false)
			Expect(line).To(HavePrefix("Run summary:"))
			Expect(line).To(ContainSubstring("model=claude-opus-4-8"))
			Expect(line).To(ContainSubstring("llm_calls=2"))
			Expect(line).NotTo(ContainSubstring("session="))
		})

		It("labels a suspended run and includes the session id", func() {
			s := &RunStats{Model: "m", Session: "sess1", Suspended: true}
			line := s.summaryLine(false)
			Expect(line).To(HavePrefix("Run suspended:"))
			Expect(line).To(ContainSubstring("session=sess1"))
		})

		It("shows the remote tool count only when any were made", func() {
			Expect((&RunStats{}).summaryLine(false)).NotTo(ContainSubstring("remote_tool_calls"))
			Expect((&RunStats{RemoteToolCalls: 3}).summaryLine(false)).To(ContainSubstring("remote_tool_calls=3"))
		})

		It("shows the cache read count only when the cache was hit", func() {
			Expect((&RunStats{}).summaryLine(false)).NotTo(ContainSubstring("cached="))
			Expect((&RunStats{CacheReadTokens: 4096}).summaryLine(false)).To(ContainSubstring("cached=4096"))
		})

		It("shows the cache write count only under verbose", func() {
			s := &RunStats{CacheCreateTokens: 8192}
			Expect(s.summaryLine(false)).NotTo(ContainSubstring("cache_write"))
			Expect(s.summaryLine(true)).To(ContainSubstring("cache_write=8192"))
		})
	})
})
