# MCP Servers

Instead of running an agent loop, Fisk AI can serve a Fisk application's commands over the
[Model Context Protocol](https://modelcontextprotocol.io/) so another client, such as Claude Desktop or Claude Code,
calls them directly as tools.

The tool set, the input schemas, and the tag rules are the same ones the [agent](../agents/) uses. Only the caller
changes: where the agent drives the tools with an LLM against a prompt, an MCP server hands the same tools to whatever
client connects and lets it decide when to call them.

> [!info] Note
> Serving over MCP is opt-in. The configuration must carry an `expose.agent.mcp` block, otherwise `fisk mcp` refuses
> to start.

## Starting a server

Serving over MCP needs less configuration than an agent: there is no agent loop, so `system_prompt` and `llm.model` are
not used. A minimal config needs only the application to introspect, a tool selection, and the `expose.agent.mcp` block:

```yaml
application_path: /usr/local/bin/nats
include:
  tools:
    - ^stream_
expose:
  agent:
    mcp: {}
```

Start the server with the `mcp` command:

```nohighlight
$ fisk mcp --config nats.yaml --port 8080
```

The transport is HTTP, the streamable MCP transport. The port is taken from `--port` (or `FISK_AI_MCP_PORT`); if unset,
from `expose.agent.mcp.port` in the config; otherwise it defaults to `8080`. All progress and logging go to stderr.

Use `fisk info` to preview which tools a configuration exposes before starting the server:

```nohighlight
$ fisk info --config nats.yaml
```

## Connecting a client

Wire an MCP client to the server by pointing it at the running URL. For Claude Code:

```nohighlight
$ claude mcp add --transport http nats http://127.0.0.1:8080
```

A client that takes a JSON server map uses:

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

## Configuration

MCP mode uses only the parts of the [configuration](../reference/) that describe the application and the tool set. An
`identity` becomes the MCP server name, defaulting to `fisk` when unset; `system_prompt`, `llm.model`, and the
agent-only harness settings are ignored.

| Field                                   | Description                                                                                                                                        |
|-----------------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------|
| `application_path`                      | path to the Fisk application binary to introspect and serve; optional, omit it to serve only allowlisted built-ins (today the two knowledge tools) |
| `expose.agent.mcp.builtins`             | the built-ins this operator wants served; only `knowledge_search` and `knowledge_enumerate` are accepted. A tool must also declare MCP exposure itself, so this can narrow what is served but never widen it |
| `expose.agent.mcp`                      | the opt-in block that enables MCP serving; must be present                                                                                         |
| `expose.agent.mcp.port`                 | default listen port when `--port` and `FISK_AI_MCP_PORT` are unset, default `8080`                                                                 |
| `expose.agent.mcp.address`              | host or IP to bind when `--address` and `FISK_AI_MCP_ADDRESS` are unset, default `127.0.0.1` (loopback); use `0.0.0.0` to listen on all interfaces |
| `expose.agent.mcp.instructions`         | free-text guidance sent to clients on connect                                                                                                      |
| `expose.agent.mcp.confirm_over_mcp`     | how confirmation-gated commands behave when a client cannot be asked                                                                               |
| `expose.agent.mcp.max_concurrent_tools` | maximum tool calls run at once; `0` or unset uses the default `2`, negative is rejected, capped at `1024`                                          |
| `expose.agent.mcp.tool_timeout`         | duration bounding a single served tool call, for example `60s`; unset uses the default `30s`                                                       |
| `include` / `exclude`                   | select which commands become tools, matched on tool name (regex) or tag                                                                            |
| `expose.agent.tools`                    | narrow the exposed set further within the `include`/`exclude` selection                                                                            |
| `identity`                              | the MCP server name; optional                                                                                                                      |

### Instructions

`expose.agent.mcp.instructions` sets a block of free text sent to clients when they connect. A client may pass it to the
model as a hint about how to use the server, which suits orientation the individual tool descriptions are too terse to
carry:

```yaml
expose:
  agent:
    mcp:
      instructions: |
        These tools wrap the NATS CLI. Prefer stream_info before stream_edit,
        and treat all subjects as relative to the FOO account.
```

## How tools are exposed

Each command becomes an MCP tool named by its command path, for example `stream_info`, with its input schema and a
description built from the command's help. Both the short help and any long help are surfaced to the client, so a
command that carries detailed long help gives the model richer guidance than a one-line summary alone.

Each tool carries a readable title annotation holding the space-separated command path, so `stream rm` rather than the
underscore tool name, and the behavioral hints its [behavior tags](../reference/#behavior-tags) declare:

| Tag              | Annotation                |
|------------------|---------------------------|
| `ai:read_only`   | `readOnlyHint: true`      |
| `ai:destructive` | `destructiveHint: true`   |
| `ai:additive`    | `destructiveHint: false`  |
| `ai:idempotent`  | `idempotentHint: true`    |

Untagged commands send no hints at all, leaving the client on the MCP defaults, which treat a tool as destructive and
open-world. A confirmation gate does not change these hints: a command tagged both `ai:read_only` and `ai:confirm` is
still advertised as read-only.

Approval itself is not expressed as an annotation, because annotations are advisory hints a client may ignore rather
than a control channel.

Every tag a command carries, reserved and free-form alike, also reaches the client as description text, as described
under [Command tags over MCP](#command-tags-over-mcp) below.

The served tools are the agent's `include`/`exclude` selection, narrowed further by `expose.agent.tools` when it is set.
With neither, every command is served, subject to the tag rules below. Tool selection uses the same regular expressions
over the tool name as the [agent](../agents/#tool-selection). A tool call runs the command and returns its result,
bounded by `tool_timeout` per call and `max_concurrent_tools` in flight at once.

## Command tags over MCP

The reserved [command tags](../agents/#command-tags) are honored over MCP, differing from the agent loop where noted:

| Tag             | Behavior over MCP                                                          |
|-----------------|-----------------------------------------------------------------------------|
| `ai:deny`       | never exposed, the reliable way to keep a command off MCP entirely          |
| `ai:no_defer`   | no effect, since MCP does not defer tools behind a tool-search tool         |
| `ai:confirm`    | exposed and gated through elicitation rather than a local operator prompt   |
| behavior tags   | advertised as tool annotations, as described above                          |

All of a command's tags, reserved and free-form alike, are included in the tool description as a trailing `Tags: ...`
line, the same as in the agent, so a client's prompt can reference them.

## Confirmation over MCP

Commands tagged `ai:confirm`, or a configured `confirm_tags` tag, require approval before they run. There is no local
operator on the MCP path, so Fisk AI requests approval from the calling client through MCP elicitation: before running a
gated command it asks the client to approve, showing the server name, the resolved command line, and the tag that gated
it, and runs the command only on an explicit approval. A refusal, a dismissal, or any elicitation error denies the call
and returns an authoritative result the model is told not to retry.

Not every client supports elicitation. `expose.agent.mcp.confirm_over_mcp` chooses what happens when the connected
client cannot be asked:

| Value    | Behavior                                                                                                        |
|----------|-----------------------------------------------------------------------------------------------------------------|
| `auto`   | default; ask clients that support elicitation, run the command ungated for clients that do not                  |
| `always` | ask clients that support elicitation, refuse the command for clients that cannot be asked                       |
| `never`  | never ask, run gated commands ungated regardless of client support, delegating approval to the client's own UI  |

```yaml
expose:
  agent:
    mcp:
      confirm_over_mcp: always
```

> [!info] Warning
> Elicitation is a request, not an enforcement boundary. A client is free to auto-approve, and under `auto` or `never` a
> client that cannot elicit runs gated commands ungated. For a command that must never be reachable over MCP, use
> `ai:deny` rather than relying on confirmation.

A client that already has its own approval UI may prompt twice under `auto` or `always`; set `never` when the client's
own gating is trusted and the second prompt is unwanted.

## What is not served

The built-in operator tools are agent-mode only and are never exposed over MCP, since there is no local operator on the
MCP path:

* the [human-in-the-loop](../agents/#human-in-the-loop-hitl) `ask_human_*` tools
* the [memory](../agents/#memory) `memory_*` tools

## Safety

Every served command gets the same per-command protections as the [agent](../agents/#safety): it runs as an argument
vector rather than through a shell, its arguments are bound to the command's schema, its `ANTHROPIC_API_KEY` is stripped,
its output combines stdout and stderr and is capped at 64 KiB, and `LLMFORMAT=1` is set.

The threat model is wider than an agent run:

* Any client that can reach the server's port can invoke every exposed tool with any schema-valid arguments.
  `ai:deny` and `include`/`exclude` are the gate on what is reachable, so scope the exposed set deliberately.
* There is no agent loop, prompt, or token budget bounding aggregate use. `tool_timeout` and `max_concurrent_tools`
  bound a single call and how many run at once, but not the total number of calls, so do not expose the server on an
  untrusted network.
* Command output is returned to the connected client rather than to Anthropic, so whoever connects sees whatever the
  commands print.
* Confirm-tagged commands are gated by elicitation, a request the client fulfills rather than an access control the
  server enforces. Use `ai:deny`, not confirmation, for anything that must never be reachable over MCP.
