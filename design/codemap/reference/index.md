# Reference

Seven commands, registered in `main.go`, plus the packages behind them and the words this codebase uses for its own parts.

## Commands

### `fisk run [query...]`

Hosts an agent and talks to it, or talks to somebody else's.

| Flag | Effect |
|---|---|
| `--config` | Configuration file, default `agent.yaml` |
| `--api-key`, `--base-url` | Provider credentials and endpoint |
| `--nats-context` | Turns the process into a pure client of a remote worker |
| `--identity` | Names the agent to reach; requires `--nats-context` |
| `--resume`, `--force` | Continue a session id or conversation token, optionally across a changed configuration |
| `--no-tui`, `--no-color`, `--verbose`, `--tool-output`, `--thinking` | Presentation |
| `--trace`, `--http-debug`, `--a2a-debug` | Diagnostic files |
| `--state-dir`, `--no-telemetry` | Storage and export |

Two validation gates run before anything opens. `fisk run` refuses `--resume` with a query, `--force` without `--resume`, and `--identity` without `--nats-context`. In client mode it also refuses seven flags belonging to the worker, but only when they are set on the command line, so an exported environment variable never fails a run.

There is no chat flag. Chat is implicit whenever the full-screen view runs.

### `fisk serve`

Runs the endpoints the configuration enables, with `--workers`, `--work-dir`, `--state-dir`, `--api-key`, `--base-url`, `--no-telemetry` and `--verbose`. It refuses a configuration that enables no endpoint, and provisions no storage: the queue, task store, session stream and memory bucket are the operator's to create.

### `fisk mcp`

Serves the selected tools over MCP, with `--port` and `--address` defaulting to loopback. It refuses a configuration with no MCP exposure block.

### `fisk knowledge` (aliases `rag`, `k`)

Group flags `--config` and `--store-dir`.

| Subcommand | Purpose |
|---|---|
| `index [paths...]` | Build or update the index. `--reindex`, `--dry-run` |
| `watch [paths...]` | Re-index on change. `--debounce`, `--no-initial` |
| `search <query>` | Ranked retrieval. `--top-k`, `--full` |
| `match <query>` (aliases `enumerate`, `which`) | Complete set membership. `--all`, `--count`, `--paths-only`, `--explain`, `--exit-code`, `--min-matches`, `--sort`, `--limit` |
| `words` (aliases `vocab`, `terms`) | Vocabulary with document frequencies. `--field`, `--min-docs`, `--max-docs`, `--words-only`, `--count`, `--exit-code` |
| `show <citation>` | One chunk by path and ordinal |
| `rm <sources...>`, `reset --force` | Remove documents, or the whole index |
| `sources`, `stats`, `doctor`, `rebuild` | Inspect and repair |

### `fisk session`

`ls`, `show <id>` and `rm <id>`, with `--config` and `--state-dir`. The show subcommand takes `--transcript`, `--thinking` and `--no-tui`.

### `fisk info`

Reports the effective configuration, including telemetry values with their origins. It parses in the most lenient mode so it can describe a configuration it could not run.

### `fisk discover <agent>`

Fetches a peer's agent card over the configured NATS context.

## Source map

| Path | Holds | Page |
|---|---|---|
| `main.go`, `run_*.go`, `*_command.go` | Command registration, flags, the two client surfaces, rendering | [The terminal]({{% relref "terminal" %}}) |
| `config/config.go` | Every configuration type, the parser, defaults, validation, accessors | [Configuration]({{% relref "configuration" %}}) |
| `internal/agent` | Run setup, the loop, hooks, events, approvals, the PII guard | [The agent loop]({{% relref "agent-loop" %}}) |
| `internal/toolkit` | The tool interface, tag behavior, the prompter, deferral | [Tools and introspection]({{% relref "tools" %}}) |
| `internal/toolkit/fisk` | CLI introspection into tools, subprocess execution | [Tools and introspection]({{% relref "tools" %}}) |
| `internal/toolkit/functool` | Go function tools, the backend for built-ins, remote and MCP | [Tools and introspection]({{% relref "tools" %}}) |
| `internal/toolkit/builtin` | The harness's own tools and their system notes | [Tools]({{% relref "tools" %}}), [Memory]({{% relref "memory" %}}), [Knowledge]({{% relref "knowledge" %}}) |
| `internal/mcpclient`, `internal/remotetools` | Importing tools from MCP servers and peer agents | [Tools and introspection]({{% relref "tools" %}}) |
| `internal/llm`, `internal/llm/anthropic` | The neutral conversation model and its one backend | [Model providers]({{% relref "providers" %}}) |
| `internal/memory` | The memory contract, key rules, scope, two backends | [Memory]({{% relref "memory" %}}) |
| `internal/rag` | The knowledge store, indexing, retrieval, enumeration | [Knowledge]({{% relref "knowledge" %}}) |
| `internal/runstate`, `internal/tasks` | The journal, the fold, the fingerprint, the task record | [Durable state]({{% relref "state" %}}) |
| `internal/serve` and subpackages | The host, the three channels, shared resources | [Serving]({{% relref "serving" %}}) |
| `internal/a2a` | The protocol, its schemas, the client and server, the NATS binding | [Serving]({{% relref "serving" %}}) |
| `internal/mcpserver` | The MCP tool server, a surface of its own | [Serving]({{% relref "serving" %}}) |
| `internal/telemetry` | Spans, metrics, content capture, propagation | [Telemetry]({{% relref "telemetry" %}}) |
| `internal/tui`, `internal/multiplex` | The full-screen view and multiplexer reporting | [The terminal]({{% relref "terminal" %}}) |
| `internal/pii` | Personal-data detection and redaction | [The agent loop]({{% relref "agent-loop" %}}) |
| `internal/conns`, `internal/util` | NATS connection ownership, shared helpers | [Architecture]({{% relref "architecture" %}}) |
| `internal/agenttest` | Fakes for an embedder's tests | [Architecture]({{% relref "architecture" %}}) |

