# Serving

The `fisk serve` command hosts an agent behind the surfaces its configuration enables. It runs until it is stopped,
taking work as it arrives, and it is the same agent as [`fisk run`](../../agents/): the tool set, the prompt, the model
and the harness settings all come from the same configuration file.

A channel supplies work the agent runs, which is [queued jobs](../asyncjobs/) today. [Serving tools](../a2a/) answers
another agent's tool call directly, running one tool and no agent loop.

> [!info] Note
> At least one surface must be enabled. A configuration enabling none leaves `fisk serve` with nothing to serve and it
> refuses to start.

## Starting a worker

A minimal configuration names the application to drive, the tool selection, and one channel. Here that channel is
[queued jobs](../asyncjobs/):

```yaml
identity: worker
application_path: /usr/local/bin/nats
nats_context: production
system_prompt: |
  You inspect NATS servers on behalf of an operator. Answer concisely.
include:
  tools:
    - ^stream_
expose:
  agent:
    jobs: {}
```

```nohighlight
$ fisk serve --config nats.yaml
```

The startup banner reports what was bound and what every run will use:

```nohighlight
Serving worker/1.2.0:

          Surfaces: asyncjobs/FISK_AI
             Model: claude-sonnet-5
     Queue Context: production
          Sessions: jetstream (FISK_SESSIONS)
         Knowledge: disabled
         Telemetry: disabled
    Tool Directory: /var/lib/fisk-ai
      Tool Timeout: 5m0s
           Workers: 1 (config)
```

An `Agent Context` line joins them when the agent's own `nats_context` differs from the queue's, since the queue and the
stores may be on different clusters.

A worker hosting no channel has no model, no queue, no worker count and no session store, and prints none of those
lines. [Serving tools](../a2a/) shows that banner.

## Shared resources

The model provider, the session store, the memory store, the knowledge index and the NATS connection are built once at
startup and shared by every run, rather than rebuilt per unit of work.

This moves a configuration mistake from the first job to the banner. A missing stream or bucket fails the process
immediately instead of failing whatever job happens to arrive first.

> [!info] Warning
> Under a process supervisor that restarts on failure, a worker pointed at storage nobody provisioned crash-loops
> rather than starting and failing its work one item at a time.

An index built after the worker started is picked up by later runs. A knowledge index that does not exist yet is not
shared at all, so each run opens its own until one exists.

## Concurrency

`--workers` sets how many units of work run at once, overriding the value in the configuration:

```nohighlight
$ fisk serve --config nats.yaml --workers 4
```

The bound is per channel rather than a total for the process. A channel that has an opinion of its own states it and
gets that instead, so a process serving two channels at two runs each is running four.

Raising the number above a channel's own limit does not raise throughput. A work queue, for example, bounds every
worker on it together, so a process above that bound holds slots it never fills.

## Timeouts

`harness.tool_timeout` bounds a single tool call. A configured value always wins, and `fisk serve` fills in five minutes
only when the configuration sets none.

A run at a terminal is unbounded by default because an operator can interrupt a command that will never answer. Nobody
can interrupt one here, so a bound is applied whether or not one was configured.

> [!info] Note
> This is the opposite precedence to `--workers`, deliberately. A flag beats the file for the worker count, because that
> is a property of the process and one file is often shared by every container that reads it. The file beats the
> built-in default for the tool bound, because that is a property of the agent.

The bound stops a command and its process group. It does not stop an in-process handler that ignores its context, and a
call the tool declared operator paced is never bounded.

## Where tools run

Command tools run in the worker's own working directory unless `--work-dir` names another. It must be an absolute path
that already exists.

Every run shares it. A tool that mutates local state is responsible for doing so safely under concurrency; set the
worker count to 1 if it cannot.

> [!info] Note
> A wrapped application chooses its own target by its own rules. A CLI that reads a context or a profile of its own does
> not inherit the agent's `nats_context`, so pass the selection explicitly where it matters.

## Shutdown

On the first interrupt the worker drains. It stops taking new work and lets whatever is in flight stop at a point it can
be resumed from:

```nohighlight
draining: no new work is taken and running work stops where it can resume. Interrupt again to stop now
```

A second interrupt stops it at once. Anything still running is left for a later delivery, which resumes from its
journal rather than starting again.

A drain stops every surface, so a worker also [serving tools](../a2a/#shutdown) stops answering peers at the same
point. A worker with no channel has nothing to resume:

```nohighlight
draining: the surfaces stop answering. Interrupt again to stop now
```

## Sessions

Every run a channel checkpoints is journaled, so an interrupted unit of work resumes rather than paying again for the
model calls a previous attempt already made.

Sessions need a store every worker can read. On one machine the default file backend is enough. Across machines,
configure a shared `harness.sessions` backend, or work redelivered to a different worker lands somewhere that cannot see
the journal.

Where two workers reach the same journal, the arriving one takes it. The claim is written before anything runs and the
incumbent notices before its next tool call, which bounds double execution to a tool that was already in flight.

## What a served run does not inherit

These narrow the other serving surfaces and not a run served over a channel:

* `expose.agent.tools` selects what is served over MCP and a2a. A channel runs the whole agent loop, so it uses the
  agent's own `include` and `exclude` instead
* the waiver that lets a tool-serving configuration omit `identity`, `system_prompt` and `llm.model` does not apply to a
  channel, since a run needs all three

## Safety

A served run is a full agent loop driven by caller-supplied prompt text, running every tool the configuration allows.
Whatever admits a caller to a channel is therefore the whole of the access control, and each channel documents its own.

The bounds that do apply are `harness.tool_timeout` on any single tool call and the run budget in `llm.budget`, which a
caller may lower but never raise. The tool safety rules described in the [Reference](../../reference/#safety) hold here
as everywhere else: commands run as an argument vector rather than through a shell, arguments are bound to each
command's schema, and credentials are stripped from tool environments.
