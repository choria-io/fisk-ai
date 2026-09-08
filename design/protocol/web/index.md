# Web channel

The web channel answers a browser over HTTP. A page POSTs a turn, the run streams back on the response, and the thread
the page named is one conversation for as long as the page keeps posting to it. Two frontend protocols are mounted, the
Vercel AI SDK UI message stream and AG-UI, beside an agent card and a session API that both formats share.

> [!info] Note
> The channel is available since {{% badge style="primary" title="Version" %}}0.0.6{{% /badge %}}.

> [!info] Warning
> Nothing on these routes is authenticated and every route reads and writes this agent's conversations. A listener
> reachable from anywhere but loopback needs an authenticating proxy in front of it.

## Turning it on

```yaml
identity: nats-ops
application_path: /usr/local/bin/nats
system_prompt: |
  You operate NATS.
llm:
  model: claude-sonnet-5
expose:
  agent:
    web:
      listen: 127.0.0.1:8080
      base_path: /fisk/v1
      origins:
        - http://localhost:5173
```

The presence of `expose.agent.web` is the switch. `listen` defaults to `127.0.0.1:8080`, `base_path` to `/fisk/v1`, and
`origins` is required. `identity`, `system_prompt` and `llm.model` are required beside the block: a web turn runs the
whole agent loop, and the identity is hashed into every journal this channel writes.

A page served over https cannot call an http endpoint, so a deployment naming an `https://` origin puts a TLS proxy in
front of this listener. That proxy also authenticates the caller.

## Routes

Every route sits under `base_path`. The paths below assume the default.

| Method   | Path                             | Answers                                              |
|----------|----------------------------------|------------------------------------------------------|
| `POST`   | `/fisk/v1/vercel`                | one turn as an AI SDK UI message stream              |
| `GET`    | `/fisk/v1/vercel/sessions/{id}`  | a stored conversation as an AI SDK UI message stream |
| `POST`   | `/fisk/v1/agui`                  | one turn as AG-UI events                             |
| `GET`    | `/fisk/v1/agui/sessions/{id}`    | a stored conversation as AG-UI events                |
| `GET`    | `/fisk/v1/card`                  | the agent card as JSON                               |
| `GET`    | `/fisk/v1/sessions`              | a page of stored conversations as JSON               |
| `DELETE` | `/fisk/v1/sessions/{id}`         | nothing, with `204`                                  |

The status codes are the same across every route, and a refusal body is plain text.

| Code  | When                                                                                              |
|-------|---------------------------------------------------------------------------------------------------|
| `200` | a turn, a stored conversation, the card or a listing                                              |
| `204` | a deleted conversation, and a CORS preflight                                                      |
| `400` | a body the format could not decode, a turn naming no thread or carrying neither a prompt nor an answer, an answer naming no call, an AG-UI `resume` array naming no interrupt this agent raised, an answer payload of the wrong shape, a bad `limit`, a cursor this agent did not mint |
| `403` | an `Origin` outside the configured list, or a `Host` that does not name the loopback listener on its bound port |
| `404` | an answer for a thread holding no conversation, a session this channel does not hold, an unmounted path |
| `405` | a method the route does not answer                                                                |
| `409` | a second turn on a conversation already running                                                   |
| `500` | a card that cannot be built, or stored conversations that cannot be listed or read                |
| `503` | a draining worker or one already running as many turns as it can, both carrying `Retry-After`; a turn caught by a shutdown after it was admitted, or one the worker abandoned, carrying none |

Request bodies are capped at 1 MiB and a larger one is refused as a body that could not be decoded.

A turn that fails after its response head is written stays a `200`, and the failure goes out as an `error` part or an
AG-UI run-error event.

## What the version in the base path means

Nothing in the channel reads `v1`. It is a segment of a default prefix an operator replaces whole, and it marks the
shape of the routes below it: their paths and methods, the fields on the card, the fields on a session row, and what
each status code means. Adding a field or a route leaves it where it is. Removing or renaming one, or changing what a
code means, moves it.

The format endpoints carry their own protocol versions. The AI SDK stream answers with
`x-vercel-ai-ui-message-stream: v1`, and AG-UI sends no version header.

## Taking a turn

A turn is one POST. The request names the thread and carries a prompt, an answer to the question the last turn ended
on, or both.

{{< tabs >}}
{{% tab title="AI SDK" %}}
```json
{
  "id": "thread-42",
  "messages": [
    {
      "id": "msg-1",
      "role": "user",
      "parts": [{"type": "text", "text": "list the streams"}]
    }
  ]
}
```
{{% /tab %}}
{{% tab title="AG-UI" %}}
```json
{
  "threadId": "thread-42",
  "runId": "run-1",
  "messages": [
    {"id": "msg-1", "role": "user", "content": "list the streams"}
  ]
}
```
{{% /tab %}}
{{< /tabs >}}

