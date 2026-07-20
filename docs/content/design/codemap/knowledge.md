+++
title = "Knowledge and RAG"
weight = 75
description = "A local, in-process document index the model can search, lexical by default and semantic on opt-in."
+++

Knowledge gives the model one tool, `knowledge_search`, over a locally built index of the operator's own markdown and text files, so it can ground an answer in project documentation instead of its training data. The whole retrieval system, index and search, lives in the one `fisk-ai` binary and one process. It is opt-in and off by default, agent-mode only, and served over MCP only through an explicit allowlist, the same posture as [Memory]({{% relref "memory" %}}).

{{% notice style="note" title="Where it lives" %}}
`internal/rag`: the SQLite store in `store.go`, chunking in `chunk.go`, indexing in `index.go`, hybrid search in `search.go`, the embeddings client in `embed.go`, health checks in `doctor.go`, vector helpers in `vec.go`, filesystem watching in `watch.go`, and the cross-process write lock in `lock_unix.go` and `lock_windows.go`. The model-facing `knowledge_search` tool is in `internal/toolkit/builtin/builtin_rag.go`; the `fisk-ai knowledge` CLI is in `rag_command.go` and `rag_watch.go`; the `harness.knowledge` schema is in `config/config.go`.
{{% /notice %}}

## Two tiers, one search

A search always runs the lexical tier and, when the vector tier is on, runs it too and fuses the two. The lexical tier is an FTS5/BM25 full-text index; it is always active when knowledge is enabled and needs no model, no server, and no per-query cost. The vector tier is a separate opt-in: adding an `embeddings` block turns it on, each chunk is embedded through a local OpenAI-compatible server, and a query fuses the lexical and vector rankings with Reciprocal Rank Fusion. Lexical matches on shared words; vector matches on meaning, so a query finds the right section even when it shares no keywords with it.

`Search` runs FTS5 first through `ftsSearch`, then, if an embedder is configured, embeds and normalizes the query and runs a KNN over the `sqlite-vec` table through `vecSearch`. Each retriever over-fetches `searchFanout` = 50 candidates, `rrf` fuses the two ranked lists with the constant `rrfK` = 60, and the top_k survivors are hydrated into cited hits. RRF fuses by rank, not score, so the incomparable scales of BM25 and L2 distance never need normalizing against each other.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 270" role="img" aria-label="A query runs FTS5 always and a vector KNN when the tier is on, RRF fuses both into cited hits, and an embeddings outage degrades to lexical only">
    <defs>
      <marker id="ks" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- query -->
    <rect class="cm-svg-box" x="14" y="112" width="112" height="50" rx="8"/>
    <text class="cm-svg-label" x="70" y="134" text-anchor="middle">query</text>
    <text class="cm-svg-sub"   x="70" y="151" text-anchor="middle">text terms</text>
    <!-- lexical tier -->
    <rect class="cm-svg-box" x="182" y="42" width="188" height="50" rx="8"/>
    <text class="cm-svg-label" x="276" y="64" text-anchor="middle">FTS5 / BM25</text>
    <text class="cm-svg-sub"   x="276" y="81" text-anchor="middle">lexical, always on</text>
    <!-- vector tier -->
    <rect class="cm-svg-box" x="182" y="182" width="188" height="50" rx="8"/>
    <text class="cm-svg-label" x="276" y="204" text-anchor="middle">vector KNN</text>
    <text class="cm-svg-sub"   x="276" y="221" text-anchor="middle">opt-in, sqlite-vec</text>
    <!-- rrf fuse (accent) -->
    <rect x="440" y="112" width="132" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="506" y="134" text-anchor="middle" style="fill:var(--cm-accent)">RRF fuse</text>
    <text class="cm-svg-sub"   x="506" y="151" text-anchor="middle">by rank, K=60</text>
    <!-- hits -->
    <rect class="cm-svg-box" x="632" y="112" width="114" height="50" rx="8"/>
    <text class="cm-svg-label" x="689" y="134" text-anchor="middle">top_k hits</text>
    <text class="cm-svg-sub"   x="689" y="151" text-anchor="middle">each cited</text>
    <!-- query fans out to both tiers -->
    <line x1="126" y1="128" x2="180" y2="74"  stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ks)"/>
    <line x1="126" y1="146" x2="180" y2="200" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ks)"/>
    <!-- both tiers into rrf -->
    <line x1="370" y1="70"  x2="438" y2="126" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ks)"/>
    <line x1="370" y1="204" x2="438" y2="150" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ks)"/>
    <!-- rrf to hits -->
    <line x1="572" y1="137" x2="630" y2="137" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ks)"/>
    <!-- degrade path: lexical straight to hits when vectors unreachable -->
    <path d="M370,50 C500,8 620,10 688,110" fill="none" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="5 3" marker-end="url(#ks)"/>
    <text class="cm-svg-sub" x="512" y="20" text-anchor="middle">embeddings unreachable: lexical only</text>
  </svg>
  <figcaption>Every query runs FTS5. When the vector tier is on, KNN runs too and RRF fuses both by rank. If the embeddings server is unreachable the query degrades to lexical and says so.</figcaption>
