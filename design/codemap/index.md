# Code Map

Fisk AI turns any [fisk](https://github.com/choria-io/fisk)-based command-line application into a safety-first LLM agent. It introspects the application's command tree, exposes the allowed commands as tools, and runs an agent loop against the Anthropic API that calls those tools to satisfy a prompt. This code map explains how that is built.

{{% notice style="note" title="Snapshot" %}}
Generated 2026-07-20 against commit `60d1f94` on branch `main`. Commits after this one may make parts of this map stale.

Create a Code Map for your code using the [Choria Codemap Plugin for Claude Code](https://github.com/choria-io/agent-plugins)
{{% /notice %}}

## The mental model

There is one core and several faces. The core is the tool model: a fisk application is introspected once, its runnable leaf commands become named tools, and reserved tags plus include and exclude rules decide which of them an LLM may ever see. That same tool set is then consumed three ways. The `run` command wraps it in an agent loop that calls the model, runs the tools the model asks for, and feeds results back under a budget. The `mcp` command serves the tools over the Model Context Protocol for an external client. The `a2a` command serves them to peer agents. Safety is not a layer on top; it is built into the core, so every face inherits the same guardrails: commands run through `exec` with an argument vector rather than a shell, output is capped, credentials are stripped from the child environment, and a tagged command can require a human to approve it before it runs.

A second axis runs underneath. The core is fixed, but the edges are swappable: where a run is journaled, where the model's notes are kept, and which wire the agent-to-agent protocol rides on are each chosen by name at startup from a registry, with the implementation linked into the binary by import. Only one implementation of each exists today, so the seams matter less for what they currently offer than for where the code draws its boundaries.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 360" role="img" aria-label="A fisk application is introspected into a shared tool model that is served three ways, over pluggable backends">
    <defs>
      <marker id="ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- source: the fisk application -->
    <rect class="cm-svg-box" x="18" y="112" width="150" height="76" rx="8"/>
    <text class="cm-svg-label" x="93" y="146" text-anchor="middle">Fisk CLI app</text>
    <text class="cm-svg-sub"   x="93" y="164" text-anchor="middle">command tree</text>
    <!-- core: the tool model -->
    <rect x="206" y="112" width="176" height="76" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="294" y="146" text-anchor="middle" style="fill:var(--cm-accent)">Tool model</text>
    <text class="cm-svg-sub"   x="294" y="164" text-anchor="middle">commands as tools</text>
    <!-- faces -->
    <rect class="cm-svg-box" x="430" y="40"  width="150" height="46" rx="8"/>
    <text class="cm-svg-label" x="505" y="67" text-anchor="middle">Agent loop</text>
    <rect class="cm-svg-box" x="430" y="127" width="150" height="46" rx="8"/>
    <text class="cm-svg-label" x="505" y="154" text-anchor="middle">MCP server</text>
    <rect class="cm-svg-box" x="430" y="214" width="150" height="46" rx="8"/>
    <text class="cm-svg-label" x="505" y="241" text-anchor="middle">A2A server</text>
    <!-- endpoints -->
    <rect class="cm-svg-box" x="610" y="40"  width="132" height="46" rx="8"/>
    <text class="cm-svg-label" x="676" y="67" text-anchor="middle">Anthropic API</text>
    <rect class="cm-svg-box" x="610" y="127" width="132" height="46" rx="8"/>
    <text class="cm-svg-label" x="676" y="154" text-anchor="middle">MCP client</text>
    <rect class="cm-svg-box" x="610" y="214" width="132" height="46" rx="8"/>
    <text class="cm-svg-label" x="676" y="241" text-anchor="middle">Peer agents</text>
    <!-- edges: source to core -->
    <line x1="168" y1="150" x2="204" y2="150" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <!-- edges: core fans out to the three faces -->
    <line x1="382" y1="140" x2="428" y2="62"  stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <line x1="382" y1="150" x2="428" y2="150" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <line x1="382" y1="160" x2="428" y2="238" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <!-- edges: faces to endpoints -->
    <line x1="580" y1="57" x2="608" y2="57" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <line x1="608" y1="71" x2="580" y2="71" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3" marker-end="url(#ah)"/>
    <line x1="580" y1="150" x2="608" y2="150" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <line x1="580" y1="237" x2="608" y2="237" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ah)"/>
    <!-- pluggable backends band -->
    <rect class="cm-svg-box" x="206" y="292" width="374" height="48" rx="8" stroke-dasharray="5 4"/>
    <text class="cm-svg-label" x="393" y="314" text-anchor="middle">Pluggable backends</text>
    <text class="cm-svg-sub"   x="393" y="331" text-anchor="middle">session store, memory, a2a transport</text>
    <!-- dashed connectors down to the backends band -->
    <line x1="294" y1="188" x2="294" y2="290" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3"/>
    <line x1="505" y1="260" x2="505" y2="290" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3"/>
  </svg>
  <figcaption>One tool model, three faces. The agent loop is the only face that talks to the model; the dashed edge is the model's response feeding the next iteration. The dashed band is chosen by name at startup, not compiled in.</figcaption>
</figure>

## What a run looks like

The default `run` face drives the loop until the model produces a final answer or a budget stops it. Only the answer reaches stdout; traces and the run summary go to stderr, so the output stays safe to pipe.

<div class="cm-terminal">
  <div class="cm-tbar"><i class="r"></i><i class="y"></i><i class="g"></i></div>
  <div class="cm-tbody">$ fisk-ai run --tool-output --no-tui 'how many consumers on the busiest stream?'
<span class="c-tool">-&gt; stream_ls</span>  <span class="c-ok">ok</span>
<span class="c-tool">-&gt; consumer_ls --stream ORDERS</span>  <span class="c-ok">ok</span>
<span class="c-dim">The ORDERS stream is busiest and has 4 consumers.</span>
<span class="c-dim">run summary: llm_calls=3 tool_calls=2 tokens=4102/210 latency=3.9s</span></div>
</div>

## Explore

{{% notice style="tip" title="Next" %}}
Start with [Architecture]({{% relref "architecture" %}}) for the package layering, then follow the subsystem pages in menu order.
{{% /notice %}}
