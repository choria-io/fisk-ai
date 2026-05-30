# Fisk AI

fisk-ai turns any [fisk](https://github.com/choria-io/fisk)-based command line
application into an LLM agent. It introspects the application's command tree,
exposes the commands the LLM is allowed to use as tools, and runs an agent loop
against the Anthropic API that calls those commands to satisfy a prompt.

There is no glue code to write: if your CLI is built with fisk and supports
introspection, fisk-ai can drive it.

It is designed in particular to complement
[Choria App Builder](https://choria-io.github.io/appbuilder/). App Builder lets
you define a command line application declaratively in YAML, and because it is
built on fisk, any App Builder application can be introspected and driven by
fisk-ai. Together they let you define a strict, purpose-built set of tools in a
YAML file and expose exactly those to an agent, without writing or compiling any
code: App Builder describes the commands, fisk-ai's configuration selects which
of them the agent may use and how it should behave.

## Features

**Turning a CLI into an agent**

- No glue code: any [fisk](https://github.com/choria-io/fisk)-based CLI, including
  declarative [App Builder](https://choria-io.github.io/appbuilder/) YAML
  applications, becomes an agent through introspection alone.
- Command-tree introspection turns each runnable leaf command into a tool with its
  own JSON schema; grouping and hidden commands are skipped.
- Tool selection with `include`/`exclude` rules matched on tool name (regex) or
  fisk tag, so you expose exactly the commands the agent may use.
- Deferred tool discovery scales to large command sets: past ten tools, commands
  are offered through a tool-search tool rather than sent up front, keeping
  requests small and selection accurate.

**Running the agent**

- Agent loop against the Anthropic API with a per-run budget on tokens,
  iterations, and per-call timeout.
- Optional extended thinking, surfaced on stderr separately from the answer.
- Interactive `--chat` mode: keep the full-screen UI open for follow-ups after each
  turn, optionally checkpointed into a durable, resumable conversation.
- Terminal-aware output: markdown rendered for a TTY, raw when piped, with the
  final answer on stdout and everything else on stderr so results stay pipeable.
- JSON-lines tracing of every LLM request and response for debugging.

**Sessions**

- Checkpoint a run to disk and resume it later, in a fresh process or on another
  machine.
- Graceful suspend on Ctrl-C or `SIGTERM`: the current step finishes and the
  session is checkpointed before exit.
- Durable, event-by-event journal with defined crash-recovery semantics and a
  configuration-fingerprint guard against resuming an incompatible run.
- Session management from the CLI: `session ls`, `session show`, `session rm`.

**Human in the loop**

- `ai:confirm` command tag (extendable with `confirm_tags`) gates a command behind
  the operator's explicit approval before it runs, denying by default.
- Opt-in `ask_human_*` tools let the model itself ask the operator to confirm,
  choose, or supply a value.

**Memory**

- Opt-in `memory_*` tools give the model a pluggable key/value store that persists
  across runs, with the stored keys and descriptions injected into the prompt so it
  builds on what it already knows.

**Serving and interop**

- MCP server mode exposes the same tools over the Model Context Protocol (HTTP) to
  clients such as Claude Desktop or Claude Code, with confirmation requested
  through elicitation.
- Agent-to-agent tools over NATS let one agent serve its tools and another
  discover and import them, with no LLM on the serving side.

**Safety**

- Commands run as an argument vector, never through a shell, with arguments bound
  to each command's schema.
- `ai:deny` keeps a command permanently out of reach, ahead of any
  `include`/`exclude` rule.
- The `ANTHROPIC_API_KEY` is stripped from executed commands, output is capped at
  64 KiB, and `LLMFORMAT=1` signals fisk apps to render LLM-friendly output.

## How it works

1. The configured application binary is introspected to obtain its command 
   tree and per-command JSON schemas.
2. Each runnable leaf command becomes a tool, named by its command path (for
   example `auth user add` becomes the tool `auth_user_add`). Grouping commands
   and hidden commands are skipped.
3. The tool set is filtered by your `include`/`exclude` rules. Commands tagged
   `ai:deny` are always removed and can never be exposed.
4. fisk-ai runs the agent loop: it sends your prompt and the available tools to
   the model, and when the model calls a tool it executes the matching command,
   captures the output, and feeds the result back. This repeats until the model
   produces a final answer or a budget is reached.

How tools are presented to the model depends on how many there are. Below 10
tools, every tool is sent directly and the model selects from the full set. At 10
tools or more, fisk-ai switches to deferred tool discovery: each command is
offered as a deferred tool and a tool-search tool is added so the model loads the
schemas of the commands it actually needs rather than receiving all of them up
front, keeping the request small and tool selection accurate.

Deferred discovery uses Anthropic's server-side tool-search tool, which the model
searches to load the deferred commands it needs. Every model in
[Supported models](#supported-models) supports it; older models (Claude Opus 4.1
and earlier) do not. Pointing `llm.model` at an unsupported model while exposing ten
or more tools leaves the model holding only the tool-search tool with no way to
reach the deferred commands, and the run stalls. With such a model, keep the exposed
set below ten tools so every tool is sent directly.

A command tagged `ai:no_defer` is always sent directly even when the rest of the
set is deferred, keeping the handful of commands the model needs on most requests
immediately available.

## Usage

```
fisk-ai run [<prompt>] [flags]
```

- `<prompt>` is the interactive prompt to run against the agent.
- `--config` selects the YAML configuration file, defaulting to `agent.yaml` in
  the working directory (see below).

Flags:

| Flag           | Environment variable | Description                                                                                    |
|----------------|----------------------|------------------------------------------------------------------------------------------------|
| `--config`     |                      | Path to the agent configuration file (default `agent.yaml`).                                   |
| `--api-key`    | `ANTHROPIC_API_KEY`  | Anthropic API key (required).                                                                  |
| `--base-url`   | `ANTHROPIC_BASE_URL` | Anthropic API base URL to use.                                                                 |
| `--http-debug` | `HTTP_DEBUG`         | Dump Anthropic API request and response bodies to `http-debug.log`.                            |
| `--no-color`   | `NO_COLOR`           | Disable markdown rendering of the final answer, emitting raw text.                             |
| `--no-tui`     | `NO_TUI`             | Disable the full-screen terminal UI and use the line-by-line output.                          |
| `--chat`       |                      | Keep the full-screen UI open for interactive follow-ups after each turn (see [Chat](#chat)).   |
| `--verbose`    | `VERBOSE`            | Show more verbose output.                                                                      |
| `--trace`      |                      | Write a JSON-lines trace of every LLM request and response to a file.                          |
| `--checkpoint` |                      | Journal the run to a session that can be suspended and resumed (see [Sessions](#sessions)).    |
| `--resume`     |                      | Resume a checkpointed session by id instead of starting a new run (see [Sessions](#sessions)). |

On an interactive terminal the run renders in a full-screen terminal UI: a scrolling
transcript with a live status bar and native approve/deny prompts. It falls back to the
line-by-line output when stdout or stdin is not a terminal (a pipe, CI), or when it is
turned off with `--no-tui`/`NO_TUI` or the config's `harness.no_tui: true`. `--http-debug`
writes to `http-debug.log` (not stderr), so it works alongside the full-screen UI.

Example:

```
export ANTHROPIC_API_KEY=sk-ant-...
fisk-ai run --config nats.yaml "report on the ORDERS stream"
```

### Output

The model's prose is markdown: both the final answer and any mid-conversation
updates. When stdout is a terminal it is rendered for readability with a style
matched to the terminal background; when stdout is piped or redirected, the raw
markdown is written so the result stays free of ANSI escape codes. Rendering can
also be disabled with `--no-color`, or the standard `NO_COLOR` environment
variable.

Output is separated by kind. Only the final answer goes to stdout; everything
else goes to stderr: the commands being run, mid-conversation updates, a final
run summary (LLM calls, tool calls, tokens, latency), and, when thinking is
enabled, the model's reasoning (each line prefixed with a thought bubble). This
keeps stdout safe to pipe into other tools.

## Chat

By default a run is one-shot: the agent answers the prompt and exits. `--chat`
keeps the full-screen UI open after each turn, opening an input row for a
follow-up so the conversation continues with the accumulated context. Type a
follow-up and press Enter to send it; Ctrl-D ends the session, Ctrl-C aborts it.
Up/Down recall this session's earlier follow-ups.

Chat is full-screen only: it needs an interactive terminal and is unavailable with
`--no-tui`, `NO_TUI`, or `harness.no_tui` in the config, where it fails fast rather than
falling back silently.

```
fisk-ai run --chat "review the report in report.md"
```

`--chat` combines with `--checkpoint` to make the conversation durable and
resumable; see [Sessions](#sessions).

## Sessions

By default a run is ephemeral: its conversation lives only in memory and is lost
when the process exits. `--checkpoint` instead journals the run to a session on
disk so it can be suspended and resumed later, in a fresh process or on another
machine. Sessions are the foundation for longer-running work where the agent may
need to pause, for example while a slow external step completes.

Start a checkpointed run. fisk-ai prints the session id at startup; it is
generated unless `--name` sets it:

```
fisk-ai run --checkpoint "report on the ORDERS stream"
fisk-ai run --checkpoint --name orders-report "report on the ORDERS stream"
```

Resume a session by id. No prompt is given, since the original prompt is restored
from the session; passing one is an error:

```
fisk-ai run --resume orders-report
```

On resume fisk-ai replays the conversation so far to stderr, so the run continues
in context rather than from a blank screen, then carries on from where it left
off.

### Chat sessions

`--chat` and `--checkpoint` combine into a durable, resumable conversation:

```
fisk-ai run --chat --checkpoint "review the ORDERS design doc"
fisk-ai run --resume <id>
```

Each follow-up is journaled, so the whole conversation survives a suspend or a
crash. Leaving the input bar with Ctrl-D suspends the session rather than ending
it (the status bar reads `ctrl-d suspend`): it stays resumable, and fisk-ai prints
how to resume it on exit. Ctrl-C aborts; the journal is kept, so an aborted chat is
still resumable from its last completed turn.

Resuming a chat session reopens the input bar automatically; you need not re-pass
`--chat` (it is ignored on resume, since the session already knows what it is), and
fisk-ai first replays the conversation into the viewport. Because the
input bar needs a real terminal, a chat session can only be resumed in the
full-screen UI, not with `--no-tui` or over a pipe. A checkpointed chat has no
"completed" state; remove it with `session rm` when you are done with it.

### Suspending

For a checkpointed run the first Ctrl-C, or a `SIGTERM`, requests a graceful
suspend: the current step finishes, the session is checkpointed, and the process
exits printing how to resume it. A second Ctrl-C aborts immediately. A run started
without `--checkpoint` keeps the usual behavior, where Ctrl-C cancels it. In a
checkpointed chat the input bar's Ctrl-D is the graceful leave instead (see [Chat
sessions](#chat-sessions)).

### Durability

A session is journaled event by event as the run proceeds: each model turn and
each tool result is recorded as it happens.

- A clean suspend is exactly-once. Nothing runs after the last recorded event, so
  a resume never repeats a tool call or an LLM call.
- A crash resumes from the last recorded event, so at most one tool call is
  repeated. A tool whose side effect completed but whose result was not yet
  recorded runs again on resume, since fisk-ai cannot make an external side effect
  idempotent. Already-recorded turns and results are never replayed.

Resume a session against the same agent configuration you started it with. A
session can be resumed from anywhere, including a machine that no longer has the
original `agent.yaml`, so this is partly on you: continuing a conversation against
a different model, tool set, or system prompt can make the replayed transcript
incoherent. fisk-ai guards this by fingerprinting the configuration (model,
prompt, tool set, budget) at checkpoint time and refusing a resume when it no
longer matches; the refusal names what changed. `--force` overrides the check,
accepting that the restored conversation may not fit the current configuration. A
session that already completed cannot be resumed.

### Managing sessions

Sessions are stored under the XDG state directory, `$XDG_STATE_HOME/fisk-ai/runs`,
defaulting to `~/.local/state/fisk-ai/runs`; `--state-dir` overrides the location.
A suspended or completed session is kept until it is removed.

```
fisk-ai session ls
fisk-ai session show <id>
fisk-ai session show <id> --transcript
fisk-ai session rm <id>
```

`session ls` lists each session with its status, model, and prompt. `session show`
prints a session's counters and status; `--transcript` shows the full conversation
(prompt, thinking, narration, tool calls, and tool output). On an interactive terminal
`--transcript` opens the full-screen viewer with thinking and tool output folded, which
`z` and `Z` expand; `--no-tui`/`NO_TUI` prints it as line output instead. `session rm`
deletes a session.

## Configuration

A configuration file describes the application to drive, which of its commands
to expose, the prompt, and the model. A minimal example:

```yaml
identity: nats
application_path: /usr/local/bin/nats

include:
  tools:
    - ^stream_report
    - ^stream_info
    - ^stream_find

llm:
  model: claude-sonnet-4-6
  budget:
    max_tokens: 200000
    max_iterations: 50
    call_timeout: 120s

system_prompt: |
  You manage NATS JetStream Streams using tools.
```

Implemented fields:

- `identity` - a name for the agent, used in discovery and reused as a NATS queue
  group, so it must contain only letters, digits, `-` or `_`. Defaults to the
  application binary's name; set it explicitly if that base name carries other
  characters (a dot, a space), which are rejected.
- `application_path` - path to the fisk application binary to introspect and run
  (required).
- `nats_context` - name of a NATS context (as managed by `nats context` and
  resolved by jsm.go) used to connect to NATS. Required when `remote_tools` is set
  or when running `fisk-ai a2a`. See
  [Agent-to-agent tools](#agent-to-agent-tools-over-nats).
- `remote_tools` - import tools from one or more remote fisk-ai agents over NATS
  and expose them to this agent alongside its local tools. See
  [Agent-to-agent tools](#agent-to-agent-tools-over-nats).
  - `name` - the remote agent's identity (also the NATS subject key).
  - `alias` - a prefix for the imported tool names; defaults to `name`.
  - `include` / `exclude` - select which of the remote agent's tools to import,
    matched against the tool name only. A `tags` filter cannot be honored, since
    discovery does not carry tags; an `exclude` by tag is rejected at startup.
- `system_prompt` - the system prompt describing what the agent should do (required).
- `include` / `exclude` - select which commands become tools.
  - `tools` - a list of regular expressions matched against the tool name (the
    underscore-joined command path, for example `stream_info`).
  - `tags` - match commands by fisk tag. An empty string matches untagged
    commands. `ai:deny` is always active and can never be included.
  - `include` keeps only matching commands; `exclude` removes matching commands.
- `llm.model` - the model identifier to use (required). See below.
- `llm.budget` - bounds on a run:
  - `max_tokens` - cumulative token spend cap for the run (default 200000).
  - `max_iterations` - maximum agent loop iterations (default 50).
  - `call_timeout` - per-call timeout as a duration string, for example `60s`
    (default `120s`).
- `llm.thinking` - controls whether the model exposes its reasoning. Off by
  default; when off, no thinking is requested and the model uses its default
  behavior.
  - `enabled` - when true, asks the model to think and surfaces its reasoning on
    stderr, separately from the answer. Older Anthropic models that predate
    adaptive thinking (for example Sonnet 4.5 and Haiku 4.5) reject it, so leave
    it off for those.
- `harness` - settings that govern how the agent harness behaves during a run, as
  distinct from the model (`llm`) or the tool selection. All optional; the block can
  be omitted to leave every setting at its default.
  - `human_in_the_loop` - opt-in built-in tools that let the model ask the operator
    a question at the terminal (agent mode only). See
    [Human in the loop](#human-in-the-loop).
    - `enabled` - when true, offers the model the `ask_human_*` tools
      (`ask_human_confirm`, `ask_human_select`, `ask_human_input`). Off by default.
  - `confirm_tags` - a list of command tags that, in addition to the always-on
    `ai:confirm` tag, gate a command behind approval before it runs. Matching is
    exact, not a regex, and additive to `ai:confirm`. For example,
    `confirm_tags: ["impact:rw"]` requires confirmation for every command tagged
    `impact:rw`. In the agent loop the operator is prompted at the terminal; over MCP
    the calling client is asked through elicitation, governed by
    `expose.agent.mcp.confirm_over_mcp`. See [`ai:confirm`](#command-tags) and
    [Serving tools over MCP](#serving-tools-over-mcp).
  - `no_tui` - when true, disables the full-screen terminal UI for this agent and
    always uses the line-by-line output, even on an interactive terminal. It is a hard
    off switch the command line cannot re-enable; use `--no-tui` for a one-off run.
  - `no_bell` - the full-screen UI rings the terminal bell each time a run blocks
    waiting on you (an approval gate or an `ask_human_*` prompt), so you notice a run
    that is waiting even if you looked away. This is on by default; set `no_bell: true`
    to silence it for an agent that prompts often or where an audible bell is unwelcome.
    Like `no_tui` it is a negative switch, and it has no effect in the line UI.
  - `memory` - opt-in built-in tools that give the model a key/value store that
    survives across runs (agent mode only). See [Memory](#memory).
    - `enabled` - when true, offers the model the `memory_*` tools (`memory_list`,
      `memory_read`, `memory_write`, `memory_delete`). Off by default.
    - `backend` - the store implementation. Defaults to `file`, which keeps each
      memory in a markdown file under a directory; it is the only backend today.
    - `no_index` - by default the list of stored memories (key and description) is
      injected into the system prompt at run start so the model knows what it has
      saved without calling `memory_list`. Set `no_index: true` to keep the store's
      contents out of the prompt. Like `no_tui` it is a negative switch.
    - `options` - backend-specific settings. For the `file` backend it accepts
      `directory`, the path memory files live under, defaulting to `memory/<identity>`.

## Command tags

fisk commands can carry tags, set in their fisk definition (or, for App Builder
applications, in YAML). Tags can be referenced by the `include`/`exclude` rules
to select commands by group, and a few tags are reserved and interpreted by
fisk-ai itself to control how a command is exposed to the model:

| Tag           | Description                                                                                                                                                           |
|---------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never expose the command to the model; it is dropped before include/exclude and can never be added back.                                                              |
| `ai:no_defer` | Always send the command directly instead of deferring it behind the tool-search tool.                                                                                 |
| `ai:confirm`  | Require the operator to approve the command at the terminal before it runs; an "allow for the session" answer is remembered for that command for the rest of the run. |

`ai:deny` is the reliable way to keep a command the agent should never call out of
reach, since it applies before any `include`/`exclude` rule. `ai:no_defer` keeps the
handful of commands the model needs on most requests immediately available rather
than discoverable only through tool search; see [How it works](#how-it-works) for
how deferral works.

`ai:confirm` gates a command behind the operator's explicit permission. When the
model calls a command tagged `ai:confirm`, fisk-ai pauses before running it and
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

An `ai:confirm` command is still exposed over
[MCP](#serving-tools-over-mcp), where confirmation is requested from the calling
client through MCP elicitation rather than from a local operator: a client that
supports elicitation is asked to approve each run, and how a client that does not is
treated is set by `expose.agent.mcp.confirm_over_mcp` (see
[Confirmation over MCP](#confirmation-over-mcp)). Elicitation is a request, not an
enforcement boundary: a client can auto-approve or lack the capability, so if a
command must never be reachable without approval, keep it off MCP with `ai:deny`
rather than relying on `ai:confirm`.

The same gate can be extended to other tags with the `harness.confirm_tags`
configuration key: any tag listed there gates its commands exactly as `ai:confirm` does, which
lets an operator require confirmation for a tag the application already uses (for
example `impact:rw`) without editing the application. It is additive to the
always-on `ai:confirm` tag and matching is exact rather than a regex. A
`confirm_tags` entry that matches no loaded command is reported as a warning at
startup, since a typo would otherwise leave a command ungated. The approval prompt
names the tag that gated the command, so you can tell why you are being asked. Run
`fisk-ai info` to see each command's tags and which commands a run would gate. Like
`ai:confirm`, a `confirm_tags` tag gates both the agent loop and MCP, where it is
requested through elicitation.

Any other tags are free-form: they have no built-in meaning to fisk-ai but can be
matched by the `tags` field of an `include` or `exclude` rule.

All of a command's tags, reserved and free-form alike, are also included in the
tool description fisk-ai sends the model, as a trailing `Tags: ...` line, in both
the agent and over [MCP](#serving-tools-over-mcp). This lets your prompt reference
them, for example "always use `ask_human_confirm` before running any command
tagged `impact:rw`". The human-facing `fisk-ai info` listing keeps the plain
description.

## Human in the loop

When enabled, fisk-ai gives the model built-in tools to ask the operator a question
at the terminal and wait for the answer. They are off by default and only available
when running the agent:

```yaml
harness:
  human_in_the_loop:
    enabled: true
```

Three tools are offered:

- `ask_human_confirm` - a yes/no question. Returns `{"confirmed": true}` or
  `{"confirmed": false}`.
- `ask_human_select` - choose one of a list of options the model provides. Returns
  `{"selected": "<option>"}`, or `{"selected": null}` if no choice was made.
- `ask_human_input` - a free-text value, optionally pre-filled with a default the
  operator can accept or edit. Returns `{"value": "<text>"}`, or `{"value": null}`
  if none was given.

The model decides when to call them, and you shape that through the prompt. Use them
for decisions the model should not make alone: confirming a destructive action,
choosing between options that depend on your intent, or supplying a value it cannot
derive. The question is rendered on the terminal (stderr, so a piped final answer
stays clean), and the model-supplied text is stripped of terminal control sequences
first so it cannot spoof what you see. Each tool denies by default: an interrupt, an
end-of-input, or no terminal at all yields a negative answer (no confirmation, no
selection, no value) rather than a guess. They require an interactive terminal:
without one the call is declined with a reason rather than hanging on a prompt no
one can answer, and they are never exposed over [MCP](#serving-tools-over-mcp),
where there is no operator. Tool calls within a turn run one at a time, so a prompt
has the terminal to itself.

### Human in the loop: `human_in_the_loop` vs `ai:confirm`

Two mechanisms put a human in the loop, each from a different angle:

- **`human_in_the_loop`** (a [configuration flag](#human-in-the-loop)) lets the
  **model** ask its own question through a fisk-ai-provided `ask_human_*` tool,
  with no application command involved. The human answers a question the model
  chose to ask.
- **`ai:confirm`** (a [command tag](#command-tags)) lets the **application author**
  gate an ordinary, non-interactive command so the **operator** must approve it
  before it runs. The human is a checkpoint on a command the model wanted to run
  anyway; nothing about the command itself changes.

Reach for `human_in_the_loop` when you want the model to decide when to check in;
for `ai:confirm` when a normal command should run only with the operator's say-so,
typically something destructive or irreversible.

## Memory

Memory gives the model a small key/value store that persists across runs, so it
can keep durable notes (a layout it worked out, a convention, the outcome of an
investigation) and pick them up next time rather than rediscovering them. It is
opt-in and agent-mode only; like the human-in-the-loop tools it is never exposed
over MCP.

```yaml
harness:
  memory:
    enabled: true
    backend: file
    options:
      directory: memory
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

The `file` backend keeps each memory as a markdown file named for its key under
the configured directory, which defaults to `memory/<identity>`. Point two agents
at the same directory and they share a memory; leave the default and each agent
keeps its own. Because the files are shared state, treat what a memory contains
as data the model saved, not as trusted instructions.

## Serving tools over MCP

Instead of running the agent, fisk-ai can expose the same tools over the
[Model Context Protocol](https://modelcontextprotocol.io/) so another MCP client
(such as Claude Desktop or Claude Code) can call them directly:

```
fisk-ai mcp --config <file> [--port 8080]
```

Serving over MCP is opt-in: the configuration must carry an `expose.agent.mcp`
block, otherwise `fisk-ai mcp` refuses to start. Beyond that it needs only
`application_path` and, optionally, `include`/`exclude` and an `expose.agent.tools`
selection; `system_prompt`, `identity`, and `llm.model` are not used (an identity, if
set, becomes the MCP server name). A minimal config:

```yaml
application_path: /usr/local/bin/nats
include:
  tools:
    - ^stream_
expose:
  agent:
    mcp: {}
```

The served tools are the agent's `include`/`exclude` selection, narrowed further
by `expose.agent.tools` when it is set; with neither, every command is served
(subject to the tag rules below). The transport is HTTP (the streamable MCP
transport). The port is taken from `--port` (or `FISK_AI_MCP_PORT`); if unset,
from `expose.agent.mcp.port` in the config; otherwise it defaults to `8080`. All
progress and logging go to stderr.

You can set `expose.agent.mcp.instructions` to a block of free text that is sent
to clients when they connect. Clients may pass it to the LLM as a hint about how
to use the server, which is a good place for orientation the individual tool
descriptions are too terse to carry:

```yaml
expose:
  agent:
    mcp:
      instructions: |
        These tools wrap the NATS CLI. Prefer stream_info before stream_edit,
        and treat all subjects as relative to the FOO account.
```

Each command becomes an MCP tool named by its command path (for example
`stream_info`), with its input schema and a description built from the command's
help. Both the short help and any long help are surfaced to the client, so a
command that carries detailed long help gives the model richer guidance than a
one-line summary alone. Each tool also carries MCP annotations: a readable title
(the space-separated command path, so `stream rm` rather than the underscore tool
name), and, when the command is tagged `impact:ro` or `impact:rw`, a read-only hint
so a client can tell a safe query from a mutating command.

Commands tagged `ai:deny` are never exposed. Commands tagged `ai:confirm` (or a
`confirm_tags` tag) *are* exposed and gated through elicitation; see
[Confirmation over MCP](#confirmation-over-mcp) below. Use `ai:deny` for a command
that must never be reachable this way. `ai:no_defer` has no effect here, as MCP does
not defer tools. A tool call runs the command and returns its result; a per-call
timeout and a concurrency limit bound execution.

### Confirmation over MCP

Commands tagged `ai:confirm` (or a configured `confirm_tags` tag) require approval
before they run. There is no local operator on the MCP path, so fisk-ai requests
approval from the calling client through MCP **elicitation**: before running a gated
command it asks the client to approve, showing the server name, the resolved command
line, and the tag that gated it, and runs the command only on an explicit approval.
A refusal, a dismissal, or any elicitation error denies the call and returns an
authoritative result the model is told not to retry.

Not every client supports elicitation. `expose.agent.mcp.confirm_over_mcp` chooses
what happens when the connected client cannot be asked:

| Value    | Behavior                                                                                                        |
|----------|-----------------------------------------------------------------------------------------------------------------|
| `auto`   | Default. Ask clients that support elicitation; run the command ungated for clients that do not.                 |
| `always` | Ask clients that support elicitation; refuse the command for clients that cannot be asked.                      |
| `never`  | Never ask; run gated commands ungated regardless of client support, delegating approval to the client's own UI. |

```yaml
expose:
  agent:
    mcp:
      confirm_over_mcp: always
```

Elicitation is a request, not an enforcement boundary: a client is free to
auto-approve, and `auto` and `never` run gated commands ungated for a client that
cannot elicit. It also means a client that already has its own approval UI may prompt
twice under `auto` or `always`; set `never` when you trust the client's own gating and
want to avoid the second prompt. `always` refuses clients that cannot be asked, but
the reliable control for a command that must never be reachable over MCP remains
`ai:deny`, not confirmation.

Wire it into an MCP client by pointing it at the running server's URL, for
example in Claude Code:

```
claude mcp add --transport http nats http://127.0.0.1:8080
```

or in a client that takes a JSON server map:

```json
{
  "mcpServers": {
    "nats": {
      "type": "http",
      "url": "http://127.0.0.1:8080"
    }
  }
}
```

Use `fisk-ai info --config <file>` to preview which tools a configuration
exposes before starting the server.

## Agent-to-agent tools (over NATS)

Where [MCP](#serving-tools-over-mcp) serves tools to MCP clients over HTTP,
fisk-ai can also serve tools to other fisk-ai agents over NATS, and import another
agent's tools to use as its own. This is the agent-to-agent (a2a) tool path: one
agent exposes its tools, another discovers and calls them, with no LLM on the
serving side.

Connections use a named [NATS context](https://github.com/nats-io/jsm.go)
(`nats context`), given as `nats_context` in the configuration.

### Serving tools (the producer)

```
fisk-ai a2a --config <file>
```

Serving over a2a is opt-in: the configuration must set
`expose.agent.agent_to_agent: true`, otherwise `fisk-ai a2a` refuses to start.
Beyond that, like MCP mode, it needs only `application_path`, an `identity`, the
`nats_context`, and the `include`/`exclude` selection (narrowed further by
`expose.agent.tools` when set); `system_prompt` and `llm.model` are not used. The server
answers two subjects, both keyed by the identity:

- `choria.fisk-ai.discovery.<identity>` - describes the agent and its tools.
- `choria.fisk-ai.tool.<identity>` - runs a single tool and returns its result.

As with MCP, commands tagged `ai:deny` are never served. Unlike MCP,
commands gated by confirmation (`ai:confirm` or a `confirm_tags` tag) are also not
served: the serving side runs autonomously with no operator to approve them, so it
refuses them rather than running them unattended. Use `ai:deny` to keep a command
off entirely.

### Importing tools (the consumer)

Add a `remote_tools` block to an agent configuration and the agent imports the
named agents' tools at startup, presenting them to the model alongside its local
tools:

```yaml
identity: ops
application_path: /usr/local/bin/true
nats_context: ngs

remote_tools:
  - name: nats
    include:
      tools:
        - ^stream_

llm:
  model: claude-sonnet-4-6

system_prompt: |
  You manage NATS JetStream Streams using the imported nats tools.
```

Imported tools keep their own name where it is unambiguous, and are prefixed with
the host alias only when the bare name would clash. A name clashes when a local
tool or built-in already uses it, or when more than one remote host exposes it; in
that case the tool becomes `<alias>_<remote tool name>` (the alias defaults to the
remote agent's identity), so the `nats` agent's `stream_info` becomes
`nats_stream_info`. The choice is deterministic: the same set of agents always
yields the same names. If even the prefixed name still collides, the affected
tools are reported and a run fails; set a distinct `alias` on the host to resolve
it.

A `run` is strict: if a named remote agent cannot be reached or imported, the run
fails rather than silently proceeding without tools the prompt may depend on.
`fisk-ai info` is lenient: it shows the local tools and, for each remote host,
whether it was reachable, how many tools it advertised, and how many were
imported, warning rather than failing when a host is down. A complete example pair
is in [`examples/a2a`](examples/a2a).

### Discovering an agent

To check that an agent is reachable and see the tools it exposes, without wiring
it into a configuration:

```
fisk-ai discover <identity> --context <nats-context>
```

It prints the agent card (name, version, and tools). With `--config` instead of
`--context` it reads the `nats_context` from a configuration file.

## Supported models

Well-known Anthropic model identifiers are available as constants in the
`config` package; any value accepted by the Anthropic API may be used in
`llm.model`.

| Constant              | Identifier                   | Notes                                               |
|-----------------------|------------------------------|-----------------------------------------------------|
| `ModelClaudeFable5`   | `claude-fable-5`             | Most capable overall, for demanding reasoning and long-horizon agentic work; highest cost. |
| `ModelClaudeOpus48`   | `claude-opus-4-8`            | Most capable Opus tier; slowest and most expensive Opus. |
| `ModelClaudeOpus47`   | `claude-opus-4-7`            | Prior Opus release.                                 |
| `ModelClaudeOpus46`   | `claude-opus-4-6`            | Earlier Opus release.                               |
| `ModelClaudeOpus45`   | `claude-opus-4-5-20251101`   | Earlier Opus release.                               |
| `ModelClaudeSonnet5`  | `claude-sonnet-5`            | Balanced capability, speed, and cost; good default. |
| `ModelClaudeSonnet46` | `claude-sonnet-4-6`          | Prior Sonnet release.                               |
| `ModelClaudeSonnet45` | `claude-sonnet-4-5-20250929` | Earlier Sonnet release.                             |
| `ModelClaudeHaiku45`  | `claude-haiku-4-5-20251001`  | Fastest and cheapest; best for simpler tasks.       |

Every model in the table supports the server-side tool-search tool that deferred
tool discovery relies on (see [How it works](#how-it-works)). Anthropic's tool search
is generally available on Claude Opus 4.5, Sonnet 4.5, Haiku 4.5, and later releases;
Claude Opus 4.1 and earlier do not support it. If you set `llm.model` to an older
identifier, keep the exposed command set below ten tools so they are sent directly
rather than deferred behind a tool-search tool the model cannot use.

## Safety

fisk-ai is designed so the model's choices cannot escape the bounds of the
exposed commands:

- Commands are executed as an argument vector, never through a shell, so model
  input cannot be interpreted as shell syntax.
- Tool arguments are bound to each command's schema before execution.
- Commands tagged `ai:deny` are never exposed as tools.
- The `ANTHROPIC_API_KEY` is stripped from the environment of executed commands
  so a tool can never read the agent's own credentials.
- Command output is captured with combined stdout and stderr (preserving
  ordering) and capped at 64 KiB so a chatty command cannot flood the model's
  context.
- Executed commands run with `LLMFORMAT=1` set, signalling fisk applications to
  render output suited to an LLM rather than a terminal.
- A command tagged `ai:confirm` (or carrying a `confirm_tags` tag) is gated behind
  the operator's permission: before it runs, the agent prompts at the terminal,
  showing the command line with its argument values stripped of terminal control
  sequences, and runs it only on an affirmative answer. It denies by default (an
  interrupt, an end-of-input, or no terminal all decline). A declined command
  returns an authoritative result the model is told not to retry. Over MCP there is
  no local operator, so the same gate instead requests approval from the calling
  client through elicitation, governed by `expose.agent.mcp.confirm_over_mcp`; see
  [Confirmation over MCP](#confirmation-over-mcp).
- When `human_in_the_loop` is enabled, the model is offered the built-in
  `ask_human_*` tools. Each prompt is rendered in-process by the agent (it runs no
  command and has the agent's own privileges, not a tool's restricted environment,
  but only ever reads the operator's answer). The model-supplied text (question,
  options, default) is stripped of terminal control sequences before display so it
  cannot spoof what the operator sees. The tools deny by default, require an
  interactive terminal, are opt-in via configuration, and are never exposed over
  MCP.
- When `memory` is enabled, keys are constrained to a charset that cannot form a
  path separator or traverse out of the store directory, the store never follows a
  symlink when reading a memory, and a value is written atomically so a concurrent
  reader never sees a half-written file. Memory is opt-in, agent-mode only, and
  never exposed over MCP. A memory's contents are data the model saved on an
  earlier run, possibly shared with other agents pointed at the same directory;
  the index injected into the prompt frames them as data, not instructions.

In MCP server mode the same per-command protections apply, but the threat model
is wider and worth understanding:

- Any client that can reach the server's port can invoke every exposed tool with
  any schema-valid arguments. `ai:deny` and `include`/`exclude` are the gate on
  what is reachable, so scope the exposed set deliberately.
- There is no agent loop, prompt, or token budget bounding aggregate use. A
  per-call timeout and a concurrency limit bound execution, but not the total
  number of calls; do not expose the server on an untrusted network.
- Command output is returned to the connected client rather than to Anthropic, so
  whoever connects sees whatever the commands print.
- Confirm-tagged commands (`ai:confirm`, `confirm_tags`) are gated by elicitation,
  which is a request the client fulfills, not an access control the server enforces:
  a client may auto-approve, and under `auto` or `never` a client that cannot elicit
  runs them ungated. Use `ai:deny`, not confirmation, for anything that must never be
  reachable over MCP. See [Confirmation over MCP](#confirmation-over-mcp).

The [agent-to-agent server](#agent-to-agent-tools-over-nats) (`fisk-ai a2a`) has
the same shape and a few NATS-specific points worth understanding. This binding
ships without the signing and AAA that a later Choria Protocol binding will add,
so:

- Access control is entirely the NATS account and subject permissions. The
  message `sender` is not authenticated and is never used to make an authorization
  decision; anyone who can publish on the tool subject can run any exposed tool
  with any schema-valid arguments. Do not serve on an untrusted or shared NATS
  account, and use subject permissions to scope who may reach the discovery and
  tool subjects.
- A served command runs with the same protections as everywhere else (output
  capped at 64 KiB, `ANTHROPIC_API_KEY` stripped, `LLMFORMAT=1` set), bounded by a
  per-call timeout and a concurrency limit. Confirmation-gated commands are never
  served; `ai:deny` is the reliable way to keep a command off entirely.
- Confirmation tags do not cross the wire: discovery carries no tags, so an agent
  that imports a remote tool cannot see that it was `ai:confirm`-tagged on the
  serving side and cannot gate it with `confirm_tags`. The serving side refuses
  such tools outright for this reason; on the importing side, `ai:deny` on the
  server (not `confirm_tags` on the client) is what keeps a sensitive command out
  of reach.
- A remote agent's tool descriptions, schemas, and results are untrusted input.
  They reach the model only as tool definitions and tool results (never as system
  or assistant text), the same boundary as a local tool, but they are authored by
  a third party.

## Requirements

- An Anthropic API key.
- A target application built with a current fisk v0.9.0 or newer that supports `--fisk-introspect` and precomputed per-command schemas.

## License

Apache-2.0. Copyright (c) 2026, R.I. Pienaar and the Choria Project
contributors.
