# Reference

A Fisk AI agent is described by a single YAML configuration file. It has the path to the application, selects which of
its commands become tools, and sets the model, the prompt, and how the harness behaves. The `run`, `mcp`, and `serve`
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
    max_tokens: 500000
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
# "fisk-ai" when no application_path is set; set it explicitly when the
# derived name carries a dot, a space, or other characters, which are
# rejected, or to keep memory/knowledge stores separate between agents.
identity: nats

# Path to the Fisk application binary to introspect and run. OPTIONAL.
# When set, the binary is introspected once at startup to obtain its
# command tree and per-command JSON schemas. Leave it out to run an agent
# on the built-in tools (knowledge, memory, human_in_the_loop) and any
# remote tools alone, with no wrapped application. Required only when
# expose.agent.a2a.serve_tools exposes the wrapped application's tools.
application_path: /usr/local/bin/nats

# The system prompt describing what the agent should do. REQUIRED for a
# "run" and for a channel that runs one, ignored by "mcp" mode and by the
# a2a endpoint. Think of it as a one-file SKILL: describe the goals and
# give broad guidance.
system_prompt: |
  You manage NATS JetStream Streams using tools.
```

`identity` is load-bearing beyond a label: it is the NATS subject key when the agent serves or is discovered over
[agent-to-agent](#agent-to-agent), and the default memory directory is `memory/<identity>`. Keep it to the safe
character set so those uses stay valid.

`application_path` is optional for `run` and `mcp` modes and required only when `expose.agent.a2a.serve_tools` is set,
because no built-in is offered over a2a today and such an endpoint would have nothing to serve. When set, the target must
be built with a current [Fisk](https://github.com/choria-io/fisk) (v0.9.0 or newer) that supports `--fisk-introspect`
and precomputed per-command schemas. When it is left out, Fisk AI skips introspection entirely and the agent runs on its built-in and
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

## Model and budget

The `llm` block selects the model and limits what the loop may do. `llm.model` is the only required field in it:

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

  # Limits on the agent loop so it cannot run without end. The two caps
  # have different scopes: max_iterations is per turn, max_tokens is
  # cumulative over a conversation.
  budget:
    # Tokens a whole conversation may process, counted across every turn
    # of it. Default 500000. A conversation that reaches it takes no
    # further turn; start a new conversation or raise this. It counts
    # tokens rather than money: cache reads weigh the same as uncached
    # input here and are priced at a fraction of it.
    max_tokens: 500000
    # Cap on the tokens a single response may generate, distinct from the
    # cumulative max_tokens. Left unset it uses a built-in default that is
    # raised when thinking is on. Set it only to fit an endpoint whose
    # per-response limit is lower than that default; an explicit value wins.
    max_output_tokens: 0
    # Agent loop iterations one turn may take, a fresh allowance per
    # turn. Default 50.
    max_iterations: 50
    # Per-call timeout as a Go duration string, for example "60s" or
    # "2m". Default "120s".
    call_timeout: 120s

  # Controls whether the model exposes its reasoning, which some providers
  # call reasoning rather than thinking. The whole block is optional and
  # leaving it out is the default: nothing is sent and the model uses its own
  # behavior. Including it states a preference either way, so omitting it and
  # setting enabled false are different requests.
  #
  # Older models that predate adaptive thinking (Sonnet 4.5, Haiku 4.5) reject
  # the parameter, and so may a proxy behind ANTHROPIC_BASE_URL. Both explicit
  # states send one, so remove the block for those rather than setting false.
  thinking:
    # true asks the model to think and surfaces its reasoning separately from
    # the answer (thought-bubble lines on stderr in shell mode, folding blocks
    # in the TUI). false asks it not to think, which changes only a model that
    # would otherwise reason unaided.
    enabled: true

  # How hard the model works, which governs how deeply it reasons and how many
  # tokens it spends overall. Unset asks for nothing and the model uses its own
  # default. It sits beside the thinking block rather than inside it, so an
  # effort can be set without sending a thinking parameter.
  #
  # The value is passed to the provider as written and is not checked against a
  # list of levels, because the levels belong to the model: Anthropic takes low,
  # medium, high, xhigh and max, other providers name their own, and a model
  # released after this build may take one Fisk AI has never heard of. A level
  # the model does not take is refused at the first model call, naming it.
  reasoning_effort: high

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
settings apply to the agent loop only; `mcp` mode and the a2a endpoint ignore them.

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
    - ai:destructive
    - impact:rw

  # Limits a single tool call, at a terminal and on a worker alike.
  # Unset uses the default of 5m; set 0s for no limit at all, which is
  # what a command that legitimately runs for hours needs.
  #
  # The timeout cancels the call. A command is killed along with its
  # process group; an in-process tool stops only if it checks. A call
  # waiting on your answer runs as long as it needs. Separate from
  # expose.agent.mcp.tool_timeout and expose.agent.a2a.tool_timeout,
  # which limit a served call.
  tool_timeout: 5m

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
    # Serve memory_list and memory_read only, withholding memory_write and
    # memory_delete, so a run uses what earlier runs saved without changing
    # it. The store is untouched: anything else writing to it still does.
    read_only: false
    # Backend-specific settings. For the "file" backend: "directory", the
    # path memory files live under, defaulting to "memory/<identity>". A
    # relative directory resolves under the store base when a deployment
    # sets one and against the working directory otherwise; an absolute
    # directory is used as-is.
    options:
      directory: memory

  # Where run journals are stored, which is what --resume continues.
  # Optional; absent it uses the "file" backend under the XDG state
  # directory. Sessions cannot be disabled: every run is a conversation.
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
matched by `include`/`exclude`. The `ai:` prefix is reserved for the tags Fisk AI interprets; a tag under that prefix
that is not one of the tags below does nothing and is reported as a warning at startup, by `fisk info`, by the MCP
server and by the a2a endpoint.

### Control tags

These change what Fisk AI does with a command.

| Tag           | Meaning                                                                                                       |
|---------------|---------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never expose the command; dropped before include/exclude and can never be added back. The reliable off switch. |
| `ai:no_defer` | Always send the command directly instead of deferring it behind the tool-search tool.                         |
| `ai:confirm`  | Require the operator to approve the command at the terminal before it runs; always active, no config flag.     |

`ai:confirm` denies by default: no interactive terminal, or a prompt that cannot be shown, declines rather than runs. An
interrupt or an end-of-input at the prompt ends the run instead of declining, since the operator did not answer; on a
the conversation survives and `fisk run --resume` puts the question again. An "allow for the conversation"
answer is remembered by command regardless of its arguments: the conversation records it and honors it on every
resume. `/clear` and a `--force` resume across a changed
configuration drop it, and a resume with no terminal attached declines a gated command rather than honoring it.
`harness.confirm_tags` extends the same gate to any other tag your application already uses. Over MCP these gates are
requested through elicitation instead of a local operator prompt; over agent-to-agent, confirmation-gated commands are
not served at all. The full behavior is documented under [Command Tags](../agents/#command-tags) in the Agents guide.

### Behavior tags

These describe what the command does. They enforce nothing: they are advice, carried to the model, to MCP clients as
[tool annotations](https://modelcontextprotocol.io/specification/server/tools#tool-annotations), and to peer agents. Use
`ai:deny` and `ai:confirm` for control.

| Tag              | Meaning                                                                            |
|------------------|------------------------------------------------------------------------------------|
| `ai:read_only`   | The command does not modify anything.                                              |
| `ai:destructive` | The command may destroy or overwrite existing state.                               |
| `ai:additive`    | The command changes state but only adds to it.                                     |
| `ai:idempotent`  | Running the command again with the same arguments has no further effect.           |

Most commands need one tag: `ai:read_only` for a read, `ai:destructive` for a delete. Leave a command untagged and
clients fall back to the MCP defaults, which assume the worst and treat it as destructive.

Each tag sets only what it names. `ai:read_only` does not imply `ai:idempotent`, and MCP clients ignore the destructive
and idempotent hints for a read-only tool. A command tagged both `ai:read_only` and `ai:destructive` is used as
destructive and the contradiction is reported as a warning.

Because these are ordinary tags, `harness.confirm_tags: [ai:destructive]` gates every destructive command behind
approval, and `include: {tags: [ai:read_only]}` serves a read-only tool set. Both select on what the command author
remembered to tag, so they are a convenience rather than a boundary; `ai:deny` and name-based `include`/`exclude` remain
the reliable controls.

All of a command's tags, reserved and free-form alike, are appended to the tool description Fisk AI sends the model as a
trailing `Tags: ...` line, so a prompt can reference them. Adding or changing a tag changes that description, which
changes the tool-set fingerprint a conversation is keyed on: one stopped before the change refuses to continue after
it.

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

      # How long a single served tool call may run, e.g. 60s. Unset uses
      # the default 30s. Named tool_timeout, not call_timeout, to avoid
      # colliding with llm.budget.call_timeout, which limits a different
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
Both sides use a named [NATS context](https://github.com/nats-io/jsm.go), given as `nats_context`. Serving is an
endpoint of `fisk serve`; the [Serving tools](../channels/a2a/) guide covers it end to end.

> [!info] Note
> A2A capabilities are under development, this is included here for completeness but subject to radical change

```yaml
# Name of a NATS context (as managed by "nats context" and resolved by
# jsm.go) used to connect to NATS. REQUIRED when remote_tools is set or
# when serving tools to other agents.
nats_context: ngs

