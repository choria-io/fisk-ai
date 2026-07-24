# Reference

A Fisk AI agent is described by a single YAML configuration file. It names the application to drive, selects which of
its commands become tools, and sets the model, the prompt, and how the harness behaves. The `run`, `mcp`, and `a2a`
commands all read the same file; each uses the parts it needs and ignores the rest.

The `--config` flag selects the file, defaulting to `agent.yaml` in the working directory:

```nohighlight
$ fisk run --config nats.yaml "report on the ORDERS stream"
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

### A knowledge-only agent

`application_path` is optional. Leave it out to run an agent with no wrapped application, on the built-in tools alone.
This is useful for a knowledge agent that answers from an indexed corpus over [knowledge](../knowledge/):

```yaml
# agent.yaml - answer questions from a local knowledge base, no wrapped app

llm:
  model: claude-sonnet-4-6

system_prompt: |
  You answer questions using the knowledge_search tool over the indexed docs.

harness:
  knowledge:
    enabled: true
```

With no `application_path`, the identity defaults to `fisk`; set an explicit `identity` to keep the
`knowledge/<identity>` and `memory/<identity>` stores separate when you run more than one such agent in a directory.

## Identity and application

```yaml
# The name of the agent. Used in discovery and reused as a NATS queue
# group, so it must contain only letters, digits, "-" or "_". If you
# leave it out it defaults to the application binary's base name, or to
# "fisk" when no application_path is set; set it explicitly when the
# derived name carries a dot, a space, or other characters, which are
# rejected, or to keep memory/knowledge stores separate between agents.
identity: nats

# Path to the Fisk application binary to introspect and run. OPTIONAL.
# When set, the binary is introspected once at startup to obtain its
# command tree and per-command JSON schemas. Leave it out to run an agent
# on the built-in tools (knowledge, memory, human_in_the_loop) and any
# remote tools alone, with no wrapped application. Required only for "a2a"
# mode, which serves the wrapped application's tools.
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

