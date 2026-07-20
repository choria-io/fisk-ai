# Knowledge (RAG)

Knowledge gives an agent a search tool over a locally built index of its own markdown and text documents. The agent
gains a single in-process `knowledge_search` tool that returns the most relevant sections of the indexed corpus, each
with a citation, so it can ground its answers in project documentation rather than its training data.

In AI terms this is a RAG (retrieval-augmented generation) system contained entirely in a single binary and a single
process. It is aimed at keeping source data local and handles markdown files. It runs with or without a local embedding
model; without one it uses full-text search alone.

Everything ships in the one `fisk-ai` binary. The index is a single SQLite file built and queried in-process, with no
CGo and no external database. The orchestrating LLM stays remote at Anthropic or a local compatible model; only storage
and retrieval are local. A local embeddings server is the only optional external process, and only when semantic search
is turned on.

> [!info] Note
> Knowledge is opt-in and off by default. Like the [memory](../agents/#memory) tools it is only wired into the agent
> loop, though `knowledge_search` can be exposed over [MCP](#serving-over-mcp) through an explicit allowlist.

## Enabling knowledge

The minimal enable needs no model and no server. Turn the feature on and point it at the documents to index:

```yaml
harness:
  knowledge:
    enabled: true
    paths:
      - docs/
```

Build the index and search it from the command line, then run the agent, which now has the `knowledge_search` tool:

```nohighlight
$ fisk-ai knowledge index docs/    # build the index, incremental, no embeddings needed
$ fisk-ai knowledge search "backpressure"
$ fisk-ai run "how does backpressure work?"
```

The index is incremental. A second `knowledge index` re-reads only files whose content changed, detected by hash, and
reconciles deletions when a full configured root is walked.

## Two retrieval tiers

Knowledge has two retrieval tiers. The lexical tier is always on and is the default; the vector tier is a separate
opt-in.

The two tiers match a query in different ways. The lexical tier matches on words: a query and a section rank together
when they share the same terms. The vector tier matches on meaning: a query and a section rank together when an
embedding model places them close in vector space, even when they share no words. Lexical search is exact and literal;
vector search is fuzzy and semantic. Hybrid mode runs both and fuses the results, so a query gets lexical precision on
the terms it names and vector recall on the ideas it only paraphrases.

### Lexical, the default

The lexical tier is an FTS5/BM25 full-text index. It is always active when knowledge is enabled and needs no embedding
model, no external service, and no per-query cost. This is the baseline the feature works on for everyone, and for a
corpus of local technical documents it is often all that is needed.

Lexical search is strongest when the query uses the corpus's own vocabulary: an exact identifier, a command name, an
error string, or a term of art the documents themselves use. Its limit is the mirror image. A query worded differently
from the documents can miss a section that explains the same idea in other words, since a section that never uses the
searched term does not match however relevant it is.

### Semantic, opt-in

Add an `embeddings` block to turn on the vector tier. Its presence is the switch: with no block, knowledge stays
lexical-only. When it is set, each chunk is embedded through a local OpenAI-compatible embeddings server, and a query is
answered by fusing the lexical and vector rankings with Reciprocal Rank Fusion behind the one search call.

The benefit is recall on meaning rather than wording. A natural-language question finds the right section even when it
shares no keywords with it: asking "how do I stop the agent spending too much" can surface the section on budgets though
that section never says "spending". This suits an agent, which phrases a search in its own words rather than the
documents' exact terms. Fusing the two tiers keeps lexical's precision on named terms while adding this semantic reach,
so the hybrid result is usually better than either tier alone.

The cost is what the vector tier adds. It needs a local embedding model and server to run, a `--reindex` to embed the
existing corpus, and one embedding call per query at search time. Retrieval stays local either way; the vector tier
trades the extra moving part for better recall on paraphrased and conceptual queries.

```yaml
harness:
  knowledge:
    enabled: true
    paths:
      - docs/
    embeddings:
      base_url: http://127.0.0.1:1234/v1
      model: text-embedding-embeddinggemma-300m
```

EmbeddingGemma-300m, used in the examples here, is a good default to start from for local embedding: a small
(300M-parameter) Gemma-based model that runs comfortably on CPU or modest hardware, is multilingual, and is well
supported by the local runtimes this feature talks to, such as Ollama and LM Studio. It supports Matryoshka dimension
truncation if you want smaller, faster vectors. The feature stays model-agnostic - any OpenAI-compatible endpoint works -
but if you have no specific reason to prefer another model, this is a sound choice.

The embedding model is user-chosen, so nothing about it is assumed. `fisk-ai knowledge doctor` probes the configured
server and reports the model, its vector dimension, and whether its output is normalized. After turning embeddings on,
rebuild the index so the vectors are populated:

```nohighlight
$ fisk-ai knowledge doctor
$ fisk-ai knowledge index --reindex
$ fisk-ai knowledge stats
```

Changing the model, its dimension, or a prefix changes the vector identity and forces a `--reindex`. The index refuses a
mismatched model upfront, before embedding anything, rather than silently returning wrong rankings.

### Tier line

Every surface, the CLI commands and the `knowledge_search` tool result, prints one canonical tier line so it is never
ambiguous which tier answered a query:

```nohighlight
tier: lexical (FTS5) - no embeddings configured
tier: hybrid (FTS5 + vectors, RRF) - model=<name> dim=<n>
tier: hybrid -> DEGRADED to lexical (embeddings unreachable: <reason>)
```

A configured embeddings server that is unreachable at query time degrades to lexical-only and says so, rather than
failing the search. A configured embeddings server that is unreachable at index time fails loud, so an index the user
asked to be semantic is never silently built lexical-only.

### When to enable embeddings

Start lexical. It has nothing to run and no per-query cost, and it is often enough on its own. Add embeddings when the
searches that matter are worded differently from the documents.

| Aspect        | Lexical (default)                          | Hybrid (with embeddings)                             |
|---------------|--------------------------------------------|------------------------------------------------------|
| Matches on    | shared words, exact terms                  | meaning, plus shared words                           |
| Best for      | identifiers, command names, error strings  | natural-language questions, paraphrased queries      |
| Needs         | nothing beyond the binary                  | a local embedding model and server                   |
| Per-query cost| none                                       | one embedding call                                   |
| Index cost    | text index only                            | a `--reindex` to embed the corpus                    |

The two are not exclusive: enabling embeddings keeps the lexical tier and fuses the two, so nothing is lost by turning it
on beyond the extra model to run.

## Configuration

The `harness.knowledge` block mirrors `harness.memory`. An absent block, or `enabled: false`, means off.

```yaml
harness:
  knowledge:
    enabled: true
    paths:
      - docs/
    directory: ""
    top_k: 5
    max_injected_tokens: 6000
    embeddings:
      base_url: http://127.0.0.1:1234/v1
      model: text-embedding-embeddinggemma-300m
      api_key_env: RAG_EMBED_KEY
      timeout: 30s
      query_prefix: ""
      document_prefix: ""
```

| Field                          | Description                                                                                 |
|--------------------------------|---------------------------------------------------------------------------------------------|
| `enabled` (boolean)            | turns the feature on; absent or `false` means off                                           |
| `paths` (array)                | default index roots used when `knowledge index` is run with no path argument                |
| `directory` (string)           | store location, resolved relative to the working directory; default `knowledge/<identity>`  |
| `top_k` (integer)              | default retrieval count, default `5`, hard ceiling `20`                                      |
| `max_injected_tokens` (integer)| cap on the total retrieved text fed to the model, default `6000`                             |
| `embeddings`                   | optional block; its presence turns on the vector tier                                       |

The `directory` follows the same rule as memory's `options.directory`: resolved against the working directory when it is
not absolute, and defaulting to `knowledge/<identity>`. The `identity` is the agent's name, so two agents pointed at the
same directory share an index and the default keeps each agent's index its own.

### Embeddings

The `embeddings` block is only read when the vector tier is on. It describes a local OpenAI-compatible endpoint that
`fisk-ai` POSTs to at `<base_url>/embeddings`.

| Field                       | Description                                                                                |
|-----------------------------|--------------------------------------------------------------------------------------------|
| `base_url` (string)         | OpenAI-compatible base URL; requests go to `<base_url>/embeddings`                          |
| `model` (string)            | the embedding model name to request                                                        |
| `api_key_env` (string)      | name of an environment variable holding the API key, never the secret itself; optional     |
| `timeout` (duration)        | per-request timeout, default `30s`                                                          |
| `query_prefix` (string)     | text prepended to a query before embedding; optional, default empty                        |
| `document_prefix` (string)  | text prepended to a chunk before embedding, supports `{title}`; optional, default empty    |

`api_key_env` names an environment variable rather than carrying the secret, so no secret lives in `agent.yaml` and none
is logged. Prefixes default to empty because the model is user-chosen and a wrong prefix is worse than none; the models
that need one document it. Run `knowledge doctor` to see whether a chosen model expects a prefix.

> [!info] Note
> A non-loopback `base_url` must use `https`. The embeddings endpoint is only ever contacted when the vector tier is on;
> the lexical path makes no network calls.

#### EmbeddingGemma prefixes

Google's EmbeddingGemma is trained with task-specific prompts, so it expects a prefix on both sides: a query is embedded
under a retrieval instruction and a document under a title-and-text template. Setting them to the model's documented
values improves retrieval; leaving them empty still works but embeds text bare, the way the model was not trained to see
it.

```yaml
harness:
  knowledge:
    enabled: true
    paths:
      - docs/
    embeddings:
      base_url: http://127.0.0.1:1234/v1
      model: text-embedding-embeddinggemma-300m
      query_prefix: "task: search result | query: "
      document_prefix: "title: {title} | text: "
```

The trailing space is significant, so quote both values in YAML. `{title}` in `document_prefix` is filled from each
chunk's heading path, or the literal `none` when a chunk has no heading, matching the template the model expects. Because
a prefix is part of the pinned vector identity, adding or changing one forces a `--reindex`; the index refuses a
mismatched prefix upfront rather than mixing vectors embedded under different prompts.

> [!info] Note
> Some GGUF builds of EmbeddingGemma log `'tokenizer.ggml.add_eos_token' should be set to 'true' in the GGUF header` on
> every embedded string. This comes from the embeddings server, not `fisk-ai`: the OpenAI embeddings API exposes no
> control over tokenization, so it cannot be silenced from the client. It is harmless but means the model embeds without
> the end-of-sequence token it was trained with; a GGUF whose header sets `tokenizer.ggml.add_eos_token = true` resolves
> it.

## The knowledge_search tool

When knowledge is enabled the agent is offered one tool, `knowledge_search`, that takes a `query` and an optional
`top_k`. It runs the lexical search, adds and fuses the vector search when the vector tier is on, and returns the ranked
sections. The effective count is `min(requested or configured top_k, 20)`, and the total returned text is capped at
`max_injected_tokens`.

Each result carries a citation token of the form `<relpath>#<ordinal>`, the file path relative to the index root and the
chunk's position in that file, alongside the human-readable heading path of the section. The same token is printed by
`knowledge search` and `knowledge sources` and accepted verbatim by `knowledge show`, so a result can be resolved back to
its full text.

Results are returned to the model as untrusted reference data, framed as material to draw on rather than as
instructions. When the store has no index yet the tool returns a soft `index_not_built` status naming the fix, rather
than failing the run, so a missing index never bricks agent startup.

> [!info] Warning
> Retrieved text is data the corpus contains, not trusted instructions. Treat a `knowledge_search` result the same way as
> a memory: content to reason over, not directives to follow.

## CLI commands

The `fisk-ai knowledge` command builds and inspects the index. It is separate from the agent's `knowledge_search` tool;
the CLI never runs the agent. Every command reads `--config` (default `agent.yaml`) and prints the tier line.

| Command                              | Description                                                                        |
|--------------------------------------|------------------------------------------------------------------------------------|
| `knowledge index [paths...]`         | incremental build; requires a path argument or a configured `knowledge.paths`      |
| `knowledge watch [paths...]`         | watch the configured paths and re-index on change, coalescing edit bursts          |
| `knowledge search <query>`           | retrieve from the CLI for tuning; prints citation, heading, and a snippet          |
| `knowledge show <relpath#ordinal>`   | print one chunk verbatim, resolving a citation token                               |
| `knowledge sources`                  | list indexed files with chunk counts and last-indexed time                         |
| `knowledge doctor`                   | preflight checks; probes the embeddings server only when it is configured          |
| `knowledge stats`                    | tier banner, document and chunk counts, vector count, pinned model, store size     |
| `knowledge rm <source...>`           | remove specific sources' chunks by path                                            |
| `knowledge reset`                    | wipe the index; the bare form refuses and names `--force`                          |

The command is also available as `fisk-ai rag`.

`knowledge index` is incremental and per-file: a file whose hash is unchanged is skipped, a changed file is re-chunked,
and a walk of a full configured root reconciles deletions. `--dry-run` lists the files and an embedding-call estimate
without embedding anything, and `--reindex` forces a full rebuild. Indexing walks markdown and text files only, by the
`.md`, `.markdown`, `.txt`, and `.text` extensions, and always excludes the store directory itself and the `memory/`
directory.

`knowledge watch` keeps the index current while you edit. It runs one initial index over the configured paths, then
watches them and re-indexes on every change, coalescing a burst of edits with a `--debounce` window (default `2s`,
minimum `100ms`). Each re-index is the same incremental pass, so unchanged files are skipped by hash and only edited
files are re-chunked and re-embedded, and a file is dropped from the index as it is deleted. Watching is recursive and
cross-platform, on Linux, macOS, and Windows, and applies the same exclusions as `index`: the store directory, `memory/`,
and dotdirs such as `.git`. A configured path that does not exist is warned about and skipped rather than failing the
command, and `--no-initial` skips the startup pass to watch only for later changes.

Unlike the one-shot `knowledge index`, `watch` is long-running, but it holds the single writer lock only for the moment
each re-index runs, so an occasional manual `knowledge index` still succeeds between changes and a clash simply retries.
Stop it with Ctrl-C. Paths must exist when it starts: a root created afterwards is not picked up until the next run, and
if the process misses a deletion event the stale entry clears on the next `knowledge index` or a restart, both of which
reconcile.

`knowledge doctor` degrades for lexical-only users and never exits non-zero solely because embeddings are absent. It
always checks that the store is present and writable, that FTS5 is compiled in, and that the configured paths resolve.
Only when `embeddings` is configured does it probe the endpoint and check the stored model and dimension for a mismatch.

`knowledge reset` without `--force` refuses and names the document and chunk count it would delete; `knowledge reset
--force` clears every row and leaves a clean empty index in place, ready for the next `knowledge index`.

## Store location and layout

The index is project-local. It lives at `knowledge/<identity>` relative to the working directory, alongside the
`memory/<identity>` store, which suits the one-project-per-directory workflow where an `agent.yaml`, a `memory/`
directory, and a `knowledge/` directory sit side by side. The `directory` field overrides the location.

The store is a single SQLite file with its `-wal` and `-shm` sidecars. The agent opens it read-only while `knowledge
index` is the single writer, so an index can be rebuilt while an agent runs without the agent seeing a half-written
state. A cross-process lock stops two indexers from running at once.

> [!info] Warning
> The store uses SQLite WAL and its shared-memory sidecar, so every process must be on the same machine. Do not place the
> store on a network filesystem such as NFS or SMB.

## Serving over MCP

`knowledge_search` is the one built-in tool that can also be served over [MCP](../mcp/). It is read-only and needs no
operator prompt, unlike the human-in-the-loop and memory tools, which stay agent-only. Exposure is off by default and
enabled through an explicit allowlist:

```yaml
expose:
  agent:
    mcp:
      port: 8080
      builtins:
        - knowledge_search
```

Only `knowledge_search` is accepted in `builtins`; listing any other built-in is a configuration error that names the
exposable set. The MCP process opens the read-only store and, when embeddings are configured, embeds the query itself, so
the embeddings server must be reachable from that process. Degrade-to-lexical and the stored model validation apply
unchanged.

> [!info] Warning
> The MCP server binds every interface. Exposing `knowledge_search` lets any client that can reach the port read verbatim
> snippets of the indexed corpus. Bind localhost or front it with authentication if the corpus is sensitive.

## Security

The index holds the verbatim text of every indexed document, unencrypted on disk. `modernc.org/sqlite` has no pure-Go
at-rest encryption, so the posture matches the [memory](../agents/#memory) feature: the file and its sidecars are created
`0600` inside a `0700` directory.

* The `0600` permission protects the file from other users on the same host. It does not protect against disk theft,
  backups, or a stolen copy, so do not index secrets.
* Retrieved chunks are framed as untrusted reference data and stripped of terminal control sequences before any TUI
  render, so indexed text cannot spoof the display or inject instructions.
* Embeddings secrets are supplied by environment-variable name and never logged. A non-loopback embeddings `base_url`
  must use `https`, and the request timeout is enforced.
* Over MCP the allowlist is the only gate, and only the read-only `knowledge_search` is ever served; no index or write
  path is reachable over MCP.
