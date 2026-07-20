+++
title = "Providers and the Neutral Model"
weight = 45
description = "The provider-neutral conversation model every subsystem speaks, and the seam where one model backend is swapped for another."
+++

Every part of Fisk AI that touches a model conversation speaks one provider-neutral vocabulary. The agent loop, the journal on disk, the renderers, and the tool kinds all hold `llm.Message` values, and a per-provider codec translates that vocabulary to a vendor's wire format at the last moment. The backend is chosen by name at startup. One provider ships today, `anthropic`.

{{% notice style="note" title="Where it lives" %}}
`internal/llm` owns the neutral model and the registry: `types.go` holds the conversation types, `request.go` and `response.go` one call and its reply, `provider.go` the seam, `registry.go` the name-to-backend map. `internal/llm/anthropic` holds the only provider: `codec.go` and `tools.go` translate, `provider.go` makes the call.
{{% /notice %}}

The seam keeps a vendor SDK's types out of the domain model. A journaled turn, a resume fingerprint, a tool definition, an event rendered to the terminal: none of them name a vendor type. In non-test code the Anthropic SDK is imported by `internal/llm/anthropic` and nowhere else.

## The neutral model

A conversation is a list of `Message` values, each a role plus a list of `ContentBlock`. A `ContentBlock` is a discriminated union with exactly one field set.

| Block | What it carries | Written by |
|-------|-----------------|------------|
| `Text` | Prose. | The model, or the operator's prompt. |
| `Thinking` | Summarized reasoning, plus an opaque `Signature`. | The model. |
| `ToolUse` | An id, a tool name, and the arguments as raw JSON. | The model. |
| `ToolResult` | The matching `ToolUseID`, the result text, and an error flag. | A tool. |
| `Provider` | A `Kind` discriminator and the block's own JSON in `Raw`. | A codec, for any server-side block the neutral model does not name. |

`ProviderBlock` is the escape hatch that makes the set closed without making it lossy. An Anthropic `tool_search_tool_result`, a `web_search_tool_result`, a `redacted_thinking` block: none has a neutral arm, so each crosses as a `ProviderBlock` carrying its JSON verbatim and is reconstructed on the way back out.

`ThinkingBlock.Signature` is the same idea at field scale. A provider requires it echoed back unaltered to accept the turn on a later call, so the neutral model stores it as `[]byte` and never inspects or renders it. `ToolUseBlock.Input` is `json.RawMessage` for the same reason: arguments survive byte for byte with no schema-shaped intermediate.

## The Provider seam

```go
type Provider interface {
	Call(ctx context.Context, req Request) (*Response, error)
	Capabilities() Caps
}
```

`Call` owns one wire call end to end, including its own per-call timeout: it renders the neutral `Request`, issues the request, and converts the reply back to a neutral `Response`. Nothing outside the provider enforces that timeout, so honoring `Config.Timeout` is part of the contract rather than a convenience.

`Request` carries only neutral data, which is what makes it testable: the model id, the system prompt as ordered segments, the messages, the tool definitions, and flags for tool search, thinking, prompt cache, and interactive runs. No client, no credentials, no timeout; those live on the `Provider`, so a `Request` is a plain value a test can build and assert on.

`Caps` reports what a backend supports, and the values are declared rather than discovered: no provider exposes capability flags at runtime, so each states them from static knowledge. Only `SupportsToolSearch` is consulted today. `Caps.MaxOutputTokens` is declared but read by nothing, so a provider's stated ceiling is not enforced against `Request.MaxOutputTokens`.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 300" role="img" aria-label="A neutral request crosses into the codec, becomes SDK types on the wire, and the reply crosses back to a neutral response">
    <defs>
      <marker id="lp" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- region labels -->
    <text class="cm-svg-label" x="175" y="52" text-anchor="middle">internal/llm</text>
    <text class="cm-svg-label" x="540" y="52" text-anchor="middle">internal/llm/anthropic</text>
    <!-- the vendor boundary -->
    <line x1="340" y1="30" x2="340" y2="290" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="5 4"/>
    <!-- outbound lane -->
    <rect x="40" y="70" width="170" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="125" y="94" text-anchor="middle" style="fill:var(--cm-accent)">llm.Request</text>
    <text class="cm-svg-sub"   x="125" y="112" text-anchor="middle">neutral value</text>
    <rect class="cm-svg-box" x="360" y="70" width="180" height="52" rx="8"/>
    <text class="cm-svg-label" x="450" y="94" text-anchor="middle">MessageToAnthropic</text>
    <text class="cm-svg-sub"   x="450" y="112" text-anchor="middle">codec</text>
    <rect class="cm-svg-box" x="580" y="70" width="145" height="52" rx="8"/>
    <text class="cm-svg-label" x="652" y="94" text-anchor="middle">SDK params</text>
    <text class="cm-svg-sub"   x="652" y="112" text-anchor="middle">wire form</text>
    <line x1="210" y1="96" x2="356" y2="96" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#lp)"/>
    <line x1="540" y1="96" x2="576" y2="96" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#lp)"/>
    <!-- the call itself -->
    <line x1="652" y1="124" x2="652" y2="158" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#lp)"/>
    <text class="cm-svg-sub" x="662" y="146" text-anchor="start">HTTPS</text>
    <!-- inbound lane -->
    <rect x="40" y="160" width="170" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="125" y="184" text-anchor="middle" style="fill:var(--cm-accent)">llm.Response</text>
    <text class="cm-svg-sub"   x="125" y="202" text-anchor="middle">neutral value</text>
    <rect class="cm-svg-box" x="360" y="160" width="180" height="52" rx="8"/>
    <text class="cm-svg-label" x="450" y="184" text-anchor="middle">ResponseToNeutral</text>
    <text class="cm-svg-sub"   x="450" y="202" text-anchor="middle">codec</text>
    <rect class="cm-svg-box" x="580" y="160" width="145" height="52" rx="8"/>
    <text class="cm-svg-label" x="652" y="184" text-anchor="middle">SDK Message</text>
    <text class="cm-svg-sub"   x="652" y="202" text-anchor="middle">wire form</text>
    <line x1="576" y1="186" x2="544" y2="186" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#lp)"/>
    <line x1="356" y1="186" x2="214" y2="186" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#lp)"/>
    <!-- everything else stays neutral -->
    <rect class="cm-svg-box" x="40" y="240" width="270" height="44" rx="8" stroke-dasharray="5 4"/>
    <text class="cm-svg-label" x="175" y="262" text-anchor="middle">Everything else speaks neutral</text>
    <text class="cm-svg-sub"   x="175" y="279" text-anchor="middle">runner, journal, renderers</text>
    <line x1="125" y1="214" x2="125" y2="238" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3"/>
  </svg>
  <figcaption>The dashed rule is the vendor boundary, and it cuts through the codebase rather than sitting under it. Every vendor type lives to its right; a block the neutral model does not name crosses as a `ProviderBlock` carrying raw JSON, so the round trip is byte-identical.</figcaption>
