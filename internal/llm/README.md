# internal/llm

Provider-neutral domain model for a model conversation, plus the registry that
resolves a named backend. The agent loop, the persisted journal, the renderers and
the tool kinds all speak the types in this package so that no single provider's SDK
types leak through the codebase. Per-provider codecs (see `internal/llm/anthropic`)
translate this model to and from a concrete wire format at the edges.

`anthropic` is the only provider implemented today. This document is the contract a
second provider must satisfy, and the list of work still outstanding.

## The neutral model

- `Message` / `ContentBlock` (`types.go`): a turn is a role plus a list of blocks.
  `ContentBlock` is a discriminated union with exactly one field set: `Text`,
  `Thinking`, `ToolUse`, `ToolResult`, or `Provider`. The first four are the kinds
  every provider shares; `ProviderBlock` is the opaque escape hatch for a
  server-side block the neutral model does not name (an Anthropic
  `tool_search_tool_result`, a `web_search_tool_result`, a `redacted_thinking`), so
  it survives a round-trip verbatim.
- `ThinkingBlock.Signature []byte`: the opaque payload a provider requires echoed
  back verbatim to accept the turn on a later call (an Anthropic signature, an
  OpenAI `encrypted_content`). The neutral model never inspects or renders it.
- `ToolDef` (`types.go`): a neutral tool definition. `DeferLoading` asks for the
  tool to be hidden behind server-side tool search, leaving the model to discover
  it through the search tool rather than receiving it up front. How a codec spells
  that on the wire is its own concern; false is the default in every format seen so
  far, so emitting it and omitting it are equivalent.
- `Request` (`request.go`): one call, carrying only neutral data (model, system
  blocks, messages, tools, flags). No client, credentials or timeout: those live on
  the `Provider`, so a `Request` is a plain value a test can build and assert on.
- `Response` / `Usage` / `StopReason` (`response.go`): the reply, the token
  accounting, and the neutral stop-reason vocabulary a codec maps its own strings
  onto.
- `Middleware` (`middleware.go`): an http-shaped, SDK-free request hook the caller
  assembles (request trace, HTTP debug dump) and hands to the provider through
  `Config`.

## The Provider interface

```go
type Provider interface {
    Call(ctx context.Context, req Request) (*Response, error)
    Capabilities() Caps
}
```

`Call` owns the wire call end to end, including any per-call timeout: it renders the
neutral `Request` to its wire format, issues the request, and converts the reply
back to a neutral `Response`. It is the single seam where a concrete SDK is spoken.

`Capabilities` returns declared capabilities. They are declared, not discovered:
neither Anthropic nor OpenAI expose capability flags at runtime, so a provider states
them from static knowledge of its backend.

## The registry

Providers self-register from their package `init`, the same house pattern as the
a2a, memory and session backends.

```go
// in the provider package's init:
llm.Register(ProviderName, factory, credentialEnvNames)
```

- `Register(name, factory, credentialEnvNames)` (`registry.go`). `credentialEnvNames`
  is a REQUIRED positional argument, not an omittable field, so a provider cannot be
  built without declaring its secrets (see "Credential handling" below).
- `NewProvider(name, cfg)` resolves a provider by name and constructs it. An unknown
  name (usually a provider whose package was not linked in) returns an error that
  lists the linked-in providers.
- `Providers()` lists the linked-in provider names.
- `CredentialEnvNames()` is the union of every linked provider's credential env var
  names.

The caller selects a provider by name from config (`llm.provider`, defaulting to
`anthropic`) and resolves it through `NewProvider`. `internal/agent/agent.go`
blank-imports `internal/llm/anthropic` so it self-registers; a second provider is
linked in by adding a second blank import there.

## How to add a provider

1. Create `internal/llm/<name>/` with a `Provider` type implementing `Call` and
   `Capabilities`.
2. Write a codec that converts every neutral block kind to and from the wire format,
   in both directions, including `ProviderBlock` (preserve `Raw` faithfully) and
   `ThinkingBlock.Signature`. `internal/llm/anthropic/codec.go` and `tools.go` are
   the reference: `MessageToNeutral`/`MessageToAnthropic`, `ResponseToNeutral`,
   `ToolDefTo...`, `ToolUseToNeutral`, `ToolResultTo.../From...`, and a
   `stopReasonToNeutral` mapping.
3. Register in `init` with `llm.Register(ProviderName, factory, credentialEnvNames)`.
   The factory takes `llm.Config` (API key, base URL, timeout, middlewares) and
   returns a constructed `Provider`; construction failures surface as an error so an
   operator's mistake fails at run start.