</figure>

## Building the index

`knowledge index` is the single writer. It is incremental and per-file: a file whose content hash is unchanged is skipped, a changed file is re-chunked, and a walk of a full configured root reconciles deletions. Only `.md`, `.markdown`, `.txt`, and `.text` files are walked, and the store directory and any `memory/` directory are always excluded. That exclusion rule is `shouldSkipDir`, shared by the index walk and the watcher so the two can never disagree about what the corpus is.

<ol class="cm-steps">
  <li><b>Walk and hash</b> Each eligible file's SHA-256 is compared to the stored document row; an unchanged hash skips the file, a new or changed one is queued (`ingestOne`, `index.go`).</li>
  <li><b>Chunk</b> `ChunkDocument` splits on ATX headings and packs blocks to roughly 1200 bytes, keeping fenced code blocks whole and folding the heading breadcrumb into each chunk (`chunk.go`).</li>
  <li><b>Embed outside the transaction</b> When the vector tier is on, `EmbedDocuments` runs and each vector is L2-normalized, before any write transaction opens.</li>
  <li><b>Upsert in one short transaction</b> The document, its chunks, and their vectors are written in a single `BeginTx` block; FTS5 triggers keep the full-text table in sync, and a foreign-key cascade clears old chunks first.</li>
</ol>

The embed step sits deliberately outside the transaction so a slow or hung embeddings call never holds the single writer slot open. The writer is serialized across processes by an advisory `flock` on Unix (`lock_unix.go`) and an `O_CREATE|O_EXCL` lock file on Windows (`lock_windows.go`), since WAL mode lets multiple processes open the file and `MaxOpenConns(1)` alone cannot serialize them.

## Watching for changes

