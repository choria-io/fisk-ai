+++
title = "Agents"
description = "Create LLM agents from Fisk CLI applications"
toc = true
weight = 10
pre = "<b>1. </b>"
+++

The main feature of Fisk AI is creating AI agents from CLI tools written with [Fisk](https://github.com/choria-io/fisk).

Any tool built with Fisk, such as the `nats` or `choria` CLI, or an application made
with [Choria Application Builder](https://choria-io.github.io/appbuilder/), can be turned into an AI agent.

Fisk AI creates capable systems that use the abilities LLMs have, such as reasoning and text interpretation, in a safe
and deterministic manner.

Building an agent resembles building a CLI tool: describe the goals, give broad guidance, supply tools to interact with
the world deterministically, then run it on a shell like any other utility.

## Installation

On a Mac you can install `fisk-ai` using homebrew:

```nohighlight
brew tap choria-io/tap
brew install choria-io/tap/fisk-ai
```

Other Operating System users can download the latest release from the [releases page](https://github.com/choria-io/fisk-ai/releases).

## Basic agent

This example builds an AI agent that speaks in `cowsay` bubbles.

The steps make a quick CLI application using App Builder and then drive it in various ways using the LLM.

The example needs an Anthropic API key, the `cowsay` application (try `brew install cowsay`) and `fisk-ai` installed.

### Creating a CLI tool

This example uses [Choria Application Builder](https://choria-io.github.io/appbuilder/) to create a basic CLI tool that
can say and think. Any command line tool built with Fisk works.

First create an `ABTaskFile`:

```yaml
name: cowsay
description: Tools for the Cowsay LLM Agent
author: fisk-ai@choria.io

commands:
  - name: say
    description: Say something using a talking cow, does not accept emoji
    type: exec
    arguments:
      - name: message
        description: The message to send to the terminal
        required: true
        validate: is_shellsafe(value)
    command: |
      {{ default .Config.Cowsay "cowsay" }} {{ .Arguments.message | escape }}

  - name: think
    description: Think something using a thinking cow, does not accept emoji
    type: exec
    arguments:
      - name: message
        description: The message to send to the terminal
        required: true
        validate: is_shellsafe(value)
    command: |
      cowthink {{ .Arguments.message | escape }}
```

Now install `appbuilder`:

```nohighlight
$ brew tap choria-io/tap
$ brew install appbuilder
```

Then confirm the CLI tool works:

```nohighlight
$ abt
usage: abt [<flags>] <command> [<args> ...]

Tools for the Cowsay LLM Agent

Help: https://choria-io.github.io/appbuilder

Commands:
  help [<command>...]
  say <message>
  think <message>
```

```nohighlight
$ abt say 'Hello AI'
 __________
< Hello AI >
 ----------
        \   ^__^
         \  (oo)\_______
            (__)\       )\/\
                ||----w |
                ||     ||
```

### Creating an LLM agent

Turning this CLI into an LLM agent needs an `agent.yaml` file.

```yaml
# Command to introspect and expose as an agent
application_path: /opt/homebrew/bin/abt

harness:
  # Allow the LLM to prompt us for information if needed
  human_in_the_loop:
    enabled: true

llm:
  # Choose a Model and set safety budgets
  model: claude-haiku-4-5-20251001
  budget:
    max_tokens: 100000
    max_iterations: 50

# We want a cow joke machine!
system_prompt: |
  Tell jokes using Cows!

  You have tools that can render a cow saying < 120 character sentences, when asked use the tools to tell funny jokes.

  You tell cow jokes, no other kinds of jokes, strictly jokes about cows. If asked to tell non cow jokes, refuse and show no joke.

  Keep narration short, just stick to the jokes, don't say what you are doing or planning to do, just do it and don't repeat the joke
```

Run the agent after setting the API key:

```nohighlight
$ export ANTHROPIC_API_KEY="....."
$ fisk run --tool-output --no-tui 'tell me a joke '
-> say -- Why did th...space?
<-  
 ______________________________
< Why did the cow go to space? >
 ------------------------------
        \   ^__^
         \  (oo)\_______
            (__)\       )\/\
                ||----w |
                ||     ||

-> think -- To visit t...oooon!
<- 
 ______________________
( To visit the Moooon! )
 ----------------------
        o   ^__^
         o  (oo)\_______
            (__)\       )\/\
                ||----w |
                ||     ||


  There you go! A classic cow joke for you!

Run summary: model=claude-haiku-4-5-20251001 llm_calls=2 tool_calls=2 tokens=3536/113 latency=3.613s
```

The default is a running TUI. To make the output easy to show here, the run passes `--no-tui` and shows the tool call
output with `--tool-output`.

Now ask about a cat joke:

```nohighlight
$ fisk run 'tell me a joke about a cat'

  I appreciate the request, but I only tell jokes about cows! I'm strictly a cow joke specialist.

  If you'd like to hear some funny cow jokes instead, I'd be happy to moo-ve right into those for you!

Run summary: model=claude-haiku-4-5-20251001 llm_calls=1 tool_calls=0 tokens=1632/54 latency=1.341s
```

## Running the agent

The agent has three specific modes of execution:

* A shell script style output, plain text to STDOUT with an exit at the end of the task
* A TUI for interaction, optionally continuing to chat with the agent after the main task completes
* Serving the agent over the network using an Agent-to-Agent protocol (planned)

### TUI

The TUI mode is the default: a visual runner with hot-keys to show or hide thinking and tool output, and scrolling up
and down the session history. A chat box can optionally be enabled to continue a session.

In the TUI press the `?` key to get interactive help.

#### Chat after turn

In the TUI mode the `--chat` flag keeps a Chat bar usable once the prompt is processed, instead of exiting, for follow
up questions related to the session.

Type a follow-up and press Enter to send it; `Ctrl-D` ends the session, `Ctrl-C` aborts it. Up/Down recall this
session's earlier follow-ups. Alt-Enter (Option-Enter) moves to the next line rather than send.

### Shell mode

The TUI is turned off with `--no-tui`, and the system falls back to a simple terminal output format suitable for
scripting.

The model's prose is markdown: both the final answer and any mid-conversation updates. When stdout is a terminal it is
rendered for readability with a style matched to the terminal background; when stdout is piped or redirected, the raw
markdown is written so the result stays free of ANSI escape codes. Rendering can also be disabled with `--no-color`, or
the standard `NO_COLOR` environment variable.

Output is separated by kind. Only the final answer goes to stdout; everything else goes to stderr: the commands being
run, mid-conversation updates, a final run summary (LLM calls, tool calls, tokens, latency), and, when thinking is
enabled, the model's reasoning (each line prefixed with a thought bubble). This keeps stdout safe to pipe into other
tools.

#### One-shot runs

The common use case gives a `system_prompt` that describes the goals and approach (think of it as a one-file SKILL) and
a user prompt that provides the question to solve.

The LLM runs through the prompt and, once it reaches the end of its turn, finishes processing, and the session cannot
continue later. This resembles a shell utility.

#### HTTP debugging

As a debug or learning aid, all the HTTP requests can be logged to `http-debug.log` using the `--http-debug` flag.

## Model and run settings

The `agent.yaml` sets which model runs the agent, the budget that bounds a run, and whether the model exposes its
reasoning. The [Basic agent](#basic-agent) example above shows these together. The full set of configuration fields is in
the [configuration reference](../reference/).

### Model

`llm.model` selects the model and is required. It accepts any model identifier the Anthropic API accepts:

```yaml
llm:
  model: claude-sonnet-5
```

Larger models reason better on complex, long-horizon tasks; smaller models like Haiku are faster and cheaper for narrow
ones. When the agent exposes ten or more tools it relies on the model's server-side tool search, which recent models
support and older ones (Claude Opus 4.1 and earlier and local models) do not. Set `llm.no_tool_search` to send every tool
directly on an endpoint that does not implement tool search; when a large tool set cannot use it the run warns that all
tools are being sent directly. The [configuration reference](../reference/) lists the known models and their trade-offs.

### Budget

`llm.budget` bounds a single run so the agent loop cannot spend without limit:

```yaml
llm:
  budget:
    max_tokens: 200000
    max_iterations: 50
    call_timeout: 120s
```

| Setting          | Description                                            |
|------------------|--------------------------------------------------------|
| `max_tokens`     | cumulative token spend cap for the run, default 200000 |
| `max_iterations` | maximum agent loop iterations, default 50              |
| `call_timeout`   | per-call timeout as a duration string, default `120s`  |

The run stops with a summary once a budget is reached, whether or not the task is complete.

### Thinking

Extended thinking lets the model expose its reasoning before it answers. It is off by default:

```yaml
llm:
  thinking:
    enabled: true
```

When enabled, the reasoning is surfaced separately from the answer: as thought-bubble lines on stderr in shell mode, and
as folding thinking blocks in the TUI.

> [!info] Note
> Older models that predate adaptive thinking, such as Sonnet 4.5 and Haiku 4.5, reject the request. Leave thinking off
> for those.

### Terminal UI

Two harness settings govern the full-screen UI for an agent, independent of the per-run `--no-tui` flag:

```yaml
harness:
  no_tui: true
  no_bell: true
```

* `no_tui` is a persistent off switch: the agent always uses the line-by-line output, even on an interactive terminal, and the command line cannot turn the UI back on. Use `--no-tui` instead for a one-off run.
* `no_bell` silences the terminal bell. By default the full-screen UI rings the bell each time a run blocks on an approval gate or an `ask_human_*` prompt, so a waiting run is noticed even when unattended.

Both are negative switches and have no effect in the line UI.

## Tool selection

Run the `fisk info` command to verify what tools the agent has access to:

```nohighlight
$ fisk info
╭───────────────────┬────────┬───────────────────────────────────────────────────────┬──────╮
│ TOOL              │ SOURCE │ DESCRIPTION                                           │ TAGS │
├───────────────────┼────────┼───────────────────────────────────────────────────────┼──────┤
│ say               │ local  │ Say something using the configured command            │      │
│ think             │ local  │ Think something using a cow                           │      │
│ ask_human_confirm │ local  │ Ask the human operator a yes/no question at the te... │      │
│ ask_human_select  │ local  │ Ask the human operator to choose one option from a... │      │
│ ask_human_input   │ local  │ Ask the human operator to type a free-text value a... │      │
╰───────────────────┴────────┴───────────────────────────────────────────────────────┴──────╯

Prompt:

  Tell short jokes using Cows!
...
```

The output shows the `say` and `think` tools and some Human in the Loop tools. When the configuration sets a model,
`fisk info` also prints a Model section first, listing the resolved model and provider, whether thinking is enabled,
and how tool search will behave, so you can confirm the backend and feature gates without starting a run.

There are a few ways to control what tools are visible.

### Application tags

The application can declare that the LLM never gets the `think` tool:

```yaml
  - name: think
    description: Think something using a cow
    type: exec
    tags: [ ai:deny ]
    # ...
```

Adding the `ai:deny` tag to a command means Fisk AI never exposes that tool to the LLM. `fisk info` confirms the LLM
only gets the `say` tool now.

### Agent configuration

The `agent.yaml` can also include only certain tools:

```yaml
include:
  tools:
    - ^say
```

Or exclude certain tools specifically:

```yaml
exclude:
  tools:
    - ^think
```

This uses regular expressions over the tool name, and both can be used together. For example, include `^cow` but exclude
`^cow_think`.

A tool's name is its command path joined with underscores, so a nested command like `cow think` becomes the tool
`cow_think`. Grouping commands and hidden commands are skipped and never become tools.

Tools can also be included or excluded by tag:

```yaml
exclude:
  tags:
    - scope:system
```

This excludes any command that has the `scope:system` tag.

### Global flags

A wrapped binary often has application-level global flags that apply to every subcommand. `nats`, for example, has
`--context` to select a stored connection profile, alongside sensitive globals such as `--user` and `--password`. By
default none of these are exposed to the model. `global_flags` is an allowlist of the globals you want the model to be
able to set per command:

```yaml
global_flags:
  - context
```

Each named global becomes an argument on every leaf command tool, so the model can run `nats stream ls` against a chosen
context without you hard-wiring one. Names are the long flag name, with or without the leading dashes, and are validated
against the binary's real global flags at load; a name matching none is an error. Hidden and framework flags (like
`--help`) cannot be exposed, and a global that clashes with a command's own flag or argument is skipped for that command.
A global the application marks required is always exposed, whether or not it is listed, since the command cannot run
without it.

Run `fisk info` to see which globals a binary exposes; it lists the application's global flags and marks the ones you
have allowlisted.

### Session snapshots and resumption

#### Creating a snapshot

By default a run is ephemeral: its conversation lives only in memory and is lost when the process exits. `--checkpoint`
instead journals the run to a session on disk so it can be suspended and resumed later, in a fresh process or on another
machine. Sessions are the foundation for longer-running work where the agent may need to pause, for example while a slow
external step completes.

Start a checkpointed run. fisk prints the session id at startup; it is generated unless `--name` sets it:

```nohighlight
$ fisk run --checkpoint "report on the ORDERS stream"
$ fisk run --checkpoint --name orders-report "report on the ORDERS stream"
```

Resume a session by id. No prompt is given, since the original prompt is restored from the session; passing one is an
error:

```nohighlight
$ fisk run --resume orders-report
```

On resume fisk replays the conversation so far to stderr, so the run continues in context rather than from a blank
screen, then carries on from where it left off.

#### Chat sessions

`--chat` and `--checkpoint` combine into a durable, resumable conversation.

Each follow-up is journaled, so the whole conversation survives a suspend or a crash. Leaving the input bar with
`Ctrl-D` suspends the session rather than ending it (the status bar reads `ctrl-d suspend`): it stays resumable, and
fisk prints how to resume it on exit. `Ctrl-C` aborts; the journal is kept, so an aborted chat is still resumable
from its last completed turn.

Resuming a chat session reopens the input bar automatically; re-passing `--chat` is not needed (it is ignored on resume,
since the session already knows what it is), and fisk first replays the conversation into the viewport. Because the
input bar needs a real terminal, a chat session can only be resumed in the full-screen UI, not with `--no-tui` or over a
pipe. A checkpointed chat has no "completed" state; remove it with `session rm` once it is no longer needed.

#### Suspending

For a checkpointed run the first `Ctrl-C`, or a SIGTERM, requests a graceful suspend: the current step finishes, the
session is checkpointed, and the process exits printing how to resume it. A second `Ctrl-C` aborts immediately. A run
started without `--checkpoint` keeps the usual behavior, where `Ctrl-C` cancels it.

#### Durability

A session is journaled event by event as the run proceeds: each model turn and each tool result is recorded as it
happens.

* A clean suspend is exactly-once. Nothing runs after the last recorded event, so a resume never repeats a tool call or
  an LLM call.
* A crash resumes from the last recorded event, so at most one tool call is repeated. A tool whose side effect completed
  but whose result was not yet recorded runs again on resume, since fisk cannot make an external side effect
  idempotent. Already-recorded turns and results are never replayed.

Resume a session against the same agent configuration it started with. A session can be resumed from anywhere, including
a machine that no longer has the original `agent.yaml`, so care is required: continuing a conversation against a
different model, tool set, or system prompt can make the replayed transcript incoherent. fisk guards this by
fingerprinting the configuration (provider, model, prompt, tool set, budget) at checkpoint time and refusing a resume
when it no longer matches; the refusal names what changed. `--force` overrides the check, accepting that the restored
conversation may not fit the current configuration. The provider is the one exception: a session started against one
`llm.provider` can never be resumed against another, and `--force` does not apply. A session that already completed
cannot be resumed.

#### Managing sessions

Sessions are stored under the `XDG` state directory, `$XDG_STATE_HOME/fisk-ai/runs`, defaulting to
`~/.local/state/fisk-ai/runs`; `--state-dir` overrides the location. A suspended or completed session is kept until it
is removed.

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

#### Storage backends

Where a session is journaled is configurable through `harness.sessions`, which mirrors the shape of `harness.memory`.
Two backends ship: `file` (the default) and `jetstream` {{% badge style="primary" title="Version" %}}0.0.3{{% /badge %}}.
The block is optional; leaving it out keeps the `file` backend under the `XDG` state directory described above.

The `file` backend keeps each session as a JSON-lines journal under a directory. Set `options.directory` to move it off
the default `XDG` path:

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

The stream keeps messages forever by default, which is what sessions want; do not set a max age or they would silently
expire. The subject prefix (`fisk.sessions` above) is yours to choose; the backend derives it from the stream's single
wildcard subject when it binds, so it is not set in the config. The backend fails at run start, rather than degrading silently, if
the stream does not exist or its configuration does not match this shape. Sessions are never namespaced by identity, so a
run started by one agent is found by another reading the same stream; keep separate environments in separate streams.

#### Inspecting a configured backend

`session ls`, `session show`, and `session rm` read the `file` backend under `--state-dir` by default. To inspect
sessions in a configured backend, a jetstream stream or a file directory named in the config, pass that config with
`--config`:

```nohighlight
fisk session ls --config agent.yaml
fisk session show <id> --config agent.yaml
```

To confirm which backend a config resolves to without starting a run, `fisk info` shows a `Sessions` section with the
resolved backend and, for the jetstream backend, the stream and NATS context.

## Human in the loop (HITL)

When enabled, fisk gives the model built-in tools to ask the operator a question at the terminal and wait for the
answer. They are off by default and only available when running the agent:

```yaml
harness:
  human_in_the_loop:
    enabled: true
```

Three tools are offered:

* `ask_human_confirm` - a yes/no question. Returns `{"confirmed": true}` or `{"confirmed": false}`
* `ask_human_select` - choose one of a list of options the model provides. Returns `{"selected": "<option>"}`, or
  `{"selected": null}` if no choice was made
* `ask_human_input` - a free-text value, optionally pre-filled with a default the operator can accept or edit. Returns
  `{"value": "<text>"}`, or `{"value": null}` if none was given

### Optional communication from the agent

The model decides when to call the HITL tools, shaped through the prompt. They suit decisions the model should not make
alone: confirming a destructive action, choosing between options that depend on operator intent, or supplying a value it
cannot derive. The question is rendered on the terminal (stderr, so a piped final answer stays clean), and the
model-supplied text is stripped of terminal control sequences first so it cannot spoof what is shown. Each tool denies
by default: an interrupt, an end-of-input, or no terminal at all yields a negative answer (no confirmation, no
selection, no value) rather than a guess. They require an interactive terminal: without one the call is declined with a
reason rather than hanging on a prompt no one can answer, and they are never exposed over MCP, where there is no
operator. Tool calls within a turn run one at a time, so a prompt has the terminal to itself.

### Required tool use confirmations

Two mechanisms put a human in the loop:

* `human_in_the_loop` (a configuration flag) lets the model ask its own question through a fisk-provided
  `ask_human_*` tool, with no application command involved. The human answers a question the model chose to ask.
* `ai:confirm` (a command tag) lets the application author gate an ordinary, non-interactive command so the operator
  must approve it before it runs. The human is a checkpoint on a command the model wanted to run anyway; nothing about
  the command itself changes.

Reach for `human_in_the_loop` when the model should decide when to check in; reach for `ai:confirm` when a normal
command should run only with the operator's say-so, typically something destructive or irreversible.

## Command Tags

Fisk commands can carry tags, set in their fisk definition (or, for App Builder
applications, in YAML). Tags can be referenced by the `include`/`exclude` rules
to select commands by group, and a few tags are reserved and interpreted by
fisk itself to control how a command is exposed to the model:

| Tag           | Description                                                                                                                                                           |
|---------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never expose the command to the model; it is dropped before include/exclude and can never be added back.                                                              |
| `ai:no_defer` | Always send the command directly instead of deferring it behind the tool-search tool.                                                                                 |
| `ai:confirm`  | Require the operator to approve the command at the terminal before it runs; an "allow for the session" answer is remembered for that command for the rest of the run. |

`ai:deny` is the reliable way to keep a command the agent should never call out of
reach, since it applies before any `include`/`exclude` rule. `ai:no_defer` keeps the
handful of commands the model needs on most requests immediately available rather
than discoverable only through tool search.

`ai:confirm` gates a command behind the operator's explicit permission. When the
model calls a command tagged `ai:confirm`, fisk pauses before running it and
prompts the operator at the terminal, showing the resolved command line with its
arguments, and offers three choices: run it once, run it and stop asking for that
command for the rest of the session, or decline. Declining returns an
authoritative result to the model (the command is not run and the model is told
the decision is final), so it stops rather than working around the refusal. An
"allow for the session" answer is remembered **by command, regardless of its
arguments**: once you bless `stream rm`, every later `stream rm` call runs without
asking again, so reserve that choice for a command you trust the agent to repeat.
It applies for the rest of that run only; nothing is persisted across runs. The
prompt is rendered on stderr (so a piped final answer stays clean), the displayed
command line is stripped of terminal control sequences so model-supplied argument
values cannot spoof what you see, and it denies by default: an interrupt, an
end-of-input, or no interactive terminal declines rather than runs. Unlike
`human_in_the_loop`, the tag is always active: there is no configuration flag to
enable it.

The same gate can be extended to other tags with the `harness.confirm_tags`
configuration key: any tag listed there gates its commands exactly as `ai:confirm` does, which
lets an operator require confirmation for a tag the application already uses (for
example `impact:rw`) without editing the application. It is additive to the
always-on `ai:confirm` tag and matching is exact rather than a regex. A
`confirm_tags` entry that matches no loaded command is reported as a warning at
startup, since a typo would otherwise leave a command ungated. The approval prompt
names the tag that gated the command, so you can tell why you are being asked. Run
`fisk info` to see each command's tags and which commands a run would gate. Like
`ai:confirm`, a `confirm_tags` tag gates both the agent loop and MCP, where it is
requested through elicitation.

Any other tags are free-form: they have no built-in meaning to fisk but can be
matched by the `tags` field of an `include` or `exclude` rule.

All of a command's tags, reserved and free-form alike, are also included in the
tool description fisk sends the model, as a trailing `Tags: ...` line, in both
the agent and over MCP. This lets your prompt reference them, for example "always
use `ask_human_confirm` before running any command
tagged `impact:rw`". The human-facing `fisk info` listing keeps the plain
description.

## Memory

Memory gives the model a small key/value store that persists across runs, so it
can keep durable notes (a layout it worked out, a convention, the outcome of an
investigation) and pick them up next time rather than rediscovering them. It is
opt-in and agent-mode only; like the human-in-the-loop tools it is never exposed
over MCP.

> [!info] Warning
> Memory is shared state. Treat what a memory contains as data the model saved, not as trusted instructions.

Enable it under `harness.memory`. The `backend` field selects where memories are
kept; it defaults to `file`, so the minimal configuration is just:

```yaml
harness:
  memory:
    enabled: true
```

When enabled the model is offered four tools: `memory_list` (keys and their
descriptions), `memory_read` (one memory by key), `memory_write` (save a memory
with a key, a one-line description, and a body), and `memory_delete`. A key uses
letters, digits and `.`, `_`, `=` or `-` (no slashes or spaces), which keeps it
valid both as a filename and as a NATS KV key. `memory_write` creates by default
and refuses to overwrite an existing key unless called with `overwrite: true`, so
the model does not silently clobber a note; the create still fails cleanly if two
writers race for the same new key.

At the start of a run the stored keys and descriptions are injected into the
system prompt as an index so the model knows what it has saved; `memory_list` is
the live view during the run. Turn the index off with `no_index: true`.

A memory body is capped at 64 KB and a store holds at most 1024 entries. Both
limits are shared by every backend, and a write that would exceed them fails
cleanly. The on-disk format is shared too, so a value written by one backend
migrates to another unchanged.

`fisk info` shows a `Memory` section with the resolved backend and, for the
jetstream backend, the bucket, NATS context and key prefix, so you can confirm
where memory is stored without starting a run.

Two backends ship today: `file` (the default) and `jetstream`  {{% badge style="primary" title="Version" %}}0.0.3{{% /badge %}}.

### File backend

The `file` backend keeps each memory as a markdown file named for its key under
the configured directory, which defaults to `memory/<identity>`.

```yaml
harness:
  memory:
    enabled: true
    backend: file
    options:
      directory: memory
```

A relative directory, including that default, resolves under the store base when a
deployment sets one and against the working directory otherwise; an absolute
directory is used as-is. The `identity` is the agent's name, set with the
`identity` configuration field and defaulting to the application binary's base
name; the [configuration reference](../reference/) covers it in detail. Point two
agents at the same directory and they share a memory; leave the default and each
agent keeps its own.

### JetStream backend

The `jetstream` backend keeps memories in a NATS JetStream KV bucket instead of on
disk, so a fleet of agents can share durable memory over a broker. It uses the
connection from the configured `nats_context`, the same one remote tools use, and
binds to a bucket that must already exist: the agent never creates it, so you own
the bucket's durability policy.

```yaml
nats_context: production

harness:
  memory:
    enabled: true
    backend: jetstream
    options:
      bucket: agent-memory
```

Create the bucket first, without a TTL so memories do not silently expire and with a
max value size that fits a full entry (the 64 KB body cap plus the small frontmatter
header stored with it), up to 1024 entries:

```
nats --context production kv add agent-memory --history=1 --max-value-size=69600
```

The backend fails at run start, rather than degrading silently, if the bucket does
not exist, has a TTL set, or caps values below that full-entry size.

By default each agent's keys are namespaced under a prefix equal to its `identity`
(stored as `<identity>.<key>`), mirroring the file backend's per-identity directory
so two agents pointed at one bucket do not collide. Set `options.prefix` to a shared
value for agents that deliberately share memory, or to `""` for a flat, unprefixed
keyspace:

```yaml
harness:
  memory:
    enabled: true
    backend: jetstream
    options:
      bucket: agent-memory
      prefix: fleet-shared   # agents with the same prefix share memory; "" is flat
```

#### Read-before-update

The jetstream backend adds a safety guard the file backend cannot: an overwrite
must follow a read of the current value in the same run, and is refused if the
memory was not read or has changed since it was read. The model then reads the
current value and retries. This is the same read-before-edit discipline that keeps
an editor from clobbering a file it has not seen.

It is more than a workflow nicety. Because the check rides on the KV entry's
revision, it is an atomic compare-and-swap: when two agents share a bucket and both
try to update the same memory, the second write is rejected rather than silently
overwriting the first. That lost-update protection is a concrete reason to prefer
this backend for any shared or concurrent deployment, where the file backend's
last-writer-wins overwrite would quietly drop a change.

The guard is on by default. Set `no_require_read_before_update: true` to allow blind
overwrites, matching the file backend's behavior:

```yaml
harness:
  memory:
    enabled: true
    backend: jetstream
    options:
      bucket: agent-memory
      no_require_read_before_update: true
```

We can use memory to ensure our agent never repeats jokes; change the`system_prompt` as follows:

```yaml
harness:
  memory:
    enabled: true

system_prompt: |
  Tell short jokes using Cows!

  You have tools that can render a cow saying short sentences, when asked 
  use the tools to tell funny jokes.
  
  You tell cow jokes, no other kinds of jokes, strictly jokes about cows. 
  If asked to tell non cow jokes, refuse and show no joke.

  Do not use emoji, keep general narration short, just stick to the jokes

  Save the jokes you told to a single memory file with all the past jokes 
  and make sure you dont repeat jokes you previously told.

  Finish your turn by making a funny quip related to the joke or cows or similar
```

We will get a new joke every time - be ready to get some awful jokes after a while :)

## Safety

When Fisk AI runs a command in a CLI tool it passes a slice of arguments to the `exec` system call. No shell is involved
that can be escaped or influenced.

App Builder is often involved and calls shell scripts, so App Builder commands need to be written defensively.

* Use type hints on arguments for ints, floats and so on
* Use `is_shellsafe(value)` on string input arguments
* Use escaping when passing arguments to commands, for example `{{ .Arguments.message | escape }}`
* Tag commands with the various helper tags so the harness understands the intent
* Mark every mandatory argument as `required`

Fisk AI has no tools that can interact with arbitrary files on the system. The only way it interacts with the system is
through the supplied tools or the Memory feature.

Every command the agent runs gets the same protections:

* Its output combines stdout and stderr, preserving order, and is capped at 64 KiB so a chatty command cannot flood the model's context
* The `ANTHROPIC_API_KEY` is stripped from its environment, so a tool can never read the agent's own credentials
* `LLMFORMAT=1` is set, signalling fisk applications to render output suited to an LLM rather than a terminal

## Local LLMs

Local LLM hosting tools like `ollama`, `LM Studio` and others support exposing an Anthropic-compatible API. Fisk AI can
communicate with those tools.

There are some caveats. To support a large number of tools, Fisk AI uses the
[Tool Search Tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool), which these local
runners do not support. When targeting a locally hosted model, the total tool count may need to stay around 15.

I set these environment variables before invoking `fisk` to access my local Anthropic API instead of reaching to the internet.

```nohighlight
$ export ANTHROPIC_BASE_URL=http://localhost:1234
$ export ANTHROPIC_API_KEY=lmstudio
```

The `base_url` is validated: a non-loopback host must use `https`, so the API key and conversation are never sent in
cleartext. Plain `http` is allowed only for a loopback address (`127.0.0.1`, `::1`, `localhost`) as used by the local
runners above.