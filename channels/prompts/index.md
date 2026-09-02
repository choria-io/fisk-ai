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

`identity` must be one you wrote. It is the subject peers reach this worker on and the queue group it joins, so a name
taken from the application binary or left at the default would put unrelated agents into one group, sharing each
other's work.

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

A caller publishes a request on `choria.fisk-ai.task.<identity>`:

```json
{
  "protocol": "io.choria.fisk-ai.v1.request.prompt",
  "id": "3Hzmp2RBLG713TfOTU5aJpATRg2",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:00:00Z",
  "sender": {"name": "peer1"},
  "prompt": "how many streams are there"
}
```

`request` names the turn. Every reply to it echoes the value, cancelling the turn addresses it by that
value, and so does answering a question the run asks, so pick it before you send and keep it. It must
name one turn and one only: two turns sharing a value make their replies indistinguishable, and a
cancel aimed at one of them stops both. It is at most 64 characters of letters, digits, `-` and `_`,
because a worker builds subjects from it.

`id` names the message rather than the turn, so it is fresh on every message including a resend, and
`conversation` is the caller's own tag across the turns of one conversation.

| Protocol                                 | Asks for                                    | Required                        | Also takes                                                             |
|------------------------------------------|---------------------------------------------|---------------------------------|------------------------------------------------------------------------|
| `io.choria.fisk-ai.v1.request.prompt`     | a turn: the agent runs the prompt            | `prompt`                        | `context`, `tool_hints`, `budget`, `stream`, `conversation_token`, `replay`, `force` |
| `io.choria.fisk-ai.v1.request.answer`     | a question answered and the run resumed      | `conversation_token`, `answer`  | `budget`, `stream`, `replay`, `force`                                  |
| `io.choria.fisk-ai.v1.request.resume`     | a run that stopped part way continued        | `conversation_token`            | `budget`, `stream`, `force`                                            |
| `io.choria.fisk-ai.v1.request.read`       | the conversation read back, no turn taken    | `conversation_token`, `replay`  | nothing                                                                |

The queued-jobs channel takes a `request.prompt` as its payload and none of the other three.