expose:
  agent:
    # What this agent answers for other agents over NATS, and how long it
    # waits on the calls it makes to them. Opt-in: without the block nothing
    # answers, and a block must set serve_tools or prompts unless it holds
    # request_timeout and nothing else. Both endpoints use one connection under one
    # identity. Its knobs are separate from the mcp block's because the two
    # servers sit on different trust boundaries (NATS peers vs anything
    # reaching a TCP port).
    a2a:
      # When true, "fisk serve" answers tool calls from peers, serving one
      # tool per call and running no agent loop. It needs only
      # application_path, identity, nats_context and the tool selection: no
      # prompt and no model. Confirmation-gated commands are never served,
      # since there is no operator to approve them.
      serve_tools: true

      # Maximum tool calls run at once. A call arriving with every slot in
      # use is refused at once with a "capacity" code and no command is
      # started for it. 0 or unset uses the machine's CPU count clamped to
      # between 2 and 8 (a container's own limit, not the host's), a
      # negative value is rejected, and the ceiling is 1024.
      max_concurrent_tools: 4
      # How long a single served tool call may run, e.g. 60s. Unset uses
      # the default 30s. Config-only, no flag or environment override.
      tool_timeout: 30s

      # How long this agent waits for a peer to say anything, e.g. 30s,
      # where tool_timeout limits a call it answers. A served call is
      # answered with an acknowledgement, a message every ten seconds
      # while the tool runs, and then the reply, so this applies to the
      # gap between messages while harness.tool_timeout limits the call. A
      # card fetch is one message, so for discovery the
      # two are the same number. Unset uses the default 120s; 0s and a
      # negative are rejected, and a value under 30s is raised to it. An
      # agent that only calls other agents sets this with nothing else in
      # the block, which is valid.
      request_timeout: 120s

      # Answers prompts from peers by running the agent loop over each one
      # and streaming the run back. Its presence enables the endpoint and an
      # empty block works. Answering a prompt runs the whole loop, so
      # identity, system_prompt and llm.model are all required, and the run
      # reaches every tool include and exclude selected, not the served set.
      prompts:
        # How many prompts this process answers at once, and the number
        # above which a caller is refused rather than made to wait.
        # Default 1. The --workers flag does not reach it: that sizes the
        # queue.
        workers: 2

        # Lets a run put its questions to the caller that sent the prompt:
        # an approval for a confirmation-gated command, or a
        # human-in-the-loop question. Default false: the worker refuses
        # every gated command. Anyone permitted to answer this identity's
        # questions can approve a gated command, and an answer carries no
        # verified caller identity. The worker holds a question for
        # request_timeout, and its worker slot with it. A caller answers
        # waiting before the window runs out to restart it, for as long as
        # somebody is reading the question.
        elicit: true

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