The thread id is the page's own name for the conversation. It is hashed with the serving identity into the session id
the journal is written under, so a page reaches only the conversations this agent minted for it, and a thread id
belonging to a Slack conversation opens a new conversation of its own instead.

Both endpoints answer with server-sent events. The AI SDK stream sends one `data:` line per stream part and ends the
body with `data: [DONE]`. AG-UI opens with `RUN_STARTED` and a `fisk.capabilities` custom event, sends one event per
AG-UI event, and finishes with `RUN_FINISHED` or `RUN_ERROR`. Both set `content-type: text/event-stream`,
`cache-control: no-cache`, `connection: keep-alive` and `x-accel-buffering: no`, and both flush after every event.

The response stays open for as long as the run does, whether or not the page is still reading. A closed tab does not
cancel a conversation, and the answer is in the journal either way.

## The conversation belongs to the worker

The AI SDK's documented server contract has the server rebuild the conversation from the `messages` array the client
sent. This channel reads the last message of that array, takes it as the person's turn when it is the person's own, and
takes everything else from its own journal. AG-UI is read the same way: the last message of the thread, and nothing
else the client says about the conversation.

A stock frontend works, since it sends the whole history and the worker ignores it. Anything that depends on the
client's history being authoritative behaves differently:

* regenerate resends the same body and starts a new turn rather than replacing the last one
* edit-and-resend posts the edited text as the next turn, leaving the original where it is in the journal
* deleting a message on the client removes it from the page and from nothing else

A console that wants those verbs has to reach them through the session endpoints or do without them.

## A question ends the turn

A page reading a response cannot answer until that response is over. So a question the run asks is the last thing on
the response that ends the turn, the tool call it guards is left unanswered, and the answer arrives on the page's next
POST, which resumes the conversation and dispatches that same call again.

The questions are the confirm gate's approval of a command that has not run, and the three human-in-the-loop tools
`ask_human_confirm`, `ask_human_select` and `ask_human_input`. An answer names the tool call rather than a
question id, because the question is asked again under a new id on every resume, and the call is the one thing both
ends agree an answer is about.

Nothing persists a question or an answer. The answer is held for the one turn that carries it, so any worker can serve
the turn that answers a question another worker asked.

A POST that carries a prompt while a question is outstanding does not deliver it. The resume dispatches the gated call,
the question is put again, and the turn ends without the conversation reaching a point that takes a user message. The
AI SDK stream reports that as an `error` part, AG-UI as a `fisk.prompt_not_taken` custom event, and the page has to
send the message again once the question is answered.

A request that carries an answer and a typed message delivers both. The answer settles the question it names, the run
resumes, and where the conversation reaches a boundary that takes a user message the message is delivered as the turn
after the answered one. Where it does not, the not-taken notice fires the same way it does for a message-only request.

## Questions on the wire

On the AI SDK endpoint an approval arrives as two parts. A `tool-approval-request` mutates the tool part its
`toolCallId` names, and the confirm gate runs before the run traces the call, so the format synthesizes that part
first: a `tool-input-available` carrying the call's id as `toolCallId`, the command path as `toolName`, and
`{"command": "<the rendered command line>", "tag": "<the tag that gated it>"}` as its `input`. The approval part
follows, naming the same `toolCallId`, with an `approvalId` of `approval-<toolCallId>` and the rendered command line as
its `reason`. An answer names that `toolCallId` as its tool use id, which is where a console that does not use the
vendored components gets it.

The confirm gate has three answers: refuse, run it once, and run it and stop asking about this tool. The AI SDK's
approval is a boolean and has no third state, so the AI SDK endpoint carries the three-way answer, and the three
human-in-the-loop questions, in a `fiskAnswer` field on the POST body.

```json
{
  "id": "thread-42",
  "messages": [],
  "fiskAnswer": {"toolUseId": "toolu_016s", "kind": "approve", "approval": "always"}
}
```

| Field       | Description                                                                 |
|-------------|-----------------------------------------------------------------------------|
| `toolUseId` | the call being answered, from the question that asked about it              |
| `kind`      | `approve`, `confirm`, `select` or `input`                                   |
| `approval`  | `no`, `once` or `always`, for `approve`                                     |
| `confirmed` | the boolean answer, for `confirm`                                           |
| `index`     | the position of the option chosen, for `select`                             |
| `value`     | the text answer, for `input`, where an empty string is a valid answer       |

A page that never sets `fiskAnswer` still works. An approval answered through the SDK's own `approval-responded` part
reaches the gate as run-it-once or refuse, and the questions that have no shape in the SDK arrive as `data-question`
parts carrying `toolUseId`, `kind`, `question`, and `options` or `default`.

AG-UI needs none of this. Each question is an AG-UI interrupt on the run-finished event, carrying the question in
words and a JSON Schema for the payload that answers it, and the client answers in the `resume` array of its next run.

