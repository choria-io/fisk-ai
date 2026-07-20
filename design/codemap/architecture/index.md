# Architecture

Fisk AI is a single Go module, `github.com/choria-io/fisk-ai`, built on Go 1.25. The code separates into a thin command layer, three drivers that each expose the tools a different way, the tool model those drivers share, and a tier of subsystems and contracts underneath. Narrow interface seams keep the tiers independent, and several of them are pluggable at runtime.

## The layers

The top layer is `package main` at the repository root: `main.go` registers the subcommands and each `*_command.go` file owns one of them. It does flag wiring, mode selection, the signal and suspend contract, and the choice between the full-screen and line UIs. It holds no run logic of its own.

Below it are three drivers. `internal/agent` runs the agentic loop for `run`. `internal/mcpserver` serves the tools over MCP for `mcp`. `internal/a2a` serves them to peer agents for `a2a`, with `internal/a2a/nats` binding that protocol to NATS. `internal/remotetools` sits beside them as the import-policy layer for tools pulled from a peer.

The center is the tool model. `internal/toolkit` defines the contracts every tool kind satisfies, `internal/toolkit/fisk` introspects and runs the wrapped application's commands, and `internal/toolkit/builtin` holds the tools Fisk AI implements itself. `config` is the single `agent.yaml` schema and its mode-based validation.

The base is four packages that import nothing else internal: `internal/toolkit`, `internal/runstate`, `internal/conns`, and `config`. Everything else layers on top of them.

{{% notice style="note" title="The toolkit tree is not one layer" %}}
A shared import prefix does not mean a shared position in the graph. `internal/toolkit` is at the base, but `internal/toolkit/fisk` sits above `internal/util`, and `internal/toolkit/builtin` sits above `internal/memory` and `internal/rag` because it wraps both as tools. Reading "toolkit" as a single tier inverts real dependency edges. Derive the graph with `go list -deps` rather than from the directory names.
{{% /notice %}}

`internal/util` is no longer the core it once was. It kept the cross-cutting helpers that have no better home: the confirm gate, the model call, run statistics and tracing, terminal detection and sanitization, markdown rendering, and the Anthropic request shaping in `anthropic.go`. It defines no tools and owns no prompter; both moved into `internal/toolkit`.

Like memory, `internal/rag` is reached only through a built-in tool: `internal/toolkit/builtin/builtin_rag.go` opens a `rag.Store` and wraps it as `knowledge_search`. The agent and MCP drivers open the store read-only while the `knowledge` command is its single writer, so an index can be rebuilt while an agent runs. It is the one subsystem that reaches an external system of its own, an optional local embeddings server, and only when the vector tier is on.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 470" role="img" aria-label="Seven layers from CLI commands down to external systems, each depending on the one below">
    <defs>
      <marker id="ad" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/>
      </marker>
    </defs>
    <!-- layer 1: CLI -->
    <rect class="cm-svg-box" x="40" y="16" width="680" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="42" text-anchor="middle">CLI commands (package main)</text>
    <text class="cm-svg-sub"   x="380" y="59" text-anchor="middle">run, session, info, knowledge, mcp, a2a, discover</text>
    <!-- layer 2: drivers -->
    <rect class="cm-svg-box" x="40" y="78" width="680" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="104" text-anchor="middle">Drivers and transport bindings</text>
    <text class="cm-svg-sub"   x="380" y="121" text-anchor="middle">internal/agent, internal/mcpserver, internal/remotetools, internal/a2a/nats</text>
    <!-- layer 3: tool kinds and protocol engine -->
    <rect class="cm-svg-box" x="40" y="140" width="680" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="166" text-anchor="middle">Protocol engine and built-in tools</text>
    <text class="cm-svg-sub"   x="380" y="183" text-anchor="middle">internal/a2a, internal/toolkit/builtin</text>
    <!-- layer 4: command tools, subsystems, UI -->
    <rect class="cm-svg-box" x="40" y="202" width="680" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="228" text-anchor="middle">Command tools, subsystems, terminal UI</text>
    <text class="cm-svg-sub"   x="380" y="245" text-anchor="middle">internal/toolkit/fisk, internal/memory, internal/rag, internal/tui</text>
    <!-- layer 5: shared helpers -->
    <rect class="cm-svg-box" x="40" y="264" width="680" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="290" text-anchor="middle">Shared helpers</text>
    <text class="cm-svg-sub"   x="380" y="307" text-anchor="middle">internal/util: confirm gate, model call, tracing, sanitization</text>
    <!-- layer 6: base (accent) -->
    <rect x="40" y="326" width="680" height="46" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="352" text-anchor="middle" style="fill:var(--cm-accent)">Contracts and state (no internal dependencies)</text>
    <text class="cm-svg-sub"   x="380" y="369" text-anchor="middle">internal/toolkit, internal/runstate, internal/conns, config</text>
    <!-- layer 7: externals -->
    <rect class="cm-svg-box" x="40" y="388" width="680" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="414" text-anchor="middle">External systems</text>
    <text class="cm-svg-sub"   x="380" y="431" text-anchor="middle">fisk binaries (exec), Anthropic API, NATS, embeddings server</text>
    <!-- downward dependency arrows -->
    <line x1="380" y1="62"  x2="380" y2="76"  stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="124" x2="380" y2="138" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="186" x2="380" y2="200" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="248" x2="380" y2="262" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="310" x2="380" y2="324" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="372" x2="380" y2="386" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
  </svg>
  <figcaption>Dependencies point down. Registration imports are the deliberate exception: they run upward from the top two layers so no core package names a concrete backend.</figcaption>
