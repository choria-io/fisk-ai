# Serving tools

The a2a surface serves the wrapped application's commands to other agents over NATS. A peer reads the agent's card and
invokes a tool from it. No prompt is sent and the agent loop does not run, so the model, budget and session settings do
not apply.

> [!info] Note
> The surface is opt-in. The configuration must set `expose.agent.a2a.serve_tools: true`, otherwise `fisk serve` serves
> no tools.
>
> Serving tools is a surface of `fisk serve` since
> {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}. The `fisk a2a` command is gone, and a configuration
> carrying the old `expose.agent.agent_to_agent` key is refused at startup.

## Serving a set of tools

```yaml
identity: nats-tools
application_path: /usr/local/bin/nats
nats_context: production
include:
  tools:
    - ^stream_
expose:
  agent:
    a2a:
      serve_tools: true
```

A worker serving only tools needs no `system_prompt` and no `llm.model`. It does need `application_path`: built-in
tools are never served, so an agent with no wrapped application serves nothing.

```nohighlight
$ fisk serve --config tools.yaml
```

```nohighlight
Serving nats-tools/1.2.0:

         Surfaces: a2a
    Agent Context: production
        Telemetry: disabled

  Serving tools over a2a:

          Discovery: choria.fisk-ai.discovery.nats-tools
              Tools: choria.fisk-ai.tool.nats-tools
        Concurrency: 4
       Tool Timeout: 30s
            Exposed: stream_add
                     stream_ls
                     stream_info
```

## Reading the card

`fisk discover` fetches the card the worker answers discovery with:

```nohighlight
$ fisk discover nats-tools --config peer.yaml

Agent Card for nats-tools:

        Agent: nats-tools
      Version: 1.2.0
    Protocols: io.choria.fisk-ai.v1
```

Importing those tools into another agent is `remote_tools` in that agent's configuration, covered in the
[Reference](../../reference/#agent-to-agent).

## What is served

`expose.agent.tools` applies on top of the agent's `include` and `exclude`. One file can run every `stream_` tool in a
job and serve two of them to peers.

A tool carrying `ai:confirm` or a configured confirm tag is left off the card, because no operator is behind a served
call to approve it. Use `ai:deny` to keep a command out entirely.

Built-in tools are never served. Knowledge, memory and the human-in-the-loop tools declare no a2a exposure, and the
startup banner lists them as withheld when the configuration enables them.

## Bounds

```yaml
expose:
  agent:
    a2a:
      serve_tools: true
      max_concurrent_tools: 4
      tool_timeout: 30s
      request_timeout: 120s
```

| Setting                      | Description                                                          |
|------------------------------|----------------------------------------------------------------------|
| `max_concurrent_tools` (int) | tool calls run at once; default is the CPU count clamped to 2 to 8   |
| `tool_timeout` (duration)    | bound on one call this agent answers; default `30s`                  |
| `request_timeout` (duration) | wait for a peer's next message; default `2m`, minimum `30s`          |

In a container the concurrency default reads the container's CPU limit, not the host's, and the banner prints the
concurrency in use. `--workers` sizes the queued-jobs intake and does not reach these. `harness.tool_timeout` bounds a
tool call inside the agent loop.

`request_timeout` bounds a call this agent makes to a peer. The peer answers with a set of messages: an
acknowledgement, a keepalive every ten seconds while the tool runs, then the reply. The timeout bounds the gap between
those messages, so a peer that keeps sending keepalives is waited for and `harness.tool_timeout` ends the call. A card
fetch is a single message, so there the same value bounds the whole request.

An agent that imports `remote_tools` and serves nothing sets `request_timeout` in an `a2a` block with no surface
enabled. That is the only configuration in which a surface-less `a2a` block is accepted.

The server refuses a call that arrives with every slot in use rather than queueing it. The acknowledgement says no and
the reply carries a `capacity` code and a message saying nothing ran. The identity is a NATS queue group, so a peer
that tries again reaches whichever member takes the message next.

## Several surfaces at once

One process hosts a channel and this surface together, over one NATS connection:

```nohighlight
Serving nats-worker/1.2.0:

          Surfaces: asyncjobs/FISK_AI
                    a2a
             Model: claude-sonnet-5
     Queue Context: production
          Sessions: file
         Knowledge: disabled
         Telemetry: disabled
    Tool Directory: /var/lib/fisk-ai
      Tool Timeout: 5m0s
     Queue Workers: 1 (config)

  Serving tools over a2a:

           Discovery: choria.fisk-ai.discovery.nats-worker
               Tools: choria.fisk-ai.tool.nats-worker
         Concurrency: 4
        Tool Timeout: 30s
             Exposed: stream_ls
                      stream_info
```

Adding a `prompts` block to the same `a2a` block also answers prompts, covered in
[Answering prompts](../prompts/).

## Shutdown and faults

A drain stops the surface answering and removes the identity from its queue group, so a peer calling during the drain
reaches a sibling. Prompts stop with it, both surfaces using one transport and one identity.

A drain does not wait for a call that is already running. The call runs to completion with nowhere to reply to, and a
command it started may outlive the worker. `tool_timeout` stops a call that does not finish.

An error on any of the service's subscriptions stops the whole micro service, taking discovery, tools and prompts for
that identity down together. `fisk serve` logs it, drains the runs in flight and exits non-zero, so a supervisor
restarts the worker. A drain stops the service by the same path, and is logged rather than reported as a fault.

## Safety

Whoever can publish to `choria.fisk-ai.tool.<identity>` can run every tool on the card. NATS permissions are the whole
of the access control: a served call carries no verified caller, so nothing can distinguish one peer from another.

The tool safety rules described in the [Reference](../../reference/#safety) hold here as everywhere else: commands run
as an argument vector rather than through a shell, arguments are bound to each command's schema, and credentials are
stripped from tool environments.
