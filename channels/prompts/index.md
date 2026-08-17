# Answering prompts

The prompts channel takes a prompt from another agent over NATS and runs the agent loop over it. The caller waits and
receives an acknowledgement, then the events the run produces, then the answer or the failure.

> [!info] Note
> The channel is opt-in: without an `expose.agent.a2a.prompts` block `fisk serve` answers no prompts. Available since
> {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}.

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

| Field                | Description                                                             |
|----------------------|-------------------------------------------------------------------------|
| `context`            | supporting material offered alongside the prompt                         |
| `budget`             | lowers this worker's token and model-call limits                         |
| `stream`             | `false` asks for the answer without the event stream                     |
| `conversation_token` | runs the prompt as the next turn of a conversation, see [Follow-up turns](#follow-up-turns) |

A budget above the worker's own configuration is ignored. On a conversation it limits the conversation rather than the
turn, since a run measures the whole journal's token count against it.

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
| `unknown_conversation` | the `conversation_token` names no conversation here; send the prompt without one |
| `conversation_busy` | a turn of this conversation is running here; wait for its terminal message |
| `turn_not_taken`    | the conversation could not take the turn, and the prompt did not run |

The worker refuses at capacity rather than queueing the prompt.

A `deferred` run has stopped waiting for a tool answer. The `error` carries the session id and the outstanding tool calls.
An operator supplies the answer with `fisk session` on the worker holding the journal. Only an operator resumes it.

## Canceling

A caller cancels by publishing an `io.choria.fisk-ai.v1.cancel` on
`choria.fisk-ai.cancel.<identity>.<request>`, where `request` is the correlation id it sent. The request id is part of
the subject, so only the worker running that prompt is subscribed to it. It answers with an `ack`.

A no-responder error means this instance is not running that request: it was never accepted, it already finished, or
another instance took it.

## Follow-up turns

A caller can send another turn of the same conversation. Every `ack` that accepts a prompt carries a
`conversation_token`, and a later request carrying that token runs its prompt as the conversation's next turn:

```nohighlight
{"protocol":"io.choria.fisk-ai.v1.ack","id":"3Hzmp3SCMH824UgPUV6bKqBUSh3","request":"docs1",
 "conversation":"docs1","sequence":1,"time":"2026-08-16T11:24:10.749134Z",
 "sender":{"name":"nats-worker"},"recipient":{"name":"peer1"},"accepted":true,
 "conversation_token":"3Hzmp8VqrKL42NmXcPd7bTgWfR1"}
```

```json
{
  "protocol": "io.choria.fisk-ai.v1.request",
  "id": "docs2",
  "request": "docs2",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:26:00Z",
  "sender": {"name": "peer1"},
  "prompt": "what is the first one called",
  "conversation_token": "3Hzmp8VqrKL42NmXcPd7bTgWfR1"
}
```

A caller that asks once and stops ignores the token the worker handed it; one that wants another turn sends the token
it already has. A follow-up opens a reply set of its own, with its own `ack`, events, cancel address and terminal
message, so it is an ordinary request in every respect but which conversation it joins.

**No worker holds a conversation between turns.** Each turn loads the journal, runs, and stores the result, so any
instance in the queue group serves any turn. That also means a caller sends one turn at a time: a second turn sent
while the first is still running is refused with `conversation_busy`, and it must wait for the first turn's terminal
message rather than try another instance.

A conversation has no end state and no expiry: the journal stays in the session store until an operator removes it.

* **A turn cannot join a conversation waiting on a deferred tool result.** The worker answers `turn_not_taken` without
  running the prompt. With `elicit` set, a human-in-the-loop question the caller neither answers nor holds open within
  `request_timeout` leaves the conversation waiting on exactly that, so an operator supplies the answer with
  `fisk session` before the conversation continues.
* **A configuration change ends a conversation.** Every turn is a resume, and the worker refuses a resume when the
  model, the system prompt or the tool set has changed since the conversation started. It answers `failed` and the
  caller starts a new conversation.

The `usage` on a `result` counts the whole conversation rather than the turn, since it is read from the journal.

## Answering questions

A run puts a question back to the caller when it needs a person: an approval for a confirmation-gated command, or one
of the three human-in-the-loop questions. A run asks only when `elicit` is set.

> [!info] Warning
> Anyone who may answer this identity's questions can approve a confirmation-gated command in a run. An answer carries
> no verified caller identity.

```yaml
expose:
  agent:
    a2a:
      prompts:
        workers: 2
        elicit: true
```

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
  "tag": "ai:confirm",
  "wait_ms": 120000
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

| `answer`      | Field       | Values                            |
|---------------|-------------|-----------------------------------|
| `choice`      | `choice`    | `no`, `once`, `always`            |
| `confirmed`   | `confirmed` | `true`, `false`                   |
| `index`       | `index`     | a position in `options`           |
| `value`       | `value`     | any string, empty included        |
| `no_operator` | none        | no operator is available          |
| `waiting`     | none        | the caller is holding the question open, see [Holding a question open](#holding-a-question-open) |

The worker replies with an `ack`. An answer to a question it is not waiting on gets a `404`, as does an answer sent
after the question's window closed, and as does a `waiting` sent after the question was answered.

`once` runs the command that one time. `always` stops the worker asking about that tool for the rest of the run. `no`
and `no_operator` both leave the command unrun and tell the model the refusal is final.

The worker holds the question for `expose.agent.a2a.request_timeout`, and its worker slot with it. The question's
`wait_ms` carries that number, so the caller knows how long it has. An unanswered question ends the run differently
depending on what asked it:

* an approval leaves the command unrun, and the run ends with `suspended`
* a human-in-the-loop tool leaves its call deferred, and the run ends with `deferred`

An operator answers a deferred call with `fisk session`. No `fisk session` command answers an approval, so a resume
puts the question again.

### Holding a question open

A person reading a command approval can take longer than two minutes. A caller with the question in front of somebody
sends an `elicit.reply` with `answer: waiting`, and each one restarts the window:

```json
{
  "protocol": "io.choria.fisk-ai.v1.elicit.reply",
  "id": "3Hzq9UxpYOvW2ZuvFk0njI0QOzb",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:25:59.104812Z",
  "sender": {"name": "peer1"},
  "question_id": "3Hzq7RvnWMtU0XstDiYlhG8OMxz",
  "answer": "waiting"
}
```

The rules a client follows:

* Send a `waiting` every `wait_ms / 3`, starting when the question goes on screen. The window restarts when the worker
  receives the message, so the remaining two thirds cover the round trip and one lost message. In Go,
  `a2a.NewWaitingAck(question, sender)` builds the message and `question.AckInterval()` is the interval.
* Stop before sending the answer. A `waiting` that arrives after the answer is refused, since the worker has finished
  with the question.
* A `404` means the question is gone: take it off the screen and send no answer, since that would be refused too.
* A `400`, or a question with no `wait_ms`, comes from a worker older than this feature. Answer inside the window
  instead.
* Send `no_operator` when the person walks away. `waiting` says somebody is there to answer, and `no_operator` ends
  the question at once. Silence leaves the command unrun too, but only after a whole window.
* The reply set is silent while the worker holds the question, so a client learns the worker is still there only from
  the `ack` to each `waiting`.

A caller that sends no `waiting` answers within one window or loses the question.

## Concurrency and shutdown

Each prompt holds a worker slot from acknowledgement until the run ends. No setting limits total run time;
`harness.tool_timeout` limits a tool call and `llm.budget.call_timeout` limits a model call. With `workers: 1`, one
long run makes the worker refuse every other caller until it finishes. A run whose caller keeps sending `waiting` is
one such run, and it holds its slot for as long as the caller sends them.

An interrupt starts a drain, which takes the identity out of its queue group, so the worker accepts no further
prompts. The worker waits for runs already under way and answers their callers. A prompt acknowledged but not started
ends with `draining`. A second interrupt cancels the runs in flight and answers each caller with `failed`.

A drain stops restarting the window of a question already outstanding, so it ends within one window and the runs
behind it finish. A caller sending `waiting` at that point still gets an `ack`, but the `ack` no longer restarts the
window.

## Tool selection

A run started this way reaches every tool the top-level `include` and `exclude` selected, exactly as a queued job does.
`expose.agent.tools` selects what peers may invoke directly over MCP and a2a, and does not affect a run.

A command tagged `ai:confirm`, or a configured confirm tag, needs an approval before it runs. Without `elicit` the run
has no operator to ask, so the model sees the tool, calls it, and the worker refuses the call before the command runs.
The worker logs how many such tools the run loaded. With `elicit` the question goes to the caller, as [Answering
questions](#answering-questions) describes.

## Sessions

The worker mints a conversation token and journals every run under its hash. A crash leaves a resumable run, and a
deferred tool call has a journal to answer into. A caller holding the token continues that conversation; a caller that
wants the work redone from scratch sends the prompt without one.

`conversation` on a request is echoed on every reply and never names a journal. It is the caller's own correlation tag,
free for grouping whatever it likes, and the token names the conversation.

Journals from this channel are named `t-` and a hash, so an operator reading `fisk session ls` can tell a prompt's
journal from a queued job's. A session listing shows the last run's outcome, so a conversation resting between turns
reads as `completed`, and the prompt column shows the conversation's first prompt.

## Safety

NATS publish permission on `choria.fisk-ai.task.<identity>` is the only access control: anyone holding it runs this
agent's tools against a prompt of their choosing. A request carries no verified caller identity, and `sender` is an
unverified claim the worker records and logs.

A caller needs publish on the request subject. Canceling needs publish on `choria.fisk-ai.cancel.<identity>.>`, and
answering questions needs publish on `choria.fisk-ai.elicit.<identity>.>`. The reply set arrives on the caller's own
inbox.

Anyone holding the cancel permission who learns a request id can cancel a run they did not start. Anyone holding the
answer permission who learns a request id and a question id can approve a confirmation-gated command in a run they did
not start, and can hold that run's worker slot for as long as they keep sending `waiting`.

A `conversation_token` is a credential on the same terms: holding it is the authorization to add a turn to that
conversation, and any holder can continue a conversation, whoever started it. It carries more
than a fresh prompt does, because a standing approval an earlier turn recorded is restored with the conversation, so
with `elicit` set a turn can reach a confirmation-gated command that somebody else approved. Tokens carry 128 bits of
randomness and cannot be guessed, so treat one as a secret: this agent neither logs it nor puts it in an error message,
and a caller should not either.

The session store is shared with the other channels of this identity. The queued-jobs channel resumes the journal its
submitter names, so a party holding queue-submit rights who learns one of these journal ids can resume a conversation
started here. Restrict queue submission accordingly.

The worker logs the caller, the request id and the session as a prompt is accepted, runs and ends:

```nohighlight
level=INFO msg="Accepted a prompt" channel=a2a/prompts request=docs1 caller=peer1 session=3Hzq5OghUNlK8SnoBgVjfE6MKvx prompt_bytes=26
level=INFO msg=Running channel=a2a/prompts work=3Hzq5OghUNlK8SnoBgVjfE6MKvx caller=peer1 caller_verified=false resume=3Hzq5OghUNlK8SnoBgVjfE6MKvx
level=INFO msg="Ending a run" channel=a2a/prompts request=docs1 caller=peer1 session=3Hzq5OghUNlK8SnoBgVjfE6MKvx code=failed reason="llm call: 401 Unauthorized"
```

The worker logs the window it gave a question and, when the question closes, how long it held it and how many
`waiting` messages the caller sent. An operator reads from these which caller is holding a worker, and for how long:

```nohighlight
level=INFO msg="Asked the caller a question" channel=a2a/prompts request=docs1 caller=peer1 question=3Hzq7RvnWMtU0XstDiYlhG8OMxz kind=approve wait_ms=120000
level=INFO msg="A question was answered" channel=a2a/prompts request=docs1 caller=peer1 question=3Hzq7RvnWMtU0XstDiYlhG8OMxz held=14m32s acks=21
```

The tool safety rules in the [Reference](../../reference/#safety) apply here as everywhere else.
