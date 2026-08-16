# Answering prompts

This channel takes a prompt from another agent over NATS, runs the agent loop over it, and streams the run back to the
caller as it happens. The caller waits: it receives an acknowledgement, then the events the run produces, then the
answer or the failure.

> [!info] Note
> The channel is opt-in. The configuration must carry an `expose.agent.a2a.prompts` block, otherwise `fisk serve`
> answers no prompts.
>
> Answering prompts is available since {{% badge style="primary" title="Version" %}}0.0.6{{% /badge %}}.

## Answering prompts

```yaml
identity: nats-worker
application_path: /usr/local/bin/nats
nats_context: production
system_prompt: |
  You operate NATS. Answer with what you did and what you found.
llm:
  model: claude-sonnet-5
include:
  tools:
    - ^stream_
expose:
  agent:
    a2a:
      serve_tools: true
      tool_timeout: 60s
      prompts:
        workers: 2
```

Answering a prompt runs the whole agent loop, so the worker needs everything a run needs: an `identity`, a
`system_prompt`, an `llm.model` and a `nats_context`. It does not need an application: an agent whose tools are all
built-in, or one with no tools at all, answers prompts perfectly well.

```nohighlight
$ fisk serve --config prompts.yaml
```

```nohighlight
Serving nats-worker/1.2.0:

          Surfaces: a2a/prompts
                    a2a
             Model: claude-sonnet-5
     Agent Context: production
          Sessions: file
         Knowledge: disabled
         Telemetry: disabled
    Tool Directory: /var/lib/fisk-ai
      Tool Timeout: 5m0s

  Answering prompts over a2a:

            Requests: choria.fisk-ai.task.nats-worker
             Cancels: choria.fisk-ai.cancel.nats-worker.*
             Workers: 2
    Answering a prompt runs the agent loop and reaches every tool the top-level include and exclude selected.

  Serving tools over a2a:

           Discovery: choria.fisk-ai.discovery.nats-worker
               Tools: choria.fisk-ai.tool.nats-worker
         Concurrency: 2
        Tool Timeout: 1m0s
             Exposed: stream_ls
                      stream_info
```

`workers` is how many prompts the process answers at once. The `--workers` flag does not change it: that flag sizes the
queued-jobs intake.

## What a caller sends

A caller publishes an `io.choria.fisk-ai.v1.request` message on `choria.fisk-ai.task.<identity>`, the same message the
queued-jobs channel takes as a payload:

```json
{
  "protocol": "io.choria.fisk-ai.v1.request",
  "id": "docs1",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:00:00Z",
  "sender": {"name": "peer1"},
  "prompt": "how many streams are there"
}
```

`prompt` is required. `context` adds supporting material, `budget` lowers this worker's limits for the one run, and
`stream: false` asks for the answer without the events on the way to it. A request may only lower a limit: a budget
above the worker's own configuration is ignored.

The reply set arrives on the request's own inbox, in order:

| Message   | When                                                    |
|-----------|---------------------------------------------------------|
| `ack`     | once, first, saying whether the prompt was taken         |
| `event`   | zero or more, carrying the run's output as it is produced |
| `result`  | the answer, with its stop reason and token usage         |
| `error`   | instead of a result when the run did not produce one     |

The acknowledgement comes first, so a plain request-reply sees it and nothing else:

```nohighlight
$ nats req choria.fisk-ai.task.nats-worker "$(cat request.json)"
```

```nohighlight
{"protocol":"io.choria.fisk-ai.v1.ack","id":"3Hzmp3SCMH824UgPUV6bKqBUSh3","request":"docs1",
 "conversation":"docs1","sequence":1,"time":"2026-08-16T11:24:10.749134Z",
 "sender":{"name":"nats-worker"},"recipient":{"name":"peer1"},"accepted":true}
```

Every message of the set carries `sequence`, numbered from the acknowledgement without gaps, so a caller can tell a lost
event from a quiet run. Events are advisory: the answer is in the terminal message, and the worker's own run journal is
the authoritative transcript.

## Refusals and endings

A request the worker cannot read at all is refused before anything is acknowledged, as a NATS service error with no
reply set behind it:

```nohighlight
$ nats req choria.fisk-ai.task.nats-worker '{"protocol":"io.choria.fisk-ai.v1.request", ...}'
```

```nohighlight
Nats-Service-Error: the request is not a valid v1 message: jsonschema validation failed with
'https://choria.io/schemas/io.choria.fisk-ai.v1/request.json#' - at '': missing property 'prompt'
Nats-Service-Error-Code: 400
```

