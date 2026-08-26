# Architecture

A package's imports place it in one of four layers. The root holds commands and presentation, the middle holds `agent` and `serve`, and the leaves import nothing else from this repository.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 300" role="img" aria-label="Four layers from package main down to leaf packages that import nothing from the tree">
    <defs>
      <marker id="arch-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="60" y="30" width="640" height="50" rx="8"/>
    <text class="cm-svg-label" x="380" y="52" text-anchor="middle">package main</text>
    <text class="cm-svg-sub" x="380" y="70" text-anchor="middle">command registration, flag parsing, terminal presentation</text>
    <line x1="380" y1="80" x2="380" y2="94" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#arch-ah)"/>
    <rect x="60" y="95" width="640" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="117" text-anchor="middle" style="fill:var(--cm-accent)">agent, serve</text>
    <text class="cm-svg-sub" x="380" y="135" text-anchor="middle">run the loop, host it behind channels</text>
    <line x1="380" y1="145" x2="380" y2="159" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#arch-ah)"/>
    <rect class="cm-svg-box" x="60" y="160" width="640" height="50" rx="8"/>
    <text class="cm-svg-label" x="380" y="182" text-anchor="middle">toolkit  llm  memory  rag  runstate  a2a  tasks</text>
    <text class="cm-svg-sub" x="380" y="200" text-anchor="middle">a registry and several backends each</text>
    <line x1="380" y1="210" x2="380" y2="224" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#arch-ah)"/>
    <rect class="cm-svg-box" x="60" y="225" width="640" height="50" rx="8"/>
    <text class="cm-svg-label" x="380" y="247" text-anchor="middle">config  telemetry  util  conns</text>
    <text class="cm-svg-sub" x="380" y="265" text-anchor="middle">import nothing else from this tree</text>
  </svg>
  <figcaption>An arrow is an import. The bottom band is the rule that keeps the middle band free of cycles.</figcaption>
</figure>

## The two hard leaves

`config` imports the standard library, a duration parser and a YAML library. `internal/telemetry` imports the standard library and OpenTelemetry.

Because `config` cannot see the rest of the tree, two lists are hand-maintained duplicates: the OTLP credential variable names, mirrored in `telemetry`, and the built-in tool names that may be exposed over MCP, mirrored on each tool's own spec. Both are pinned by a test assertion so they cannot drift.

Because `telemetry` cannot see the rest of the tree, its constructors take primitives rather than domain types, and its error classes are unforgeable values rather than a classifier over somebody else's sentinels. The HTTP middleware's type is written out longhand rather than named, and the `llm` package declares both halves as type aliases, so the value satisfies the interface without either package importing the other.

## Patterns that repeat

**A registry with backends.** Memory, sessions, tasks, model providers and a2a transports all follow the same shape: a factory registered from `init` under a name, a `RequiresNats` style option so the host resolves a backend's needs without naming any backend, and a `Register` that panics on an empty name, a nil factory or a duplicate. Each registry has one or two implementations today, and adding a third needs no change in the host.

**Failures land at startup.** An unknown backend, a typo in an options block, a bucket with a time-to-live, an unreachable peer, a stale knowledge manifest: each stops the process before the model is contacted, and the error names the key to change. One decoder handles every backend's options block, so no backend can relax the rule.

**Nothing is silently weakened.** A configured confirm tag matching no tool is warned about, because leaving it unreported would give a false sense of safety. A tool-name collision aborts the run rather than shadowing, because shadowing a gated command would strip its gate. A tag-based exclude on a remote host is rejected outright, because discovery carries no tags and the filter could never be honored.

**Untrusted text stays data.** Model-written memories and retrieved documents are fenced, labeled as data rather than instruction, sanitized at write time and sanitized again at render time.

**Closed vocabularies are structs, not strings.** The telemetry error class, the degrade reason and the MCP transport are each a struct wrapping a string, because a string type is convertible from any string and passing an error's own text would compile.

**State is derived, never cached beside its source.** Counters, resume position and the committed conversation are all recomputed from the journal, so they cannot drift from what happened.

**Resources are borrowed.** A run uses every injectable store, connection and session as given, and never closes one. A host builds them once and shares them across runs; a CLI run falls back to building its own.

## Where a decision is enforced

| Concern | Enforced by |
|---|---|
| Which tools exist | The flat namespace built at run start; collisions abort |
| Which tools the model may see | Config include and exclude, after `ai:deny` is stripped unconditionally |
| Which tools need a human | The confirm gate, on the union of the original and rewritten call |
| Which tools reach a peer | The exposure methods on the interface, plus a per-surface allowlist |
| Whether a conversation may continue | The run fingerprint, split into hard, blocking, tools and budget classes |
| What may leave the process on a span | Closed vocabularies and constructors that own their own attribute sets |

## The library standard

The packages under `internal/` are being prepared to leave it, so others can build agents on them. `agent`, `llm`, `telemetry`, `toolkit`, `memory`, `rag`, `runstate`, `util`, `conns`, `serve` and `agenttest` are held to a public standard: names, signatures and doc comments are contracts.

Logic an embedder would have to reimplement does not belong in `package main`; the root holds command registration, flag parsing, presentation and wiring. A library supplies the value and the caller decides what to do with it, so where something is the CLI's business, the library returns it or takes it as a parameter rather than deciding.

`a2a`, `mcpserver`, `serve/asyncjobs` and `tasks` are not there yet and their current APIs are not contracts. `remotetools` and `tui` are not libraries at all: one is the agent's own run-path helper, the other is terminal presentation that happens not to live in the root.

`agenttest` is the embedder-facing test surface, and several of its fakes double as an audit: a compile-time assertion fails if an injectable interface stops being implementable from outside its own package using only exported identifiers.

## Concurrency

One run is one goroutine. Every event, hook and prompt call happens on it, so a per-run sink holds state without locking. MCP advisories arrive on another goroutine and land in a mutex-guarded queue the loop drains where it takes tools for a call. The tool set is an atomic pointer to a whole immutable set.

A host runs many such goroutines with per-channel slot pools. They share the knowledge store, the MCP session set, the model provider, and stores that resolve per-run state per call rather than at construction. Each of those is read-only or safe for concurrent use.

{{% notice style="tip" title="Next" %}}
Start with [Configuration]({{% relref "configuration" %}}), since every entry point begins by parsing a file, then [The agent loop]({{% relref "agent-loop" %}}) and [Tools and introspection]({{% relref "tools" %}}).
{{% /notice %}}