The timeouts around a2a each cover one thing. `expose.agent.a2a.tool_timeout` limits a call this agent answers for a
peer. `expose.agent.a2a.request_timeout` sets how long it waits for a peer to say anything before treating it as gone.
`harness.tool_timeout` limits any tool call the loop makes, remote ones included, so it is how long a remote call may
take in total. `llm.budget.call_timeout` limits a model call and reaches nothing on the network.

## Queued jobs

`expose.agent.jobs` opts the agent in to taking whole units of work off a Choria asyncjobs work queue. Its presence is
the switch for `fisk serve`: without the block, the command refuses to start. Every field under it defaults, so an empty
block is a working worker.

```yaml
expose:
  agent:
    jobs:
      # The work queue to consume. It must already exist: the worker binds
      # to it and creates nothing, so its run time, retry cap and
      # concurrency stay with whoever owns the queue. Default "FISK_AI".
      queue: FISK_AI

      # The asyncjobs task type this worker handles. A task of another
      # type on the same queue is left alone, so a task submitted under a
      # type no worker handles stays queued until it expires, with no
      # error logged at either end. Default "fisk-ai:run".
      task_type: fisk-ai:run

      # How many jobs this process runs at once, default 1. The --workers
      # flag overrides it. It cannot raise throughput past the queue's own
      # concurrency, which limits every worker on that queue together.
      workers: 1

      # The NATS context the queue is reached over, defaulting to the
      # top-level nats_context. It is dialed separately from the shared
      # connection, so the queue may live on a different cluster from the
      # session store and remote tools.
      nats_context: production

      # Caps a task payload in bytes before anything decodes it, default
      # 524288. It is the only limit on a third party's input to an endpoint
      # whose sole access control is permission to write to the queue.
      max_payload: 524288
```

