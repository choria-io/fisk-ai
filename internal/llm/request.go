//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package llm

// Request is a single provider-neutral model call: the conversation plus the
// knobs a provider needs to render it to its own wire format. It carries no
// infrastructure (client, credentials, per-call timeout); those live on the
// Provider, so a Request is a plain value a test can build and assert on.
type Request struct {
	// Model is the provider model id to call.
	Model string

	// SystemBlocks is the system prompt as an ordered list of text segments. It is a
	// slice, not a single string, because a provider may treat each segment as its own
	// block: the Anthropic provider sends them as separate system blocks and places the
	// prompt-cache breakpoint on the last one, while a provider whose system prompt is
	// one string joins them.
	SystemBlocks []string

	// Messages is the conversation so far. It ends on a boundary the provider accepts
	// (an initial user prompt, or an assistant turn whose tool_use blocks are all
	// answered by a following user results turn).
	Messages []Message

	// Tools is the model-facing tool set. A tool that requests deferral is hidden
	// behind server-side tool search by a provider that supports it; see ToolSearch.
	Tools []ToolDef

	// ToolSearch asks the provider to add its server-side tool search tool so the
	// model can discover the deferred tools by name and description. It is set when at
	// least one tool actually defers, so the search tool is present only when there is
	// something for it to find.
	ToolSearch bool

	// ThinkingEnabled requests reasoning output. The neutral model carries only the
	// toggle, matching the single llm.thinking.enabled config knob; a provider maps it
	// to its own thinking configuration.
	ThinkingEnabled bool

	// MaxOutputTokens caps the tokens generated for this one response. It bounds a
	// single reply, distinct from any cumulative token budget the caller enforces.
	MaxOutputTokens int64

	// PromptCache turns on provider prompt caching for this request. It is deliberately
	// not part of the run fingerprint, so toggling it never refuses a resume.
	PromptCache bool

	// Interactive marks a chat run whose think-time between turns is long, letting a
	// provider pick a longer cache TTL than an autonomous loop's tight cadence needs.
	Interactive bool
}
