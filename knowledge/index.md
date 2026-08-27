# Knowledge (RAG)

Knowledge gives an agent search tools over a locally built index of its own markdown and text documents. They run
in-process and cite what they return, so it can ground its answers in project documentation rather than its training
data.

In AI terms this is a RAG (retrieval-augmented generation) system contained entirely in a single binary and a single
process. It is aimed at keeping source data local and private. It runs with or without a local embedding model; without
one it uses full-text search alone.

Everything ships in the one `fisk` binary. The index is a single SQLite file built and queried in-process, with no
external database. A local embeddings server is the only optional external process, and only when semantic search
is turned on.

## Enabling knowledge

Knowledge is off by default. You get full-text search without any LLM requirements.

```yaml
harness:
  knowledge:
    enabled: true
    paths:
      - docs/
```

Build the index and search it from the command line, then run the agent, which now has the knowledge tools:

```nohighlight
$ fisk knowledge index docs/    # build the index, incremental, no embeddings needed
$ fisk knowledge search "backpressure"
$ fisk run "how does backpressure work?"
```

The index is incremental. A second `knowledge index` re-reads only files whose content changed, detected by hash, and
reconciles deletions when a full configured root is walked.

## Two retrieval tiers

The lexical tier is always on, has no dependencies, and is the default. Vector search is opt-in and requires an
embeddings model.

### Lexical search

Lexical search finds only exact words present in the text. Synonyms and concepts do not match.