`knowledge watch` keeps the index current while files change. It registers a `fsnotify` watch per directory, waits for changes to settle, then runs an ordinary incremental index pass. The reindex decision is not per-file: a pass is a full incremental walk, so correctness rests on the same content-hash skip path that `knowledge index` uses.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 230" role="img" aria-label="Filesystem events are debounced into a single index pass that takes and releases the writer lock, then returns to idle">
    <defs>
      <marker id="aw" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
      <marker id="awf" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/>
      </marker>
    </defs>
    <!-- fs events -->
    <rect class="cm-svg-box" x="20" y="76" width="132" height="52" rx="8"/>
    <text class="cm-svg-label" x="86" y="99"  text-anchor="middle">fs events</text>
    <text class="cm-svg-sub"   x="86" y="116" text-anchor="middle">per directory</text>
    <!-- debounce -->
    <rect class="cm-svg-box" x="200" y="76" width="140" height="52" rx="8"/>
    <text class="cm-svg-label" x="270" y="99"  text-anchor="middle">debounce</text>
    <text class="cm-svg-sub"   x="270" y="116" text-anchor="middle">trailing, 2s default</text>
    <!-- index pass (accent) -->
    <rect x="388" y="76" width="160" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="468" y="99"  text-anchor="middle" style="fill:var(--cm-accent)">index pass</text>
    <text class="cm-svg-sub"   x="468" y="116" text-anchor="middle">writer lock held</text>
    <!-- idle -->
    <rect class="cm-svg-box" x="596" y="76" width="144" height="52" rx="8"/>
    <text class="cm-svg-label" x="668" y="99"  text-anchor="middle">idle</text>
    <text class="cm-svg-sub"   x="668" y="116" text-anchor="middle">lock released</text>
    <!-- forward arrows -->
    <line x1="152" y1="102" x2="198" y2="102" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#aw)"/>
    <line x1="340" y1="102" x2="386" y2="102" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#aw)"/>
    <line x1="548" y1="102" x2="594" y2="102" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#aw)"/>
    <!-- coalesce loop back -->
    <path d="M468,128 L468,180 L270,180 L270,130" fill="none" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3" marker-end="url(#awf)"/>
    <text class="cm-svg-sub" x="369" y="198" text-anchor="middle">changes during a pass coalesce into one follow-up</text>
  </svg>
  <figcaption>The writer lock is taken and released per pass, not held for the life of the watcher, so `knowledge index` in another terminal is never blocked between changes.</figcaption>
</figure>

Two decisions carry the design. The watcher opens the writer store for the duration of a pass and closes it again, so a losing race for the lock is a warning and a retry on the next tick rather than a fatal error. And events under the store directory are rejected outright, because the index pass writes its own WAL and lock sidecars there and reacting to them would be a feedback loop.

Deletions are applied after a pass and are stat-guarded: a path that exists again is not deleted, so an editor's atomic save, which briefly looks like a rename, does not drop a live file from the index. A reactive pass never reconciles; it deletes per event.

## The knowledge_search tool

When knowledge is enabled the agent run path opens the store read-only and builds one built-in tool with `RAGTools`, tracked in its own slice beside the memory tools. A start-of-run system note tells the model the tool exists and to consult it before answering project-specific questions (`RAGSystemNote`). The tool takes a `query` and an optional `top_k`; the effective count is `min(requested or configured, 20)`, and the total returned text is capped at `max_injected_tokens` by `capHits` at roughly four characters per token.

Each result carries a citation token of the form `relpath#ordinal` plus the section's heading path. The same token is printed by `knowledge search` and `knowledge sources` and accepted verbatim by `knowledge show`, so a hit resolves back to its full text. When no index exists yet the tool returns a soft `index_not_built` status naming the fix rather than failing the run, so a missing index never bricks agent startup.

Over MCP the tool is served only when it is allowlisted in `expose.agent.mcp.builtins`, checked by `MCPExposesKnowledgeSearch`. `knowledge_search` is the one exposable built-in because it is read-only and needs no operator prompt; the memory and human-in-the-loop tools stay agent-only. The MCP path opens the same read-only store and closes it after the server stops.

{{% notice style="warning" title="Load-bearing decision" %}}
Transient and permanent failures are handled asymmetrically. An embeddings server that is unreachable at query time degrades to lexical and sets `Degraded` with a reason, so a search never fails just because the model is offline. A dimension, model, or prefix mismatch instead fails loud and demands a `--reindex`, because a silently wrong vector identity would return wrong rankings. An unreachable server at index time also fails loud, so an index asked to be semantic is never quietly built lexical-only.
{{% /notice %}}

{{% notice style="warning" title="Caveat" %}}
The index holds the verbatim text of every indexed document, unencrypted on disk. The file and its `-wal` and `-shm` sidecars are created `0600` inside a `0700` directory, opened with `O_NOFOLLOW` on Unix, and retrieved chunks are framed as untrusted reference data, not instructions. This is the memory posture: do not index secrets. The MCP server binds `127.0.0.1` by default, so reaching `knowledge_search` from another host takes a deliberate change to `expose.agent.mcp.address` or `--address`; add authentication before making one.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [The Terminal UI]({{% relref "terminal-ui" %}}) for how a run is presented to an operator.
{{% /notice %}}
