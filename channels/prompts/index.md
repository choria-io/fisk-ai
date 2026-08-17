# Answering prompts

The prompts channel takes a prompt from another agent over NATS and runs the agent loop over it. The caller waits and
receives an acknowledgement, then the events the run produces, then the answer or the failure.

> [!info] Note
> The channel is opt-in. The configuration must carry an `expose.agent.a2a.prompts` block, otherwise `fisk serve`
> answers no prompts.
>
> Answering prompts is available since {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}.

## Configuration

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

Answering a prompt runs the whole agent loop, so the configuration needs `identity`, `system_prompt`, `llm.model` and
`nats_context`. `application_path` is optional: an agent with only built-in tools, or with none, still answers
prompts.

```nohighlight
$ fisk serve --config prompts.yaml
```

```nohighlight
Serving nats-worker/1.2.0:

         Endpoints: a2a/prompts
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
             Answers: choria.fisk-ai.elicit.nats-worker.*
             Workers: 2
    Answering a prompt runs the agent loop and reaches every tool the top-level include and exclude selected.

  Serving tools over a2a:

           Discovery: choria.fisk-ai.discovery.nats-worker
               Tools: choria.fisk-ai.tool.nats-worker
         Concurrency: 4
        Tool Timeout: 1m0s
             Exposed: stream_ls
                      stream_info
```

`workers` is how many prompts the process answers at once. `--workers` does not change it; that flag applies to the
queued-jobs channel.

## Making requests

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

`prompt` is required. The optional fields are:

| Field     | Description                                                     |
|-----------|-----------------------------------------------------------------|
| `context` | supporting material offered alongside the prompt                 |
| `budget`  | lowers this worker's token and model-call limits for the one run |
| `stream`  | `false` asks for the answer without the event stream             |

A budget above the worker's own configuration is ignored.

The reply set arrives on the request's own inbox, in order:

| Message           | When                                                        |
|-------------------|-------------------------------------------------------------|
| `ack`             | once, first, saying whether the prompt was taken             |
| `event`           | zero or more, carrying the run's output as it is produced    |
| `elicit.request`  | a question the run puts to the caller, only when `elicit` is set |
| `result`          | the answer, with its stop reason and token usage             |
| `error`           | instead of a result when the run did not produce one         |

The acknowledgement comes first, so a plain `nats req` receives it and stops there:

```nohighlight
$ nats req choria.fisk-ai.task.nats-worker "$(cat request.json)"
```

```nohighlight
{"protocol":"io.choria.fisk-ai.v1.ack","id":"3Hzmp3SCMH824UgPUV6bKqBUSh3","request":"docs1",
 "conversation":"docs1","sequence":1,"time":"2026-08-16T11:24:10.749134Z",
 "sender":{"name":"nats-worker"},"recipient":{"name":"peer1"},"accepted":true}
```

Every message of the set carries `sequence`, numbered from the acknowledgement without gaps, so a caller can tell a
lost event from a quiet run. Events are advisory. The answer is in the terminal message, and the worker's run journal
is the authoritative transcript.

## Refusals and endings

The worker refuses a request it cannot parse with a NATS service error, before any acknowledgement:

```nohighlight
$ nats req choria.fisk-ai.task.nats-worker '{"protocol":"io.choria.fisk-ai.v1.request", ...}'
```

```nohighlight
Nats-Service-Error: the request is not a valid v1 message: jsonschema validation failed with
'https://choria.io/schemas/io.choria.fisk-ai.v1/request.json#' - at '': missing property 'prompt'
Nats-Service-Error-Code: 400
```

Everything the worker refuses after that is an `ack` with `accepted: false` and a reason, followed by an `error` that
closes the set. The `error` carries a `code` the caller can branch on:

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

The worker refuses at capacity rather than queueing the prompt.

A `deferred` run has stopped waiting for a tool answer. The `error` carries the session id and the outstanding tool calls.
An operator supplies the answer with `fisk session` on the worker holding the journal. No caller action resumes it.

## Canceling

A caller cancels by publishing an `io.choria.fisk-ai.v1.cancel` on
`choria.fisk-ai.cancel.<identity>.<request>`, where `request` is the correlation id it sent. The request id is part of
the subject, so only the worker running that prompt is subscribed to it. It answers with an `ack`.

A no-responder error means this instance is not running that request: it was never accepted, it already finished, or
another instance took it.

## Answering questions

A run puts a question back to the caller when it needs a person: an approval for a confirmation-gated command, or one
of the three human-in-the-loop questions. A run asks only when `elicit` is set.

```yaml
expose:
  agent:
    a2a:
      prompts:
        workers: 2
        elicit: true
```

> [!info] Warning
> Anyone who may answer this identity's questions can approve a confirmation-gated command in a run. An answer carries
> no verified caller identity.

The worker sends the question on the reply set, after the `ack` and before the `result` or `error`:

```json
{
  "protocol": "io.choria.fisk-ai.v1.elicit.request",
  "id": "3Hzq6PkmVLsT9WqrChXkgF7NLwy",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 4,
  "time": "2026-08-16T11:24:11.912084Z",
  "sender": {"name": "nats-worker"},
  "recipient": {"name": "peer1"},
  "question_id": "3Hzq7RvnWMtU0XstDiYlhG8OMxz",
  "kind": "approve",
  "command": "stream rm",
  "display": "stream rm ORDERS --force",
  "tag": "ai:confirm"
}
```

