# Model providers

`internal/llm` describes a conversation in types that name no vendor. `internal/llm/anthropic` is the only package in the tree that imports the Anthropic SDK, and it is the only place a concrete API is spoken.

{{% notice style="note" title="Where it lives" %}}
`internal/llm` holds the neutral model and the registry: `types.go`, `request.go`, `response.go`, `provider.go`, `registry.go`, `middleware.go`. `internal/llm/anthropic` holds the backend: `provider.go`, `codec.go`, `tools.go`. `internal/llm/README.md` is written as the contract a second provider must satisfy.
{{% /notice %}}

## The neutral model

A `Message` is a role and a list of content blocks. `ContentBlock` is a union with exactly one of `Text`, `Thinking`, `ToolUse`, `ToolResult` or `Provider` set.

<dl class="cm-kv">
  <dt>ThinkingBlock.Signature</dt><dd>A byte slice, because the neutral model never inspects or renders it. It only preserves it, and the model rejects a turn whose signature was dropped or altered.</dd>
  <dt>ToolUseBlock.Input</dt><dd>Raw JSON, so arguments survive with no schema-shaped intermediate in between.</dd>
  <dt>ProviderBlock</dt><dd>The escape hatch for server-side blocks the neutral model does not name: tool search results, web search results, redacted thinking. Kind plus faithful raw JSON.</dd>
  <dt>SystemBlocks</dt><dd>A slice rather than a string, so a provider that supports separate system blocks can place a cache breakpoint on the last one.</dd>
</dl>

`ThinkingMode` has three states: unset sends no parameter at all, where off sends the parameter set false. `ReasoningEffort` is a plain string rather than an enum, since the levels belong to the model and a newer one may take a level this build never heard of.

`Usage` carries five numbers, and `Thinking` counts inside `Out` rather than adding to it.

## One call, end to end

<figure class="cm-diagram">
  <svg viewBox="0 0 760 250" role="img" aria-label="A neutral request encoded to the Anthropic API and decoded back through one codec">
    <defs>
      <marker id="prov-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="50" width="150" height="50" rx="8"/>
    <text class="cm-svg-label" x="95" y="72" text-anchor="middle">llm.Request</text>
    <text class="cm-svg-sub" x="95" y="89" text-anchor="middle">neutral value</text>
    <line x1="170" y1="75" x2="209" y2="75" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#prov-ah)"/>
    <rect x="215" y="50" width="150" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="290" y="72" text-anchor="middle" style="fill:var(--cm-accent)">buildParams</text>
    <text class="cm-svg-sub" x="290" y="89" text-anchor="middle">cache, thinking</text>
    <line x1="365" y1="75" x2="424" y2="75" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#prov-ah)"/>
    <rect class="cm-svg-box" x="430" y="50" width="160" height="50" rx="8"/>
    <text class="cm-svg-label" x="510" y="72" text-anchor="middle">Messages.New</text>
    <text class="cm-svg-sub" x="510" y="89" text-anchor="middle">one blocking call</text>
    <line x1="510" y1="100" x2="510" y2="144" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#prov-ah)"/>
    <rect x="430" y="150" width="160" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="510" y="172" text-anchor="middle" style="fill:var(--cm-accent)">block codec</text>
    <text class="cm-svg-sub" x="510" y="189" text-anchor="middle">one representation</text>
    <line x1="430" y1="175" x2="371" y2="175" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#prov-ah)"/>
    <rect class="cm-svg-box" x="215" y="150" width="150" height="50" rx="8"/>
    <text class="cm-svg-label" x="290" y="172" text-anchor="middle">llm.Response</text>
    <text class="cm-svg-sub" x="290" y="189" text-anchor="middle">usage, stop reason</text>
    <text class="cm-svg-sub" x="330" y="232" text-anchor="middle">block kinds the neutral model does not name survive byte for byte</text>
  </svg>
  <figcaption>A reply and a journaled turn go through the same block codec, so they share one representation.</figcaption>
</figure>

