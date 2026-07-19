# Reference and Map

A quick index into the codebase: what each command does, where each package lives, the types worth knowing, and the vocabulary the rest of this map uses.

## Command surface

`main.go` registers seven commands on the `fisk-ai` root. Each is defined in its own `*_command.go` file.

| Command | What it does | File |
|---------|--------------|------|
| `run` | Drives the agent loop against a prompt; the default face. Flags include `--checkpoint`, `--resume`, `--chat`, `--no-tui`, `--tool-output`. | `run_command.go` |
| `session` | Lists, shows, and removes checkpointed sessions (`ls`, `show`, `rm`); `show --transcript` opens the viewer. | `session_command.go` |
| `info` | Prints the resolved tool table, gated commands, and prompt without calling the model. | `info_command.go` |
| `knowledge` | Builds and inspects the local RAG index behind `knowledge_search` (`index`, `search`, `show`, `sources`, `rm`, `reset`, `doctor`, `stats`); aliased `rag` and `k`. | `rag_command.go` |
| `mcp` | Serves the tools over MCP for an external client. | `mcp_command.go` |
| `a2a` | Serves the tools over NATS for peer agents. | `a2a_command.go` |
| `discover` | Sends an A2A discovery request and prints a peer's agent card. | `discover_command.go` |

## Source-file map

| Package | Responsibility | Key files |
|---------|----------------|-----------|
| `main` (root) | CLI wiring, flag parsing, mode and UI selection, signal contract. | `main.go`, `run_command.go`, `run_events.go`, `run_tui_events.go`, `resume_replay.go`, `rag_command.go` |
| `config` | The single `agent.yaml` schema, mode-based validation, accessors. | `config.go` |
| `internal/util` | The shared core: introspection, tool params, built-ins, confirm gate, prompter, LLM call, stats. | `fisk.go`, `anthropic.go`, `builtin.go`, `builtin_memory.go`, `builtin_rag.go`, `confirm.go`, `prompter.go`, `llm.go` |
| `internal/agent` | The agentic loop: setup, iteration, events. | `agent.go`, `runner.go`, `events.go` |
| `internal/runstate` | Durable sessions: records, fold, store, fingerprint, locking. | `record.go`, `state.go`, `store.go`, `filestore.go`, `fingerprint.go` |
| `internal/memory` | The backend-agnostic memory store: `Store` interface, backend registry, key and write validation. | `store.go`, `registry.go`, `key.go`, `write.go` |
| `internal/memory/file` | The file memory backend, registered by import: one markdown file per key. | `file.go`, `frontmatter.go`, `nofollow.go` |
| `internal/rag` | The local RAG index: SQLite store, chunking, hybrid search, embeddings, doctor, write lock. | `store.go`, `chunk.go`, `index.go`, `search.go`, `embed.go`, `doctor.go`, `vec.go`, `lock_unix.go` |
| `internal/tui` | The full-screen runner and transcript viewer. | `viewer.go`, `live.go`, `prompter.go`, `splash.go` |
| `internal/mcpserver` | Serving tools over MCP. | `mcpserver.go` |
| `a2a` | Transport-agnostic A2A protocol types. | `messages.go`, `block.go`, `header.go`, `types.go`, `schemas.go` |
| `internal/a2anats` | The NATS binding for A2A. | `client.go`, `server.go`, `a2anats.go`, `header.go` |
| `internal/remotetools` | Import policy for tools pulled from a peer. | `remotetools.go` |

## Key types

| Type | Role | Explained in |
|------|------|--------------|
| `util.Tool` | A single introspected command as a tool: path, model, schema, tags. | [Tools and Introspection]({{% relref "tools" %}}) |
| `config.Config` | The parsed `agent.yaml` for any mode. | [Architecture]({{% relref "architecture" %}}) |
| `agent.runner` | The loop state, split into rebuilt infrastructure and resumable state. | [The Agent Loop]({{% relref "agent-loop" %}}) |
| `util.ConfirmGate` | The default-deny approval enforcement for gated commands. | [Safety and Human in the Loop]({{% relref "safety" %}}) |
| `runstate.RunState` | The folded, resumable state of a run. | [Sessions and Resume]({{% relref "sessions" %}}) |
| `runstate.Fingerprint` | The configuration hash that guards a resume. | [Sessions and Resume]({{% relref "sessions" %}}) |
| `memory.Store` | The pluggable key/value backend interface. | [Memory]({{% relref "memory" %}}) |
| `rag.Store` | The SQLite-backed knowledge index: chunk, embed, hybrid search. | [Knowledge and RAG]({{% relref "knowledge" %}}) |
| `tui.Live` | The full-screen live-run controller. | [The Terminal UI]({{% relref "terminal-ui" %}}) |
| `a2a.Header` | The self-describing framing on every A2A message. | [Interoperability]({{% relref "interop" %}}) |

## Glossary

<dl class="cm-kv">
  <dt>Tool</dt><dd>A single runnable fisk leaf command exposed to the model, named by its command path joined with underscores.</dd>
  <dt>Introspection</dt><dd>Running a fisk binary with `--fisk-introspect` to read its command tree as a model.</dd>
  <dt>Deferral</dt><dd>Sending tool definitions on demand through the tool-search tool once the set reaches ten tools, rather than all up front.</dd>
  <dt>Confirm gate</dt><dd>The default-deny check that makes an operator approve a command tagged `ai:confirm` or a `confirm_tags` tag before it runs.</dd>
  <dt>HITL</dt><dd>Human in the loop: the `ask_human_*` tools the model can call to ask the operator a question.</dd>
  <dt>Journal</dt><dd>The append-only record stream a checkpointed run writes to disk, event by event.</dd>
  <dt>Fold</dt><dd>Replaying a record stream into a resumable `RunState` with no IO.</dd>
  <dt>Fingerprint</dt><dd>A hash of model, prompt, tool set, thinking mode, and budgets that must match for a resume to proceed.</dd>
  <dt>Identity</dt><dd>An agent's logical name, defaulting to the binary base name, used for the memory directory and the A2A subjects.</dd>
  <dt>Elicitation</dt><dd>The MCP mechanism by which a served command asks the calling client to approve it.</dd>
  <dt>Agent card</dt><dd>An A2A agent's self-description: name, version, protocols, and tools.</dd>
  <dt>RAG</dt><dd>Retrieval-augmented generation: searching a local document index and feeding the matches to the model as grounding.</dd>
  <dt>Chunk</dt><dd>A heading-delimited, size-packed slice of a document, the unit that is indexed, ranked, and cited.</dd>
  <dt>Lexical tier</dt><dd>The always-on FTS5/BM25 full-text search over chunks; needs no model or server.</dd>
  <dt>Vector tier</dt><dd>The opt-in semantic search: chunk embeddings in sqlite-vec, matched by nearest neighbor.</dd>
  <dt>RRF</dt><dd>Reciprocal Rank Fusion: combining the lexical and vector rankings by rank rather than score, with constant K=60.</dd>
  <dt>Citation</dt><dd>A `relpath#ordinal` token naming a chunk, resolvable back to full text by `knowledge show`.</dd>
</dl>

{{% notice style="tip" title="Back to the start" %}}
Return to the [Code Map overview]({{% relref "/design/codemap" %}}) for the system-at-a-glance diagram and the mental model.
{{% /notice %}}
