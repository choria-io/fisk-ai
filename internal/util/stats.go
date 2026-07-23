//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"fmt"
	"os"
	"time"

	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// RunStats accumulates per-run counters for the summary line.
type RunStats struct {
	Start time.Time
	// Model is the LLM model the run used, shown in the summary line.
	Model string
	// Session is the checkpointed session id, shown in the summary line when set.
	Session string
	// Suspended reports that the run was checkpointed and paused rather than
	// completed, so the summary reads "Run suspended" rather than "Run summary".
	Suspended bool
	// LlmCalls is the number of LLM requests made.
	LlmCalls int64
	// ToolCalls is the total number of tool invocations, including the remote ones
	// counted separately in RemoteToolCalls.
	ToolCalls int64
	// RemoteToolCalls is the number of tool invocations dispatched to a remote
	// agent over a2a, a subset of ToolCalls.
	RemoteToolCalls int64
	// ToolCallsByKind counts dispatched calls by the provider that supplied the tool.
	// Every call counted in ToolCalls is counted here too, including the ones rejected
	// before execution, so on a fresh run the buckets partition ToolCalls. It is
	// live-only: a resumed run seeds only the coarse totals, so a resume leaves this
	// nil and reflects at most the calls made since the resume. It is nil until the
	// first call is counted.
	ToolCallsByKind map[toolkit.Kind]int64
	InTokens        int64
	OutTokens       int64
	// CacheReadTokens is the input tokens served from the prompt cache (billed at
	// roughly a tenth of the uncached input rate). CacheCreateTokens is the input
	// tokens written into the cache this run (billed at a premium). InTokens keeps
	// its meaning: the uncached input remainder. A healthy multi-iteration run shows
	// InTokens small and CacheReadTokens climbing; a silent cache miss shows
	// CacheReadTokens stuck at zero against a large InTokens.
	CacheReadTokens   int64
	CacheCreateTokens int64
}

// CountToolKind records one dispatched tool call against its provider kind,
// allocating the map on first use. It is called for every call ToolCalls counts,
// the rejected ones included, so the buckets partition ToolCalls on a fresh run. A
// RunStats is driven only from its own single run goroutine, so this needs no lock.
func (s *RunStats) CountToolKind(kind toolkit.Kind) {
	if s.ToolCallsByKind == nil {
		s.ToolCallsByKind = make(map[toolkit.Kind]int64)
	}

	s.ToolCallsByKind[kind]++
}

// Print writes the run summary to stderr. It is deferred so every exit path,
// including errors and a graceful suspend, reports what was spent. Verbose adds the
// cache-write count, of interest mainly when diagnosing cache behavior.
func (s *RunStats) Print(verbose bool) {
	fmt.Fprintf(os.Stderr, "\n%s\n", s.summaryLine(verbose))
}

// summaryLine formats the run summary. The label distinguishes a suspended run
// from a completed one; the session id and model are shown only when set, and the
// remote tool call count only when any were made, so an ephemeral run stays
// uncluttered. The cache-read count appears only when the cache was hit (following
// the remote_tool_calls pattern); the cache-write count is verbose-only.
func (s *RunStats) summaryLine(verbose bool) string {
	label := "Run summary"
	if s.Suspended {
		label = "Run suspended"
	}

	session := ""
	if s.Session != "" {
		session = fmt.Sprintf("session=%s ", s.Session)
	}

	model := ""
	if s.Model != "" {
		model = fmt.Sprintf("model=%s ", s.Model)
	}

	tools := fmt.Sprintf("tool_calls=%d", s.ToolCalls)
	if s.RemoteToolCalls > 0 {
		tools += fmt.Sprintf(" remote_tool_calls=%d", s.RemoteToolCalls)
	}

	cache := ""
	if s.CacheReadTokens > 0 {
		cache += fmt.Sprintf(" cached=%d", s.CacheReadTokens)
	}
	if verbose && s.CacheCreateTokens > 0 {
		cache += fmt.Sprintf(" cache_write=%d", s.CacheCreateTokens)
	}

	return fmt.Sprintf("%s: %s%sllm_calls=%d %s tokens=%d/%d%s latency=%s",
		label, session, model, s.LlmCalls, tools, s.InTokens, s.OutTokens, cache, time.Since(s.Start).Round(time.Millisecond))
}