4. Return a `Caps` whose `Provider` field is the neutral id you registered under.
5. Add a blank import of the package on the run path (today: `internal/agent/agent.go`).
6. The config value `llm.provider: <name>` now selects it; add it to the candidate
   list in `docs/content/reference/_index.md`.

## Contracts a provider must honor

- Fingerprint identity. The run fingerprint stores `Capabilities().Provider` and the
  resume gate refuses a resume across a provider change unconditionally (`--force`
  cannot cross it). Return a stable id, distinct from any other provider whose wire
  format or turn semantics differ. A config selector that maps onto an existing
  provider (a future `anthropic-compat`) should still report that provider's id when
  the backend semantics are identical.
- Opaque payloads. `ThinkingBlock.Signature` and `ProviderBlock.Raw` must round-trip
  byte-for-byte; the model rejects a turn whose signature was dropped or altered.
- Deferral. Render `ToolDef.DeferLoading` in whatever form the wire format takes, and
  add the server-side tool-search tool only when the request asks for it
  (`Request.ToolSearch`, set only when at least one tool actually deferred). Report
  `SupportsToolSearch` truthfully; a provider that returns false makes the agent send
  every tool directly and raise a degradation warning.
- Truncation. Map an output-cap stop to `StopMaxTokens`; a truncated turn's trailing
  `tool_use` is not safe to execute.
- Per-call timeout. `Call` owns `Config.Timeout` and must bound the wire call with it.
  Nothing outside the provider enforces this, so a provider that ignores it silently
  drops the `llm.budget.call_timeout` guarantee with no test catching the loss.
- Prompt cache stays out of the fingerprint, so toggling it never refuses a resume.

## Credential handling (security boundary)

`credentialEnvNames` is not documentation. Those names are stripped from the
environment of any tool subprocess whose command line the model chooses (see
`internal/toolkit/fisk`), so a tool can never read the agent's own credentials. List
every SECRET-BEARING variable the provider's default credential chain reads (API
keys, bearer or identity tokens, signing keys, any header variable that can carry an
Authorization value). Do NOT list selector variables that only point at on-disk
credentials (a profile name, a config-dir path): those hold no secret and stripping
them buys nothing. The strip covers every linked provider, not just the active one.

## Outstanding work

The remaining work for this package, kept where the code lives rather than in a
separate planning document.

- Second provider. Only `anthropic` exists. The next target is OpenAI: the Responses
  API where its own tool search lives (`defer_loading` plus a `tool_search` tools
  entry), and Chat Completions for the compat long tail. No new dependency is
  planned: follow the hand-rolled `net/http` client in `internal/rag/embed.go` rather
  than take on an SDK whose types would leak back through this neutral layer.
- `Caps` is deliberately minimal and grows as a real second provider makes a
  capability difference concrete rather than predicted. Today only
  `SupportsToolSearch` is consulted. `Caps.MaxOutputTokens` is declared but NOT yet
  enforced: nothing clamps or validates `Request.MaxOutputTokens` against a
  provider's stated ceiling. Wiring that check (and surfacing it, likely as a
  warning) is open.
- Capabilities are config-declared because runtime discovery is unavailable: neither
  provider's `/v1/models` returns context-window or capability flags. Revisit if that
  changes.
- Known hard spots the neutral model must absorb for the second provider:
  - Tool results. Chat Completions needs N separate `role:tool` messages where
    Anthropic batches tool_result blocks into one synthetic user message. The neutral
    Fold contract (`internal/runstate`) must serve both.
  - System prompt. An array of blocks on Anthropic (with the prompt-cache breakpoint
    on the last block) versus a single string on Chat Completions / `instructions` on
    Responses. The cache-breakpoint mechanism has no meaning once system is one string.
  - Thinking round-trip. Anthropic echoes a `signature`; Responses uses
    `encrypted_content` plus item ids with different lifecycle rules. `ThinkingBlock`'s
    opaque `Signature` is the carrier for both, but the item-id lifecycle may need
    more than a single opaque field.
- Credential selection. `--api-key` is currently hardwired to `ANTHROPIC_API_KEY` and
  unconditionally required. A second provider needs an `api_key_env`-style convention
  and provider-conditional required-ness so `provider: openai` does not force an
  Anthropic key that then gets sent to the OpenAI endpoint.
- `StopReason` is seeded from Anthropic's vocabulary and the a2a seed; a new provider
  may report a reason not yet named here.
