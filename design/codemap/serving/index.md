# Serving: MCP and A2A

Instead of running a loop, Fisk AI can serve a Fisk application's commands to something else: over the Model Context
Protocol to a client such as Claude Code, or over NATS to another Fisk AI agent. Both modes reuse the same tool selection.
They diverge on one question: whether there is a human on the other end.

{{% notice style="note" title="Where it lives" %}}
`internal/mcpserver` is the whole MCP mode in one file. `internal/a2a` is the A2A protocol engine, split into protocol
machinery and a pluggable transport, with `internal/a2a/nats` as the one live binding. `internal/remotetools` is the
policy layer for importing another agent's tools, and `internal/conns` owns connection establishment.
{{% /notice %}}

## Both modes are opt-in, and the switch is config presence

The presence of `expose.agent.mcp` enables MCP. `expose.agent.agent_to_agent: true` enables A2A. An agent that says
nothing serves nothing, and each command refuses to start with an error naming the exact key to add.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 360" role="img" aria-label="One tool selection feeding two serving modes with different confirmation policies">
    <defs>
      <marker id="sv-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect x="280" y="18" width="200" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="40" text-anchor="middle" style="fill:var(--cm-accent)">ServedTools</text>
    <text class="cm-svg-sub" x="380" y="57" text-anchor="middle">one filtering path</text>
    <path d="M380,68 L380,94 L175,94 L175,114" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sv-ah)"/>
    <path d="M380,68 L380,94 L585,94 L585,114" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sv-ah)"/>
    <rect class="cm-svg-box" x="40" y="120" width="270" height="50" rx="8"/>
    <text class="cm-svg-label" x="175" y="142" text-anchor="middle">mcpserver</text>
    <text class="cm-svg-sub" x="175" y="159" text-anchor="middle">streamable HTTP, no stdio</text>
    <rect x="40" y="200" width="270" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="175" y="222" text-anchor="middle" style="fill:var(--cm-accent)">elicitation gate</text>
    <text class="cm-svg-sub" x="175" y="239" text-anchor="middle">gated tools are exposed</text>
    <rect class="cm-svg-box" x="40" y="290" width="270" height="50" rx="8"/>
    <text class="cm-svg-label" x="175" y="312" text-anchor="middle">any client on the port</text>
    <text class="cm-svg-sub" x="175" y="329" text-anchor="middle">the wider trust boundary</text>
    <line x1="175" y1="170" x2="175" y2="194" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sv-ah)"/>
    <line x1="175" y1="250" x2="175" y2="284" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sv-ah)"/>
    <rect class="cm-svg-box" x="450" y="120" width="270" height="50" rx="8"/>
    <text class="cm-svg-label" x="585" y="142" text-anchor="middle">a2a server</text>
    <text class="cm-svg-sub" x="585" y="159" text-anchor="middle">core NATS request-reply</text>
    <rect x="450" y="200" width="270" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent3) 12%, transparent)" stroke="var(--cm-accent3)"/>
    <text class="cm-svg-label" x="585" y="222" text-anchor="middle" style="fill:var(--cm-accent3)">gated tools dropped</text>
    <text class="cm-svg-sub" x="585" y="239" text-anchor="middle">no operator to ask</text>
    <rect class="cm-svg-box" x="450" y="290" width="270" height="50" rx="8"/>
    <text class="cm-svg-label" x="585" y="312" text-anchor="middle">NATS peers</text>
    <text class="cm-svg-sub" x="585" y="329" text-anchor="middle">subject permissions apply</text>
    <line x1="585" y1="170" x2="585" y2="194" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sv-ah)"/>
    <line x1="585" y1="250" x2="585" y2="284" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sv-ah)"/>
  </svg>
  <figcaption>The same selection, two confirmation policies, decided by whether a human can be reached.</figcaption>
</figure>

Both servers cap concurrency at 2 and per-call time at 30 seconds by default, for the same stated reason: an external
caller has no iteration budget the way the agent loop does, so an ungated path could spawn unbounded concurrent commands.

The knobs are nonetheless separate, because they bound different trust boundaries. MCP bounds calls from anything that
reaches the TCP port, and the address may be set to a non-loopback interface. A2A bounds NATS peers, which already passed
account authentication.