## Key types

| Type | Package | What it is |
|---|---|---|
| `Config` | `config` | The whole of `agent.yaml`, parsed, defaulted and validated for a mode |
| `Tool` | `toolkit` | Nine methods every tool kind answers, whatever provides it |
| `Behavior` | `toolkit` | What a tool declares about itself, resolved conservatively |
| `Prompter` | `toolkit` | The only path permitted to read the terminal or draw a prompt |
| `ToolSet` | `agent` | An immutable set of definitions plus the registry that dispatches them |
| `Hooks` | `agent` | Seven points a caller can observe or interrupt |
| `Provider` | `llm` | Call and Capabilities: the only code that calls a provider SDK |
| `ContentBlock` | `llm` | The neutral union, with a provider block as its escape hatch |
| `Store` | `memory` | Five methods, no close, safe across processes |
| `Store` | `rag` | The knowledge database and every operation on it |
| `Record`, `RunState` | `runstate` | One journal entry, and the fold of all of them |
| `Fingerprint` | `runstate` | The configuration a stored conversation was written under |
| `Channel`, `Work`, `Outcome` | `serve` | Where work comes from, what it is, and what came of it |
| `Header` | `a2a` | The framing embedded flat into every protocol message |
| `Provider` | `telemetry` | Nil-safe spans and metrics, registering nothing globally |

## Glossary

<dl class="cm-kv">
  <dt>Identity</dt><dd>The agent's name: a NATS subject token, a queue group, and the first field hashed into a session id.</dd>
  <dt>Named identity</dt><dd>One an operator wrote, as opposed to one derived from the application's basename. Every serving path requires a named one.</dd>
  <dt>Introspection</dt><dd>Running a fisk application with a flag that makes it describe its own command tree, which is where tool schemas come from.</dd>
  <dt>Confirm gate</dt><dd>The default-deny check in front of a tagged tool. Independent of the human-in-the-loop tools.</dd>
  <dt>Standing grant</dt><dd>An approval that covers a tool for the rest of the conversation, journaled after the call that triggered it resolves. There is no standing denial.</dd>
  <dt>Deferral</dt><dd>A tool saying it will answer later. The call is never dispatched again; its turn finishes when an answer is supplied.</dd>
  <dt>Elicitation</dt><dd>Putting a question to a person through the caller rather than at a terminal, over a2a or MCP.</dd>
  <dt>Fingerprint</dt><dd>The stamped configuration that decides whether a stored conversation may continue, and whether its approvals survive.</dd>
  <dt>Claim</dt><dd>A record appended on resume. Appending it excludes a second runner; the payload only says who took the run.</dd>
  <dt>Reply set</dt><dd>The sequence of messages answering one a2a request, numbered gap-free from the ack.</dd>
  <dt>Conversation token</dt><dd>The caller's credential for adding a turn to a conversation. Neither logged nor displayed.</dd>
  <dt>Tool search</dt><dd>Deferring tool definitions past a threshold so the model looks them up instead of receiving them all.</dd>
  <dt>Knowledge</dt><dd>The operator's corpus. Go identifiers call it <code>rag</code>; every user-facing name calls it knowledge.</dd>
  <dt>Memory</dt><dd>Notes the model writes and reads back. Always data, never instruction.</dd>
  <dt>Degrade</dt><dd>A knowledge search that fell back to the lexical tier, reported with which step failed.</dd>
  <dt>Content capture</dt><dd>Exporting prompts and completions on spans. Off by default, and it bypasses the other protections in that area by construction.</dd>
</dl>

{{% notice style="tip" title="Next" %}}
Return to the [overview]({{% relref "_index" %}}), or read [Architecture]({{% relref "architecture" %}}) for how these packages layer.
{{% /notice %}}
