//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Fingerprint captures the configuration a run was started with. It is validated
// on resume: continuing a conversation against a changed model, prompt or tool
// set can be incoherent (a stored tool_use may reference a tool that no longer
// exists, or a thinking signature may be rejected), so a mismatch is refused
// unless the operator forces it.
//
// The system prompt is stored as a hash, not verbatim, so the fingerprint never
// leaks prompt contents.
type Fingerprint struct {
	// Provider is the neutral provider id the run was started with. It is a HARD
	// resume gate that --force cannot cross (a turn from another provider is
	// incoherent, see SECURITY.md finding 4), so it is deliberately excluded from
	// Equal and Diff, which govern only the forceable configuration drift. The
	// provider check lives at the resume gate and is unconditional.
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	SystemHash    string `json:"system_hash"`
	ToolsHash     string `json:"tools_hash"`
	ThinkingMode  string `json:"thinking_mode"`
	MaxTokens     int64  `json:"max_tokens"`
	MaxIterations int64  `json:"max_iterations"`
}

// HashHex returns the hex-encoded SHA-256 of b, for building the system prompt
// and tool-set hashes.
func HashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Equal reports whether two fingerprints match exactly.
func (f Fingerprint) Equal(o Fingerprint) bool {
	return len(f.Diff(o)) == 0
}

// Diff returns a human-readable line per field that differs between f (the saved
// fingerprint) and o (the current one), for an actionable mismatch error. Hashed
// fields are reported as changed rather than showing their opaque values.
func (f Fingerprint) Diff(o Fingerprint) []string {
	var out []string

	if f.Model != o.Model {
		out = append(out, fmt.Sprintf("model: %s -> %s", f.Model, o.Model))
	}
	if f.SystemHash != o.SystemHash {
		out = append(out, "system prompt: changed")
	}
	if f.ToolsHash != o.ToolsHash {
		out = append(out, "tool set: changed")
	}
	if f.ThinkingMode != o.ThinkingMode {
		out = append(out, fmt.Sprintf("thinking: %s -> %s", f.ThinkingMode, o.ThinkingMode))
	}
	if f.MaxTokens != o.MaxTokens {
		out = append(out, fmt.Sprintf("max_tokens: %d -> %d", f.MaxTokens, o.MaxTokens))
	}
	if f.MaxIterations != o.MaxIterations {
		out = append(out, fmt.Sprintf("max_iterations: %d -> %d", f.MaxIterations, o.MaxIterations))
	}

	return out
}
