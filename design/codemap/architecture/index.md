# Architecture

Fisk AI is a single Go module, `github.com/choria-io/fisk-ai`, built on Go 1.26. The code separates cleanly into a thin command layer, three drivers that each expose the tools a different way, a shared core, and a persistence and protocol tier underneath. A handful of interface seams keep those tiers independent.

## The layers

The top layer is `package main` at the repository root: `main.go` registers the subcommands and each `*_command.go` file owns one of them. It does flag wiring, mode selection, the signal and suspend contract, and the choice between the full-screen and line UIs. It holds no run logic of its own.

Below it are three drivers. `internal/agent` runs the agentic loop for `run`. `internal/mcpserver` serves the tools over MCP for `mcp`. `internal/a2anats` serves them over NATS for `a2a`. All three consume the same tool set from the shared core.

The core is `internal/util` plus `config`. `util` owns command-tree introspection, translation of tools into Anthropic API parameters, the built-in tools, the confirm gate, the prompter contract, the model call, and run statistics. `config` is the single `agent.yaml` schema and its mode-based validation.

The bottom tier is durable state and protocol types: `internal/runstate` journals a run, `internal/memory` persists model notes, `internal/rag` holds the SQLite knowledge index behind the `knowledge_search` tool, and the `a2a` package holds the transport-agnostic protocol messages that `internal/a2anats` binds to NATS. `internal/remotetools` sits beside the core as the import-policy layer for tools pulled from a peer agent.

Like memory, `internal/rag` is reached only through a built-in tool in the core: `../../../../internal/toolkit/builtin/builtin_rag.go` opens a `rag.Store` and wraps it as `knowledge_search`. The agent and MCP drivers open the store read-only while the `knowledge` command is its single writer, so an index can be rebuilt while an agent runs. It is the one package in this tier that reaches an external system of its own, an optional local embeddings server, and only when the vector tier is on.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 334" role="img" aria-label="Five layers from CLI commands down to external systems, each depending on the one below">
    <defs>
      <marker id="ad" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/>
      </marker>
    </defs>
    <!-- layer 1: CLI -->
    <rect class="cm-svg-box" x="70" y="16" width="620" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="42" text-anchor="middle">CLI commands (package main)</text>
    <text class="cm-svg-sub"   x="380" y="59" text-anchor="middle">run, session, info, knowledge, mcp, a2a, discover</text>
    <!-- layer 2: drivers -->
    <rect class="cm-svg-box" x="70" y="78" width="620" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="104" text-anchor="middle">Drivers</text>
    <text class="cm-svg-sub"   x="380" y="121" text-anchor="middle">internal/agent, internal/mcpserver, internal/a2anats</text>
    <!-- layer 3: core (accent) -->
    <rect x="70" y="140" width="620" height="54" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="165" text-anchor="middle" style="fill:var(--cm-accent)">Core: internal/util + config</text>
    <text class="cm-svg-sub"   x="380" y="183" text-anchor="middle">introspection, tool params, built-ins, confirm gate, prompter</text>
    <!-- layer 4: persistence and protocol -->
    <rect class="cm-svg-box" x="70" y="210" width="620" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="236" text-anchor="middle">Persistence and protocol</text>
    <text class="cm-svg-sub"   x="380" y="253" text-anchor="middle">internal/runstate, internal/memory, internal/rag, a2a</text>
    <!-- layer 5: externals -->
    <rect class="cm-svg-box" x="70" y="272" width="620" height="46" rx="8"/>
    <text class="cm-svg-label" x="380" y="298" text-anchor="middle">External systems</text>
    <text class="cm-svg-sub"   x="380" y="315" text-anchor="middle">fisk binaries (exec), Anthropic API, NATS, embeddings server</text>
    <!-- downward dependency arrows -->
    <line x1="380" y1="62"  x2="380" y2="76"  stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="124" x2="380" y2="138" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="194" x2="380" y2="208" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
    <line x1="380" y1="256" x2="380" y2="270" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#ad)"/>
  </svg>
  <figcaption>Dependencies point down. The three drivers share the core; nothing in the core reaches back up into a driver or a UI.</figcaption>
</figure>

## The seams that keep it composable

The tiers stay independent because the boundaries between them are narrow interfaces, not concrete types. Each one lets a driver swap an implementation without the core knowing.

<dl class="cm-kv">
  <dt>Events</dt><dd>`internal/agent/events.go`. The agent decides what happened; the caller decides how it looks. `cliEvents` renders line output, `tcellEvents` drives the full-screen UI, both from the same callbacks.</dd>
  <dt>Prompter</dt><dd>`internal/util/prompter.go`. The only path allowed to read the terminal. A line implementation (`survey_prompter.go`) and a full-screen one (`internal/tui/prompter.go`) satisfy it; deny-by-default lives at the caller, never in the prompter.</dd>
  <dt>Store / Journal</dt><dd>`internal/runstate/store.go`. A run is an append-only record stream behind a pluggable backend registry; the `file` backend exists, a JetStream backend is the planned second. `Fold` turns records into resumable state with no IO.</dd>
  <dt>memory.Store</dt><dd>`internal/memory/store.go`. A pluggable key/value store; the `file` backend exists, a NATS KV backend is the planned second.</dd>
  <dt>rag.Embedder</dt><dd>`internal/rag/embed.go`. The knowledge vector-tier seam; the OpenAI-compatible client is the only implementation, tests mock it, and a nil embedder is the lexical-only path, so the vector tier is fully optional.</dd>
  <dt>RemoteInvoker</dt><dd>`internal/util/remote.go`. A one-method interface so `util` depends only on `a2a` types, not the NATS binding, which avoids an import cycle and lets tests supply a fake.</dd>
</dl>

## One configuration, three modes

A single `agent.yaml` drives all three faces. `config.ParseConfigForMode` validates it against `ModeAgent`, `ModeMCP`, or `ModeServer`, each requiring a different subset of fields. The `info` command deliberately parses with the most lenient mode so it can inspect a config written for any face.

## How a run composes

The `run` path threads every tier together in a fixed order.

<ol class="cm-steps">
  <li><b>Parse and select tools</b> `main` parses the config, then `util.LoadTools` introspects the fisk binary, strips `ai:deny`, and applies include and exclude rules.</li>
  <li><b>Set up the run</b> `agent.Run` injects the built-in tools, imports any remote tools, builds the Anthropic tool params, constructs the confirm gate, and opens or resumes the journal.</li>
  <li><b>Drive the loop</b> The `runner` calls the model, runs the tools it asks for through `util.ExecuteToolUse` behind the confirm gate, and journals each event as it happens.</li>
  <li><b>Surface it</b> Every step is reported through `Events` to whichever UI is active, and the operator answers gates through the `Prompter`.</li>
</ol>

{{% notice style="warning" title="Load-bearing decision" %}}
The core never imports a driver or a UI. `internal/util` and `config` depend downward only. This is what lets the same tool model and the same safety guarantees back the agent loop, the MCP server, and the A2A server without duplication.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [Tools and Introspection]({{% relref "tools" %}}) to see how a command tree becomes the tool model at the center of this diagram.
{{% /notice %}}
