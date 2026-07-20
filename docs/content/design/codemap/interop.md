+++
title = "MCP and A2A"
weight = 90
description = "Serving the same tools to an MCP client or a peer agent, and importing a peer's tools."
+++

The same tool model that backs the agent loop can be served two other ways: over the Model Context Protocol for a client like Claude Desktop, and over an agent-to-agent protocol for peer agents. A run can also import a peer's tools and present them to the model as if they were local.

{{% notice style="note" title="Where it lives" %}}
`internal/mcpserver` serves over MCP. `internal/a2a` is the A2A engine: protocol types in `messages.go` and `types.go`, framing in `header.go` and `stamp.go`, schema validation in `schemas.go` and `validate.go`, the server in `server.go`, the client in `client.go`, and the transport seam in `transport.go` and `registry.go`. `internal/a2a/nats` binds that protocol to NATS. `internal/conns` establishes the shared connection. `internal/remotetools` is the import-policy layer. The commands are `mcp_command.go`, `a2a_command.go`, and `discover_command.go`.
{{% /notice %}}

## Serving over MCP

`mcp` serves `fisk.ServedTools`, the loaded set narrowed by `expose.agent.tools`, over streamable HTTP. Each tool keeps its fisk input schema. A tool with a name the protocol rejects is skipped with a warning.

The listen address resolves from `--address`, then `expose.agent.mcp.address`, then a default of `127.0.0.1`; the port resolves the same way from `--port`, `expose.agent.mcp.port`, then 8080. Binding loopback by default means an MCP server is reachable only from the local machine until an operator deliberately widens it.

{{% notice style="warning" title="Load-bearing decision" %}}
Setting the address to `0.0.0.0` exposes every served tool to the network. The served set is already narrowed by `expose.agent.tools` and stripped of `ai:deny`, but nothing in the protocol authenticates the caller. Widen the bind only behind authentication.
{{% /notice %}}

A confirm-tagged command is realized over MCP as an elicitation that asks the client for an `approve` boolean, driven by a confirm mode: `auto` asks clients that can elicit and runs others ungated with a warning, `always` refuses a client that cannot elicit, and `never` delegates approval to the client's own UI. The gate fails closed on anything that is not an explicit accept.

Built-in tools are not automatically part of a served set, because they are appended inside an agent run where an operator exists. One is servable by explicit allowlist: `knowledge_search`, when `expose.agent.mcp.builtins` names it, because it is read-only and never prompts. The `ask_human_*` and memory tools have no such path and stay agent-only. See [Knowledge and RAG]({{% relref "knowledge" %}}).

## The A2A engine and its transport

A2A is split in two. The engine owns what a message *means*: it validates every message against a JSON schema in both directions, caps a body at 768 KiB before any decode or allocation, and dispatches on the `Header.Protocol` id carried inside the message. A transport owns only how bytes *move*.

The seam is the `Transport` interface, with `RoundTrip`, `Serve`, `Describe`, and `Close`. Transports register themselves by name from `init`, the same shape as `database/sql` drivers, and `NewTransport` builds the selected one from a shared `conns.Provider`. NATS is the only implementation today; `config.A2ATransport` returns the fixed name `nats` because a config field is deliberately deferred until a second binding exists.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 268" role="img" aria-label="A client calls the engine which validates and dispatches, then hands bytes to a transport that carries them over NATS subjects">
    <defs>
      <marker id="ia" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- consumer -->
    <rect class="cm-svg-box" x="14" y="104" width="132" height="60" rx="8"/>
    <text class="cm-svg-label" x="80" y="130" text-anchor="middle">a2a.Client</text>
    <text class="cm-svg-sub"   x="80" y="148" text-anchor="middle">or a2a.Server</text>
    <!-- engine (accent) -->
    <rect x="186" y="90" width="186" height="88" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="279" y="114" text-anchor="middle" style="fill:var(--cm-accent)">a2a engine</text>
    <text class="cm-svg-sub"   x="279" y="133" text-anchor="middle">schema validate</text>
    <text class="cm-svg-sub"   x="279" y="149" text-anchor="middle">768 KiB cap</text>
    <text class="cm-svg-sub"   x="279" y="165" text-anchor="middle">dispatch on Header.Protocol</text>
    <!-- boundary -->
    <line x1="410" y1="40" x2="410" y2="238" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="5 4"/>
    <text class="cm-svg-sub" x="330" y="32" text-anchor="middle">meaning</text>
    <text class="cm-svg-sub" x="492" y="32" text-anchor="middle">bytes</text>
    <!-- transport interface -->
    <rect class="cm-svg-box" x="442" y="104" width="150" height="60" rx="8"/>
    <text class="cm-svg-label" x="517" y="130" text-anchor="middle">a2a.Transport</text>
    <text class="cm-svg-sub"   x="517" y="148" text-anchor="middle">RoundTrip, Serve</text>
    <!-- nats binding -->
    <rect class="cm-svg-box" x="622" y="104" width="124" height="60" rx="8"/>
    <text class="cm-svg-label" x="684" y="130" text-anchor="middle">nats</text>
    <text class="cm-svg-sub"   x="684" y="148" text-anchor="middle">subjects</text>
    <!-- arrows -->
    <line x1="146" y1="134" x2="184" y2="134" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <line x1="372" y1="134" x2="440" y2="134" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <line x1="592" y1="134" x2="620" y2="134" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ia)"/>
    <!-- registry note -->
    <text class="cm-svg-sub" x="594" y="200" text-anchor="middle">selected by name from the transport registry,</text>
    <text class="cm-svg-sub" x="594" y="216" text-anchor="middle">built from a shared conns.Provider</text>
    <!-- routehint note -->
    <text class="cm-svg-sub" x="279" y="212" text-anchor="middle">RouteHint picks a path, never a type</text>
  </svg>
  <figcaption>The engine validates in both directions and decides what a message is. A transport keeps the discovery and tool paths separate and moves bytes; it never decodes them.</figcaption>