## MCP

The transport is streamable HTTP, and only that. There is no stdio serving mode anywhere in the tree; clients connect
with an HTTP transport, and the server prints a copy-pasteable `claude mcp add --transport http` hint on startup.

Address resolution is three-tier: flag or environment, then `expose.agent.mcp` port and address, then 8080 on 127.0.0.1.
The loopback default is deliberate.

Three settings are warned about because they have no effect over MCP: a missing `application_path`, `human_in_the_loop`,
and `memory`. Warning beats silently ignoring an operator's intent.

### Shutdown ordering

`http.Server.Shutdown` neither cancels request contexts nor closes hanging server-sent-event streams, so a single Ctrl-C
appeared to hang. The fix is a cancelable connection context used as the server's `BaseContext`: shutdown cancels that
first to unblock held-open GETs, then calls `Shutdown` with a five second budget.

### The elicitation gate

There is no local operator, so approval is requested from the client through MCP elicitation with a single-required-boolean
form.

The session's negotiated capability is read at call time rather than cached, so a client that cannot be elicited is
detected per call. The two minute elicitation timeout is applied outside the run timeout and before the semaphore is
acquired, so a human deliberating holds no execution slot.

Everything except an explicit approval under an accept action fails closed: a decline, a dismissal, a non-boolean answer,
or an elicitation error all deny.

{{% notice style="warning" title="Load-bearing decision" %}}
`confirm_over_mcp: never`, and any client that does not support elicitation, runs gated tools ungated. Both serving modes
say the same thing in their source comments: `ai:deny` is the reliable exclusion, and confirmation is not an enforcement
boundary. Anything that must be unreachable needs the tag, not the gate. See
[Tools and introspection]({{% relref "tools" %}}).
{{% /notice %}}

### Annotations

`toolAnnotations` maps a tool onto the MCP annotation object, which carries a readable title alongside the protocol's
behavioral hints.

What a command says about itself travels by a different route. Both serving paths register with `ModelDescription()`
rather than the plain `Description()`, so every surviving tag is appended to the description as a trailing `Tags:` line
and reaches the peer as text. `fisk info` is the only surface that takes the plain description and gives tags a column of
their own.

Approval is deliberately not an annotation. Annotations are advisory hints a client may ignore, not a control channel, so
expressing the confirm gate as one would imply an enforcement it does not have.

### Built-ins over MCP

Two gates apply and both must pass. Each tool declares whether it may ever be served on a surface, and the operator's
`builtins` allowlist selects which of those to serve, validated by name at config parse time. The declaration is the
ceiling and the allowlist can only narrow it, so a tool added alongside an allowlisted one is never served on its
neighbour's entry. Only `knowledge_search` declares MCP exposure; the memory and human-in-the-loop built-ins need
operator state or interaction, so they declare none.

The declaration is default-deny: a new tool is served nowhere until someone says otherwise, and `mustNew` refuses to
build a built-in that has not stated a posture, so a harness tool cannot arrive at "nowhere" by forgetting.

Every kind registers through one list and one handler. The caller orders wrapped-application commands ahead of built-ins,
and the first tool to claim a name keeps it, so a built-in can never shadow a command tool and strip its confirm gate. A
collision is a hard error in `Serve`; `BuildServer` skips the loser as a library-level backstop for a caller that
bypasses `Serve`. The reason is the same as everywhere else: the model addresses every tool by one flat name.

A tool that cannot report whether it is confirmation-gated is refused rather than served ungated, since an absent gate
must not be indistinguishable from not needing one. Built-in calls share the semaphore and the timeout, and receive a
default-deny prompter, since nothing on that path can prompt.

Both kinds return a `toolkit.Outcome`, and its exec metadata decides the rendering: a command's output is wrapped in the
`CommandResult` envelope carrying the exit code, while an in-process tool's output is already the JSON the client asked
for and travels verbatim.

## A2A

`ModeServer` additionally requires `application_path` and `nats_context`. The server itself accepts any tool kind, but no
built-in declares A2A exposure today, so an application-less A2A server would start with an empty tool set. The
requirement is an earlier, clearer version of that empty-set error rather than a correctness gate, and it is deletable
once a built-in first opts in.

