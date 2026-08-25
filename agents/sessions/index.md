# Session snapshots

Every run is a conversation, and every conversation is journaled. You do not turn this on. Leave a run and continue it
later, in a fresh process or on another machine, using the id it prints when it ends.

## Continuing a conversation

Ask something. fisk prints the id of the conversation when the run ends:

```nohighlight
$ fisk run "report on the ORDERS stream"
```

Continue it by that id. No prompt is given, since the conversation is restored from the journal; passing one is an
error:

```nohighlight
$ fisk run --resume t-3f2a9c...
```

fisk reads the conversation back before it goes on, so you continue in context rather than from a blank screen.

Against an agent somewhere else, see [remote agents](../remote/), the journal is on the worker
rather than here, so `--resume` takes the conversation token instead of the id. `fisk session show` on the worker prints
it.

## Chat sessions

Every full-screen run is a durable, resumable conversation.

Each turn is journaled, so the whole conversation survives leaving, a stop or a crash. Press `Ctrl-D` to leave the input
bar when you are finished for now; the status bar reads `ctrl-d done`. This does not end the conversation. fisk prints
how to continue it as it exits. `Ctrl-C` asks the current turn to stop at its next safe point. The conversation is kept
either way.

Resuming reads the conversation back into the viewport before the input bar opens, so you continue in context rather
than from a blank screen. Because the bar needs a real terminal, a conversation can only be continued in the full-screen
UI, not with `--no-tui` or over a pipe, where a run answers one prompt. A conversation has no "completed" state; remove
it with `session rm` once it is no longer needed.

## Stopping

The first `Ctrl-C`, or a SIGTERM, asks the run to stop where the conversation can be continued: the current step
finishes, the turn is journaled, and fisk prints how to continue it. A second gives up on the run and leaves.

Both keep the conversation. The difference is that the first lets the turn reach a safe point, so the work it had
already done is recorded rather than lost part way.

## Durability

A session is journaled event by event as the run proceeds: each model turn and each tool result is recorded as it
happens.

* A clean suspend is exactly-once. Nothing runs after the last recorded event, so a resume never repeats a tool call or
  an LLM call.
* A crash resumes from the last recorded event, so at most one tool call is repeated. A tool whose side effect completed
  but whose result was not yet recorded runs again on resume, since fisk cannot make an external side effect
  idempotent. Already-recorded turns and results are never replayed.

Resume a session against the same agent configuration it started with. A session can be resumed from anywhere, including
a machine that no longer has the original `agent.yaml`, so care is required: continuing a conversation against a
different model, tool set, or system prompt can make the replayed transcript incoherent. fisk fingerprints the
configuration when the conversation started and refuses to continue it when that no longer matches, naming what
changed. `--force`
overrides it, except for the provider: a session started against one `llm.provider` can never be resumed against
another. A session that already completed cannot be resumed.

## Managing sessions

A suspended or completed session is kept until it is removed. List, inspect, and remove sessions with the `session`
subcommands:

```nohighlight
fisk session ls
fisk session show <id>
fisk session show <id> --transcript
fisk session rm <id>
```

`session ls` lists each session with its status, model, and prompt. `session show` prints a session's counters and
status; `--transcript` shows the full conversation (prompt, thinking, narration, tool calls, and tool output). On an
interactive terminal `--transcript` opens the full-screen viewer with thinking and tool output folded, which `z` and `Z`
expand; `--no-tui`/`NO_TUI` prints it as line output instead. `session rm` deletes a session.

## Answering a deferred tool call

A tool can report that its answer arrives later rather than now. The run then suspends, releasing the process, and
resumes once the answer exists. `session show` lists what such a session is waiting on under `Waiting on`, giving the
`tool_use` id, the tool, and whatever the tool said it is waiting for.

The answer travels on a request carrying the conversation's token, described in
[Answering after the run ended](../../channels/prompts/#answering-after-the-run-ended). The tool is never called again: it
already started the work, which is why it deferred.

No tool that ships with fisk defers; the mechanism is for tools a Go program registers through `agent.Options.CustomTools`.

These commands read the `file` backend under `--state-dir` by default. To inspect sessions in a configured backend, a
jetstream stream or a file directory named in the config, pass that config with `--config`:

```nohighlight
fisk session ls --config agent.yaml
fisk session show <id> --config agent.yaml
```

Where a session is journaled is configurable through `harness.sessions`, which mirrors the shape of `harness.memory`.
Two backends ship: `file` (the default) and `jetstream` {{% badge style="primary" title="Version" %}}0.0.3{{% /badge %}}.
The block is optional; leaving it out keeps the `file` backend under the `XDG` state directory.

`fisk info` shows a `Sessions` section with the resolved backend and, for the jetstream backend, the stream and NATS
context, so you can confirm where sessions are stored without starting a run.

## File backend

The `file` backend keeps each session as a JSON-lines journal under a directory. Sessions are stored under the `XDG`
state directory, `$XDG_STATE_HOME/fisk-ai/runs`, defaulting to `~/.local/state/fisk-ai/runs`. Set `options.directory` to
move it off the default `XDG` path:

```yaml
harness:
  sessions:
    backend: file
    options:
      directory: /var/lib/fisk-ai/runs
```

`--state-dir` overrides `options.directory` for a single `run` or `session` command, so the flag always wins over the
configured path. It applies only to the `file` backend: combining it with a non-file backend is an error rather than a
silently ignored flag.

## JetStream backend

The `jetstream` backend keeps sessions as messages on a NATS JetStream stream instead of on disk, so a run suspended on
one machine resumes on another over a broker. It uses the connection from the configured `nats_context`, the same one
memory and remote tools use, and binds to a stream that must already exist: the agent never creates it, so you own the
stream's retention policy.

```yaml
nats_context: production

harness:
  sessions:
    backend: jetstream
    options:
      stream: FISK_SESSIONS
```

Create the stream first, subscribed to one wildcard subject and keeping a single message per subject so each run record
is write-once:

```nohighlight
nats --context production stream add FISK_SESSIONS \
  --subjects 'fisk.sessions.>' --max-msgs-per-subject=1 \
  --discard=new --discard-per-subject
```

The stream keeps messages forever by default, which suits sessions; do not set a max age or they would silently
expire. The subject prefix (`fisk.sessions` above) is yours to choose; the backend derives it from the stream's single
wildcard subject when it binds, so it is not set in the config. The backend fails at run start, rather than degrading silently, if
the stream does not exist or its configuration does not match this shape. Sessions are never namespaced by identity, so a
run started by one agent is found by another reading the same stream; keep separate environments in separate streams.
