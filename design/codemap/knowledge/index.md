# Knowledge

Knowledge is the corpus the operator supplies. One SQLite file holds the documents, the lexical index and, when embeddings are configured, the vectors. The user-facing name is knowledge everywhere: the config block, the CLI command and the tool names. Go identifiers keep `rag`, because retrieval-augmented generation is the technique.

{{% notice style="note" title="Where it lives" %}}
`internal/rag` holds the store and every operation on it. Key files: `store.go` for the lifecycle and format gate, `index.go` for the corpus walk, `chunk.go`, `embed.go`, `search.go`, `enumerate.go`, `integrity.go`, `watch.go`. `internal/toolkit/builtin/builtin_rag.go` is the tool surface; `rag_command.go` is the CLI.
{{% /notice %}}

## The store

One file, `knowledge.db`, in the store directory. The driver is pure Go with no CGo, and the vector extension is linked in unconditionally so a lexical-only build is still one binary; the vector table is created only when embeddings are configured.

The file is created at mode 0600 before SQLite touches it, the directory at 0700, and the modes are re-asserted on the database and its write-ahead log and shared-memory sidecars afterwards. A symlink at any of those three paths is refused. Document text is stored verbatim and unencrypted, so a secret indexed is a secret on disk.

The schema is `documents`, `chunks`, two FTS5 tables, a vocabulary view and a metadata table. There are two FTS tables because FTS5 sets the tokenizer per table rather than per column: the porter-stemmed table answers every query, and the exact table exists to name the real words behind a stem and to be the one place a prefix search grows monotonically.

Chunk bodies and heading breadcrumbs are stored apart, so each is searchable without the other and a phrase cannot match across the join. Headings carry a 2.0 BM25 weight, set deliberately, where the old single table double-counted them by accident.

## Indexing

<ol class="cm-steps">
  <li><b>Plan the vector tier before writing anything</b> The live model's dimension is probed and reconciled with the pinned manifest first, so a reindex against an unreachable embeddings server leaves the index whole.</li>
  <li><b>Walk the roots</b> Skipping the store's own directory, dot directories, a sibling memory directory, symlinks, files over 512 KiB and anything that is not UTF-8.</li>
  <li><b>Classify by content hash</b> A file whose SHA-256 matches the stored hash is skipped with its chunk count carried forward. A reindex bypasses that short circuit.</li>
  <li><b>Chunk on headings, pack by size</b> A fenced code block is collected whole and never split. A heading flushes the section and updates the breadcrumb stack.</li>
  <li><b>Embed outside the write transaction</b> The slow network call never holds the single writer slot.</li>
  <li><b>Replace the document's chunks in one short transaction</b> Deleting the old rows fires the triggers that clear the FTS and vector rows, so a shrinking file leaves no ghosts.</li>
</ol>

Embedding requests batch sixty-four inputs and halve recursively on failure down to single inputs, preserving order. Responses are mapped strictly by each object's own index with a seen-set: a gap, a duplicate, an out-of-range index, an empty vector, a count mismatch or an error object inside a 200 body fails the whole batch, so a vector never lands on the wrong chunk.

Orphan reconciliation runs only when asked, and only when the walk saw at least one file, so an early walk error cannot wipe the index.

## Retrieval

<figure class="cm-diagram">
  <svg viewBox="0 0 760 260" role="img" aria-label="A query answered by a lexical tier and an optional vector tier fused by reciprocal rank">
    <defs>
      <marker id="rag-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="95" width="130" height="50" rx="8"/>
    <text class="cm-svg-label" x="85" y="117" text-anchor="middle">query</text>
    <text class="cm-svg-sub" x="85" y="134" text-anchor="middle">free text</text>
    <line x1="150" y1="112" x2="205" y2="66" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#rag-ah)"/>
    <line x1="150" y1="128" x2="205" y2="174" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#rag-ah)"/>
    <rect class="cm-svg-box" x="210" y="35" width="190" height="50" rx="8"/>
    <text class="cm-svg-label" x="305" y="57" text-anchor="middle">BM25 lexical</text>
    <text class="cm-svg-sub" x="305" y="74" text-anchor="middle">always on</text>
    <rect class="cm-svg-box" x="210" y="155" width="190" height="50" rx="8"/>
    <text class="cm-svg-label" x="305" y="177" text-anchor="middle">vector KNN</text>
    <text class="cm-svg-sub" x="305" y="194" text-anchor="middle">when embeddings set</text>
    <line x1="400" y1="66" x2="445" y2="108" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#rag-ah)"/>
    <line x1="400" y1="174" x2="445" y2="132" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#rag-ah)"/>
    <rect x="450" y="95" width="150" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="525" y="117" text-anchor="middle" style="fill:var(--cm-accent)">RRF fusion</text>
    <text class="cm-svg-sub" x="525" y="134" text-anchor="middle">rank, not score</text>
    <line x1="600" y1="120" x2="634" y2="120" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#rag-ah)"/>
    <rect class="cm-svg-box" x="640" y="95" width="100" height="50" rx="8"/>
    <text class="cm-svg-label" x="690" y="117" text-anchor="middle">hits</text>
    <text class="cm-svg-sub" x="690" y="134" text-anchor="middle">citations</text>
    <text class="cm-svg-sub" x="380" y="240" text-anchor="middle">an embeddings failure degrades to lexical and says which step failed</text>
  </svg>
  <figcaption>Fusion is on rank alone, so the two tiers never need their scores normalized against each other.</figcaption>
</figure>