The lexical tier is an [FTS5/BM25](https://sqlite.org/fts5.html) full-text index. It is always active when knowledge
is enabled and needs no embedding model or other dependencies. Command output calls this tier `lexical`.

### Vector search

Semantic search recalls on meaning rather than wording. A natural-language question finds the right section even when it
shares no keywords with it: asking "how do I stop the agent spending too much" can surface the section on budgets though
that section never says "spending". This suits an agent, which phrases a search in its own words rather than the
documents' exact terms.

Fusing the two tiers keeps lexical's precision on named terms while adding this semantic reach, so the hybrid result is
usually better than either tier alone.

It needs a local embedding server, such as Ollama or LM Studio, running at both index and query time.

Add an `embeddings` block to turn on the vector tier. When it is set, each chunk is embedded through a local
OpenAI-compatible embeddings server, and a query is answered by fusing the lexical and vector rankings with Reciprocal
Rank Fusion behind the one search call.

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

`text-embedding-embeddinggemma-300m`, used in the examples here, is a good default to start from for local embedding: a
small (300M-parameter) Gemma-based model that runs comfortably on CPU or modest hardware, is multilingual, and is well
supported by the local runtimes this feature talks to, such as Ollama and LM Studio. The feature stays model-agnostic,
any OpenAI-compatible endpoint works, and it is a sound default absent a specific reason to prefer another.

The embedding model is user-chosen, so nothing about it is assumed. `fisk knowledge doctor` probes the configured
server and reports the model, its vector dimension, and whether its output is normalized. After turning embeddings on,
rebuild the index so the vectors are populated:

```nohighlight
$ fisk knowledge doctor
$ fisk knowledge index --reindex
$ fisk knowledge stats
```

Changing the model, its dimension, or a prefix changes the vector identity and forces a `--reindex`. The index refuses a
mismatched model upfront, before embedding anything, rather than silently returning wrong rankings.

### Tier line

All invocations of related tools will print a line indicating configuration and active state:

```nohighlight
tier: lexical (FTS5) - no embeddings configured
tier: hybrid (FTS5 + vectors, RRF) - model=<name> dim=<n>
tier: hybrid -> DEGRADED to lexical (embeddings unreachable: <reason>)
```

A configured embeddings server that is unreachable at query time degrades to lexical-only, rather than failing the
search. A configured embeddings server that is unreachable at index time errors, so an index the user asked to be
semantic is never silently built lexical-only.

### When to enable embeddings

Start with lexical search. It has nothing to run and no per-query cost, and it is often enough on its own. Add
embeddings when the searches that matter are worded differently from the documents.

| Aspect         | Lexical (default)                         | Hybrid (with embeddings)                        |
|----------------|-------------------------------------------|-------------------------------------------------|
| Matches on     | shared words, exact terms                 | meaning, plus shared words                      |
| Best for       | identifiers, command names, error strings | natural-language questions, paraphrased queries |
| Needs          | nothing beyond the binary                 | a local embedding model and server              |
| Per-query cost | none                                      | one embedding call                              |
| Index cost     | text index only                           | a `--reindex` to embed the corpus               |

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

| Field                           | Description                                                                                                                         |
|---------------------------------|-------------------------------------------------------------------------------------------------------------------------------------|
| `enabled` (boolean)             | turns the feature on; absent or `false` means off                                                                                   |
| `paths` (array)                 | default index roots used when `knowledge index` is run with no path argument                                                        |
| `directory` (string)            | store location; a relative value resolves under the store base when set, else the working directory; default `knowledge/<identity>` |
| `top_k` (integer)               | default retrieval count, default `5`, hard ceiling `20`                                                                             |
| `max_injected_tokens` (integer) | cap on the total retrieved text fed to the model, default `6000`                                                                    |
| `embeddings`                    | optional block; its presence turns on the vector tier                                                                               |

An absolute `directory` is used as-is; a relative value, including the default `knowledge/<identity>`, resolves under
the store base when one is set and against the working directory otherwise. The `identity` is the agent's name, so two
agents pointed at the same directory share an index and the default keeps each agent's index its own.

The store base is a deployment concern for running many agents in one process, not an agent setting: a programmatic
caller passes `store_dir`, and the `knowledge` command takes a matching `--store-dir` flag or `FISK_AI_STORE_DIR`
environment variable. An absolute `directory` both the agent and the `knowledge` command read from the same config
needs neither, and is the surest way to keep them pointed at the same index.

### Embeddings

The `embeddings` block is only read when the vector tier is on. It describes a local OpenAI-compatible endpoint that
`fisk` POSTs to at `<base_url>/embeddings`.

| Field                      | Description                                                                             |
|----------------------------|-----------------------------------------------------------------------------------------|
| `base_url` (string)        | OpenAI-compatible base URL; requests go to `<base_url>/embeddings`                      |
| `model` (string)           | the embedding model name to request                                                     |
| `api_key_env` (string)     | name of an environment variable holding the API key, never the secret itself; optional  |
| `timeout` (duration)       | per-request timeout, default `30s`                                                      |
| `query_prefix` (string)    | text prepended to a query before embedding; optional, default empty                     |
| `document_prefix` (string) | text prepended to a chunk before embedding, supports `{title}`; optional, default empty |

`api_key_env` names an environment variable rather than carrying the secret, so no secret lives in `agent.yaml` and none
is logged. Prefixes default to empty because the model is user-chosen and a wrong prefix is worse than none; the models
that need one document it. Run `knowledge doctor` to see whether a chosen model expects a prefix.

> [!info] Note
> The `base_url` may be `http` or `https`. The embeddings endpoint is only ever contacted when the vector tier is on;
> the lexical path makes no network calls.

#### EmbeddingGemma prefixes

`text-embedding-embeddinggemma-300m` is trained with task-specific prompts, so it expects a prefix on both sides: a query is embedded
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
      # trailing space is required
      query_prefix: "task: search result | query: "
      document_prefix: "title: {title} | text: "
```

## The agent tools

When knowledge is enabled the agent is offered these tools, along with instructions.

### knowledge_search

`knowledge_search` runs the lexical search, adds and fuses the vector search when the vector tier is on, and returns
the ranked sections.

Each result carries a citation token of the form `<relpath>#<ordinal>`, the file path relative to the index root and the
chunk's position in that file, alongside the human-readable heading path of the section.

Results are returned to the model as untrusted reference data, framed as material to draw on rather than as
instructions. When the store has no index yet the tool returns a soft `index_not_built` status rather than failing the
run, so a missing index never bricks agent startup.

### knowledge_enumerate

{{% badge style="primary" title="Version" %}}0.0.4{{% /badge %}} `knowledge_enumerate` is the tool form of
[`knowledge match`](#which-documents-mention-a-word), with the same syntax. The model routes here
before answering that something is absent, then reads what it needs with `knowledge_search`.

## CLI commands

The `fisk knowledge` command builds and inspects the index. It is separate from the agent's tools; the CLI never runs
the agent. Every command reads `--config` (default `agent.yaml`) and prints the tier line.

| Command                            | Description                                                                                                                                                |
|------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `knowledge index [paths...]`       | incremental build; requires a path argument or a configured `knowledge.paths`                                                                              |
| `knowledge watch [paths...]`       | watch the configured paths and re-index on change, coalescing edit bursts                                                                                  |
| `knowledge search <query>`         | retrieve from the CLI for tuning; prints citation, heading, and a snippet                                                                                  |
| `knowledge match <query>`          | list every document containing the words, as a complete set; aliases `enumerate`, `which` {{% badge style="primary" title="Version" %}}0.0.4{{% /badge %}} |
| `knowledge words [pattern]`        | list the words the documents actually use, with document counts; aliases `vocab`, `terms` {{% badge style="primary" title="Version" %}}0.0.4{{% /badge %}} |
| `knowledge show <relpath#ordinal>` | print one chunk verbatim, resolving a citation token                                                                                                       |
| `knowledge sources`                | list indexed files with chunk counts and last-indexed time                                                                                                 |
| `knowledge doctor`                 | preflight and general consistency checks for the index and embeddings requirements                                                                         |
| `knowledge rebuild`                | rebuild the search index from the stored text, without re-embedding {{% badge style="primary" title="Version" %}}0.0.4{{% /badge %}}                       |
| `knowledge stats`                  | tier banner, document and chunk counts, vector count, pinned model, store size                                                                             |
| `knowledge rm <source...>`         | remove specific sources' chunks by path                                                                                                                    |
| `knowledge reset`                  | wipe the index; the bare form refuses and names `--force`                                                                                                  |

Indexing is incremental and per-file: a file whose hash is unchanged is skipped, a changed file is re-chunked,
and a walk of a full configured root reconciles deletions. Indexing walks markdown and text files only, by the
`.md`, `.markdown`, `.txt`, and `.text` extensions, and always excludes the store directory itself and the `memory/`
directory.

### Which documents mention a word

{{% badge style="primary" title="Version" %}}0.0.4{{% /badge %}} The search functions answer "what do the documents say
about this" and return the sections that scored best.

`knowledge match` answers what search cannot: which documents mention a word. It returns a complete list of all
matching documents. An empty result means the documents do not contain the words.

```nohighlight
$ fisk knowledge match "retention policy"
$ fisk knowledge match deprecated --paths-only
$ fisk knowledge which api -deprecated
```

> [!info] Complete, not literal
> The set is complete: every indexed document containing the words is listed. It is not literal. Matching is by word
> stem, so `deprecated` also finds `deprecate` and `deprecation`, and it merges words that share a stem: `universe` and
> `university` both reduce to `univers`. Case always folds. Diacritics fold only within Latin-1, so `cafe` finds `café`,
> but `strasse` does not find `Straße` and `viet` does not find `Việt`. A complete answer therefore includes documents
> about a word that was not typed, and excludes spellings it might have been expected to reach.

#### Query syntax

| Form               | Example              | Matches                                             |
|--------------------|----------------------|-----------------------------------------------------|
| words side by side | `deprecated api`     | documents containing both, anywhere in the document |
| a quoted phrase    | `"retention policy"` | the words adjacent, within one section              |
| a leading minus    | `api -deprecated`    | documents with the first and without the second     |
| `body:`            | `body:retention`     | the section body only, not its heading              |
| `heading:`         | `heading:retention`  | the section heading breadcrumb only                 |

### What words the documents use

{{% badge style="primary" title="Version" %}}0.0.4{{% /badge %}} `knowledge words` lists the vocabulary of the index,
which is every word the documents actually contain.

```nohighlight
$ fisk knowledge words              # the whole vocabulary
$ fisk knowledge words depre        # only words containing "depre"
$ fisk knowledge words '^depre'     # only words starting with it
```

The argument is a regular expression used to narrow the listing.

A short list is shown with its counts, since a short list is there to be compared:

```nohighlight
Word           Stem       As written   Any form
deprecation    deprec              9         17
deprecated     deprec              6         17
deprecate      deprec              2         17
```

`As written` counts documents holding that exact word. `Any form` counts documents holding any word sharing its stem,
which is the number `knowledge match <word>` reports.

A long list is shown as plain words several to a line, because a vocabulary runs to thousands of words and is scanned
for one rather than read row by row.

## Store location and layout

The index is project-local by default. It lives at `knowledge/<identity>` relative to the working directory, supporting
the one-project-per-directory workflow where an `agent.yaml`, a `memory/` directory, and a `knowledge/` directory sit
side by side. A store base relocates that default under it, and the `directory` field overrides the location outright.

> [!info] Warning
> The store uses SQLite WAL and its shared-memory sidecar, so every process must be on the same machine. Do not place the
> store on a network filesystem such as NFS or SMB.

## Serving over MCP

Both knowledge tools can be served over [MCP](../mcp/) as well as to the agent. Exposure is off by default and enabled
by naming them in an allowlist:

```yaml
expose:
  agent:
    mcp:
      port: 8080
      builtins:
        - knowledge_search
        - knowledge_enumerate
```

Name both. A client that can rank but cannot enumerate cannot tell an absent term from a low-scoring one, which is the
whole reason the second tool exists. See [MCP](../mcp/) for binding, ports, and the rest of the serving configuration.

## Security

The index holds the verbatim text of every indexed document, unencrypted on disk. The file and its sidecars are created
`0600` inside a `0700` directory.

- Retrieved chunks are framed as untrusted reference data and stripped of terminal control sequences before any TUI
  render, so indexed text cannot spoof the display or inject instructions.
- Embeddings secrets are supplied by environment-variable name and never logged, and are stripped from the environment of
  model-chosen command tools, so a tool cannot read the embeddings credential. The request timeout is enforced.
- Over MCP two gates apply, and both must pass: the tool itself declares whether it may ever be served over MCP, and the
  allowlist selects which of those this operator wants served. The allowlist can only narrow the tools declared servable,
  never widen past them, so a tool added alongside `knowledge_search` is not served on the strength of its neighbor's
  entry. That holds between the two knowledge tools themselves: allowlisting one never serves the other. Only the two
  read-only knowledge tools declare MCP exposure; no index or write path is reachable over MCP, and no built-in declares
  a2a exposure at all.
- `knowledge_enumerate` returns a complete set rather than a ranked sample, so a client that can reach it can inventory
  which documents mention which terms without reading any of them. That is less text than `knowledge_search` discloses
  per call and more structure. Both matter when deciding what to bind.