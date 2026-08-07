# Reference and map

A lookup page. Where the other pages explain reasoning, this one answers "where does that live" and "what does that word
mean here".

## Command surface

| Command | Purpose |
|---------|---------|
| `fisk run [query...]` | Runs the agent. The largest flag set: `--config`, `--api-key`, `--base-url`, `--http-debug`, `--no-color`, `--verbose`, `--tool-output`, `--no-tui`, `--chat`, `--trace`, `--checkpoint`, `--name`, `--resume`, `--force`, `--state-dir` |
| `fisk session ls` | Lists checkpointed sessions, newest first. Alias `list` |
| `fisk session show` | Shows one session, with `--transcript` for the full-screen viewer. Alias `view` |
| `fisk session rm` | Removes a checkpointed session. Alias `delete` |
| `fisk info` | Explains a config without contacting a model: tools, tags, confirm column, globals, prompt |
| `fisk knowledge index` | Builds or updates the index, incrementally by content hash |
| `fisk knowledge watch` | Watches the configured paths and re-indexes on change |
| `fisk knowledge search` | Retrieves from the index for tuning, printing citations and snippets |
| `fisk knowledge match` | Lists every document containing the words, as a complete set. Aliases `enumerate`, `which` |
| `fisk knowledge words` | Lists the vocabulary of the index with document counts. Aliases `vocab`, `terms` |
| `fisk knowledge show` | Prints one chunk verbatim, resolving a citation |
| `fisk knowledge rm` | Removes specific indexed sources by path |
| `fisk knowledge reset` | Wipes the entire index |
| `fisk knowledge sources` | Lists indexed files with chunk counts and last-indexed time |
| `fisk knowledge doctor` | Checks the index, its full-text integrity, and when configured the embeddings server |
| `fisk knowledge rebuild` | Rebuilds the search indexes from the stored text, without re-embedding |
| `fisk knowledge stats` | Prints the tier banner and index counts and sizes |
| `fisk mcp` | Serves the tools over MCP on streamable HTTP |
| `fisk a2a` | Serves the tools to other agents over NATS |
| `fisk discover <agent>` | Discovers a remote agent and prints its tools |

The `knowledge` command carries `rag` and `k` as aliases.

## Source map

Line counts exclude tests, at the snapshot commit.

| Package | Files | Lines | Role | Page |
|---------|------:|------:|------|------|
| `main` (repo root) | 15 | 3869 | The CLI: command registration, flags, signals, and both terminal renderers | [Configuration]({{% relref "configuration" %}}) |
| `config` | 1 | 1281 | The whole configuration surface, parsing, defaulting, and validation | [Configuration]({{% relref "configuration" %}}) |
| `internal/agent` | 5 | 2975 | Run setup, the loop, hooks, and the events contract | [The agent loop]({{% relref "agent-loop" %}}) |
| `internal/toolkit` | 8 | 637 | Neutral tool contracts: `Tool`, `Kind`, `Presentation`, `Prompter` | [Tools]({{% relref "tools" %}}) |
| `internal/toolkit/fisk` | 5 | 1384 | Introspection, filtering, argv construction, execution | [Tools]({{% relref "tools" %}}) |
| `internal/toolkit/builtin` | 4 | 1299 | The human-in-the-loop, memory, and knowledge tools | [Tools]({{% relref "tools" %}}) |
| `internal/toolkit/functool` | 3 | 446 | The generic function-tool backend | [Tools]({{% relref "tools" %}}) |
| `internal/llm` | 6 | 423 | The provider-neutral message model and registry | [Providers]({{% relref "providers" %}}) |
| `internal/llm/anthropic` | 3 | 534 | The only provider, and the only SDK importer | [Providers]({{% relref "providers" %}}) |
| `internal/rag` | 16 | 4905 | The knowledge index: chunking, embedding, hybrid search, enumeration, vocabulary, watching | [Knowledge]({{% relref "knowledge" %}}) |
| `internal/memory` | 6 | 513 | The memory contract and shared rules | [Memory]({{% relref "memory" %}}) |
| `internal/memory/file` | 3 | 316 | One markdown file per memory | [Memory]({{% relref "memory" %}}) |
| `internal/memory/jetstream` | 1 | 487 | One KV entry per memory, with a read-before-update guard | [Memory]({{% relref "memory" %}}) |
| `internal/runstate` | 7 | 986 | Records, the fold, the fingerprint, and the schemas | [Sessions and replay]({{% relref "state" %}}) |
| `internal/runstate/file` | 3 | 472 | A JSON-lines journal per run, with an advisory lock | [Sessions and replay]({{% relref "state" %}}) |
| `internal/runstate/jetstream` | 1 | 627 | One record per subject, with a tail fence | [Sessions and replay]({{% relref "state" %}}) |
| `internal/a2a` | 13 | 1627 | The agent-to-agent protocol engine and its transport contract | [Serving]({{% relref "serving" %}}) |
| `internal/a2a/nats` | 1 | 242 | The one live transport binding | [Serving]({{% relref "serving" %}}) |
| `internal/mcpserver` | 1 | 638 | The whole MCP serving mode | [Serving]({{% relref "serving" %}}) |
| `internal/tui` | 4 | 2740 | The full-screen surface: live view, viewer, prompter, splash | [Terminal and events]({{% relref "terminal" %}}) |
| `internal/util` | 8 | 1149 | Sanitization, markdown, the confirm gate, the tracer, run stats | [Terminal and events]({{% relref "terminal" %}}) |
| `internal/remotetools` | 1 | 315 | Import policy: discovery, filtering, deterministic naming | [Serving]({{% relref "serving" %}}) |
| `internal/conns` | 1 | 89 | Connection establishment and ownership | [Serving]({{% relref "serving" %}}) |
| `internal/agenttest` | 8 | 976 | Fakes proving each contract is implementable from outside the package | |

