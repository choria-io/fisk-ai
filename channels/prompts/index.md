# Answering prompts

The prompts channel takes a prompt from another agent over NATS and runs the agent loop over it. The caller waits and
receives an acknowledgement, then the events the run produces, then the answer or the failure.

> [!info] Note
> The channel is opt-in: without an `expose.agent.a2a.prompts` block `fisk serve` answers no prompts. Available since
> {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}.

The request and reply shapes, the event vocabulary, the error codes, cancelation and the question protocol are in
[the prompts protocol](../../design/protocol/prompts/).

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

| Key                 | Description                                                             |
|---------------------|--------------------------------------------------------------------------|
| `workers` (int)     | how many prompts this process answers at once, default `1`               |
| `elicit` (boolean)  | let a run put its questions to the caller, default `false`               |

Every field defaults, so an empty `prompts` block enables the endpoint.

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

`elicit` lets a run ask the caller the questions it would otherwise put to an operator at a terminal: whether a
confirmation-gated command may run, and the three human-in-the-loop questions.

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

The worker holds a question for `expose.agent.a2a.request_timeout`, and its worker slot with it. That key sits on the
`a2a` block rather than inside `prompts`, and raising it for a slow peer raises the question window too. What travels
on the wire is in [Answering questions](../../design/protocol/prompts/#answering-questions).

Every agent that answers prompts also answers discovery, so `fisk discover <identity>` reports what this worker is
running, described in [Asking what an agent is](../../design/protocol/prompts/#asking-what-an-agent-is).

## Concurrency and shutdown

Each prompt holds a worker slot from acknowledgement until the run ends. No setting limits total run time;
`harness.tool_timeout` limits a tool call and `llm.budget.call_timeout` limits a model call. With `workers: 1`, one
long run makes the worker refuse every other caller until it finishes. A run whose caller keeps sending `waiting` is
one such run, and it holds its slot for as long as the caller sends them.

An interrupt starts a drain, which takes the identity out of its queue group, so the worker accepts no further
prompts. The worker waits for runs already under way and answers their callers. A prompt acknowledged but not started
ends with `draining`, one of the codes in
[Refusals and endings](../../design/protocol/prompts/#refusals-and-endings). A second interrupt cancels the runs in
flight and answers each caller with `failed`.

A drain stops restarting the window of a question already outstanding, so it ends within one window and the runs
behind it finish. A caller sending `waiting` at that point still gets an `ack`, but the `ack` no longer restarts the
window.

## Tool selection

A run started this way reaches every tool the top-level `include` and `exclude` selected, exactly as a queued job does.
`expose.agent.tools` selects what peers may invoke directly over MCP and a2a, and does not affect a run.

A command tagged `ai:confirm`, or a configured confirm tag, needs an approval before it runs. Without `elicit` the run
has no operator to ask, so the model sees the tool, calls it, and the worker refuses the call before the command runs.
The worker logs how many such tools the run loaded. With `elicit` the question goes to the caller, as [Answering
questions](../../design/protocol/prompts/#answering-questions) describes.

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
