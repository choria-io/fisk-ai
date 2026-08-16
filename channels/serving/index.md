# Serving

The `fisk serve` command hosts an agent behind the surfaces its configuration enables. It runs until interrupted. The
agent is the one [`fisk run`](../../agents/) drives: tool set, prompt, model and harness settings come from the same
configuration file.

[Queued jobs](../asyncjobs/) takes each job off a work queue and stores the answer back; nobody waits.
[Answering prompts](../prompts/) takes a prompt from another agent and streams the run back while that agent waits.
[Serving tools](../a2a/) runs one tool for another agent and starts no agent loop.

> [!info] Note
> At least one surface must be enabled. `fisk serve` exits with an error when the configuration enables none.

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

The startup banner lists the bound surfaces and the settings every run uses:

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
     Queue Workers: 1 (config)
```

The banner adds an `Agent Context` line when the agent's `nats_context` differs from the queue's.

Each surface prints its own section below, listing the addresses it answers on and the bounds it applies.
[Answering prompts](../prompts/) and [serving tools](../a2a/) show examples.

A worker hosting no channel has no model, no tool directory and no session store, and prints none of those lines.
[Serving tools](../a2a/) shows the shorter banner.

## Shared resources

`fisk serve` builds the model provider, the session store, the memory store, the knowledge index and the NATS
connection once at startup. Every run shares them. A missing stream or bucket fails the process immediately instead of
failing whatever job happens to arrive first.

> [!info] Warning
> A worker whose storage does not exist fails at startup. Under a supervisor that restarts on failure it crash-loops.

When the knowledge index does not exist at startup, each run opens the index for itself, so an index built after the
worker started is visible to later runs.

## Concurrency

Each channel bounds its own runs, so a process serving two channels at two runs each is running four.

`--workers` sets how many queued jobs run at once, overriding `expose.agent.jobs.workers`:

```nohighlight
$ fisk serve --config nats.yaml --workers 4
```

`--workers` affects the queued-jobs channel only. The prompts channel takes its count from
`expose.agent.a2a.prompts.workers` and refuses a caller when every slot is busy.

A work queue bounds every worker on it together with its own concurrency setting. Setting `workers` above what the
queue allows leaves slots idle rather than raising throughput.

## Timeouts

`harness.tool_timeout` bounds a single tool call, in `fisk serve` and `fisk run` alike. The default is five minutes.
`0s` removes the bound, for commands that run for hours.

> [!info] Note
> `--workers` overrides the configuration file. `harness.tool_timeout` in the file overrides the built-in default.

The bound stops a command and its process group. It does not stop an in-process handler that ignores its context.

## Where tools run

Command tools run in the worker's own working directory unless `--work-dir` names another. It must be an absolute path
that already exists.

Every run shares it. Set the worker count to 1 when a tool writes local state that concurrent runs would corrupt.

> [!info] Note
> A CLI that reads a context or a profile of its own does not inherit the agent's `nats_context`, so pass the selection
> explicitly where it matters.

## Shutdown

On the first interrupt the worker drains. It takes no new work, and runs in flight continue to their next resumable
point:

```nohighlight
draining: no new work is taken and running work stops where it can resume. Interrupt again to stop now
```

A second interrupt stops the worker at once. The queue redelivers any queued job still running, and the redelivery
resumes from the journal. The prompts channel answers its callers with a failure instead.

A drain stops every surface, so a worker also [serving tools](../a2a/#shutdown-and-faults) stops answering peers at
the same point. A worker with no channel has nothing to resume:

```nohighlight
draining: the surfaces stop answering. Interrupt again to stop now
```

## Sessions

Each channel journals its runs, so an interrupted run resumes instead of repeating the model calls the first attempt
already paid for.

Sessions need a store every worker can read. On one machine the default file backend is enough. Across machines,
configure a shared `harness.sessions` backend. Without one, a job redelivered to a different worker cannot read the
journal and starts again.

When two workers reach the same journal, the second one claims it. The claim is written before the run starts, and the
first worker sees it before its next tool call and stops. Only a tool already running can execute twice.

## What a served run does not inherit

The following settings narrow the MCP and a2a tool surfaces, not a run served over a channel:

* `expose.agent.tools` selects what is served over MCP and a2a. A channel runs the whole agent loop, so it uses the
  agent's own `include` and `exclude` instead
* the waiver that lets a tool-serving configuration omit `identity`, `system_prompt` and `llm.model` does not apply to a
  channel, since a run needs all three

## Safety

A served run is a full agent loop driven by caller-supplied prompt text, running every tool the configuration allows.
The channel's own admission check is therefore the only access control: queue publish permission for queued jobs, NATS
publish permission for prompts.

`harness.tool_timeout` bounds each tool call and `llm.budget` bounds the run. A caller may lower the budget, never
raise it. The tool safety rules described in the [Reference](../../reference/#safety) hold here as everywhere else:
commands run as an argument vector rather than through a shell, arguments are bound to each command's schema, and
credentials are stripped from tool environments.
