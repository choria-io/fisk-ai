# Queued jobs

The queued-jobs channel takes whole units of work off a [Choria asyncjobs](https://github.com/choria-io/asyncjobs) work
queue, runs the agent loop against each one, and stores the answer back on the task. Nobody waits: a caller enqueues a
job and reads the result whenever it is ready.

> [!info] Note
> The channel is opt-in. The configuration must carry an `expose.agent.jobs` block, otherwise `fisk serve` has no queue
> to bind to.
>
> The queued-jobs channel is available since {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}.

## Creating the storage

A worker requires its storage to exist. Create it with [`ajc`](https://github.com/choria-io/asyncjobs), version 0.4.0
or newer.

The task store holds every job and the answer written back to it:

```nohighlight
$ ajc tasks initialize
```

The work queue holds the jobs waiting to be taken:

```nohighlight
$ ajc queue add FISK_AI --run-time 10m --tries 3 --concurrent 10
```

The queue's run time, retry cap and concurrency stay with the queue. They are read from the bound consumer at startup
and reported on the banner. The run time must be longer than a job takes, or the queue redelivers work that is still
running.

A worker started before either exists fails:

```nohighlight
fisk: error: building the jobs channel: connecting to queue "FISK_AI": storage not ready: stream CHORIA_AJ_TASKS does not exist, create it with 'ajc tasks initialize'
```

## Submitting work

A caller enqueues a task with the queue engine's own client. No Fisk AI code sits in that path, so the contract is the
queue name, the task type, and a payload that is a v1 `request` message:

| Item      | Value                                                |
|-----------|------------------------------------------------------|
| Queue     | `expose.agent.jobs.queue`, default `FISK_AI`          |
| Task type | `expose.agent.jobs.task_type`, default `fisk-ai:run`  |
| Payload   | an `io.choria.fisk-ai.v1.request` message             |

The request carries the prompt and the framing every v1 message needs:

```json
{
  "protocol": "io.choria.fisk-ai.v1.request",
  "id": "2fT8kMxQ1aBcDeFgHiJkLmNoPqR",
  "request": "2fT8kMxQ1aBcDeFgHiJkLmNoPqR",
  "conversation": "2fT8kMxQ1aBcDeFgHiJkLmNoPqR",
  "sequence": 0,
  "time": "2026-08-15T11:30:00Z",
  "sender": {
    "name": "ops-console"
  },
  "prompt": "Which streams have no consumers?"
}
```

On a request, `request` and `id` are the same value. A one-shot job has nothing to group, so `conversation` takes that
value too. `sender.name` is limited to letters, digits, `-` and `_`.

```nohighlight
$ ajc tasks add fisk-ai:run --payload-file request.json --queue FISK_AI
Enqueued task 3Hwxl119ZbwKHCKPzWlslXZYnB0
```

The task id is chosen by the submitter or minted by the engine. It also names the session the run journals under, so it
must be letters, digits, `-` or `_`, which is stricter than what the queue itself accepts.

Optional fields narrow what one job may do:

| Field                   | Description                                       |
|-------------------------|---------------------------------------------------|
| `context`               | supporting material offered alongside the prompt    |
| `budget.max_tokens`     | lowers the token budget for this job                |
| `budget.max_iterations` | lowers the model-call cap for this job              |

A budget may only lower what the configuration allows. A value above the configured limit is clamped down to it.

A payload the worker cannot run is refused once rather than retried. An oversized payload, one that is not a valid v1
request, one carrying no prompt, and a task id that cannot name a session all fail this way, with the reason on the
task's `LastErr`.

## Reading the answer

The answer is stored on the task itself as a v1 `result` message:

```nohighlight
$ ajc tasks view 3Hwxl119ZbwKHCKPzWlslXZYnB0 --json
```

```json
{
  "protocol": "io.choria.fisk-ai.v1.result",
  "request": "2fT8kMxQ1aBcDeFgHiJkLmNoPqR",
  "sender": {
    "name": "worker"
  },
  "recipient": {
    "name": "ops-console"
  },
  "stop_reason": "end_turn",
  "text": "ORDERS and PAYMENTS have no consumers.",
  "usage": {
    "input_tokens": 1608,
    "cache_read_tokens": 663,
    "output_tokens": 51,
    "llm_calls": 2,
    "tool_calls": 1
  }
}
```

The `request` field echoes the id the caller submitted, and `recipient` names the caller that asked. `input_tokens` is
every input token the job consumed, cached and uncached together, with `cache_read_tokens` and `cache_create_tokens`
breaking it down rather than adding to it.

A run that failed is a completed job whose answer says it failed. It is stored as a v1 `error` message carrying a
`stop_reason`, and the task is acknowledged rather than retried, since a model refusal or an exhausted budget does not
become true on redelivery.

| Stop reason         | Meaning                                          |
|---------------------|--------------------------------------------------|
| `end_turn`          | the agent finished and answered                   |
| `budget_exhausted`  | the token budget ran out                          |
| `max_iterations`    | the model-call cap was reached                    |
| `suspended`         | the run stopped at a point it can resume from     |
| `error`             | the run failed                                    |

## Redelivery

Every run is journaled under the task id, so a worker that dies mid-job leaves a session behind and the redelivery
resumes it rather than starting again.

A job whose session already completed is answered from the journal. Nothing runs and no model is called, which covers a
worker that finished its work and died before its acknowledgement landed.

> [!info] Note
> Deploying a changed tool set while jobs are in flight fails their resume check, and those jobs are retried until the
> queue's try limit and then expire. Drain a worker before replacing it.

## Configuration

Every field under `expose.agent.jobs` has a default, so an empty block is a working channel.

```yaml
expose:
  agent:
    jobs:
      # The work queue to consume. It must already exist.
      queue: FISK_AI
      # The task type this worker handles. Tasks of another type on the
      # same queue are left alone.
      task_type: fisk-ai:run
      # How many jobs this process runs at once. The --workers flag
      # overrides it.
      workers: 1
      # The NATS context the queue is reached over, defaulting to the
      # top-level nats_context. It is dialed separately, so the queue may
      # live on a different cluster from the session store.
      nats_context: production
      # Bounds a task payload in bytes before anything decodes it.
      max_payload: 524288
```

| Field               | Description                                                            |
|---------------------|------------------------------------------------------------------------|
| `queue`             | work queue to consume, default `FISK_AI`                                 |
| `task_type`         | asyncjobs task type handled, default `fisk-ai:run`                       |
| `workers` (int)     | jobs run at once, default `1`                                            |
| `nats_context`      | NATS context for the queue, defaulting to the top-level `nats_context`   |
| `max_payload` (int) | payload cap in bytes before decoding, default `524288`                   |

A task of another type on the same queue is not this worker's and is left alone. A submitter and a worker that disagree
on the type produce a job nobody runs and nobody reports.

## Safety

Permission to write to the queue is the whole of the access control. Behind it is a full agent loop driven by
caller-supplied prompt text, running every tool the configuration allows.

Anyone who can enqueue a task of the right type can therefore run the agent. Restrict publish permission on the queue's
subjects to the callers that should have it, the way any other NATS resource is restricted.

`max_payload` bounds a caller's input before anything decodes it. The rest of what applies to any served run is covered
in [Serving](../serving/#safety).