```json
{
  "threadId": "thread-42",
  "runId": "run-2",
  "messages": [],
  "resume": [
    {
      "interruptId": "approve:toolu_016s",
      "status": "resolved",
      "payload": {"approval": "always"}
    }
  ]
}
```

The interrupt id is the question's kind and the call it belongs to, joined by a colon, and both ends derive it rather
than remember it. A client that ignores the schema and sends the `{"approved": true}` AG-UI recommends gets run-once or
refuse, the same two answers the AI SDK boolean gives. A `cancelled` entry settles nothing: the conversation resumes,
the call is dispatched again, and the question is put again.

The AI SDK endpoint takes a field of this agent's own for the answers its protocol cannot express, and the AG-UI
endpoint carries all four questions in the protocol's own vocabulary.

### An AG-UI thread ends on the run that asked

An approval interrupt sends no assistant message, the gate running before the run traces the call, so the run that
asked leaves nothing in the thread. A client whose accumulated thread ends on the person's own message and then answers
with no new message has that trailing message read as this turn's prompt and runs it a second time. A client for this
agent ends its thread on the run that asked.

### Answering and typing on the AI SDK endpoint

An approval answered through the SDK's own `approval-responded` part is read only when the newest message is the
assistant's. A stock frontend that answers that way and types in the same breath ends its history on the person's
message, so the request reaches the run as a prompt alone and the question is asked again. A page sending both in one
request puts the answer in `fiskAnswer`, which is read whatever the history ends on.

## The approval button does nothing on its own

The AI SDK's automatic continuation after an approval does not fire against a server the library does not control.
`addToolApprovalResponse` skips its automatic send while a response is streaming or submitted and never comes back to
it, and `lastAssistantMessageIsCompleteWithApprovalResponses` separately wants every tool part in the message to be
terminal. In stock AI Elements the approval control renders, the click lands, and nothing happens until the page POSTs
again. The console has to send that POST itself.

An answer sent on its own goes with no new user message, as `sendMessage(undefined, {body: {fiskAnswer}})`. The worker
takes an answer and a typed message on one body, and the client still renders that pair badly: a user message starts a
fresh assistant message, so the `tool-output-available` part for the answered call finds no part to update, which the
SDK raises as a stream error that ends the response early.

The response head names the assistant message the turn writes into. A request whose newest message is the assistant's
opens under that message's id, so the answered question's card is updated in place rather than a second copy of it
appearing beside the first.

## The agent card

`GET /fisk/v1/card` answers the agent's self description as JSON, built per request, so a worker restarted with a
different model or tool set answers with what it is running now.

| Field                          | Description                                                          |
|--------------------------------|-----------------------------------------------------------------------|
| `name`                         | the routing identity                                                 |
| `version`                      | the agent's own version                                              |
| `description`                  | what the agent is for, in the operator's words                       |
| `display_name`                 | the human name, where `name` is the identity                         |
| `icon`, `icon_url`             | an emoji and an `https` image URL, both decoration the agent asserts |
| `notes`                        | one sentence per source whose tools are missing from this card       |
| `protocols`                    | the message namespaces this agent speaks                             |
| `tools`                        | the tools, each with `name`, `description`, `input_schema` and `behavior` |
| `model`                        | the model that answers a prompt                                      |
| `telemetry`, `telemetry_content` | whether the agent exports traces, and whether they carry the conversation |

A tool's `behavior` carries `read_only`, `destructive`, `idempotent` and `open_world`, each a tri-state where an absent
field asserts nothing. Confirm-gated commands are on this card, where the a2a card drops them: this channel has an
operator in front of it who can approve one.

`icon_url` is an unverified claim from the configuration, checked for an `https` scheme and a length and nothing more.
Fetching it discloses every viewer to the host the card named, and proxying it creates a server-side request forgery, so
whether to fetch it at all is the console's decision.

## Listing, opening and deleting a conversation

Picking a past conversation is outside what either frontend protocol has words for, so these routes are Fisk's own and
every format shares them.

`GET /fisk/v1/sessions` takes `limit` and `cursor`. The limit defaults to 20 and is refused outside 1 to 100. The
cursor is the store's own value: a console stores the one it was given and hands it back unchanged, and it stays good
across a reload and a restart.

```json
{
  "sessions": [
    {
      "id": "w-9f2c...",
      "title": "list the streams",
      "created": "2026-09-08T09:12:44Z",
      "updated": "2026-09-08T09:13:02Z",
      "model": "claude-sonnet-5",
      "terminal": "completed",
      "summary": {"turns": 3, "context_tokens": 18422, "tool_calls": 5}
    }
  ],
  "cursor": "MTI4"
}
```