`application_path` is optional for `run` and `mcp` modes and required only for `a2a`, which serves the wrapped
application's tools and cannot serve the built-ins. When set, the target must be built with a current
[Fisk](https://github.com/choria-io/fisk) (v0.9.0 or newer) that supports `--fisk-introspect` and precomputed
per-command schemas. When it is left out, Fisk AI skips introspection entirely and the agent runs on its built-in and
remote tools alone; see [a knowledge-only agent](#a-knowledge-only-agent) below.

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

Run `fisk info` to preview the resulting tool set before a run.

## Model and run budget

The `llm` block selects the model and bounds a single run. `llm.model` is the only required field in it:

```yaml
llm:
  # The model identifier. REQUIRED. Accepts any value the Anthropic API
  # accepts; the well-known identifiers are listed under "Models" below.
  model: claude-sonnet-4-6

  # The model backend. Defaults to "anthropic" when unset, so most agents
  # never set it. Set it only to target a different backend that has been
  # built in; naming one that is not available fails at run start with the
  # list of providers that are.
  provider: anthropic

  # Bounds on a single run so the agent loop cannot spend without limit.
  # The run stops with a summary once any of these is reached, whether or
  # not the task is complete.
  budget:
    # Cumulative token spend cap for the run. Default 200000.
    max_tokens: 200000
    # Cap on the tokens a single response may generate, distinct from the
    # cumulative max_tokens. Left unset it uses a built-in default that is
    # raised when thinking is on. Set it only to fit an endpoint whose
    # per-response limit is lower than that default; an explicit value wins.
    max_output_tokens: 0
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

  # When true, disables server-side tool search: every tool is sent to the
  # model directly instead of being deferred behind a search tool at ten or
  # more tools. Left off, tool search is used automatically when the provider
  # supports it and the tool count crosses the threshold. Set true only for an
  # endpoint that does not implement it.
  no_tool_search: false
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
    # path memory files live under, defaulting to "memory/<identity>". A
    # relative directory resolves under the store base when a deployment
    # sets one and against the working directory otherwise; an absolute
    # directory is used as-is.
    options:
      directory: memory

  # Where checkpointed run journals are stored (the --checkpoint and
  # --resume sessions). Optional; absent it uses the "file" backend under
  # the XDG state directory. Sessions cannot be disabled.
  sessions:
    # The store implementation. "file" (the default) keeps each session as a
    # JSON-lines journal under a directory. "jetstream" keeps them on a NATS
    # JetStream stream shared over a broker, using the nats_context above.
    backend: file
    # Backend-specific settings. For "file": "directory", the path journals
    # live under, defaulting to the XDG state directory; --state-dir
    # overrides it. For "jetstream": "stream", the name of an
    # operator-created stream to bind, for which --state-dir does not apply.
    options:
      directory: /var/lib/fisk-ai/runs
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
loop, add an `expose.agent.mcp` block. It is opt-in: without this block, `fisk mcp` refuses to start. MCP mode uses
only the fields that describe the application and the tool set; `system_prompt`, `llm.model`, and the `harness` settings
are ignored.

```yaml
expose:
  agent:
    # The opt-in block that enables MCP serving. Must be present, even if
    # empty ({}), for "fisk mcp" to start.
    mcp:
      # Default listen port, used when neither --port nor the
      # FISK_AI_MCP_PORT environment variable is set. Default 8080.
      port: 8080

      # Host or IP to bind to, used when neither --address nor the
      # FISK_AI_MCP_ADDRESS environment variable is set. Defaults to the
      # loopback address 127.0.0.1, so the server serves only local clients
      # unless you set this; use 0.0.0.0 to listen on all interfaces.
      address: 127.0.0.1

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

      # Maximum tool calls run at once. 0 or unset uses the default 2, a
      # negative value is rejected, and the ceiling is 1024. It is separate
      # from the a2a knob because the MCP port can be network-reachable
      # (address 0.0.0.0), a wider trust boundary than a2a's NATS peers.
      max_concurrent_tools: 2

      # Duration bounding a single served tool call, e.g. 60s. Unset uses
      # the default 30s. Named tool_timeout, not call_timeout, to avoid
      # colliding with llm.budget.call_timeout, which bounds a different
      # unit of work. Config-only; there is no flag or environment override.
      tool_timeout: 30s

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
# when running "fisk a2a".
nats_context: ngs

expose:
  agent:
    # When true, "fisk a2a" serves this agent's tools over NATS. Opt-in:
    # without it, "fisk a2a" refuses to start. Like MCP mode, serving
    # needs only application_path, identity, nats_context, and the tool
    # selection. Confirmation-gated commands are never served, since there
    # is no operator to approve them.
    agent_to_agent: true

    # Optional per-server tuning, a sibling of the agent_to_agent switch.
    # agent_to_agent alone still serves with defaults. Its knobs are
    # separate from the mcp block's because the two servers bound different
    # trust boundaries (NATS peers vs anything reaching a TCP port).
    a2a:
      # Maximum tool calls run at once. 0 or unset uses the default 2, a
      # negative value is rejected, and the ceiling is 1024.
      max_concurrent_tools: 2
      # Duration bounding a single served tool call, e.g. 60s. Unset uses
      # the default 30s. Config-only, no flag or environment override.
      tool_timeout: 30s

# Import tools from one or more remote fisk agents over NATS and expose
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
would collide. A `run` is strict: an unreachable or unimportable remote agent fails the run. `fisk info` is lenient
and reports each remote host's reachability instead.

## Models

Well-known Anthropic model identifiers are available as constants in the `config` package; any value the Anthropic API
accepts may be used in `llm.model`, local LLMs will have their own convention. `fisk` does not restrict what you enter here.

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

| Flag           | Environment variable | Description                                                                                                                                                                |
|----------------|----------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `--config`     |                      | Path to the configuration file. Default `agent.yaml`.                                                                                                                      |
| `--api-key`    | `ANTHROPIC_API_KEY`  | Anthropic API key. Required.                                                                                                                                               |
| `--base-url`   | `ANTHROPIC_BASE_URL` | Anthropic API base URL to use, for example a local Anthropic-compatible runner. A non-loopback host must use `https`; plain `http` is allowed only for a loopback address. |
| `--http-debug` | `HTTP_DEBUG`         | Dump Anthropic API request and response bodies to `http-debug.log`. The file holds the full conversation and is created mode 0600.                                         |
| `--no-color`   | `NO_COLOR`           | Disable markdown rendering of the final answer, emitting raw text.                                                                                                         |
| `--no-tui`     | `NO_TUI`             | Disable the full-screen terminal UI for this run and use line-by-line output.                                                                                              |
| `--chat`       |                      | Keep the full-screen UI open for interactive follow-ups after each turn.                                                                                                   |
| `--verbose`    | `VERBOSE`            | Show more verbose output.                                                                                                                                                  |
| `--trace`      |                      | Write a JSON-lines trace of every LLM request and response to a file.                                                                                                      |
| `--checkpoint` |                      | Journal the run to a session that can be suspended and resumed.                                                                                                            |
| `--resume`     |                      | Resume a checkpointed session by id instead of starting a new run.                                                                                                         |
| `--state-dir`  |                      | Override where sessions are stored, default `$XDG_STATE_HOME/fisk-ai/runs`.                                                                                                |

The MCP server port also reads `FISK_AI_MCP_PORT`, which `--port` overrides and which in turn overrides
`expose.agent.mcp.port`. Sessions, chat, and their durability semantics are covered in the [Agents guide](../agents/).

## Safety

The configuration is the boundary on what the model can reach: `application_path` fixes the one binary it can drive
(and with no `application_path` set the agent can drive no external binary at all), `include`/`exclude` and `ai:deny`
fix which of its commands become tools, and nothing outside that set is callable.
Commands run as an argument vector rather than through a shell, their arguments are bound to each command's schema, the
`ANTHROPIC_API_KEY` is stripped from their environment, output is capped at 64 KiB, and `LLMFORMAT=1` is set. The
[Agents](../agents/#safety) and [MCP](../mcp/#safety) guides describe the full threat model for each mode.
