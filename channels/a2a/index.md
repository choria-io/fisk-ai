# Serving tools

The a2a surface serves the wrapped application's commands to other agents over NATS. A peer discovers a card and
invokes a tool, and that is the whole exchange: no prompt is involved and the agent loop never runs, which makes it
cheaper than handing that peer a job and gives it a different security posture.

It is a surface rather than a channel. Nothing it does produces work for the agent, so none of the settings that govern
a run apply to it.

> [!info] Note
> The surface is opt-in. The configuration must set `expose.agent.agent_to_agent: true`, otherwise `fisk serve` serves
> no tools.
>
> Serving tools is a surface of `fisk serve` since
> {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}. It was the `fisk a2a` command before that, and
> that command is gone.

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
    agent_to_agent: true
```

A worker serving only tools needs no `system_prompt` and no `llm.model`, since nothing it does calls a model. It does
need `application_path`: no built-in tool declares a2a exposure, so an agent with no wrapped application would serve
nothing.

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
        Concurrency: 2
       Tool Timeout: 30s
            Exposed: stream_add
                     stream_ls
                     stream_info
```

The lines describing a run are absent because there is none: no model, no queue, no worker count and no session store.

## Reading the card

A peer reads the same card `fisk serve` answers discovery with:

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

`expose.agent.tools` narrows the served set on top of the agent's own `include` and `exclude`, so one file can run every
`stream_` tool in a job and offer two of them to peers.

Confirmation-gated commands are never served. There is no operator behind a served call to approve one, so a tool
carrying `ai:confirm` or any configured confirm tag is dropped from the card rather than offered and refused. Use
`ai:deny` to keep a command out entirely.

No built-in tool is served. Knowledge, memory and the human-in-the-loop tools declare no a2a exposure, and a
configuration that enables some says so at startup rather than leaving an operator to notice they are missing from the
card.

## Bounds

```yaml
expose:
  agent:
    agent_to_agent: true
    a2a:
      max_concurrent_tools: 2
      tool_timeout: 30s
```

`max_concurrent_tools` is how many calls run at once and `tool_timeout` bounds a single call. Both are separate from
`--workers` and `harness.tool_timeout`, which pace and bound the agent loop, and the banner reports each under the
surface it belongs to.

Intake is back-pressured rather than queued: a full server does not take another request until a slot frees.

## Both surfaces at once

A configuration enabling a channel and this surface serves both from one process, over one connection:

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
           Workers: 1 (config)

  Serving tools over a2a:

           Discovery: choria.fisk-ai.discovery.nats-worker
               Tools: choria.fisk-ai.tool.nats-worker
         Concurrency: 2
        Tool Timeout: 30s
             Exposed: stream_ls
                      stream_info
```

Enabling a2a in a file a worker reads is what turns the surface on, so a file shared between deployments serves tools
from every worker that reads it.

## Shutdown

A drain stops the surface answering, which takes the identity out of its queue group, so a peer calling during the
drain reaches a sibling rather than waiting on a worker that is going away. A tool call already running is not waited
for: it keeps running with nowhere to reply to, and a command it started may outlive the worker.

## Safety

Whoever can publish to `choria.fisk-ai.tool.<identity>` can run every tool on the card. NATS permissions are the whole
of the access control: a served call carries no verified caller, so nothing can distinguish one peer from another.

The tool safety rules described in the [Reference](../../reference/#safety) hold here as everywhere else: commands run
as an argument vector rather than through a shell, arguments are bound to each command's schema, and credentials are
stripped from tool environments.