The root `main` package holds one file per command plus the two event renderers: `main.go`, `run_command.go`,
`run_events.go`, `run_tui_events.go`, `session_command.go`, `resume_replay.go`, `info_command.go`, `rag_command.go`,
`rag_match.go`, `rag_words.go`, `rag_watch.go`, `mcp_command.go`, `a2a_command.go`, `discover_command.go`, `remote_tools.go`.

## Key types

| Type | Package | What it is |
|------|---------|-----------|
| `agent.Options` | `internal/agent` | Everything a caller supplies for one run, including every injection point |
| `agent.Events` | `internal/agent` | The typed display sink; the package decides what happened, the caller how it looks |
| `agent.Hooks` | `internal/agent` | Eight optional callbacks on the run goroutine |
| `toolkit.Tool` | `internal/toolkit` | The four-method interface every tool kind satisfies |
| `toolkit.Kind` | `internal/toolkit` | The accounting axis: who provides the tool |
| `toolkit.Presentation` | `internal/toolkit` | The visibility axis: how a call is shown |
| `toolkit.Prompter` | `internal/toolkit` | The only sanctioned path to an operator |
| `toolkit.CommandResult` | `internal/toolkit` | The JSON shape a command-ish tool returns, local or remote |
| `llm.Provider` | `internal/llm` | The one place a concrete SDK is spoken on the request path |
| `llm.Message` | `internal/llm` | The neutral message model, and also the on-disk format |
| `runstate.Store` | `internal/runstate` | Describe, create, open, load, list, delete for run journals |
| `runstate.RunState` | `internal/runstate` | The folded, resumable state |
| `runstate.Fingerprint` | `internal/runstate` | The configuration a journal was written against |
| `memory.Store` | `internal/memory` | Describe, list, read, write, delete over durable model-written notes |
| `rag.Store` | `internal/rag` | One handle over the single SQLite index file |
| `a2a.Transport` | `internal/a2a` | Round-trip, serve, describe, close; moves bytes and never decodes |
| `a2a.Header` | `internal/a2a` | Framing embedded flat into every message |
| `config.Config` | `config` | The whole YAML surface, read through nil-safe accessors |

## Constants worth knowing

| Value | Meaning |
|-------|---------|
| 10 | Tool count at which deferral and tool search engage |
| 200000 / 50 / 120s | Default token budget, iteration budget, and per-call timeout |
| 8192 / 16384 | Default max output tokens, and the value used when thinking is on |
| 2 / 30s | Default concurrency and per-call timeout for both serving modes |
| 64 KiB / 32 KiB | Head and tail kept from a tool's output before truncation |
| 16 MiB | Introspection output cap, rejected rather than truncated |
| 768 KiB | A2A message size cap, sitting under the NATS 1 MiB default |
| 64 KiB / 1024 | Memory content cap and entry count cap |
| 69600 | Memory entry cap including frontmatter, the `nats kv add` figure |
| 1200 / 1500 | Knowledge chunk target and maximum in bytes |
| 50 / 20 / 6000 | Search fanout per tier, result ceiling, and default injected-token budget |
| 8080 / 127.0.0.1 | Default MCP port and address |
| `choria.fisk-ai` | The A2A NATS subject prefix |