There is no streaming. `Call` issues a single blocking request, and tool calls come back as ordinary `tool_use` blocks in the completed message. The provider is also the only enforcer of the per-call timeout.

`buildParams` is split out from `Call` so request assembly is testable without a wire call. It builds the system blocks fresh on every call, which keeps a cache-control marker out of the value hashed into the run fingerprint.

## Prompt caching and thinking

`buildParams` places two cache breakpoints: the tools-and-system one on the last system block, and the conversation-tail one at request level. The TTL is one hour for an interactive run and five minutes otherwise, because an operator can sit thinking for longer than five minutes and come back to an expired cache.

Thinking blocks are stripped only when the mode is explicitly off. Stripping on unset would break the signature chain within a run, since the model emits thinking alongside `tool_use` and the next iteration has to echo it back. The stripper also returns the message untouched when everything would be removed, because an assistant turn with no content is rejected by the API.

A 400 from a call that sent thinking or a reasoning effort gains a remedy hint. For thinking the remedy is removing the block rather than setting it false, since false is still a parameter and is rejected the same way.

{{% notice style="warning" title="Load-bearing decision" %}}
Opaque payloads round-trip byte for byte. A thinking signature and a provider block's raw JSON are preserved exactly, and a golden round-trip test guards it. The discriminator for an unnamed block is read from the marshaled JSON rather than from the SDK's accessor, because the SDK leaves the type field at its zero value and fills the default only on marshal.
{{% /notice %}}

The codec also repairs a documented SDK round-trip defect: decoding a successful tool-search result drops a required field and selects the error variant, so the whole content union is rebuilt rather than patched.

## Registration and identity

A provider registers itself from `init` with a name, a factory, and the environment variables that carry its secrets. That third argument is positional and required, so a provider cannot be registered without declaring them. The union across every linked-in provider is stripped from tool subprocess environments regardless of which provider is active.

The list names the secret-bearing variables only, not selector variables like a profile or config directory, which hold no secret and are guarded by file permissions.

`Caps` separates two names. `Provider` is the neutral id stamped into the run fingerprint; `SemconvProvider` is the name the OpenTelemetry semantic conventions use. The two vocabularies do not always agree and answer to different owners.

{{% notice style="warning" title="Load-bearing decision" %}}
Provider identity is a hard resume gate that `--force` cannot cross, and it is read off the resolved provider rather than the configuration, because an injected provider bypasses the registry. A stored thinking signature or provider block belongs to the provider that produced it.
{{% /notice %}}

Capabilities are declared rather than discovered, since neither Anthropic nor OpenAI exposes capability flags at runtime. Middlewares are `net/http`-shaped type aliases rather than defined types, so a provider SDK expecting the same function shape accepts them unchanged and the caller never imports the SDK to install one.

## What is Anthropic-specific

The two-breakpoint cache scheme and its TTL choice, the adaptive summarized thinking display, the BM25 tool-search tool, forwarding extra schema keys verbatim through the SDK's extension fields, and the stop-reason mapping, which is currently an identity cast because the values coincide. An unrecognized stop reason passes through rather than being lost.

Only a plain-text tool result decomposes into the neutral shape. An image or multi-block result is preserved as a provider block instead of being flattened.

## Outstanding

`Caps.MaxOutputTokens` is declared and nothing clamps a request against it. OpenAI is the named next target, over the Responses API where its own tool search lives, with an explicit decision to hand-roll an HTTP client rather than take on an SDK whose types would leak back through the neutral layer.

Chat Completions needs one message per tool result where Anthropic batches them into a single synthetic user message; a system prompt as a plain string makes the cache-breakpoint mechanism meaningless; and the Responses thinking round trip pairs encrypted content with item ids, which may need more than the single opaque signature field.

Credential selection is hardwired to one variable today, so a second provider needs a per-provider convention before `provider: openai` stops requiring an Anthropic key.

{{% notice style="tip" title="Next" %}}
Continue to [The agent loop]({{% relref "agent-loop" %}}) for what builds the request, or [Telemetry]({{% relref "telemetry" %}}) for what a call reports.
{{% /notice %}}