Rows come oldest first, which is the order a write-once journal can enumerate. `summary` is absent for a conversation
whose last turn ended before a turn recorded one, so a rail shows an empty slot rather than counts of zero. `terminal`
is empty for a conversation with a turn in flight. `cursor` is absent once the listing has reached the end, and a page
that fills to the limit always carries one, so a full last page is followed by an empty page carrying none.

`GET /fisk/v1/{format}/sessions/{id}` writes a stored conversation in the format it is mounted under: the user turns,
the assistant turns, and the calls with the results that answered them. A conversation the run left waiting on a
question ends on the question it stopped at, so opening it shows the approval card it was left on. The two formats
write it in different shapes.

The AI SDK stream sends the parts a live turn sends, with each text and reasoning block written whole rather than in
fragments. It is one assistant message and has no part for a person's turn, so a replayed user turn goes as a
`data-user-message` part whose data is `{"text": "<what the person typed>"}`. The client stores that part and no
shipped component draws it, so a console renders the user turns of a reopened conversation itself.

AG-UI sends the whole conversation as one `MESSAGES_SNAPSHOT` event rather than the text-message and tool-call events a
live run produces, so a client waiting for those renders nothing. The snapshot sits in an ordinary run frame:
`RUN_STARTED` and the `fisk.capabilities` custom event, the snapshot, then `RUN_FINISHED`, carrying the interrupt when
the conversation stopped on a question. The run id of that frame is the session id prefixed with `open-`, the request
naming no run of its own.

`DELETE /fisk/v1/sessions/{id}` answers `204`. An id this listing does not show is a `404` and is never removed.

## Nobody is authenticated

Every request is anonymous. Neither format reads a caller name out of a request, the run records its caller as
unverified, and nothing in the agent decides anything on it.

Anyone who reaches the session endpoints lists, opens and deletes any conversation this channel holds. The session id
prefix keeps a browser out of a Slack thread's journal and the agent identity keeps two
agents on one JetStream stream apart. Neither keeps one person out of another person's conversation.

That is adequate on loopback and nowhere else. A deployment past it puts an authenticating proxy in front of the
listener, and the caller then comes from a header that proxy sets, which is configuration this channel does not have
today.

While the listener is bound to a loopback address it refuses any request whose `Host` is not `127.0.0.1`, `::1` or
`localhost` on the bound port, which stops a page on the internet reaching a loopback listener through DNS rebinding.
The port is part of that check and a `Host` carrying none is read as port 80, so a development proxy that forwards a
browser's `Host` of `localhost:5173` to a listener on `127.0.0.1:8080` is refused `403`. Such a proxy sets the `Host`
to the listener's own address and port.

On any other listen address the check is skipped: the proxy in front owns the public name, and Caddy and Traefik pass
the browser's `Host` through unchanged.

## Escaping is the console's job

Nothing here escapes anything for a browser. The gate's rendered command line, tool arguments, tool output, the
assistant's own text and the reasoning beside it are all model-supplied or tool-supplied, and they reach the response
as they were produced. AG-UI's `fisk.warning` custom events carry a tool name and an error string from the same
sources. A console escapes all of it before it reaches the page.

## One turn at a time on a conversation

Two POSTs on one thread do not both run. The first append of a resume is the journal's claim record, written before any
model call, so the loser fails with nothing streamed and the channel answers it `409`. A console sends the next turn
after the last response ended.

One worker runs a fixed number of turns at once across all its threads, five today, from `web.DefaultWorkers` with no
configuration key to change it. A request arriving above that is refused `503` rather than taken and left holding an
open response with no head, and it carries `Retry-After` from the channel's own `retryAfter` constant, five seconds
today. A console reads the header rather than the number here: both values are constants the tests compare against, so
either can change without this page being updated.

## Running several workers

Several `fisk serve` processes may sit behind a load balancer, and the channel holds nothing between requests for that
reason: a thread is a session in the store, and a held answer lives for the one turn that carries it.

Those workers need one shared journal. The `file` session backend is a directory on one machine, so two workers reading
their own would answer the same thread from different conversations and show different session lists. A deployment of
more than one worker sets `harness.sessions.backend: jetstream`, covered in
[Sessions](../../../agents/sessions/#jetstream-backend).

## Origins

`origins` has no default and no wildcard, and a console is unreachable until an operator names where it is served from.
Each entry is what a browser sends in the `Origin` header, scheme, host and optional port with no path.

The channel enforces the list rather than leaving it to the browser. CORS decides who may read an answer, while a
cross-origin POST still reaches the handler and would still run a turn, so a request carrying an unlisted `Origin` is
refused `403` before its body is read. A request carrying no `Origin` is not a browser's cross-origin request and
passes.

A preflight from a listed origin is answered `204` advertising `GET, POST, DELETE, OPTIONS`, and an allowed response
carries `Access-Control-Expose-Headers: *` so a page can read a format's own headers.
