# Reference

A Fisk AI agent is described by a single YAML configuration file. It names the application to drive, selects which of
its commands become tools, and sets the model, the prompt, and how the harness behaves. The `run`, `mcp`, and `a2a`
commands all read the same file; each uses the parts it needs and ignores the rest.

The `--config` flag selects the file, defaulting to `agent.yaml` in the working directory:

```nohighlight
$ fisk-ai run --config nats.yaml "report on the ORDERS stream"
```

Each section below is a slice of that file: read it top to bottom and you have seen every
setting Fisk AI understands. Fields that are required are called out as such; everything else has a working default and
can be left out.

> [!info] Note
> Most agents need only a handful of these settings. The [Agents](../agents/) guide walks through building one from
> scratch; this page is the exhaustive list to reach for when you want to know exactly what a field does.

## A minimal file

The smallest useful agent names the application, sets a model, and gives a prompt:

```yaml
# agent.yaml - drive the NATS CLI as an agent

application_path: /usr/local/bin/nats

llm:
  model: claude-sonnet-4-6
  budget:
    max_tokens: 200000
    max_iterations: 50
    call_timeout: 120s

system_prompt: |
  You manage NATS JetStream Streams using tools.
```

Everything after this point expands on those blocks and adds the optional ones.

## Identity and application

```yaml
# The name of the agent. Used in discovery and reused as a NATS queue
# group, so it must contain only letters, digits, "-" or "_". If you
# leave it out it defaults to the application binary's base name; set it
# explicitly when that name carries a dot, a space, or other characters,
# which are rejected.
identity: nats

# Path to the Fisk application binary to introspect and run. REQUIRED.
# The binary is introspected once at startup to obtain its command tree
# and per-command JSON schemas.
application_path: /usr/local/bin/nats

# The system prompt describing what the agent should do. REQUIRED for a
# "run", ignored by "mcp" and "a2a" mode. Think of it as a one-file SKILL:
# describe the goals and give broad guidance.
system_prompt: |
  You manage NATS JetStream Streams using tools.
```

