# Queued jobs

The queued-jobs channel takes whole units of work off a [Choria asyncjobs](https://github.com/choria-io/asyncjobs) work
queue, runs the agent loop against each one, and stores the answer back on the task. The submitter holds no connection
to the worker: it enqueues a task, and reads the answer off the task record once a worker has written it.

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

Run time, retry cap and concurrency are properties of the queue, not of the agent configuration. The worker reads them
from the consumer at startup and prints them on the banner. The run time must be longer than a job takes, or the queue
redelivers work that is still running.

A worker started before either exists fails:

```nohighlight
fisk: error: building the jobs endpoint: connecting to queue "FISK_AI": storage not ready: stream CHORIA_AJ_TASKS does not exist, create it with 'ajc tasks initialize'
```

## Submitting work

A caller enqueues a task with the queue engine's own client. The task must name the configured queue and task type,
and carry a v1 `request` message as its payload:

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

On a request, `id`, `request` and `conversation` all carry the same value. `sender.name` is limited to letters,
digits, `-` and `_`.

```nohighlight
$ ajc tasks add fisk-ai:run --payload-file request.json --queue FISK_AI
Enqueued task 3Hwxl119ZbwKHCKPzWlslXZYnB0
```

The submitter supplies the task id, or the engine mints one. It also names the session the run journals under, so it
must be letters, digits, `-` or `_`. The queue itself accepts more.

Optional fields narrow what one job may do:

| Field                   | Description                                       |
|-------------------------|---------------------------------------------------|
| `context`               | supporting material offered alongside the prompt    |
| `budget.max_tokens`     | lowers the token budget for this job                |
| `budget.max_iterations` | lowers the model-call cap for this job              |

A budget may only lower what the configuration allows. A value above the configured limit is ignored.

The worker refuses a payload it cannot run and does not retry it, recording the reason in the task's `LastErr`. This
covers:

* an oversized payload
* a payload that is not a valid v1 request
* a request carrying no prompt
* a task id that cannot name a session

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

The `request` field echoes the id the caller submitted, and `recipient` names the caller that asked. `input_tokens`
counts every input token the job consumed. `cache_read_tokens` and `cache_create_tokens` are subsets of that total, not
additions to it.

A failed run is still a completed job. The worker stores a v1 `error` message with a `stop_reason` and acknowledges the
task. It is not retried: a model refusal or an exhausted budget fails the same way on redelivery.

| Stop reason         | Meaning                                          |
|---------------------|--------------------------------------------------|
| `end_turn`          | the agent finished and answered                   |
| `budget_exhausted`  | the token budget ran out                          |
| `max_iterations`    | the model-call cap was reached                    |
| `suspended`         | the run stopped at a point it can resume from     |
| `error`             | the run failed                                    |

## Redelivery

The worker journals every run under the task id. When a worker dies mid-job, the redelivery resumes that journal
instead of starting again.

A job whose session already completed is answered from the journal, without running the agent or calling the model.
This is the case when a worker finished a job and died before acknowledging the task.

> [!info] Note
> Deploying a changed tool set while jobs are in flight fails their resume check, and those jobs are retried until the
> queue's try limit and then expire. Drain a worker before replacing it.

## Configuration

Every field under `expose.agent.jobs` has a default, so an empty block is valid.

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

A worker only claims tasks of its configured `task_type`. Submit a different type and the task stays in the queue until
it expires, with no error logged at either end.

## Safety

Publish permission on the queue is the only access control. Anyone who can enqueue a task of the configured type runs
the full agent loop with prompt text of their choosing, against every tool the configuration allows. Restrict publish
permission on the queue's subjects the way any other NATS resource is restricted.

The rest of what applies to any served run is covered in [Serving](../serving/#safety).
