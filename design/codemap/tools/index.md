# Tools and introspection

A fisk application is introspected once into a set of tools. Peer agents, MCP servers, harness built-ins and caller-supplied Go functions land in the same namespace, and the model addresses every one of them by a single flat name.

{{% notice style="note" title="Where it lives" %}}
`internal/toolkit` holds the vocabulary every kind shares: the `Tool` interface, tag behavior, the confirm predicate, the prompter and deferral. `internal/toolkit/fisk` turns a CLI command tree into tools, `internal/toolkit/functool` backs everything else, and `internal/toolkit/builtin` holds the harness's own. `internal/mcpclient` and `internal/remotetools` import from elsewhere.
{{% /notice %}}

## One namespace, five sources

<figure class="cm-diagram">
  <svg viewBox="0 0 760 330" role="img" aria-label="Five tool sources filtered and named into one flat namespace offered to the model">
    <defs>
      <marker id="tool-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="25" width="170" height="40" rx="8"/>
    <text class="cm-svg-label" x="105" y="44" text-anchor="middle">fisk app</text>
    <text class="cm-svg-sub" x="105" y="59" text-anchor="middle">command tree</text>
    <rect class="cm-svg-box" x="20" y="75" width="170" height="40" rx="8"/>
    <text class="cm-svg-label" x="105" y="94" text-anchor="middle">built-ins</text>
    <text class="cm-svg-sub" x="105" y="109" text-anchor="middle">memory, knowledge</text>
    <rect class="cm-svg-box" x="20" y="125" width="170" height="40" rx="8"/>
    <text class="cm-svg-label" x="105" y="144" text-anchor="middle">a2a peers</text>
    <text class="cm-svg-sub" x="105" y="159" text-anchor="middle">prefix on clash</text>
    <rect class="cm-svg-box" x="20" y="175" width="170" height="40" rx="8"/>
    <text class="cm-svg-label" x="105" y="194" text-anchor="middle">MCP servers</text>
    <text class="cm-svg-sub" x="105" y="209" text-anchor="middle">always prefixed</text>
    <rect class="cm-svg-box" x="20" y="225" width="170" height="40" rx="8"/>
    <text class="cm-svg-label" x="105" y="244" text-anchor="middle">custom tools</text>
    <text class="cm-svg-sub" x="105" y="259" text-anchor="middle">registered last</text>
    <line x1="190" y1="45" x2="243" y2="45" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="190" y1="95" x2="243" y2="95" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="190" y1="145" x2="243" y2="145" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="190" y1="195" x2="243" y2="195" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="190" y1="245" x2="243" y2="245" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="245" y1="45" x2="245" y2="245" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="245" y1="150" x2="294" y2="150" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tool-ah)"/>
    <rect x="300" y="95" width="180" height="110" rx="10" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="390" y="130" text-anchor="middle" style="fill:var(--cm-accent)">flat namespace</text>
    <text class="cm-svg-sub" x="390" y="153" text-anchor="middle">collision aborts the run</text>
    <text class="cm-svg-sub" x="390" y="171" text-anchor="middle">first claim keeps it</text>
    <line x1="480" y1="150" x2="554" y2="150" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tool-ah)"/>
    <rect class="cm-svg-box" x="560" y="125" width="160" height="50" rx="8"/>
    <text class="cm-svg-label" x="640" y="147" text-anchor="middle">model</text>
    <text class="cm-svg-sub" x="640" y="164" text-anchor="middle">one name per tool</text>
    <text class="cm-svg-sub" x="380" y="300" text-anchor="middle">ai:deny is stripped before any include or exclude runs</text>
  </svg>
  <figcaption>Sources are assembled in a fixed order. A name already taken aborts the run rather than shadowing what holds it.</figcaption>
</figure>

## The contract

Kind-specific policy is never folded into `Tool`; consumers reach for narrow capability interfaces instead.

```go
type Tool interface {
	Name() string
	Description() string
	ModelDescription() string
	InputSchema() map[string]any
	Definition(deferLoading bool) llm.ToolDef
	Execute(ctx context.Context, input json.RawMessage, deps ExecDeps) (*Outcome, error)
	MCPExposable() bool
	A2AExposable() bool
}
```

Exposure is the deliberate exception to that rule. It sits in the interface so the compiler forces every new kind to answer it, since a new kind would otherwise reach MCP or a2a with no exposure decision recorded.

A tool that does not implement `BehaviorDescriber` is safe, because consumers fall back to conservative defaults. A tool that cannot answer `Confirmable` is refused by both serving surfaces, because "needs no gate" and "cannot report a gate" must not look the same.

`fisk.FiskCommandTool` runs a subprocess of the wrapped application; `functool.Tool` backs everything else, discriminated by whether its spec names a remote agent or an MCP server.

<dl class="cm-kv">
  <dt>Kind</dt><dd>The accounting axis: application, builtin, remote, custom, mcp, unknown. A log line and a metric label carry the kind.</dd>
  <dt>Presentation</dt><dd>The visibility axis: command, remote, self-rendered, traced. A built-in presents as self-rendered or traced while being one kind; an MCP tool presents as remote while being accounted as MCP.</dd>
  <dt>Outcome</dt><dd>Output plus an optional exec record. The presence of that record is the whole discriminator: a command's output is wrapped in a <code>CommandResult</code> envelope, an in-process tool's is passed through as the JSON the caller asked for.</dd>
</dl>

## Introspecting a fisk application