</figure>

## The seams that keep it composable

The tiers stay independent because the boundaries between them are narrow interfaces, not concrete types. Each one lets a caller swap an implementation without the other side knowing.

<dl class="cm-kv">
  <dt>toolkit.Tool</dt><dd>`internal/toolkit/tool.go`. The contract every tool kind satisfies, so the runner dispatches over one map without knowing what kind it holds. `Confirmable` and `ArgumentValidator` beside it are narrow capability interfaces the runner type-asserts on, which keeps kind-specific policy out of the main contract.</dd>
  <dt>Events</dt><dd>`internal/agent/events.go`. The agent decides what happened; the caller decides how it looks. `cliEvents` renders line output, `tcellEvents` drives the full-screen UI, both from the same callbacks.</dd>
  <dt>Prompter</dt><dd>`internal/toolkit/prompter.go`. The only path allowed to read the terminal. A line implementation (`survey_prompter.go`) and a full-screen one (`internal/tui/prompter.go`) satisfy it; deny-by-default lives at the caller, never in the prompter.</dd>
  <dt>Store / Journal</dt><dd>`internal/runstate/store.go`. A run is an append-only record stream behind a pluggable backend registry; the `file` backend exists, a JetStream backend is the planned second. `Fold` turns records into resumable state with no IO.</dd>
  <dt>memory.Store</dt><dd>`internal/memory/store.go`. A pluggable key/value store; the `file` backend exists, a NATS KV backend is the planned second.</dd>
  <dt>a2a.Transport</dt><dd>`internal/a2a/transport.go`. The wire binding the protocol engine rides on, selected by name from a registry. The engine owns validation and back-pressure; a transport only moves bytes.</dd>
  <dt>rag.Embedder</dt><dd>`internal/rag/embed.go`. The knowledge vector-tier seam; the OpenAI-compatible client is the only implementation, tests mock it, and a nil embedder is the lexical-only path, so the vector tier is fully optional.</dd>
  <dt>RemoteInvoker</dt><dd>`internal/a2a/remote.go`. A one-method interface so a remote tool depends only on the ability to invoke, not on a client or a transport, which lets tests supply a fake.</dd>
</dl>

Three of these are selected by name at runtime rather than chosen in code: session stores, memory backends, and a2a transports each have a registry and a backend package that registers itself from `init`. The pages for [Sessions and Resume]({{% relref "sessions" %}}), [Memory]({{% relref "memory" %}}), and [MCP and A2A]({{% relref "interop" %}}) each cover their own.

`internal/conns` is the one genuinely new cross-cutting package. It is not a registry. It is a shared provider that establishes a connection once and hands it to whichever backend needs it, so backends do not each dial their own. Ownership is explicit and one-directional: the provider owns the connection, a transport borrows it, and only the command that created the provider closes it.

## One configuration, three modes

A single `agent.yaml` drives all three faces. `config.ParseConfigForMode` validates it against `ModeAgent`, `ModeMCP`, or `ModeServer`, each requiring a different subset of fields. The `info` command deliberately parses with the most lenient mode so it can inspect a config written for any face.

Session storage is the exception that has not landed yet. It has no `agent.yaml` block; `config.SessionConfigFromStateDir` synthesizes one from `--state-dir` at boot, which is deliberately the same construction path a future YAML block will use.

## How a run composes

The `run` path threads every tier together in a fixed order.

<ol class="cm-steps">
  <li><b>Parse and select tools</b> `main` parses the config, then `fisk.LoadTools` introspects the fisk binary, strips `ai:deny`, and applies include and exclude rules.</li>
  <li><b>Set up the run</b> `agent.Run` injects the built-in tools, imports any remote tools, builds the Anthropic tool params, constructs the confirm gate, and opens or resumes the journal.</li>
  <li><b>Drive the loop</b> The `runner` calls the model and runs the tools it asks for. `executeTool` looks the name up in one map, checks the capability interfaces, then calls `ExecuteUse` on whatever kind it found.</li>
  <li><b>Surface it</b> Every step is reported through `Events` to whichever UI is active, and the operator answers gates through the `Prompter`.</li>
</ol>

{{% notice style="warning" title="Load-bearing decision" %}}
No package below a driver imports a driver or a UI. The one upward edge is a blank import for registration, and it is confined to `package main`, `internal/agent`, and `internal/remotetools` so that linking in a backend stays a decision of the program being built, not of the core. This is what lets the same tool model and the same safety guarantees back the agent loop, the MCP server, and the A2A server without duplication.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [Tools and Introspection]({{% relref "tools" %}}) to see how a command tree becomes the tool model at the center of this diagram.
{{% /notice %}}