</figure>

## Choosing a backend

A provider registers itself from its package `init`, the same house pattern as the session, memory, and a2a backends, and `internal/agent` blank-imports the package so it links in. `NewProvider` resolves it by name; an unknown name fails at run start with an error listing the providers actually linked into the binary.

One argument of that registration is not documentation. `Register` takes `credentialEnvNames` as a required positional parameter, not an omittable option, because those names are stripped from the environment of every tool subprocess. A provider cannot be registered without declaring its secrets. [Safety and Human in the Loop]({{% relref "safety" %}}) covers what the strip does and does not promise.

The operator-facing surface is small:

| Setting | Effect |
|---------|--------|
| `llm.provider` | Selects the backend by name. Defaults to `anthropic`, so a config that never mentions it keeps working. |
| `llm.no_tool_search` | Turns off deferred tool loading regardless of what the provider supports. |
| `llm.budget.max_output_tokens` | Caps a single response. Left unset, thinking runs raise the cap. |
| `--api-key` | Required, and bound to `ANTHROPIC_API_KEY`. |

`fisk-ai info` resolves all of it without starting a run, and opens with what the model call will use.

<div class="cm-terminal">
  <div class="cm-tbar"><i class="r"></i><i class="y"></i><i class="g"></i></div>
  <div class="cm-tbody">$ fisk-ai info --config nats.yaml
  Model:
                Model: claude-sonnet-4-6
             Provider: anthropic
             Thinking: disabled
          Tool search: <span class="c-ok">enabled (used when 10 or more tools are available)</span></div>
</div>

Tool search reports one of four states: `disabled (no_tool_search)` when the operator turned it off, `unknown (provider ... is not available)` when the named provider is not linked into the build, `unavailable (provider ... does not support it)` when it is linked but reports no support, and the enabled form above otherwise. Resolving it reads `Capabilities()` and never makes a call, so the answer is available offline and with no credentials. The whole section is skipped for a config with no model, which is how an MCP-only config parses.

{{% notice style="warning" title="Load-bearing decision" %}}
Nothing is lost across the neutral model. The five named block kinds decompose into neutral fields, and every other block becomes a `ProviderBlock` holding the provider's own JSON, so a turn survives the round trip byte for byte. A golden test pins exactly that across thinking, redacted thinking, server tool use, and web search results. This is what lets the journal store `llm.Message` directly: a checkpointed run replays turns the neutral model never understood, and the model still accepts them.
{{% /notice %}}

## Where these types surface

Three of them appear on other pages under different names.

<dl class="cm-kv">
  <dt>llm.Message</dt><dd>What an `AssistantRecord` stores in the journal, which is why the record format is provider-neutral. See [Sessions and Resume]({{% relref "sessions" %}}).</dd>
  <dt>llm.ToolDef</dt><dd>What `toolkit.Tool.Definition` returns, so every tool kind describes itself in neutral terms. See [Tools and Introspection]({{% relref "tools" %}}).</dd>
  <dt>Caps.Provider</dt><dd>The id stamped into the run fingerprint, and the one field a resume refuses to cross even with `--force`.</dd>
</dl>

Only `anthropic` exists today. `internal/llm/README.md` is the contract a second provider satisfies and the register of what remains open, including the unenforced `Caps.MaxOutputTokens` and the `--api-key` binding above.

{{% notice style="tip" title="Next" %}}
Continue to [Safety and Human in the Loop]({{% relref "safety" %}}), where the credential names a provider declares become the names stripped from every tool's environment.
{{% /notice %}}