Everything the worker refuses after that is an `ack` with `accepted: false` and a reason, followed by an `error` that
closes the set. The `error` carries a `code` a caller can decide on:

| Code                | Meaning                                                            |
|---------------------|--------------------------------------------------------------------|
| `capacity`          | every worker slot is busy; retry, or ask another instance           |
| `duplicate_request` | a run with this `request` id is already in flight here              |
| `draining`          | the worker is shutting down and never started this run              |
| `not_started`       | the prompt was taken and the worker stopped before running it       |
| `failed`            | the run ran and failed; the message says how                        |
| `crashed`           | a bug in this software; the detail stays in the worker's log        |
| `canceled`          | the caller canceled it                                              |
| `suspended`         | the run stopped at a resumable point                                |
| `deferred`          | a tool will answer later, so the run is parked                      |

A capacity refusal is immediate rather than a queue. The caller is waiting, so being told now that the worker is full is
worth more than an acknowledgement it cannot see the depth behind.

A `deferred` run is parked and nothing resumes it: the message names the session and the tool calls it is waiting on,
and an operator supplies the answer with `fisk session` on the worker that holds the journal.

## Canceling

A caller cancels by publishing an `io.choria.fisk-ai.v1.cancel` on
`choria.fisk-ai.cancel.<identity>.<request>`, where `request` is the correlation id it sent. Only the one worker running
that prompt subscribes there, so the message reaches it and no sibling hears it. It answers with an `ack`.

No responder means nothing is running there: never accepted, already finished, or running on another instance.

## Concurrency and shutdown

Each prompt holds a worker slot from the moment it is acknowledged until the run ends, and nothing bounds how long a run
may take beyond `harness.tool_timeout` and `llm.budget.call_timeout`. With `workers: 1` a single long run makes the
worker refuse every other caller until it finishes, so size `workers` for the traffic and cancel a run you no longer
want.

A drain takes the identity out of its queue group, so a peer sending during the drain reaches a sibling. Runs already
under way are waited for and answer their callers; a prompt acknowledged but not yet started ends with `draining`. A
second interrupt cancels the runs in flight, and each caller is told the run failed.

## What a run reaches

A run started this way reaches every tool the top-level `include` and `exclude` selected, exactly as a queued job does.
`expose.agent.tools` narrows what peers may invoke directly and narrows neither.

Confirmation-gated commands are refused inside the run. There is nobody to ask on this channel, so a command carrying
`ai:confirm` or a configured confirm tag is unavailable to the model rather than approved. The caller is not told this
on the event stream; the worker's log carries the advisory.

## Sessions

Every run is journaled under a session id the worker mints, so a crash leaves a resumable run and a deferred call has
somewhere for its answer to land. The id is the worker's, not the caller's: `conversation` on a request is echoed back
on every reply and never names a journal, so no caller can reach another's session.

Nothing resumes such a session in this release. A caller that wants the work done again sends the prompt again.

## Permissions

Whoever can publish to `choria.fisk-ai.task.<identity>` can make this agent run its tools against a prompt of their
choosing. NATS permissions are the whole of the access control: a request carries no verified caller, and the `sender`
in the body is an unverified claim the worker records and logs.

A caller needs publish on the request subject, publish on `choria.fisk-ai.cancel.<identity>.>` to cancel, and its own
inbox to receive the reply set. Anyone holding that cancel permission who learns a request id can cancel a run they did
not start.

The worker logs one line per prompt with the caller, the request id and the session:

```nohighlight
level=INFO msg="Accepted a prompt" channel=a2a/prompts request=docs1 caller=peer1 session=3Hzq5OghUNlK8SnoBgVjfE6MKvx prompt_bytes=26
level=INFO msg=Running channel=a2a/prompts work=3Hzq5OghUNlK8SnoBgVjfE6MKvx caller=peer1 caller_verified=false resume=3Hzq5OghUNlK8SnoBgVjfE6MKvx
level=INFO msg="Ending a run" channel=a2a/prompts request=docs1 caller=peer1 session=3Hzq5OghUNlK8SnoBgVjfE6MKvx code=failed reason="llm call: 401 Unauthorized"
```

The tool safety rules described in the [Reference](../../reference/#safety) hold here as everywhere else: commands run
as an argument vector rather than through a shell, arguments are bound to each command's schema, and credentials are
stripped from tool environments.