`selectExposed` drops five classes of tool, logging a reason for each: tools that do not declare A2A exposure, tools that
cannot report whether they are confirmation-gated, confirm-gated tools, invalid tool names, and tools with an empty
model-facing description. The exposure check is the ceiling the rest sit under. A tool that cannot answer the
confirmation question is refused rather than served ungated, since over an interface the absence of a gate would
otherwise be indistinguishable from not needing one. A description-less tool gives the model nothing to decide on, so it
is dropped by the server and independently refused by the importer.

There is no A2A equivalent of the MCP `builtins` allowlist, which is why no built-in declares A2A exposure: without a
selection mechanism, declaring one would serve it the moment an operator enables A2A, with nothing to narrow it.

A gated tool is dropped from both the agent card and the dispatch map, so it is unadvertised and uninvokable rather than
merely hidden.

The agent card is precomputed at construction. No work happens per discovery request, and the exposure decision cannot
drift between what the card advertises and what dispatch will accept.

### The NATS binding

| Aspect | Value |
|--------|-------|
| Subject prefix | `choria.fisk-ai`, deliberately inside the existing Choria subject space |
| Discovery subject | `choria.fisk-ai.discovery.<identity>` |
| Tool subject | `choria.fisk-ai.tool.<identity>` |
| Streams | None. No JetStream, no KV, no consumers |
| Pattern | Core request-reply through a `micro` service |
| Queue group | The identity, so processes sharing it share load |
| Request timeout | 30 seconds when neither the caller's context nor the transport config carries a deadline |

The subject split is a permission boundary. A NATS account can grant publish on `choria.fisk-ai.discovery.*` without
granting `choria.fisk-ai.tool.*`, which is discovery without execution.

The `micro` service registers a fixed version of `0.0.0`. That is service metadata only; the agent card carries the real
version, which keeps the transport out of the versioning business.

Transport errors travel in reply headers rather than the body, because that is what `micro` does with a service error.

### Back-pressure is structural

The semaphore is acquired on the serving goroutine, before a worker is spawned. If the slot were taken inside the worker,
intake would continue and requests would pile up in memory. Acquiring first means `micro` stops reading, so pressure
reaches the transport. A test proves a second request never enters until the first releases its slot.

Each request gets exactly one reply. The replier is single-shot and stays valid after the handler returns, so the spawned
worker can answer.

## The message model

There is no separate envelope. Every message is one flat JSON object, because `Header` is an embedded struct whose fields
marshal at the top level. A captured message is therefore fully self-describing outside any transport.

Framing is filled by two unexported functions. `stampRequest` mints one id and assigns it to the message id, the request
id, and the conversation id, sets sequence to zero, and names the sender. `stampReply` mints a fresh id, echoes the
request and conversation ids, and swaps sender and recipient.

Sequence is always zero on the live paths, because a direct tool or discovery call is not part of a larger task and the
transport's reply inbox handles correlation.

Blocks synthesize their type discriminator at marshal time from the Go type, so the two can never disagree. An empty block
marshals to an error rather than `null`, and an unrecognized type fails to unmarshal.

{{% notice style="warning" title="Load-bearing decision" %}}
Stamping is header population, not signing. There is no cryptographic signature, MAC, or verification of A2A messages
anywhere. `Header.Sender` and `Header.Recipient` are unauthenticated assertions used for logging and reply stamping, never
for authorization. What stops a forged sender from redirecting a reply is that the replier targets only the inbox the
transport supplied; a destination is never taken from the message body. All authentication, authorization, and
confidentiality are delegated to NATS: the credentials in the named context, account and subject permissions, and TLS.
{{% /notice %}}

`ThinkingBlock.Signature` is carried for display and audit only and is never replayed into a model across the agent
boundary, because a provider-specific signature is not portable.

### Schema validation

Eleven embedded schemas, one per protocol id plus a shared definitions file. Both sides validate in both directions, and
the transport never decodes anything.

Two structural choices carry the weight:

- **`unevaluatedProperties: false` combined with `allOf` and a header `$ref`.** A plain `additionalProperties: false`
  would reject the header fields the reference contributes. `unevaluatedProperties` accounts for them and still rejects
  genuinely unknown keys.
