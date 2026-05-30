+++
title = "Interoperability: MCP and A2A"
weight = 90
description = "Serving the same tools to an MCP client or a peer agent over NATS, and importing a peer's tools."
+++

The same tool model that backs the agent loop can be served two other ways: over the Model Context Protocol for a client like Claude Desktop, and over a NATS-based agent-to-agent protocol for peer agents. A run can also import a peer's tools and present them to the model as if they were local.

{{% notice style="note" title="Where it lives" %}}
`internal/mcpserver` serves over MCP. The `a2a` package holds the transport-agnostic protocol types; `internal/a2anats` binds them to NATS. `internal/remotetools` is the import-policy layer. The commands are `mcp_command.go`, `a2a_command.go`, and `discover_command.go`.
{{% /notice %}}

## Serving over MCP

`mcp` serves `util.ServedTools`, the loaded set narrowed by `expose.agent.tools`, over streamable HTTP. The port comes from `--port`, then the config, then a default of 8080. Each tool keeps its fisk input schema. A tool with a name the protocol rejects is skipped with a warning.

A confirm-tagged command is realized over MCP as an elicitation that asks the client for an `approve` boolean, driven by a confirm mode: `auto` asks clients that can elicit and runs others ungated with a warning, `always` refuses a client that cannot elicit, and `never` delegates approval to the client's own UI. The gate fails closed on anything that is not an explicit accept.

{{% notice style="warning" title="Load-bearing decision" %}}
The human-in-the-loop and memory tools are never exposed over MCP or A2A. They are built-in tools appended only inside an agent run, so they are structurally absent from the served set; no filter could add them back. Because MCP confirmation is a request a client may auto-approve, `ai:deny` is the only way to keep a command off a served surface entirely.
{{% /notice %}}

## A2A over NATS

The `a2a` package models the protocol as self-describing messages. Every message embeds a `Header` carrying its own framing, so a captured message is fully decodable without the transport. The NATS binding never infers meaning from the subject; it decodes the header's protocol id and dispatches on that. The subjects exist only as permission seams, which is what lets the same message bodies move to another transport later without change.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 240" role="img" aria-label="A consumer discovers and invokes tools on an A2A server over two NATS subjects, with replies on the NATS inbox">
    <defs>
      <marker id="ia" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
      <marker id="if" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/>
      </marker>
    </defs>
    <!-- consumer -->
    <rect class="cm-svg-box" x="20" y="94" width="160" height="60" rx="8"/>
    <text class="cm-svg-label" x="100" y="120" text-anchor="middle">consumer</text>
    <text class="cm-svg-sub"   x="100" y="138" text-anchor="middle">a2anats.Client</text>
    <!-- discovery subject -->
    <rect class="cm-svg-box" x="300" y="46" width="180" height="42" rx="20"/>
    <text class="cm-svg-sub" x="390" y="72" text-anchor="middle">discovery.ID</text>
    <!-- tool subject -->
    <rect class="cm-svg-box" x="300" y="162" width="180" height="42" rx="20"/>
    <text class="cm-svg-sub" x="390" y="188" text-anchor="middle">tool.ID</text>
    <!-- server (accent) -->
    <rect x="590" y="94" width="150" height="60" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="665" y="120" text-anchor="middle" style="fill:var(--cm-accent)">A2A server</text>
    <text class="cm-svg-sub"   x="665" y="138" text-anchor="middle">exposed tools</text>
    <!-- request edges -->
    <text class="cm-svg-sub" x="240" y="78" text-anchor="middle">Discover</text>
    <line x1="180" y1="112" x2="300" y2="72"  stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <line x1="480" y1="72"  x2="590" y2="112" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <text class="cm-svg-sub" x="236" y="182" text-anchor="middle">InvokeTool</text>
    <line x1="180" y1="136" x2="300" y2="184" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <line x1="480" y1="184" x2="590" y2="136" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <!-- reply edge -->
    <line x1="590" y1="124" x2="182" y2="124" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3" marker-end="url(#if)"/>
    <text class="cm-svg-sub" x="386" y="118" text-anchor="middle">reply via inbox</text>
  </svg>
  <figcaption>Two request-reply round-trips over `choria.fisk-ai.discovery.<identity>` and `choria.fisk-ai.tool.<identity>`. Replies always return on the NATS-supplied inbox, never a subject from the body.</figcaption>
</figure>

Both servers bound an un-budgeted caller: a shared semaphore caps in-flight calls at two and a per-call timeout defaults to thirty seconds. An execution failure is always an in-band error result, never a transport error, and a non-zero command exit is a successful result carrying the exit code, so the caller can reason about either.

{{% notice style="warning" title="Load-bearing decision" %}}
A served A2A agent has no operator, so it drops any confirm-gated tool outright rather than serving it ungated. This is the stricter analogue of the MCP behavior, and the same advice applies: use `ai:deny` to suppress a command entirely.
{{% /notice %}}

## Importing a peer's tools

A run with configured remote hosts discovers each one, filters by name, and assigns final model-facing names. A bare name is prefixed with the host's alias when a local tool already holds it or when more than one host exposes it, decided over the whole set so runs are reproducible. A residual collision fails the run closed. The imported schema is untrusted and must parse as a JSON object. At call time a remote tool is invoked over NATS and its reply is mapped back to the same result shape a local tool produces, so the model cannot tell remote from local. Import is strict for a run, since the prompt may depend on those tools, but best-effort for `info`, which still shows local tools if a host is unreachable.

## Reserved and planned

The streaming task flow, the `Event`, `Result`, `Cancel`, and `Ack` messages with their event blocks, is fully modeled and schema-validated but not yet carried by the NATS binding, which transports only discovery and direct tool calls. Wrapping the same message bodies in the Choria Protocol, with its authentication and authorization, is the planned second binding; the subject-contract design exists so that binding needs no change here.

{{% notice style="tip" title="Next" %}}
Continue to [Reference and Map]({{% relref "reference-map" %}}) for the command surface, a source-file map, and a glossary.
{{% /notice %}}