Free text is reduced to OR-ed quoted terms: non-alphanumerics split, terms under two runes are dropped, internal quotes are doubled, and the list caps at forty. The lexical tier fans out fifty candidates. Reciprocal rank fusion breaks ties deterministically on chunk id, then the result is truncated to the requested count and hydrated in one join that preserves the fused order. A row that vanished between fusion and hydration, which a concurrent reindex can cause, is skipped rather than failing the search.

A citation is always the relative path and the chunk ordinal, and the same format is parsed back by `knowledge show`.

Degradation is classified by which step failed, never by an error's text, and a deadline or cancellation takes precedence over the failing step because a hung server is both. A dimension mismatch is a real error rather than a degrade, since the answer would be wrong rather than narrower.

## Enumeration is not ranking

`knowledge_enumerate` answers set membership: which documents hold these words. It exists because FTS5 booleans evaluate within a single row, and a row is one chunk, so `"retention" AND "policy"` misses a document holding one word in each of two chunks. Each term therefore runs as its own document-set query and the sets are intersected and subtracted in Go, holding only document ids.

The query language is small and closed: bare words, quoted phrases, a leading minus, and `heading:` or `body:` scoping. `OR`, `AND`, `NOT` and `NEAR` are rejected by name, because FTS5 would silently answer a different question; unguarded, `foo OR bar` compiled to `foo AND or AND bar`. A trailing `*` is rejected because a prefix query against a stemmed index can return fewer documents as the prefix grows.

Stems are computed with SQLite's own porter tokenizer in a scratch in-memory database rather than a second Go implementation that would drift from the index.

Body and heading match counts are kept apart, because a single combined count inverted the ranking. The matched total is recorded before the limit is applied, so a budget never hides the size of the answer.

## Honest status

The subsystem refuses to let an absence read as a result.

- `SearchStatus` separates an index that was never built from one that is empty.
- `EnumerateStatus` splits an empty corpus from an empty query, the two states the feature exists to tell apart.
- The doctor report has a third state for a check that did not run, because a report that marks an unrun check as passing is false.
- The enumerate tool emits its note on every call, since only a stated warning reaches the model.
- Terms dropped for being too short are reported rather than discarded quietly.
- `knowledge match --exit-code` is opt-in, because a complete empty answer is a successful answer.
- The machine-readable vocabulary modes error rather than print nothing on an unbuilt index, so a pipe cannot read "not built" as "empty".

{{% notice style="warning" title="Load-bearing decision" %}}
Retrieved text is data. The system note, the search tool description and the enumerate tool description each say that results are reference material the operator stored, never instructions, and that the paths they carry are data rather than targets for other tools. Everything from the corpus that reaches a terminal is sanitized and truncated, including vocabulary words, even though the current tokenizer emits only alphanumerics.
{{% /notice %}}

## Format, locking and integrity

The format version is pinned in the metadata table and both directions are refused with no migration path: too new says to upgrade, too old says to reset and rebuild. The gate compares the pinned version and, separately, the column shape and object list, because a shipped reset once emptied the metadata table without changing the table shape, leaving no pinned version to compare.

A cross-process advisory write lock is held for a writer's whole lifetime. On Unix it is a non-blocking exclusive `flock`, released by the kernel if the process dies, so a crash never wedges future indexing. On Windows it is an exclusive create, and a crash can leave a stale lock needing manual removal.

Writers set a single open connection, which is why every schema helper takes an executor and runs on the caller's transaction: a nested begin would deadlock waiting for the one connection the caller holds. Readers cap idle connections and their idle time so no pooled connection pins a snapshot and blocks checkpointing across a long agent session. A read-only store is safe to share across concurrent runs, and the fleet server does exactly that.

Reset drops tables rather than deleting rows. Against an index whose FTS no longer matches its content table, deleting rows fails because the cascade fires the delete trigger into the broken index, where dropping a table fires no row triggers.

The integrity check is a write, because FTS5 commands are inserts, and only the rank form catches drift: the bare and rank-zero forms pass on an index that has already drifted. Rebuilding re-derives both indexes from the chunks table and re-embeds nothing. It stays an operator verb rather than an automatic repair, because against a corrupt content table it would build a consistent index over the corruption and the check would then pass.

## Watching

Indexing runs on its own goroutine so a long pass never blocks event draining and overflows the kernel queue, and a dirty flag coalesces changes arriving during a pass into exactly one follow-up run. Events under the store's own directory are ignored, since the write-ahead log and lock sidecars are written by the index pass itself.

Pending deletions are applied stat-guarded, so an editor's atomic save, a transient rename the index pass has already re-added, does not drop a live file. Watch-descriptor exhaustion becomes one actionable warning naming the kernel limit rather than a fatal error.

## Budget and exposure

The injection budget is the configured token count times four characters. Adding hits stops once the budget would be exceeded, but the first hit is always included, so a large first chunk is not silently dropped to nothing. Enumeration gets a quarter of that budget, because it is a pre-check that has to leave room for the retrieval that follows, with a floor of one document so a rounding to zero never reads as absence.

`knowledge_search` and `knowledge_enumerate` are the only MCP-exposable built-ins, gated per tool against the operator's allowlist, so adding a tool to the knowledge set can never widen reach without a config change. Exposing only one of the pair is legal, and the run warns what a caller loses: set membership with no way to read the text, or retrieval with no way to check completeness.

## Reserved

The `documents.title` column is written on every upsert and read by nothing; the matched-document type has no title field, so the write-only column stays unread. There is no migration path at all, by design. Indexing, the doctor and the watcher open no telemetry spans; only search, enumerate and the embedding batch do.

{{% notice style="tip" title="Next" %}}
Knowledge is what the operator supplies. For what the model writes, see [Memory]({{% relref "memory" %}}). For the surfaces that expose these tools, see [Serving]({{% relref "serving" %}}).
{{% /notice %}}
