# Slack

The Slack channel hosts the agent behind a Slack bot. Somebody mentions the bot, a thread opens, and that thread is one
conversation for as long as people keep mentioning the bot in it. A question the agent asks is posted as a message with
buttons, and it may be answered days later.

> [!info] Note
> The channel is opt-in. The configuration must carry an `expose.agent.slack` block, and the worker reads
> `SLACK_APP_TOKEN` and `SLACK_BOT_TOKEN` from the environment.

The connection is Slack's socket mode, so the worker listens on no address and needs no public URL.

## Creating the Slack app

Create an app at [api.slack.com/apps](https://api.slack.com/apps) from this manifest:

```yaml
display_information:
  name: Fisk AI
features:
  bot_user:
    display_name: fisk-ai
oauth_config:
  scopes:
    bot:
      - app_mentions:read
      - channels:history
      - chat:write
      - groups:history
      - users:read
settings:
  event_subscriptions:
    bot_events:
      - app_mention
  interactivity:
    is_enabled: true
  socket_mode_enabled: true
```

Socket mode is the transport the channel connects over. `app_mention` is the only event it subscribes to. Interactivity
carries the button presses and the answers typed into a question back, so without it an answer to a question never
reaches the worker.

| Scope               | What it covers                                          |
|---------------------|----------------------------------------------------------|
| `app_mentions:read` | receiving the mention that opens or continues a thread    |
| `chat:write`        | the status message, the answer, the questions, the notes  |
| `channels:history`  | reading the conversation around a mention in a public channel |
| `groups:history`    | the same in a private channel                             |
| `users:read`        | resolving a user id to the name the model and the log see |

> [!info] Warning
> Changing an app's scopes or events does nothing until the app is reinstalled to the workspace. A worker whose bot
> token predates the change starts, connects, and then fails calls it has no scope for.

The credentials come out of the app:

| Token                    | Where it comes from                                       | Value           |
|--------------------------|-----------------------------------------------------------|-----------------|
| `SLACK_APP_TOKEN`        | Basic Information, an app-level token with `connections:write` | starts `xapp-`  |
| `SLACK_BOT_TOKEN`        | OAuth and Permissions, the bot user OAuth token            | starts `xoxb-`  |

Neither appears in the configuration file. A missing one fails at startup naming the variable, and a token Slack
refuses fails there too: the worker calls `auth.test` before it accepts anything.

Invite the bot to a channel with `/invite @fisk-ai`, then mention it.

## Starting a worker

```yaml
identity: helper
application_path: /usr/local/bin/nats
system_prompt: |
  You inspect NATS servers for the people in this Slack workspace. Answer concisely.
include:
  tools:
    - ^stream_
expose:
  agent:
    slack: {}
```

```nohighlight
$ export SLACK_APP_TOKEN=xapp-...
$ export SLACK_BOT_TOKEN=xoxb-...
$ fisk serve --config slack.yaml
```

```nohighlight
Serving helper/1.2.0:

         Endpoints: slack
             Model: claude-sonnet-5
          Sessions: file
         Knowledge: disabled
         Telemetry: disabled
    Tool Directory: /var/lib/fisk-ai
      Tool Timeout: 5m0s
```

A Slack turn runs the whole agent loop, so `identity`, `system_prompt` and `llm.model` are all required. The waiver
that lets a tool-serving configuration omit them does not apply.

## What a thread shows

A mention starts a turn, and that turn posts a status message it edits while the run works:
`:thinking_face: Thinking...`, `:hammer: Calling tools...`, `:books: Searching knowledge...`. The message names a
family of tools rather than the tool being run, because everybody in the channel reads the thread.

Each line opens with an emoji, so a thread scrolled past shows which turns worked and which did not before anybody
reads the words:

| The turn is                | The line reads                            |
|----------------------------|-------------------------------------------|
| waiting for a worker       | `:hourglass_flowing_sand: Queued...`      |
| thinking                   | `:thinking_face: Thinking...`             |
| using the memory tools     | `:brain: Accessing memory...`             |
| using the knowledge tools  | `:books: Searching knowledge...`          |
| using any other tool       | `:hammer: Calling tools...`               |
| waiting on somebody        | `:question: Waiting for an answer...`     |

The emoji is part of the line, so the notification a phone shows carries it too.

The status message carries a Stop button while the turn is running, and anyone in the thread may press it. The bot
finishes the step it is on and stops there. Everything the thread has said is kept, so mentioning the bot again carries
on from that point rather than starting the conversation over.

The answer is posted as a message of its own, and the status message becomes a link to it. Slack sends no notification
for an edit, so a turn that answered by editing its own status message would have pinged somebody with `Thinking...`
and told them nothing.

The answer goes out as markdown for Slack to render. The channel cuts it at 12,000 bytes and ends it with a note where
it did not fit. Everything else the channel says is plain text it wrote itself.

`no_progress` turns the status message off. The answer, the questions and the refusals are posted either way, and the
Stop button goes with the status message.

## Questions

A tool that needs a person asks in the thread, as a message of its own. It opens by mentioning whoever started the
turn, so they are notified, and anybody in the thread may answer whether or not they asked the question.

| Question              | The thread shows                                                                    |
|-----------------------|--------------------------------------------------------------------------------------|
| a yes/no question     | Yes and No                                                                           |
| a confirmation gate   | Allow once, Allow for this conversation, and Decline                                 |
| a selection           | the options as a numbered list in the message, and a button per number under it       |
| a free-text question  | a field to type the answer into, sent by pressing enter                              |

The first three carry Dismiss beside them, which answers the tool that a person was reached and gave no answer. The
gate's Decline is its dismissal: the gated command not running is the whole of what declining a gate can mean.

A selection puts the options in the message rather than on the buttons because a button label is cut at 75 characters,
where the message holds 3000. Twenty-five options share those characters, so a long option is cut to its share of them
and every option is on the list.

Nothing expires. The question stays in the thread until somebody answers it, and the bot carries on from that answer
whenever it arrives: a minute later, on Thursday, or after the worker has been restarted in between. The status message
reads `:question: Waiting for your answer.` in the meantime, and the question message records who answered and what
they chose.

`answer_grace` is how long the bot stays on that thread before it goes back to answering other people. It changes
nothing about how long an answer is accepted for.

The worker never sees a plain reply in the thread. Only `app_mention` is subscribed, so words typed under a question
reach it through the question's own field or through a mention and no other way, and every question message says so. A
mention answers a free-text question; while any other kind is open the channel refuses the mention with a link to the
question.

> [!info] Warning
> `Allow for this conversation` on a confirmation-gated command covers the whole thread, not one turn and not the
> person who pressed it. Anybody who can mention the bot in that thread runs that command from then on.

## How a turn ends

The status message says what became of the turn:

| Ending                         | The thread shows                                                                            |
|--------------------------------|----------------------------------------------------------------------------------------------|
| the agent answered             | the answer as its own message, and `:white_check_mark: Done: see the answer`                 |
| the agent answered nothing     | `:white_check_mark: I finished, but had nothing to say.`                                     |
| a question is unanswered       | `:question: Waiting for your answer.`                                                        |
| Stop was pressed               | `:octagonal_sign: Stopped. Mention me in this thread to carry on.`                           |
| a gate was never approved      | `:octagonal_sign: Nobody answered my question in time, so I stopped. Answer it and I will carry on.` |
| the worker drained             | `:octagonal_sign: I was shut down part way through. Mention me to carry on.`                 |
| the token budget ran out       | `:octagonal_sign: This conversation has used its allowance. Start a new thread to carry on.` |
| the model-call cap was reached | `:octagonal_sign: I ran out of steps on this one. Mention me to carry on.`                   |
| the run failed or crashed      | `:x:` and one line saying so                                                                 |

The budget and the step cap read as parked rather than as faults, since both lines tell the person where to carry on.
A run that finished with nothing to say reads as answered, the run having finished.

No ending names a session, a tool call or a Go error. The worker log has all three, and a thread is read by everybody
in the channel.

Where something went wrong on the way to the answer, such as a tool that ran out of time or a memory index the bot
could not read, one message under the answer says so in a sentence. It names the kind of problem and nothing a tool
returned.

## Capacity

`workers` turns run at once. A mention that arrives with no slot free shows `Queued...` until one frees. Once
`max_waiting` threads are already waiting, the next mention gets a short reply asking the person to come back in a few
minutes, which is better than watching a queued message for three of them.

Three lines typed in ten seconds are one thought, so further mentions from the same person reach the bot as one
follow-up turn, up to `max_coalesced` messages. A mention from somebody else gets a turn of its own behind that one,
with its own status message and its own answer.

A turn that ended waiting on a question, or that somebody stopped, cannot take those extra lines. The thread gets them
back as `I did not get to: ...`, so the person can see which of their messages went unanswered and send them again.

## Shutdown and faults

A drain stops the channel taking mentions. A thread whose turn was still waiting is told the turn will not run, and a
turn already going stops where the next mention can carry it on and says so on its status message. The connection
closes last, so a turn still finishing keeps receiving stop presses and answers to its questions.

The socket mode client reconnects on its own, so a dropped connection is logged and waited out. A revoked or invalid
token is a fault: `fisk serve` drains and exits non-zero, and a supervisor restarts the worker.

> [!info] Warning
> A worker that dies mid-turn leaves a status message that never changes again. Slack does not send the mention a
> second time, and nothing tidies the message up at startup. The conversation survives: the next mention in that thread
> carries on from what the journal holds.

### One worker per bot token

Run one `fisk serve` per bot token. Slack allows an app up to ten socket connections and spreads envelopes across them,
which this channel cannot use: the threads it is running and the questions it is holding are in process memory, so a
button press delivered to a process that holds neither reaches nothing.

A press that lands on a worker with no record of the question still works, because the interaction is self-describing
and the session derives from the thread. Everything else, from a Stop press to a mention folded into a running turn,
needs the process that holds the turn.

## Sessions

The session is a hash of the serving identity, the team, the channel and the thread, so two agents in one workspace
keep their conversations apart and one agent keeps two threads apart.

A thread is a conversation, so the channel needs a session store it can read: `fisk serve` refuses to start the Slack
channel without one. Threads outlive workers, so a deployment across machines wants a shared `harness.sessions`
backend rather than the default file store.

## Configuration

Every field under `expose.agent.slack` has a default, so an empty block is valid.

```yaml
expose:
  agent:
    slack:
      # Turns this process runs at once. --workers does not reach it.
      workers: 5
      # Messages of surrounding conversation a turn reads.
      context_lines: 20
      # Turns off the status message, and the Stop button with it.
      no_progress: false
      # How long a question is held before the run defers.
      answer_grace: 30s
      # Admitted turns waiting for a worker before a mention is refused.
      max_waiting: 10
      # Messages folded into one follow-up turn.
      max_coalesced: 5
```

| Field                     | Description                                                        |
|---------------------------|---------------------------------------------------------------------|
| `workers` (int)           | turns running at once, default `5`                                  |
| `context_lines` (int)     | surrounding messages a turn reads, default `20`                     |
| `no_progress` (boolean)   | turns off the status message and the Stop button, default `false`   |
| `answer_grace` (duration) | how long a question is held before the run defers, default `30s`    |
| `max_waiting` (int)       | admitted turns waiting for a worker, default twice `workers`        |
| `max_coalesced` (int)     | messages folded into one follow-up turn, default `5`                |

`workers` defaults to 5 where the other channels default to 1. A thread is a person waiting, and with one worker the
second person to ask anything watches a queued message until the first person's run finishes.

`--workers` sizes the queued-jobs intake and does not reach this channel. `context_lines` covers both reads a turn
makes: the conversation around a mention that opens a thread, and what was said in a thread since the bot last replied.

## Safety

Channel membership is the whole of the access control. Anybody who can see a channel the bot is in can run the full
agent loop against every tool the configuration allows, and can answer any question the agent asks there. Whoever can
invite the app decides who reaches the tools.

Every run records its caller as the Slack username and user id, and nothing consults that record for a decision.

The rest of what applies to any served run is covered in [Serving](../serving/#safety).