A job runs the whole agent loop, so it uses the agent's own `include` and `exclude` rather than `expose.agent.tools`,
which selects only what is served over MCP and a2a. It needs `identity`, `system_prompt` and `llm.model` like any other
run, where a worker serving only tools needs none of them. The [Queued jobs](../channels/asyncjobs/) guide covers
submitting work and reading answers.

## Telemetry

`telemetry` exports OpenTelemetry traces and metrics over OTLP/HTTP. It applies to `fisk run`, to the runs `fisk serve`
hosts, and to knowledge searches served by `fisk mcp`. The a2a endpoint exports nothing. Nothing is exported unless
`enabled` is true.

```yaml
telemetry:
  enabled: true
  endpoint: http://127.0.0.1:4318
  service_name: ""
  sample_ratio: 1.0
  no_metrics: false
  capture:
    enabled: false
    messages: delta
    max_bytes: 8192
```

| Setting        | Description                                                                                                                                       |
|----------------|---------------------------------------------------------------------------------------------------------------------------------------------------|
| `enabled`      | Turns export on. Default `false`.                                                                                                                 |
| `endpoint`     | OTLP/HTTP base URL; `/v1/traces` and `/v1/metrics` are appended. Unset, the standard `OTEL_EXPORTER_OTLP_*` variables apply, defaulting to `http://localhost:4318`. |
| `service_name` | Service name reported to the backend. Unset, it falls back to `OTEL_SERVICE_NAME`, then `identity`, then `fisk-ai`.                                |
| `sample_ratio` | Head sampling ratio from `0.0` to `1.0`. Default `1.0`. An explicit `0` samples nothing.                                                           |
| `no_metrics`   | Exports traces only. Metrics are on with telemetry.                                                                                               |
| `capture`      | Exports the conversation itself, not only structure and timing. Off by default; see below.                                                        |

### Content capture

| Setting            | Description                                                                                                          |
|--------------------|----------------------------------------------------------------------------------------------------------------------|
| `capture.enabled`  | Exports the system prompt, the conversation, model replies, tool arguments and tool results. Default `false`.         |
| `capture.messages` | `delta` (default) exports what each model call added; `full` exports the whole conversation on every call.            |
| `capture.max_bytes`| Cap per content attribute, measured on the encoded JSON. Default `8192`, from `256` to `65536`.                       |

Everything the model saw and everything the tools returned reaches the collector, including the verbatim output of
commands the model ran, and an export cannot be recalled. Nothing is redacted. There is no command-line flag; only
`--no-telemetry`, which suppresses the whole export.