- **`protocol` is a `const` per file.** The schema selected by a protocol id also asserts that id, so a mislabeled body
  cannot validate against the wrong schema. `expectProtocol` then re-checks on arrival rather than trusting the subject.

The identity pattern is enforced in the schema as well as in config, in the server, in the MCP name check, and in the
importer. Since identity keys the NATS subjects, that also prevents subject injection through a sender name.

Messages are size-capped at 768 KiB before decoding on both sides, chosen to sit under the NATS 1 MiB default with framing
headroom. A hostile peer cannot force a large allocation.

Tool input must be an object when present, which is why the client drops a `null` input: models emit `null` for
no-argument tools, and the server treats absent input as an empty object.

## Consuming another agent's tools

`remote_tools` names hosts whose tools are imported into the local agent. Naming is globally deterministic rather than
first-come: three passes count bare names across all hosts, compute final names, count final names, and only then build.

That buys four properties, each tested:

- A bare name is prefixed if a local tool holds it, or if more than one host exposes it, so two hosts sharing a name are
  prefixed symmetrically.
- Residual collisions are found by counting, so both colliding tools are skipped rather than one arbitrarily winning.
- The same inputs produce the same names between runs, which matters because the tool set is fingerprinted for resume.
- The remote's own name travels on the wire while the model sees the possibly-prefixed local name.

A remote result is reconstructed into the same `CommandResult` shape a local command returns, so the model sees
byte-identical JSON either way, and a non-zero exit code stays a successful result rather than a tool error.

Remote tools are never confirm-gated locally. `functool.New` refuses a spec that is both remote and gated, and no local
required-argument validation runs on a remote tool, because authority sits with the serving agent and the two ends must
not disagree.

A run is strict and `info` is lenient. `ImportForRun` aborts on any host error or collision, because the prompt may depend
on the missing tools. `DiscoverForInfo` warns and still renders local tools so `info` works offline.

Discovery carries no tags. That single fact is why a tag-based include filter on a remote host is warned about and
ignored, and a tag-based exclude is rejected at config parse time: an exclude that cannot be honored would silently leave
an unwanted tool imported.

{{% notice style="warning" title="There is no MCP client" %}}
The symmetry a reader will assume does not exist. Fisk AI serves over MCP but never consumes MCP. There is no MCP client
transport, no `mcp_servers` config key, and no adapter from a remote MCP tool to a local one; the MCP SDK is imported by
exactly one package and its test. The only way a foreign capability becomes a local tool is A2A `remote_tools` against
another Fisk AI agent.
{{% /notice %}}

## In-band versus transport errors

The separation is strict on both sides. An application-level failure, whether an unknown tool, a failed command, or a
denied call, is always a normal reply with an error flag set. Only a malformed, mistyped, or oversized message is a
transport error.

A Go error returned from an MCP tool handler would become a protocol-level error the client cannot reason about, which is
why an unknown tool over A2A is answered in-band too.

## Reserved and aspirational

The streaming task flow is defined but not wired. `Request`, `Event`, `Result`, `ErrorMessage`, `Cancel`, `Ack`, and all
six block types have constructors, schemas, and round-trip tests, but no transport path sends or receives them. The route
hint has only discovery and tool operations, and the server registers only those two handlers.

Also declared and unused:

- `Header.MustUnderstand` is schema-valid and never read.
- `Header.Parent` is reserved for multi-hop correlation and never set.
- `Header.Recipient.Instance` is schema-supported and never populated.
- `config.RemoteAgent` has no field on `Config` and no reader. It is the placeholder for delegating a whole task, as
  opposed to importing tools.
- `AgentCard.Description` is never set, so `discover` always renders it absent.
- `TransportConfig.Options` exists and is strictly decoded, but the NATS binding has no options at all.
- The transport registry is complete, but `A2ATransport()` is hardwired to `nats`.
- `conns.Option` is documented as the extension point for a Choria connection kind; today the memory and session stores
  take a raw connection from the provider.

{{% notice style="tip" title="Next" %}}
[Terminal and events]({{% relref "terminal" %}}) covers the surface an operator actually watches during a run.
{{% /notice %}}
