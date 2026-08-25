# Code map

Fisk AI turns a fisk command-line application into an LLM agent by introspecting its command tree and exposing the allowed commands as tools. This section is a reading guide to how that is implemented, for contributors, reviewers, and anyone auditing the harness before trusting it with a production tool.

{{% notice style="note" title="Snapshot" %}}
Generated 2026-08-25 against tag `v0.0.5`. Commits after this one may make parts of this map stale.
{{% /notice %}}

## The mental model

There is one core and several faces. A fisk application is introspected once into a set of tools; a YAML file narrows that set and decides what needs approval; and then the same selection is driven by a model in an agent loop, served to an MCP client, or served to other agents and to people. Three durable stores sit underneath, and none of them is part of the conversation the model sees until the harness decides to put it there.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 370" role="img" aria-label="One tool selection driving four faces over three durable stores">
    <defs>
      <marker id="ov-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
      <marker id="ov-ah2" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="45" width="180" height="50" rx="8"/>
    <text class="cm-svg-label" x="110" y="67" text-anchor="middle">fisk CLI app</text>
    <text class="cm-svg-sub" x="110" y="84" text-anchor="middle">introspected once</text>
    <rect class="cm-svg-box" x="20" y="140" width="180" height="50" rx="8"/>
    <text class="cm-svg-label" x="110" y="162" text-anchor="middle">agent.yaml</text>
    <text class="cm-svg-sub" x="110" y="179" text-anchor="middle">selects and gates</text>
    <path d="M200,70 L225,70 L225,95 L244,95" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ov-ah)"/>
    <path d="M200,165 L225,165 L225,145 L244,145" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ov-ah)"/>
    <rect x="250" y="60" width="230" height="120" rx="10" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="365" y="98" text-anchor="middle" style="fill:var(--cm-accent)">tool set and loop</text>
    <text class="cm-svg-sub" x="365" y="122" text-anchor="middle">one flat namespace</text>
    <text class="cm-svg-sub" x="365" y="142" text-anchor="middle">gated, bounded, journaled</text>
    <line x1="480" y1="120" x2="518" y2="120" stroke="var(--cm-accent)" stroke-width="2"/>
    <line x1="520" y1="47" x2="520" y2="209" stroke="var(--cm-accent)" stroke-width="2"/>
    <line x1="520" y1="47" x2="554" y2="47" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ov-ah)"/>
    <line x1="520" y1="101" x2="554" y2="101" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ov-ah)"/>
    <line x1="520" y1="155" x2="554" y2="155" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ov-ah)"/>
    <line x1="520" y1="209" x2="554" y2="209" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ov-ah)"/>
    <rect class="cm-svg-box" x="560" y="25" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="650" y="44" text-anchor="middle">run</text>
    <text class="cm-svg-sub" x="650" y="60" text-anchor="middle">terminal, human gate</text>
    <rect class="cm-svg-box" x="560" y="79" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="650" y="98" text-anchor="middle">mcp</text>
    <text class="cm-svg-sub" x="650" y="114" text-anchor="middle">http clients</text>
    <rect class="cm-svg-box" x="560" y="133" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="650" y="152" text-anchor="middle">a2a</text>
    <text class="cm-svg-sub" x="650" y="168" text-anchor="middle">other agents</text>
    <rect class="cm-svg-box" x="560" y="187" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="650" y="206" text-anchor="middle">serve</text>
    <text class="cm-svg-sub" x="650" y="222" text-anchor="middle">jobs and slack</text>
    <line x1="365" y1="180" x2="365" y2="255" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="195" y1="255" x2="575" y2="255" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="195" y1="255" x2="195" y2="284" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#ov-ah2)"/>
    <line x1="385" y1="255" x2="385" y2="284" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#ov-ah2)"/>
    <line x1="575" y1="255" x2="575" y2="284" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#ov-ah2)"/>
    <rect class="cm-svg-box" x="110" y="290" width="170" height="50" rx="8"/>
    <text class="cm-svg-label" x="195" y="312" text-anchor="middle">memory</text>
    <text class="cm-svg-sub" x="195" y="329" text-anchor="middle">model-written notes</text>
    <rect class="cm-svg-box" x="300" y="290" width="170" height="50" rx="8"/>
    <text class="cm-svg-label" x="385" y="312" text-anchor="middle">knowledge</text>
    <text class="cm-svg-sub" x="385" y="329" text-anchor="middle">operator documents</text>
    <rect class="cm-svg-box" x="490" y="290" width="170" height="50" rx="8"/>
    <text class="cm-svg-label" x="575" y="312" text-anchor="middle">sessions</text>
    <text class="cm-svg-sub" x="575" y="329" text-anchor="middle">append-only journal</text>
    <text class="cm-svg-sub" x="380" y="362" text-anchor="middle">the same selection drives every face; only some of them can reach a person</text>
  </svg>
  <figcaption>One core, four faces, three stores. A terminal run reaches its own agent over the same protocol it uses to reach somebody else's.</figcaption>
</figure>

## What the design optimizes for

**Nothing is silently weakened.** A configured confirm tag that matches no tool is warned about, because leaving it unreported would give a false sense of safety. A tool-name collision aborts the run rather than shadowing, because shadowing a gated command would strip its gate. A tag-based exclude on a remote host is rejected outright, because discovery carries no tags and the filter could never be honored.

**Failures land at startup.** An unknown backend, a typo in an options block, a bucket with a time-to-live, an unreachable peer, a stale knowledge manifest: all of them stop the process before the model is contacted, and the error names the key to change.

**Untrusted text stays data.** Model-written memories and retrieved documents are fenced, labeled as data rather than instruction, sanitized at write time, and sanitized again at render time.

## How to read this section

Start with [Architecture]({{% relref "architecture" %}}) for the layering and the patterns that repeat, then [Configuration]({{% relref "configuration" %}}), since every entry point begins by parsing a file. From there [The agent loop]({{% relref "agent-loop" %}}) and [Tools and introspection]({{% relref "tools" %}}) are the core, and the rest can be read in any order.

Every page names the files and symbols it describes, states the invariants the safety story depends on, and says where something is declared but not yet wired.

## Explore

{{< subpages >}}