Plain `http://` to a non-loopback host is rejected at startup while capture is on. The settings under `capture` are
ignored and unvalidated while `capture.enabled` is false. See the [telemetry guide](../telemetry/#content-capture)
for the attributes, sizing and collector limits.

Transport credentials are never written in the file. `OTEL_EXPORTER_OTLP_HEADERS` and the other standard `OTEL_*`
variables configure the connection, so the same configuration sends to a collector, Grafana Tempo, Honeycomb, or any
OTLP/HTTP endpoint without a change here. This build speaks OTLP/HTTP only: port `4318`, not `4317`.

Whether a run exports is decided in this order:

| Condition                                                        | Result             |
|------------------------------------------------------------------|--------------------|
| `--no-telemetry`, `NO_TELEMETRY`, or `OTEL_SDK_DISABLED=true`    | Off                |
| `telemetry.enabled: true`                                        | On                 |
| Otherwise                                                        | Off                |

Setting `OTEL_EXPORTER_OTLP_*` does not enable export on its own; a host-wide collector endpoint does not turn every
agent on the machine into an exporter. A run that finds those variables set while telemetry is off prints a note saying
so.

An invalid configuration fails at startup rather than exporting nowhere: an endpoint that is not an `http` or `https`
URL, an endpoint on port `4317`, `OTEL_EXPORTER_OTLP_PROTOCOL=grpc`, a `sample_ratio` outside `0.0` to `1.0`, or plain
`http` to a non-loopback host while an `OTEL_EXPORTER_OTLP_*_HEADERS` variable is set, which would send the credential in
the clear.

`fisk info` shows the resolved settings and where each came from. After a run, an export that did not reach the
collector is reported; `--verbose` also reports a successful one.

A run exports one trace covering the whole run, with spans for setup, each turn, each model call, each tool call, each
knowledge search, each request to the embeddings server and each tool served by a remote agent, plus the GenAI metric
instruments. A model call carries one event per HTTP attempt, so a retried call reports what it spent waiting.
Indexing is not instrumented. The [Telemetry guide](../telemetry/) has a local collector to try it against, the span
tree to expect, and the attribute reference.

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
| `--no-tui`     | `NO_TUI`             | Disable the full-screen terminal UI and answer one prompt with line-by-line output. The full-screen view holds a conversation of many turns; this answers one.              |
| `--verbose`    | `VERBOSE`            | Show more verbose output.                                                                                                                                                  |
| `--thinking`   | `THINKING`           | Show the model's reasoning, which is hidden by default. On `fisk session show --transcript` it includes reasoning in the transcript. The `thinking=N` token counter is reported either way. |
| `--trace`      |                      | Write a JSON-lines trace of every LLM request and response to a file.                                                                                                      |
| `--resume`     |                      | Continue a stored conversation, by the session id `fisk session ls` shows or by a conversation token.                                                                       |
| `--force`      |                      | Continue a stored conversation whose configuration has changed since it started. Standing approvals are dropped.                                                            |
| `--state-dir`  |                      | Override where the sessions of the agent this process hosts are stored, default `$XDG_STATE_HOME/fisk-ai/runs`. Refused with `--nats-context`, where the agent is elsewhere and keeps its own. |
| `--nats-context` |                    | On `fisk run`, talk to an agent on this NATS context instead of running one in this process. The configuration's `identity` names the agent and must be set.                 |
| `--a2a-debug`  |                      | Dump every a2a message between this terminal and the agent to `a2a-debug.log`. The file holds the conversation token, your prompts and all tool output; it is created mode 0600. |
| `--no-telemetry` | `NO_TELEMETRY`     | Suppress OpenTelemetry export, whatever `telemetry.enabled` says. On `fisk run` it covers the run, on `fisk serve` the whole worker. The credential scrub still applies. |
| `--workers`    |                      | On `fisk serve`, how many jobs to run at once, overriding `expose.agent.jobs.workers`.                                                                                      |
| `--work-dir`   |                      | On `fisk serve`, the directory command tools run in. Must be an absolute path that exists. Defaults to the worker's own working directory.                                  |

The MCP server port also reads `FISK_AI_MCP_PORT`, which `--port` overrides and which in turn overrides
`expose.agent.mcp.port`. Sessions, chat, and their durability semantics are covered in the [Agents guide](../agents/).

`--workers` overriding the file is the opposite of how `harness.tool_timeout` works, where a configured value beats the
built-in default. The worker count is a property of the process; the tool timeout is a property of the agent.

## Safety

The configuration is the boundary on what the model can reach: `application_path` fixes the one binary it can drive
(and with no `application_path` set the agent can drive no external binary at all), `include`/`exclude` and `ai:deny`
fix which of its commands become tools, and nothing outside that set is callable.
Commands run as an argument vector rather than through a shell, each argument is checked against the command's schema, the
`ANTHROPIC_API_KEY` is stripped from their environment, output is capped at 64 KiB, and `LLMFORMAT=1` is set. The
[Agents](../agents/#safety) and [MCP](../mcp/#safety) guides describe the full threat model for each mode.

The OpenTelemetry export credentials are stripped from tool environments too: `OTEL_EXPORTER_OTLP_HEADERS` and its
per-signal forms, and the mTLS variables `OTEL_EXPORTER_OTLP_CLIENT_KEY`, `OTEL_EXPORTER_OTLP_CERTIFICATE` and
`OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE` with their per-signal forms. This happens whether or not telemetry is enabled,
so `--no-telemetry` does not re-expose a collector token. The mTLS variables name a file path rather than holding a
secret, so removing them from the environment hides the location of the key, not the key: a tool running as the same
user can still read that file if it knows where to look.

Spans carry structure and timing, tool names and argument key names, and no prompts, tool arguments or results.
Setting `telemetry.capture.enabled` reverses that for every one of them, and for the `error.type` reduction as well,
since a tool's error text is part of its result.