`request.answer` is described in [Answering after the run ended](#answering-after-the-run-ended), and `request.read`
in [Reading a conversation](#reading-a-conversation).

`context` is supporting material offered alongside the prompt, `stream: false` asks for the answer without the event
stream, and `conversation_token` joins an existing conversation, see [Follow-up turns](#follow-up-turns).

`force` is a caller's decision about its own conversation. Without it a worker refuses a resume across a changed model,
system prompt or tool set; with it the run continues under the current configuration and drops the standing approvals it
can no longer vouch for.

A budget above the worker's own configuration is ignored. On a conversation it limits the conversation rather than the
turn, since a run measures the whole journal's token count against it.

The reply set arrives on the request's own inbox, in order:

| Message           | When                                                        |
|-------------------|-------------------------------------------------------------|
| `ack`                    | once, first, saying whether the prompt was taken             |
| `event.<kind>`           | zero or more, carrying the run's output as it is produced    |
| `elicit.request.<kind>`  | a question the run puts to the caller, only when `elicit` is set |
| `result`                 | the answer, with its stop reason and token usage             |
| `error`                  | instead of a result when the run did not produce one         |

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

## Event blocks

Each event holds one block. Where an id under `io.choria.fisk-ai.v1.event.` is one your client does not recognize, keep
the message and render what you can rather than rejecting it: a newer worker sends kinds this one does not define.

| Protocol                                | Fields                                     | What it is                              |
|-----------------------------------------|--------------------------------------------|------------------------------------------|
| `io.choria.fisk-ai.v1.event.text`        | `text`, `final`                            | the model's prose                        |
| `io.choria.fisk-ai.v1.event.thinking`    | `text`                                     | the model's reasoning, when it produces any |
| `io.choria.fisk-ai.v1.event.tool_call`   | `id`, `name`, `input`                      | a tool the run is about to invoke        |
| `io.choria.fisk-ai.v1.event.tool_result` | `call_id`, `output`, `is_error`            | what that call returned                  |
| `io.choria.fisk-ai.v1.event.agent_call`  | `id`, `name`, `task`                       | a question delegated to a peer agent     |
| `io.choria.fisk-ai.v1.event.warning`     | `kind`, `name`, `count`, `params`, `error` | an advisory the run raised               |
| `io.choria.fisk-ai.v1.event.prompt`      | `text`                                     | a turn somebody asked for; sent only in a replay |
| `io.choria.fisk-ai.v1.event.status`      | `iteration`, `usage`, `phase`, `count`, `truncated` | progress, and the markers around a replay |

A text event in full:

```nohighlight
{"protocol":"io.choria.fisk-ai.v1.event.text","id":"3Hzmp8kRt1BqA4dQ2v9XnLcYm2T","request":"docs1",
 "conversation":"docs1","sequence":2,"time":"2026-08-16T11:24:11.104217Z",
 "sender":{"name":"nats-worker"},"block":{"text":"the stream is gone","final":true}}
```

**`final` marks the answer.** Only the run knows which message ended the turn, so without the flag a caller cannot tell
the answer from the narration on the way to it, and would render it twice when the same text arrives again in the
`result`.

**A `warning` names its kind and gives you the values, not a finished sentence.** Your client chooses the wording, and
a client that does not recognize a kind can still display the fields.

**A `tool_call` is not answered twice.** A call the caller was asked to approve carries the same `tool_use_id` as the
`elicit.request.approve` that asked, so a caller that drew the question knows it has already shown that call.

Not every call produces a result: a denied confirmation, a tool called without its required arguments, a tool that
answers later and an aborted run each end without one, so a caller pairing the two tolerates a call that is never
answered.

A `status` block reports progress, and its `usage` is what one model call consumed. The call that ends a turn sends no
status of its own, so a caller keeping a running total takes the totals from the terminal message rather than summing
these. The replay markers use the same block and are described below.

## Refusals and endings

The worker refuses a request it cannot parse with a NATS service error, before any acknowledgement:

```nohighlight
$ nats req choria.fisk-ai.task.nats-worker '{"protocol":"io.choria.fisk-ai.v1.request.resume","prompt":"...", ...}'
```

```nohighlight
Nats-Service-Error: the request is not a valid v1 message: jsonschema validation failed with
'https://choria.io/schemas/io.choria.fisk-ai.v1/request.resume.json#'
  - at '/prompt': false schema
Nats-Service-Error-Code: 400
```

A resume takes no prompt. Send `io.choria.fisk-ai.v1.request.prompt` to run one.

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
| `canceled`          | the run was stopped before it finished, by the worker rather than by the caller |
| `suspended`         | the run stopped at a resumable point, which is what a caller's own cancel reaches |
| `deferred`          | a tool will answer later, so the run is parked                      |
| `unknown_conversation` | the `conversation_token` names no conversation here; send the prompt without one |
| `conversation_busy` | a turn of this conversation is running here; wait for its terminal message |
| `turn_not_taken`    | the conversation could not take the turn, and the prompt did not run |
| `budget_exhausted`  | the conversation has used its whole token allowance and is finished  |
| `provider_busy`     | the agent's model provider had no capacity or refused a rate-limited call; wait and send the same work again |
| `provider_refused`  | the agent cannot use its model provider at all; an operator has to fix its credentials or its model name |
| `context_exceeded`  | the conversation holds more than the model's context window takes, so the model refused the call; start a new conversation or send less context |
| `unknown_call`      | no such call is waiting for an answer                                |
| `already_answered`  | the call already has an answer                                       |
| `answer_too_large`  | the answer is over 256KB                                             |

The worker refuses at capacity rather than queueing the prompt. `unknown_call`, `already_answered` and
`answer_too_large` are permanent: sending the same answer again reaches the same reply.

`budget_exhausted` ends the conversation, not just this request. The allowance belongs to the conversation, so every
later turn is refused no matter who sends it. Your prompt did not run and was not recorded. To carry on, send a prompt
with no `conversation_token` to start a new conversation. Only an operator on the machine running the agent can raise
`llm.budget.max_tokens`.

You can also hit this straight away, on a conversation that answered a moment earlier, by lowering `budget` on your own
request below what the conversation has already used.

Every `error` also has a `stop_reason` beside its `code`. `budget_exhausted` appears there when a run hit the cap part
way through a turn instead of before it started.

A `deferred` run is waiting for a tool answer. The `error` lists the calls. Answer one on a request carrying the
conversation token, or with `fisk session` on the worker holding the journal.

## Canceling

A caller cancels by publishing an `io.choria.fisk-ai.v1.cancel` on
`choria.fisk-ai.cancel.<identity>.<request>`, where `request` is the correlation id it sent. The request id is part of
the subject, so only the worker running that prompt is subscribed to it. It answers with an `ack`.

**A cancel asks the run to stop where the conversation can be continued.** It does not end the run where it stands: the
loop polls for it at each boundary and parks there, so the terminal message is `suspended` with the usage the turn
spent, and the conversation takes another turn whenever the caller sends one. A run blocked on a question is included,
since a cancel closes the question rather than leaving it asked with nobody to answer.

What that costs is the ability to stop a model call in flight. A run inside a tool that never returns reaches no
boundary, and a cancel will not move it; that escape hatch belongs to whoever operates the worker. A caller asks and an
operator compels.

Because the id is part of the subject, a caller mints it rather than being told it: set `id` and `request` on the
request before sending, so the tag is in hand before there is anything to cancel.

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
  "protocol": "io.choria.fisk-ai.v1.request.prompt",
  "id": "3Hzmq7WdPK628XjRVZ8cLmBUTh4",
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
  `request_timeout` leaves the conversation waiting on a deferred call. Answer the question and it takes turns again.
* **A configuration change ends a conversation.** Every turn is a resume, and the worker refuses a resume when the
  model, the system prompt, the thinking mode or the reasoning effort has changed since the conversation started. It
  answers `failed` and the caller starts a new conversation. A changed tool set does not end it: the turn runs, and
  the standing approvals the conversation held are dropped, since an approval names a tool and that tool may have
  moved under it.

The `usage` on a `result` counts the whole conversation rather than the turn, since it is read from the journal. An
`error` that ran and stopped carries it too, so a caller can tell what it owes for a turn it is about to continue.
Both also carry `trace_id`, the trace the worker recorded, which is empty when it exports no telemetry, and
`content_exported`, which says whether this turn's conversation itself reached that collector.

## Asking what an agent is

Ask an agent what it is before you send it anything. Run `fisk discover <identity>` to make that request. Every agent
that answers prompts also answers discovery, whether or not it serves tools as well. An agent that serves no tools to
peers answers with a card that lists none.

Two fields on the card describe what the agent does with a conversation:

| Field               | Meaning                                                                        |
|---------------------|--------------------------------------------------------------------------------|
| `telemetry`         | the agent exports traces of what it does                                        |
| `telemetry_content` | those traces carry the conversation itself, so a prompt sent here reaches the operator's collector |

They are published because a caller should know before it sends a prompt, and they are read off the worker's resolved
telemetry provider rather than its configuration, so a rejected endpoint does not leave the card promising an export
that will not happen. The card says what the agent is configured to do; `content_exported` on a terminal message says
what a turn actually did.

## Reading a conversation

To read a conversation back, send an `io.choria.fisk-ai.v1.request.read` with a `conversation_token` and a `replay`
count. The worker sends that many blocks of the stored conversation and ends the reply set:

```json
{
  "protocol": "io.choria.fisk-ai.v1.request.read",
  "id": "3Hzmr9YfRM839ZlTXb0eNoDVUj6",
  "request": "docs3",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:30:00Z",
  "sender": {"name": "peer1"},
  "conversation_token": "3Hzmp8VqrKL42NmXcPd7bTgWfR1",
  "replay": 200
}
```

Use this to show a conversation your client did not see live, such as one started on another machine. A finished turn
leaves a completed journal, and a plain resume will not continue one, so reading it is the only request such a
conversation accepts until you send the next prompt.

You get back the same blocks the run sent the first time, between two `status` blocks:

* `phase: "replay_start"` opens the history.
* `phase: "replay_end"` closes it, with `count` blocks sent, `truncated` when older ones were left behind, and `usage`
  for what the conversation has consumed so far. That `usage` is the whole conversation rather than one call, which is
  what lets a caller seed a running total before this turn's own calls arrive.

Set `replay` on each request that needs it. Leave it off a follow-up turn, which usually wants only the new blocks. The
worker sends at most 200 blocks whatever you ask for, and rounds up to a whole turn so that a result never arrives
without the call it answers. The largest useful value is therefore 200; ask for more and you get 200. A `read` asks for
at least 1.

Some of what the journal holds never leaves the worker: thinking signatures, the fingerprint, the caller, the
conversation token, the standing approvals, and the notes and handles of deferred calls. Long values are trimmed to fit
a block.

**`io.choria.fisk-ai.v1.request.resume` continues a run that stopped part way**, which is what a caller sends after a
`suspended` ending. Send it with the token and no `replay`.

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
  "protocol": "io.choria.fisk-ai.v1.elicit.request.approve",
  "id": "3Hzq6PkmVLsT9WqrChXkgF7NLwy",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 4,
  "time": "2026-08-16T11:24:11.912084Z",
  "sender": {"name": "nats-worker"},
  "recipient": {"name": "peer1"},
  "question_id": "3Hzq7RvnWMtU0XstDiYlhG8OMxz",
  "tool_use_id": "toolu_01A9bK2mNpQr",
  "command": "stream rm",
  "display": "stream rm ORDERS --force",
  "tag": "ai:confirm",
  "wait_ms": 120000
}
```

Every question has `question_id`, and may have `tool_use_id` and `wait_ms`:

| Protocol                                        | Asks                            | Fields                                     |
|-------------------------------------------------|---------------------------------|--------------------------------------------|
| `io.choria.fisk-ai.v1.elicit.request.approve`    | whether a gated command may run | `command`, `display`, and usually `tag`    |
| `io.choria.fisk-ai.v1.elicit.request.confirm`    | a yes or no question            | `question`                                 |
| `io.choria.fisk-ai.v1.elicit.request.select`     | one of a list                   | `question`, `options`                      |
| `io.choria.fisk-ai.v1.elicit.request.input`      | a free text value               | `question`, and `default` when it pre-fills |

`tag` is absent when the gate could name no trigger, which is a command rewritten to a tool that has none.

The caller answers on `choria.fisk-ai.elicit.<identity>.<request>`, where `request` is the correlation id it sent:

```nohighlight
$ nats req choria.fisk-ai.elicit.nats-worker.docs1 "$(cat answer.json)"
```

```json
{
  "protocol": "io.choria.fisk-ai.v1.elicit.reply.approve",
  "id": "3Hzq8TwoXNuV1YtuEjZmiH9PNya",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:24:19.310422Z",
  "sender": {"name": "peer1"},
  "question_id": "3Hzq7RvnWMtU0XstDiYlhG8OMxz",
  "choice": "once"
}
```

Reply under the id you were asked under, with `request` swapped for `reply`:

| Protocol                                            | Field       | Values                     |
|-----------------------------------------------------|-------------|----------------------------|
| `io.choria.fisk-ai.v1.elicit.reply.approve`          | `choice`    | `no`, `once`, `always`     |
| `io.choria.fisk-ai.v1.elicit.reply.confirm`          | `confirmed` | `true`, `false`            |
| `io.choria.fisk-ai.v1.elicit.reply.select`           | `index`     | a position in `options`    |
| `io.choria.fisk-ai.v1.elicit.reply.input`            | `value`     | any string, empty included |
| `io.choria.fisk-ai.v1.elicit.reply.no_operator`      | none        | no operator is available   |

Send the field even when its value is the zero one. `confirmed: false`, `index: 0` and `value: ""` are each an answer
somebody gave.

`io.choria.fisk-ai.v1.elicit.waiting` arrives on the same subject and is not an answer. It says the caller is holding
the question open, see [Holding a question open](#holding-a-question-open).

The worker replies with an `ack`. An answer to a question it is not waiting on gets a `404`, as does an answer sent
after the question's window closed, and as does a `waiting` sent after the question was answered.

`once` runs the command that one time. `always` stops the worker asking about that tool for the rest of the run. `no`
and `no_operator` both leave the command unrun and tell the model the refusal is final.

The worker holds the question for `expose.agent.a2a.request_timeout`, and its worker slot with it. The question's
`wait_ms` carries that number, so the caller knows how long it has. An unanswered question ends the run differently
depending on what asked it:

* an approval leaves the command unrun, and the run ends with `suspended`
* a human-in-the-loop tool leaves its call deferred, and the run ends with `deferred`

Both can be answered later, see [Answering after the run ended](#answering-after-the-run-ended). An operator on the
worker holding the journal answers a deferred call with `fisk session` instead.

### Holding a question open

A person reading a command approval can take longer than two minutes. A caller with the question in front of somebody
sends an `io.choria.fisk-ai.v1.elicit.waiting`, and each one restarts the window:

```json
{
  "protocol": "io.choria.fisk-ai.v1.elicit.waiting",
  "id": "3Hzq9UxpYOvW2ZuvFk0njI0QOzb",
  "request": "docs1",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T11:25:59.104812Z",
  "sender": {"name": "peer1"},
  "question_id": "3Hzq7RvnWMtU0XstDiYlhG8OMxz"
}
```

The rules a client follows:

* Send a `waiting` every `wait_ms / 3`, starting when the question goes on screen. The window restarts when the worker
  receives the message, so the remaining two thirds cover the round trip and one lost message. In Go,
  `wire.NewWaitingAck(question, sender)` builds the message and `question.AckInterval()` is the interval.
* Stop before sending the answer. A `waiting` that arrives after the answer is refused, since the worker has finished
  with the question.
* A `404` means the question is gone: take it off the screen and send no answer, since that would be refused too.
* A `400`, or a question with no `wait_ms`, comes from a worker older than this feature. Answer inside the window
  instead.
* Send `no_operator` when the person walks away. `waiting` says somebody is there to answer, and `no_operator` ends
  the question at once. Silence leaves the command unrun too, but only after a whole window.
* The reply set is silent while the worker holds the question, so a client learns the worker is still there only from
  the `ack` to each `waiting`.

A caller that sends no `waiting` either answers within the window or answers later, on a request of its own.

### Answering after the run ended

A person closes a laptop with a question on screen. The `waiting` messages stop, the window runs out, and the run ends
`suspended` or `deferred`. The worker unsubscribes from `choria.fisk-ai.elicit.<identity>.<request>` with the task, so
an hour later their answer reaches no responder.

They send an `io.choria.fisk-ai.v1.request.answer` instead, with the conversation token:

```json
{
  "protocol": "io.choria.fisk-ai.v1.request.answer",
  "id": "3Hzms0ZgSN940AmUYc1fOpEWVk7",
  "request": "docs4",
  "conversation": "docs1",
  "sequence": 0,
  "time": "2026-08-16T12:40:03Z",
  "sender": {"name": "peer1"},
  "conversation_token": "3Hzmp8VqrKL42NmXcPd7bTgWfR1",
  "answer": {
    "tool_use_id": "toolu_01A9bK2mNpQr",
    "kind": "approve",
    "answer": "choice",
    "choice": "once"
  }
}
```

Copy `tool_use_id` and `kind` from the question. A resumed run mints a new `question_id`, so the answer names the call
instead.

The `answer` object has `kind` and `answer` of its own. `answer` names the field holding the decision, and `kind` says
what that decision means where the value alone cannot: `no_operator` looks the same whichever question was asked, and
`value` serves both input and select.

| Field         | Value                                                                    |
|---------------|--------------------------------------------------------------------------|
| `tool_use_id` | the call the question named                                               |
| `kind`        | `approve`, `confirm`, `select` or `input`                                 |
| `answer`      | `choice` for approve, `confirmed` for confirm, `value` for select and input, or `no_operator` |
| `choice`      | `no`, `once` or `always`                                                  |
| `confirmed`   | `true` or `false`                                                         |
| `value`       | the text for input, and the chosen option for select                      |

A selection names the option, not its position.

You get back the usual `ack`, events, and a `result` or an `error`. The conversation gains no turn. A deferred call
takes the answer as its result; an approval is asked again by the resume and answered from the request.

A `400` means the answer does not fit its `kind`, or the message has no token, or it came with a prompt.

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

The worker records the token and the caller's claimed name with the journal, so a caller that lost a token can ask an
operator for it instead of losing the conversation. Find the conversation with `fisk session ls`, which lists the first
prompt and the time each journal was last touched, then read both values with `fisk session show <id>`. The listing has
no token column, because a token is a credential and `ls` output is often pasted into tickets.

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

The session store is shared with the other channels of this identity, and each names its journals in a space of its
own: a conversation here is a hash of the identity and the token, and a queued job is a hash of the identity and its
task id. So a queue submitter that learns one of these journal ids and spells it as a task id gets a journal of its
own rather than this conversation.

The worker records the token in the conversation's journal, so anyone who can read the session store can read the token
and continue that conversation. This gives away no access that reading the store did not already give, since the same
access reads and writes those journals directly, but the store needs the same protection the tokens do. The caller's
name is recorded beside the token, and it is the unverified claim from the `sender` field.

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