Each `kind` carries its own fields:

| Kind      | Asks                            | Fields                      |
|-----------|---------------------------------|-----------------------------|
| `approve` | whether a gated command may run | `command`, `display`, `tag` |
| `confirm` | a yes or no question            | `question`                  |
| `select`  | one of a list                   | `question`, `options`       |
| `input`   | a free text value               | `question`, `default`       |

The caller answers on `choria.fisk-ai.elicit.<identity>.<request>`, where `request` is the correlation id it sent:

```nohighlight
$ nats req choria.fisk-ai.elicit.nats-worker.docs1 "$(cat answer.json)"
```

```json
{
  "protocol": "io.choria.fisk-ai.v1.elicit.reply",
  "id": "3Hzq8TwoXNuV1YtuEjZmiH9PNya",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:24:19.310422Z",
  "sender": {"name": "peer1"},
  "question_id": "3Hzq7RvnWMtU0XstDiYlhG8OMxz",
  "answer": "choice",
  "choice": "once"
}
```

The `answer` value selects the field to read:

| `answer`      | Field       | Values                       |
|---------------|-------------|------------------------------|
| `choice`      | `choice`    | `no`, `once`, `always`       |
| `confirmed`   | `confirmed` | `true`, `false`              |
| `index`       | `index`     | a position in `options`      |
| `value`       | `value`     | any string, empty included   |
| `no_operator` | none        | no operator is available     |

The worker replies with an `ack`. An answer to a question it is not waiting on gets a `404`, as does an answer sent
after the run gave up.

`once` runs the command that one time. `always` stops the worker asking about that tool for the rest of the run. `no`
and `no_operator` both leave the command unrun and tell the model the refusal is final.

The run waits `expose.agent.a2a.request_timeout` for the answer, holding its worker slot. What an unanswered question
does depends on what asked it:

* an approval leaves the command unrun, and the run ends with `suspended`
* a human-in-the-loop tool leaves its call deferred, and the run ends with `deferred`

An operator answers a deferred call with `fisk session` on the worker holding the journal. An approval has no
out-of-band supply path, so a resume puts the question again.

## Concurrency and shutdown

Each prompt holds a worker slot from acknowledgement until the run ends. No setting limits total run time;
`harness.tool_timeout` limits a tool call and `llm.budget.call_timeout` limits a model call. With `workers: 1`, one
long run makes the worker refuse every other caller until it finishes.

A drain takes the identity out of its queue group, so the worker accepts no further prompts. The worker
waits for runs already under way and answers their callers. A prompt acknowledged but not started ends with
`draining`. A second interrupt cancels the runs in flight and answers each caller with `failed`.

## Tool selection

A run started this way reaches every tool the top-level `include` and `exclude` selected, exactly as a queued job does.
`expose.agent.tools` selects what peers may invoke directly over MCP and a2a, and does not affect a run.

A command tagged `ai:confirm`, or a configured confirm tag, needs an approval before it runs. Without `elicit` the run
has no operator to ask, so the model sees the tool, calls it, and the worker refuses the call before the command runs.
The worker logs how many such tools the run loaded. With `elicit` the question goes to the caller, as [Answering
questions](#answering-questions) describes.

## Sessions

The worker mints a session id and journals every run under it, so a crash leaves a resumable run and a deferred tool
call has a journal to answer into. `conversation` on a request is echoed on every reply and never names a journal, so
no caller can reach another caller's session.

No caller can resume that session. A caller that wants the work redone sends the prompt again.

## Safety

NATS publish permission on `choria.fisk-ai.task.<identity>` is the only access control: anyone holding it runs this
agent's tools against a prompt of their choosing. A request carries no verified caller identity, and `sender` is an
unverified claim the worker records and logs.

A caller needs publish on the request subject. Canceling needs publish on `choria.fisk-ai.cancel.<identity>.>`, and
answering questions needs publish on `choria.fisk-ai.elicit.<identity>.>`. The reply set arrives on the caller's own
inbox.

Anyone holding the cancel permission who learns a request id can cancel a run they did not start. Anyone holding the
answer permission who learns a request id and a question id can approve a confirmation-gated command in a run they did
not start.

The worker logs one line per prompt with the caller, the request id and the session:

```nohighlight
level=INFO msg="Accepted a prompt" channel=a2a/prompts request=docs1 caller=peer1 session=3Hzq5OghUNlK8SnoBgVjfE6MKvx prompt_bytes=26
level=INFO msg=Running channel=a2a/prompts work=3Hzq5OghUNlK8SnoBgVjfE6MKvx caller=peer1 caller_verified=false resume=3Hzq5OghUNlK8SnoBgVjfE6MKvx
level=INFO msg="Ending a run" channel=a2a/prompts request=docs1 caller=peer1 session=3Hzq5OghUNlK8SnoBgVjfE6MKvx code=failed reason="llm call: 401 Unauthorized"
```

The tool safety rules in the [Reference](../../reference/#safety) apply here as everywhere else.