Running `<binary> --fisk-introspect` returns the command model. Hidden commands and their subtrees are skipped, grouping nodes are not tools, and only leaves become tools, named by joining the command path with underscores. Every leaf must arrive with a precomputed schema or the whole load fails.

The introspection subprocess gets thirty seconds when the caller supplied no deadline, and its output is capped at 16 MiB. Over the limit is a rejection rather than a truncation, because the document has to decode whole.

Model arguments reach the binary only as argv, never through a shell. Exposed global flags are merged into each command's schema by cloning rather than mutating, since the model schema is reused on every request, and they are placed after the command path and before the `--` separator so they always read as flags.

Command output is captured through a single writer on both streams, so stdout and stderr keep their interleaving, and held to a head-and-tail ring within 64 KiB so a runaway command cannot grow the process.

## The reserved tag vocabulary

`ai:deny`, `ai:no_defer` and `ai:confirm` change what the harness does. The behavior tags are advice.

| Tag | Effect |
|---|---|
| `ai:deny` | Stripped before include and exclude run, and it can never be added back on any surface |
| `ai:no_defer` | Always sent to the model directly, never hidden behind tool search |
| `ai:confirm` | Always gated, matched unconditionally, so leaving it out of `confirm_tags` cannot weaken it |
| `ai:read_only`, `ai:destructive`, `ai:additive`, `ai:idempotent` | Carried to clients as advice and nothing more |

Operators gate further tools by listing them under `confirm_tags`, and any tag works, not only `ai:` ones. The trigger reported in the prompt prefers `ai:confirm` when present, and otherwise names the first of the tool's own tags found in the operator's list, in the tool's tag order, so the message is deterministic.

{{% notice style="warning" title="Load-bearing decision" %}}
The behavior vocabulary describes and does not enforce. No behavior tag gates a call, because the tool supplies its own tags and could drop `ai:destructive` to escape the gate. The gate is `ai:confirm` plus the operator's `confirm_tags`; the reliable off switch is `ai:deny`.
{{% /notice %}}

Conflicting tags resolve conservatively and never fail a run: the tags come from a binary the operator often cannot edit, so one mistagged command must not stop everything. `read_only` combined with a write tag loses `read_only`, and `destructive` beats `additive`. Resolution happens after all tags are collected, so it does not depend on order. An unrecognized `ai:` tag is a warning, since a private one is legitimate.

## Collisions

The run builds a `taken` map in a fixed order: application tools, then human-in-the-loop, memory, knowledge, a2a peers, MCP servers, and custom tools last.

| Source | On a clash |
|---|---|
| Built-ins | Abort the run, naming the tool to exclude or rename |
| a2a peers | Prefix with the host alias, but only when a local tool holds the name or more than one host exposes it, and then symmetrically for all of them |
| MCP servers | Always prefix with the server alias, so a name depends only on the server it came from and nothing is renamed when another server's list changes |
| Custom tools | Abort. An injected tool may never shadow anything, and may not claim to be remote or MCP-backed |

The a2a naming decision is a global pass over the whole set rather than a per-host one, and residual collisions are found by counting final names. Both make the outcome independent of discovery order.

## Importing from an MCP server

Connection is per server, in configuration order, with each server's own startup timeout covering transport setup and the handshake. A single failure closes everything already opened. The client advertises nothing: no roots, no sampling handler, so a foreign server cannot spend this agent's model budget, and no elicitation handler.

A third-party tool descriptor is validated before it can reach the model API. A missing name, a schema that is not an object, a root type other than `object`, or a missing description each fail the import, because all definitions travel in one request and one bad descriptor would fail every call in the run.

A run import is strict: any server error or collision fails the run. The discovery path used by `fisk info` is lenient, connects to each server alone, closes as soon as names are read, and returns tools that are structurally not callable.

On a mid-conversation `tools/list_changed` rebuild, a collision is skipped and recorded rather than failing the run, because a third party is editing its own list.

Stdio children inherit the environment minus the credential union, rather than getting a replaced one, because a child with no `PATH` or `HOME` cannot run the servers operators actually wire up. Resolved URL secrets of eight characters or more are replaced everywhere they might be printed, longest first so a value containing another is replaced whole. Configured header names are dropped from any cross-host redirect.

## Deferred loading

Past ten tools, counting deferrable plus built-ins, definitions are deferred and the model reaches them through tool search. Built-ins are never deferred. The threshold is re-evaluated per tool set, so a set that grows starts deferring and one that shrinks stops.

A tool set is immutable. A change arrives as a whole new set published to the source the loop snapshots.

## Serving surfaces gate differently

Over MCP the calling client is asked through elicitation, and anything that is not an explicit approval fails closed. Over a2a, confirm-gated tools are dropped at selection time, because no operator stands behind a served call. Both surfaces refuse a deferred result: the answer would arrive against a session the path does not have.

`functool.New` enforces the matching rules at construction. A remote or MCP-backed tool may not also declare a confirm gate, since gating another party's tool is not this process's to do, and a tool that declares exposure may not be remote, MCP-backed or gated, because re-serving somebody else's tool under this agent's identity needs an operator's explicit opt-in, and a spec has nowhere to record one.

## Not yet wired

Nothing sets a2a exposure on a `functool` spec today, so only fisk command tools reach that surface. There is no a2a builtins allowlist, and declaring exposure without one would serve the tool the moment a2a is enabled, with no operator opt-in.

{{% notice style="tip" title="Next" %}}
Continue to [Model providers]({{% relref "providers" %}}) for how a tool definition is rendered for the API, or [Serving]({{% relref "serving" %}}) for the surfaces that hand these tools to somebody else.
{{% /notice %}}