`identity` is load-bearing beyond a label: it is the NATS subject key when the agent serves or is discovered over
[agent-to-agent](#agent-to-agent), and the default memory directory is `memory/<identity>`. Keep it to the safe
character set so those uses stay valid.

`application_path` is the one field every mode needs. The target must be built with a current
[Fisk](https://github.com/choria-io/fisk) (v0.9.0 or newer) that supports `--fisk-introspect` and precomputed
per-command schemas.

## Tool selection

`include` and `exclude` choose which of the application's commands become tools. Each takes a list of regular
expressions matched against the tool name, and a list of fisk tags:

```yaml
# Keep only the commands whose tool name or tag matches. When "include" is
# present, a command must match it to be exposed.
include:
  # Regular expressions matched against the tool name: the command path
  # joined with underscores, so the "stream info" command is "stream_info".
  tools:
    - ^stream_
    - ^consumer_info$
  # Match commands by fisk tag. An empty string "" matches untagged
  # commands. The reserved ai:deny tag is always active and can never be
  # included back in.
  tags:
    - scope:read

# Remove matching commands. Applied as a filter: a command that matches
# "exclude" is dropped even if "include" allowed it.
exclude:
  tools:
    - ^stream_rm$
  tags:
    - scope:system
```

A tool's name is its command path joined with underscores, so a nested command like `stream info` becomes
`stream_info`. Grouping commands and hidden commands are skipped and never become tools. `include` and `exclude` can be
used together: for example include `^stream_` but exclude `^stream_rm$`. Commands tagged `ai:deny` are dropped before
any of this runs and can never be added back.

Run `fisk-ai info` to preview the resulting tool set before a run.

## Model and run budget

The `llm` block selects the model and bounds a single run. `llm.model` is the only required field in it:

```yaml
llm:
  # The model identifier. REQUIRED. Accepts any value the Anthropic API
  # accepts; the well-known identifiers are listed under "Models" below.
  model: claude-sonnet-4-6

  # Bounds on a single run so the agent loop cannot spend without limit.
  # The run stops with a summary once any of these is reached, whether or
  # not the task is complete.
  budget:
    # Cumulative token spend cap for the run. Default 200000.
    max_tokens: 200000
    # Maximum agent loop iterations. Default 50.
    max_iterations: 50
    # Per-call timeout as a Go duration string, for example "60s" or
    # "2m". Default "120s".
    call_timeout: 120s

  # Controls whether the model exposes its reasoning. Off by default; when
  # off, no thinking is requested and the model uses its default behavior.
  thinking:
    # When true, asks the model to think and surfaces its reasoning
    # separately from the answer (thought-bubble lines on stderr in shell
    # mode, folding blocks in the TUI). Older models that predate adaptive
    # thinking (Sonnet 4.5, Haiku 4.5) reject it, so leave it off for those.
    enabled: false

  # When true, disables Anthropic prompt caching for the run. Left off,
  # Fisk AI caches the stable prefix of each request to lower cost and
  # latency on multi-turn runs.
  no_prompt_cache: false
```

Larger models reason better on complex, long-horizon tasks; smaller models like Haiku are faster and cheaper for narrow
ones. When the agent exposes ten or more tools it relies on the model's server-side tool search, which recent models
support and older ones do not; see [Models](#models).

## Harness

The `harness` block governs how the agent harness behaves during a run, as distinct from the model (`llm`) or the tool
selection. Everything in it is optional and the whole block can be omitted to leave every setting at its default. These
settings apply to the agent loop only; `mcp` and `a2a` mode ignore them.

```yaml
harness:
  # Opt-in built-in tools that let the model ask the operator a question
  # at the terminal (agent mode only).
  human_in_the_loop:
    # When true, offers the model the ask_human_confirm, ask_human_select,
    # and ask_human_input tools. Off by default.
    enabled: true

  # Command tags that, in addition to the always-on ai:confirm tag, gate a
  # command behind operator approval before it runs. Matching is exact, not
  # a regex, and additive to ai:confirm. An entry that matches no loaded
  # command is reported as a warning at startup.
  confirm_tags:
    - impact:rw

  # A hard off switch for the full-screen terminal UI: the agent always
  # uses line-by-line output, even on an interactive terminal, and the
  # command line cannot turn the UI back on. Use the --no-tui flag for a
  # one-off run instead. Negative switch, no effect in the line UI.
  no_tui: false

  # The full-screen UI rings the terminal bell each time a run blocks
  # waiting on you (an approval gate or an ask_human_* prompt). On by
  # default; set true to silence it. Negative switch, no effect in the
  # line UI.
  no_bell: false

  # Opt-in built-in key/value store that survives across runs (agent mode
  # only).
  memory:
    # When true, offers the model the memory_list, memory_read,
    # memory_write, and memory_delete tools. Off by default.
    enabled: true
    # The store implementation. Defaults to "file", which keeps each
    # memory in a markdown file under a directory; it is the only backend
    # today.
    backend: file
    # By default the stored keys and descriptions are injected into the
    # system prompt at run start so the model knows what it has saved. Set
    # true to keep the store's contents out of the prompt. Negative switch.
    no_index: false
    # Backend-specific settings. For the "file" backend: "directory", the
    # path memory files live under, defaulting to "memory/<identity>".
    options:
      directory: memory
```

`human_in_the_loop` lets the model decide when to ask; the `ai:confirm` tag and `confirm_tags` gate a command the model
wanted to run anyway. The two are compared in detail under [Command tags](#command-tags) and in the
[Agents guide](../agents/#required-tool-use-confirmations).

Point two agents at the same memory `directory` and they share a memory; leave the default and each keeps its own.
Treat what a memory contains as data the model saved, not as trusted instructions.

## Command tags

Fisk commands can carry tags, set in their fisk definition or, for App Builder applications, in YAML. Any tag can be
matched by `include`/`exclude`, and three are reserved and interpreted by Fisk AI itself:

| Tag           | Meaning                                                                                                        |
|---------------|----------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never expose the command; dropped before include/exclude and can never be added back. The reliable off switch. |
| `ai:no_defer` | Always send the command directly instead of deferring it behind the tool-search tool.                          |
| `ai:confirm`  | Require the operator to approve the command at the terminal before it runs; always active, no config flag.     |

`ai:confirm` denies by default: an interrupt, an end-of-input, or no interactive terminal declines rather than runs. An
"allow for the session" answer is remembered by command regardless of its arguments, for the rest of that run only.
`harness.confirm_tags` extends the same gate to any other tag your application already uses. Over MCP these gates are
requested through elicitation instead of a local operator prompt; over agent-to-agent, confirmation-gated commands are
not served at all. The full behavior is documented under [Command Tags](../agents/#command-tags) in the Agents guide.

All of a command's tags, reserved and free-form alike, are appended to the tool description Fisk AI sends the model as a
trailing `Tags: ...` line, so a prompt can reference them.

## Serving over MCP

To serve the same tools over the [Model Context Protocol](https://modelcontextprotocol.io/) instead of running the agent
loop, add an `expose.agent.mcp` block. It is opt-in: without this block, `fisk-ai mcp` refuses to start. MCP mode uses
only the fields that describe the application and the tool set; `system_prompt`, `llm.model`, and the `harness` settings
are ignored.

```yaml
expose:
  agent:
    # The opt-in block that enables MCP serving. Must be present, even if
    # empty ({}), for "fisk-ai mcp" to start.
    mcp:
      # Default listen port, used when neither --port nor the
      # FISK_AI_MCP_PORT environment variable is set. Default 8080.
      port: 8080

      # Free-text guidance sent to clients when they connect. A client may
      # pass it to the model as a hint about how to use the server, a good
      # place for orientation the terse per-tool descriptions cannot carry.
      instructions: |
        These tools wrap the NATS CLI. Prefer stream_info before
        stream_edit, and treat all subjects as relative to the FOO account.

      # How confirmation-gated commands (ai:confirm or a confirm_tags tag)
      # behave when the connected client cannot be asked through
      # elicitation:
      #   auto   - default; ask clients that can elicit, run ungated for
      #            clients that cannot
      #   always - ask clients that can elicit, refuse for clients that
      #            cannot be asked
      #   never  - never ask, run gated commands ungated, delegating
      #            approval to the client's own UI
      confirm_over_mcp: auto

    # Optional: narrow the served set further, within the top-level
    # include/exclude selection. With neither, every selected command is
    # served (subject to the tag rules). Same regex-over-tool-name and tag
    # matching as the top-level filters.
    tools:
      include:
        tools:
          - ^stream_
      exclude:
        tools:
          - ^stream_rm$
```

The served tools are the agent's top-level `include`/`exclude` selection, narrowed further by `expose.agent.tools` when
set. `identity`, if set, becomes the MCP server name. Elicitation is a request the client fulfills, not an enforcement
boundary; for a command that must never be reachable over MCP, use `ai:deny` rather than confirmation. The
[MCP Servers](../mcp/) guide covers this mode end to end.

## Agent-to-agent

Fisk AI agents can also serve tools to, and import tools from, one another over NATS with no LLM on the serving side.
Both sides use a named [NATS context](https://github.com/nats-io/jsm.go), given as `nats_context`.

> [!info] Note
> A2A capabilities are under development, this is included here for completeness but subject to radical change

```yaml
# Name of a NATS context (as managed by "nats context" and resolved by
# jsm.go) used to connect to NATS. REQUIRED when remote_tools is set or
# when running "fisk-ai a2a".
nats_context: ngs

expose:
  agent:
    # When true, "fisk-ai a2a" serves this agent's tools over NATS. Opt-in:
    # without it, "fisk-ai a2a" refuses to start. Like MCP mode, serving
    # needs only application_path, identity, nats_context, and the tool
    # selection. Confirmation-gated commands are never served, since there
    # is no operator to approve them.
    agent_to_agent: true

# Import tools from one or more remote fisk-ai agents over NATS and expose
# them to this agent alongside its local tools.
remote_tools:
  - # The remote agent's identity (also the NATS subject key). REQUIRED.
    name: nats
    # A prefix for the imported tool names. Applied only when a bare name
    # would clash with a local tool or another remote's tool. Defaults to
    # "name".
    alias: nats
    # Select which of the remote agent's tools to import, matched against
    # the tool name only. A "tags" filter cannot be honored, since
    # discovery does not carry tags, and an exclude-by-tag is rejected at
    # startup.
    include:
      tools:
        - ^stream_
    exclude:
      tools:
        - ^stream_rm$
```

Imported tools keep their own name where it is unambiguous, and take the `<alias>_<name>` form only when the bare name
would collide. A `run` is strict: an unreachable or unimportable remote agent fails the run. `fisk-ai info` is lenient
and reports each remote host's reachability instead.

## Models

Well-known Anthropic model identifiers are available as constants in the `config` package; any value the Anthropic API
accepts may be used in `llm.model`, local LLMs will have their own convention. `fisk-ai` does not restrict what you enter here.

| Constant              | Identifier                   | Notes                                                                                      |
|-----------------------|------------------------------|--------------------------------------------------------------------------------------------|
| `ModelClaudeFable5`   | `claude-fable-5`             | Most capable overall, for demanding reasoning and long-horizon agentic work; highest cost. |
| `ModelClaudeOpus48`   | `claude-opus-4-8`            | Most capable Opus tier; slowest and most expensive Opus.                                   |
| `ModelClaudeOpus47`   | `claude-opus-4-7`            | Prior Opus release.                                                                        |
| `ModelClaudeOpus46`   | `claude-opus-4-6`            | Earlier Opus release.                                                                      |
| `ModelClaudeOpus45`   | `claude-opus-4-5-20251101`   | Earlier Opus release.                                                                      |
| `ModelClaudeSonnet5`  | `claude-sonnet-5`            | Balanced capability, speed, and cost; good default.                                        |
| `ModelClaudeSonnet46` | `claude-sonnet-4-6`          | Prior Sonnet release.                                                                      |
| `ModelClaudeSonnet45` | `claude-sonnet-4-5-20250929` | Earlier Sonnet release.                                                                    |
| `ModelClaudeHaiku45`  | `claude-haiku-4-5-20251001`  | Fastest and cheapest; best for simpler tasks.                                              |

Every model in the table supports the server-side tool-search tool that deferred tool discovery relies on. Anthropic's
tool search is generally available on Claude Opus 4.5, Sonnet 4.5, Haiku 4.5, and later; Claude Opus 4.1 and earlier, and
local models, do not support it. If you point `llm.model` at an older identifier or a local model while exposing ten or
more tools, the model is left holding only the tool-search tool with no way to reach the deferred commands and the run
stalls. With such a model, keep the exposed set below ten tools (around 15 for local runners) so every tool is sent
directly.

## Command-line flags and environment

Some behavior is set per run on the command line rather than in the file. The flags override the file where they
overlap, except for the hard off switches (`harness.no_tui`), which the command line cannot re-enable.

| Flag           | Environment variable | Description                                                                       |
|----------------|----------------------|-----------------------------------------------------------------------------------|
| `--config`     |                      | Path to the configuration file. Default `agent.yaml`.                             |
| `--api-key`    | `ANTHROPIC_API_KEY`  | Anthropic API key. Required.                                                       |
| `--base-url`   | `ANTHROPIC_BASE_URL` | Anthropic API base URL to use, for example a local Anthropic-compatible runner.    |
| `--http-debug` | `HTTP_DEBUG`         | Dump Anthropic API request and response bodies to `http-debug.log`.               |
| `--no-color`   | `NO_COLOR`           | Disable markdown rendering of the final answer, emitting raw text.                |
| `--no-tui`     | `NO_TUI`             | Disable the full-screen terminal UI for this run and use line-by-line output.     |
| `--chat`       |                      | Keep the full-screen UI open for interactive follow-ups after each turn.          |
| `--verbose`    | `VERBOSE`            | Show more verbose output.                                                          |
| `--trace`      |                      | Write a JSON-lines trace of every LLM request and response to a file.             |
| `--checkpoint` |                      | Journal the run to a session that can be suspended and resumed.                   |
| `--resume`     |                      | Resume a checkpointed session by id instead of starting a new run.                |
| `--state-dir`  |                      | Override where sessions are stored, default `$XDG_STATE_HOME/fisk-ai/runs`.        |

The MCP server port also reads `FISK_AI_MCP_PORT`, which `--port` overrides and which in turn overrides
`expose.agent.mcp.port`. Sessions, chat, and their durability semantics are covered in the [Agents guide](../agents/).

## Safety

The configuration is the boundary on what the model can reach: `application_path` fixes the one binary it can drive,
`include`/`exclude` and `ai:deny` fix which of its commands become tools, and nothing outside that set is callable.
Commands run as an argument vector rather than through a shell, their arguments are bound to each command's schema, the
`ANTHROPIC_API_KEY` is stripped from their environment, output is capped at 64 KiB, and `LLMFORMAT=1` is set. The
[Agents](../agents/#safety) and [MCP](../mcp/#safety) guides describe the full threat model for each mode.