## Glossary

<dl class="cm-kv">
  <dt>agent</dt><dd>One configuration driving one model over one tool set. Named by its identity.</dd>
  <dt>identity</dt><dd>The agent's name. Also the NATS queue group and the namespace for on-disk state.</dd>
  <dt>tool</dt><dd>Anything the model may call: a wrapped leaf command, a built-in, an imported remote tool, or a caller-injected custom tool.</dd>
  <dt>kind</dt><dd>Who provides a tool. Used for accounting and log tokens, never for display suppression.</dd>
  <dt>presentation</dt><dd>How a tool call is displayed. Used for suppression, never for accounting.</dd>
  <dt>confirm gate</dt><dd>The operator approval checkpoint on a command the model chose. Default deny.</dd>
  <dt>HITL</dt><dd>Human in the loop: the built-in tools with which the model asks the operator a question.</dd>
  <dt>deferral</dt><dd>Withholding full tool definitions so the model finds them through tool search instead.</dd>
  <dt>checkpoint</dt><dd>A run that writes a durable journal and can therefore be suspended and resumed.</dd>
  <dt>session</dt><dd>One journal, identified by a run id. A context reset rotates to a new one.</dd>
  <dt>journal</dt><dd>The append-only record stream for one session.</dd>
  <dt>fold</dt><dd>The pure function turning a record stream back into a resumable state.</dd>
  <dt>fingerprint</dt><dd>The hashed configuration a journal was written against, checked before a resume.</dd>
  <dt>memory</dt><dd>Short markdown notes the model writes for its future self.</dd>
  <dt>memory index</dt><dd>The key-and-description listing injected into the system prompt at startup.</dd>
  <dt>knowledge</dt><dd>The operator-owned document index the model searches. RAG is the technique; knowledge is the feature.</dd>
  <dt>tier</dt><dd>Whether knowledge search is lexical only or hybrid, always reported on its own line.</dd>
  <dt>citation</dt><dd>A knowledge reference of the form <code>path#ordinal</code>. Ordinals shift after a reindex.</dd>
  <dt>A2A</dt><dd>Agent to agent: the NATS protocol for serving tools to, and importing tools from, another Fisk AI agent.</dd>
  <dt>agent card</dt><dd>The discovery reply describing an agent's name, version, and exposed tools.</dd>
  <dt>remote tool</dt><dd>Another agent's tool imported into this one, presented to the model as if it were local.</dd>
  <dt>elicitation</dt><dd>The MCP mechanism used to request approval from a client when there is no local operator.</dd>
</dl>

## Reserved and unused, at a glance

Collected from the subsystem pages so a reader can see the shape of the unfinished edges in one place.

| Item | Status |
|------|--------|
| `remote_agents` config key and its type | Declared, read by nothing. `remote_tools` is the working feature |
| The A2A streaming task flow | Fully defined, schema-validated, round-trip tested; no transport path sends or receives it |
| `Header.MustUnderstand`, `Header.Parent`, `Recipient.Instance` | Declared and schema-valid, never set or read |
| The runstate JSON schema validator | Compiled and tested, wired into no read or write path |
| `llm.Caps.MaxOutputTokens` | Declared, never read; nothing clamps the per-call cap |
| Three exported anthropic codec functions | No non-test callers; migration residue |
| `agent.SlogEvents` | No in-tree consumer; exists for a job runner that does not exist yet |
| Most of `agent.Options` | Embedder-only; the CLI sets fewer than half its fields |
| `functool` confirm and validation specs | No production users; for embedder-supplied tools |
| `IndexOptions.Extensions` | A real extension point no caller sets |
| `documents.title` in the knowledge schema | Written on every upsert, read on no path |
| A second transport, provider, or MCP client | The registries are complete; no second implementation exists, and there is no MCP client at all |

{{% notice style="tip" title="Next" %}}
For operator-facing documentation rather than design, see the [Agents]({{% relref "/agents" %}}),
[MCP Servers]({{% relref "/mcp" %}}), [Knowledge]({{% relref "/knowledge" %}}), and
[Reference]({{% relref "/reference" %}}) sections.
{{% /notice %}}