</figure>

`RouteHint` is the one thing the engine tells a transport about routing, and it has exactly two values today, `OpDiscovery` and `OpTool`. It is routing only. A transport may use the separation as a permission seam, but the meaning of a message always comes from its header, never from the path it arrived on. That is the property that lets a second binding drop in without touching the engine.

The NATS binding maps those paths to `choria.fisk-ai.discovery.<identity>` and `choria.fisk-ai.tool.<identity>`, serves them as a NATS micro service with the identity as the queue group, and answers on the transport-supplied reply inbox rather than any address taken from a message body.

Both servers bound an un-budgeted caller: a shared semaphore caps in-flight calls at two and a per-call timeout defaults to thirty seconds. The semaphore is acquired on the serving goroutine before a worker is spawned, so a saturated server stops accepting intake rather than queueing it. An execution failure is always an in-band error result, never a transport error, and a non-zero command exit is a successful result carrying the exit code, so the caller can reason about either.

{{% notice style="warning" title="Load-bearing decision" %}}
A served A2A agent has no operator, so `Server.selectExposed` drops any confirm-gated tool outright rather than serving it ungated, logging the reason. This is stricter than the MCP behavior, where the command is still served and gated by elicitation. Adding `ai:confirm` therefore removes a tool from A2A entirely; use `ai:deny` to suppress a command from every surface.
{{% /notice %}}

## Connections are shared, not per-backend

`internal/conns` establishes a connection once and hands it to whichever backend needs it. Ownership runs one way and is explicit: the provider owns the connection, a transport borrows it, and a server borrows the transport. `Server.Stop` releases only the transport, `Transport.Close` releases only its service registration, and the command that created the provider is the single place that closes the connection. A provider with nothing provisioned returns a nil connection so a backend fails loudly at construction rather than dereferencing it later.

## Importing a peer's tools

A run with configured remote hosts discovers each one, filters by name, and assigns final model-facing names. A bare name is prefixed with the host's alias when a local tool already holds it or when more than one host exposes it. That decision is made over the whole set at once, counting both bare and final names, so the result does not depend on which host was processed first and is reproducible between runs. A residual collision fails the run closed.

The imported schema is untrusted and must parse as a JSON object; an absent or non-object schema is an error rather than something forwarded to the model API. At call time a remote tool is invoked through the transport and its reply is mapped back to the same result shape a local tool produces, including exit code and truncation, so the model cannot tell remote from local. A remote tool never prompts and carries no local tags, so it is deliberately not `Confirmable`.

Import is strict for a run, since the prompt may depend on those tools, but best-effort for `info`, which still shows local tools if a host is unreachable and records a collision as skipped rather than failing.

Filtering has one asymmetry worth knowing. Include and exclude rules match on tool name; a tag-based include cannot be honored because discovery carries no tags, so it is reported back to the caller as ignored rather than silently dropped.

## Reserved and planned

The streaming task flow, the `Request`, `Event`, `Result`, `Cancel`, and `Ack` messages with their event blocks, is fully modeled and schema-validated but not yet carried by any binding, which transports only discovery and direct tool calls. `Header.Parent` and the `agent_call` block model a multi-hop A to B to C call and are unused by the current request-reply paths. Wrapping the same message bodies in the Choria Protocol, with its authentication and authorization, is the planned second binding; the `Transport` interface and the registry exist so that binding needs no change to the engine.

{{% notice style="tip" title="Next" %}}
Continue to [Reference and Map]({{% relref "reference-map" %}}) for the command surface, a source-file map, and a glossary.
{{% /notice %}}
