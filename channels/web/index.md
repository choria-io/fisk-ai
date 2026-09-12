# Web

The web channel hosts the agent behind an HTTP listener a browser reaches. A page POSTs a turn, the run streams back on
the response, and the thread the page named is one conversation for as long as the page keeps posting to it. The turn
runs the same agent loop every other channel runs.

> [!info] Note
> The channel is opt-in. The configuration must carry an `expose.agent.web` block, and that block must list at least one
> origin. Available since
> {{% badge style="primary" title="Version" %}}0.0.6{{% /badge %}}.

> [!info] Warning
> Nothing on these routes is authenticated. Anyone who reaches them can read, continue and delete any conversation this
> channel holds.

Two frontend protocols are mounted, the [Vercel AI SDK UI message stream](https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol)
and [AG-UI](https://docs.ag-ui.com), and the configuration chooses
neither: turning one off prevents nothing an enabled one allows. The routes, the request and response shapes, the status
codes and where this channel departs from each protocol are in [the web protocol](../../design/protocol/web/).

## Frontends

The [AI SDK UI message stream](https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol) answers on `<base_path>/vercel`.
Vercel's `useChat` reads it, and [AI Elements](https://elements.ai-sdk.dev) is the component set built on that
hook: chat furniture vendored into a page as source by the `shadcn` command rather than imported as a library. This
format was built first because an agent that runs commands wants components that already exist. A page needs its
transport pointed at this channel's endpoint and nothing more, and a stock frontend works unchanged: it sends its whole
history, and the worker answers from its own journal instead.

[AG-UI](https://docs.ag-ui.com) answers on `<base_path>/agui`, and two third-party frameworks read it.
[assistant-ui](https://www.assistant-ui.com/docs/runtimes/ag-ui/overview) speaks it through its own AG-UI package and
posts at the endpoint directly, with nothing between the page and the worker.
[CopilotKit](https://docs.copilotkit.ai) runs a runtime route of its own, so a deployment on it puts that route in
front of the worker.

The two differ on how a page approves a confirmation-gated command. An AG-UI client draws the question as an interrupt,
and the standing allow, running the command and not asking about that tool again, is one of the three answers the
protocol carries. The AI SDK's approval is a boolean, so the standing allow and the three human-in-the-loop questions
travel in a field of Fisk's own; a client that never sets that field still works, with an approval that runs the command
once or refuses it.

assistant-ui hands an interrupt to the page rather than drawing it, so a console built on it writes the panel that
renders each question and submits the answer.

## Starting a worker

```yaml
identity: nats-ops
application_path: /usr/local/bin/nats
system_prompt: |
  You inspect NATS servers for whoever is at the console. Answer concisely.
llm:
  model: claude-sonnet-5
include:
  tools:
    - ^stream_
expose:
  agent:
    web:
      listen: 127.0.0.1:8080
      base_path: /fisk/v1
      origins:
        - http://localhost:5173
```

```nohighlight
$ fisk serve --config web.yaml
```

The banner names the address, the pages allowed to read the answers, every route it mounted and how many turns it runs
at once:

```nohighlight
Serving nats-ops/1.2.0:

         Endpoints: web
             Model: claude-sonnet-5
          Sessions: file
         Knowledge: disabled
       MCP Clients: none configured
         Telemetry: disabled
    Tool Directory: /var/lib/fisk-ai
      Tool Timeout: 5m0s

  Answering in a browser:

            Listen: 127.0.0.1:8080
         Base Path: /fisk/v1
           Origins: http://localhost:5173
            Routes: POST /fisk/v1/vercel, GET /fisk/v1/vercel/sessions/{id}, POST /fisk/v1/agui, GET /fisk/v1/agui/sessions/{id}, GET /fisk/v1/card, GET /fisk/v1/sessions, DELETE /fisk/v1/sessions/{id}
           Workers: 5
```

The address is the bound one, so a configuration naming port zero prints the port the system gave it.

A web turn runs the whole agent loop, so `identity`, `system_prompt` and `llm.model` are all required. The identity must
be one an operator chose: it is hashed into every journal this channel writes, so a name derived from the application or
left at the default is refused at startup.

The listener binds while the worker starts, so a port already in use fails before the banner.

## Configuration

```yaml
expose:
  agent:
    web:
      # Host and port to bind.
      listen: 127.0.0.1:8080
      # Path prefix every format is mounted under.
      base_path: /fisk/v1
      # The pages allowed to read the answers. Required.
      origins:
        - http://localhost:5173
```

| Field              | Description                                                             |
|--------------------|--------------------------------------------------------------------------|
| `listen`           | host and port to bind, default `127.0.0.1:8080`                          |
| `base_path`        | path prefix the formats are mounted under, default `/fisk/v1`            |
| `origins` (array)  | pages allowed to read the answers, required and with no default          |

`listen` defaults to loopback because an empty address binds every interface on port 80.

`base_path` must start with a slash. `fisk serve` refuses `fisk/v1`, since Go's `ServeMux` reads a pattern with no
leading slash as a host and a path, registers it, and answers `404` to every request.

`origins` names each page as a browser sends it in the `Origin` header: scheme, host and optional port with no path.
There is no default and no wildcard, so nothing reaches this channel until an operator names where the console is
served from. An entry of `*`, an entry carrying a path, and a block with no entries at all are each refused at startup.

Five turns run at once, with no key to change it. A request arriving above that is refused with `503` and a
`Retry-After: 5` header rather than left holding an open response.

## The agent card

`GET <base_path>/card` answers the agent's self description: its identity, its version, the model that answers a prompt,
the tools it holds and the behavior each declares. A console reads it to draw the agent. What an operator writes into it
sits beside `identity` rather than inside the web block:

```yaml
identity: nats-ops
display_name: NATS Operations
description: Inspects and operates the production NATS cluster.
icon_url: https://example.net/nats.png
prompts:
  - who can publish to ORDERS?
  - what changed in the auth config today?
```

| Key            | Description                                                                            |
|----------------|-----------------------------------------------------------------------------------------|
| `description`  | what the agent is for, in the operator's own words                                      |
| `display_name` | the human name, where `identity` is the name a caller addresses; up to 128 bytes         |
| `icon`         | an emoji drawn beside the name, up to 16 bytes                                           |
| `icon_url`     | an `https` URL of an image drawn beside the name, up to 512 bytes                        |
| `prompts`      | things a person can ask this agent, up to 8 of up to 256 characters each                 |

A reader with no `display_name` shows the identity. A console offers the `prompts` to somebody who has never used the
agent, so they read as the words to send rather than as a description of what it does. `icon_url` is a claim this agent makes and nothing verifies: the
card is checked for an `https` scheme, a host and the length, and whether to fetch the image is the console's decision.

The card is built for each request, so a worker restarted with a different model or a different tool set answers with
what it is running now. A value the card cannot carry fails there: an `icon_url` that is not `https` or names no host,
a `display_name`, an `icon` or a prompt over its limit, and a ninth prompt. The channel answers the request `500` and
logs the value.

> [!info] Note
> The a2a endpoint builds its card once and refuses a bad value at startup, so the worker does not start. The web
> channel refuses it per request, so the same value starts the worker, answers every other route, and fails only on the
> card.

## What a turn logs

Each turn writes one line when it ends, `A turn ended`, carrying the reason it ended, the error where there was one, and
whether it ended on a question. Every line the channel writes carries `channel=web`, the session the thread runs in, the
caller the request claimed and the turn id. A turn that put a question logs that too, with the kind of question and the
tool call it belongs to.

The reasons are `completed`, `suspended`, `error`, `budget` and `max_iterations`. A turn that ended on a question reads
as `suspended`: the run stopped where the next request continues it.

A refused request is logged too, naming the origin, the `Host` or the worker count it arrived above.

## Deployment

### Nobody is authenticated

Every request is anonymous. Neither format reads a caller name a request can be held to, the run records its caller as
unverified, and nothing in the agent decides anything on it. Anyone who reaches the session endpoints lists, opens and
deletes any conversation this channel holds.

On loopback that is adequate. Anywhere else it needs an authenticating proxy in front of the listener, which is the only
thing that keeps one person out of another person's conversation.

### TLS goes in front

A page served over https cannot call an http endpoint, so a deployment naming an `https://` origin puts a TLS proxy in
front of this listener. That proxy also authenticates the caller.

### Several workers

Several `fisk serve` processes may sit behind a load balancer. The channel holds nothing per thread: a thread is a
session in the store, an answer to a question lives for the one turn that carries it, and a second turn on a thread
already running is refused by the journal's own claim rather than by a map in one process.

Those workers need one journal they all read. The `file` backend is a directory on one machine, so two workers on it
answer the same thread from different conversations and show different session lists. A deployment of more than one
worker sets `harness.sessions.backend: jetstream`, covered in
[Sessions](../../agents/sessions/#jetstream-backend).

### Origins and the Host header

The worker enforces the origin list itself, before it reads a request body. CORS decides who may read an answer, while a
cross-origin POST still reaches a handler and still runs a turn, the browser only withholding the answer from the page.
A request carrying an `Origin` outside the list is refused `403`. A request carrying no `Origin` is not a browser's
cross-origin request and passes.

While the listener is on a loopback address the worker also checks the `Host` header, and refuses one that does not name
a loopback address on the bound port. That stops a page on the internet reaching the listener through a name its own DNS
resolved to loopback. On any other address the check is off: the proxy in front owns the public name and passes the
browser's `Host` through unchanged.

## Sessions

A conversation is a session in the store. The channel hashes the serving identity with the thread id the page sent, and
stores it under a `w-` prefix, so a page reaches only the conversations this agent minted for it and a thread id
belonging to a Slack conversation opens a new conversation of its own.

Listing, opening and deleting all filter to that prefix and to the identity on the journal, so one store shared by
several channels keeps a Slack thread and a peer's prompt out of the browser's list, and two agents on one JetStream
stream keep their conversations apart. A conversation journaled before the identity field existed carries none, and the
listing shows it.

A thread is a conversation, so the channel needs a session store it can read and `fisk serve` refuses to start it
without one. The endpoints that list, open and delete are in [the web protocol](../../design/protocol/web/).

## Safety

A web turn is a full agent loop driven by prompt text from an unauthenticated request, running every tool the
configuration allows. Reaching the listener is the whole of the access control, so an operator decides who may reach it,
with the origin list deciding only which page may read the answers.

Nothing the channel writes is escaped for a browser. Tool output, tool arguments, the model's own text and the command
line a confirmation gate shows are all model-supplied or tool-supplied, and a console escapes them before they reach the
page.

The rest of what applies to any served run is covered in [Serving](../serving/#safety).
