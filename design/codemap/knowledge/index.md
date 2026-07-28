# Knowledge

Knowledge gives an agent two tools over a locally built index of its own markdown: `knowledge_search`, which ranks, and
`knowledge_enumerate`, which does not. The design constraint that shapes everything else is that the whole feature ships
inside the single Fisk AI binary: no C toolchain at build time, no shared library at runtime, and no external database.

{{% notice style="note" title="Where it lives" %}}
`internal/rag` holds the store. Key files: `store.go` for the handle and schema, `index.go` for the write path,
`search.go` for the ranked read path, `enumerate.go` and `enumerate_query.go` for the set-valued one, `words.go` for the
vocabulary, `integrity.go` for the write-locked check and repair, `chunk.go` for chunking, `embed.go` for the embeddings
client, `watch.go`, `doctor.go`.
The tools are in `internal/toolkit/builtin/builtin_rag.go` and `builtin_rag_enumerate.go`, and the CLI in
`rag_command.go`, `rag_match.go` and `rag_words.go`.

The YAML key and user-facing noun are `knowledge`; the Go identifiers keep the `rag` prefix. Knowledge is the feature,
RAG is the technique.
{{% /notice %}}

## Why pure-Go SQLite is load-bearing

`store.go` blank-imports two packages: the `modernc.org/sqlite` driver and its `vec` subpackage, which registers the
`vec0` virtual-table module. Both come from one module that is a transpilation of upstream SQLite and sqlite-vec into Go.

{{% notice style="warning" title="Load-bearing decision" %}}
Because there is no cgo, `CGO_ENABLED=0` static builds and cross-compilation keep working, and sqlite-vec is linked in
rather than loaded as an extension. That removes an entire class of problems: no extension path to configure, no
`enable_load_extension`, and no version skew between binary and extension. Both tiers link in regardless of
configuration, so there is no build-tag matrix and no separate "vector build". A lexical-only deployment still links
`vec0`; it simply never creates the table.
{{% /notice %}}

FTS5 is compiled in for the same reason, which makes the `verifyFTS5` check a defensive guard against a surprising build
rather than an expected failure. It is nonetheless the one fatal check the doctor reports.

## Indexing

<ol class="cm-steps">
  <li><b>Open a writer</b> Build the embedder from config with no network call, create the directory at 0700, take the advisory lock, create the database at 0600 with no-follow, and enforce permissions on the database and both WAL sidecars.</li>
  <li><b>Preview the cost</b> On a first build or a reindex with the vector tier on, a dry pass reports how many chunks across how many files are about to be embedded. Nothing is embedded yet.</li>
  <li><b>Prepare the vector tier</b> Probe the embedding dimension once, reconcile against the stored manifest, and only then create the vector table and its delete trigger. This runs before any embedding spend.</li>
  <li><b>Walk each root</b> Checking the context between files so an interrupt is prompt across CPU-bound chunking.</li>
  <li><b>Classify by content hash</b> No row means add, an equal hash means skip, a different hash means update.</li>
  <li><b>Chunk, embed, then commit</b> The network call happens outside the transaction; only the upsert, purge, and insert are transactional.</li>
  <li><b>Reconcile orphans</b> Only on a full-corpus walk, and only when the walk saw at least one file.</li>
</ol>

The walk skips the store's own directory, any dotdir, and a sibling `memory` directory, through a predicate shared with
the watcher so the two agree on what the corpus is. Symlinks are skipped entirely, files over 512 KiB are skipped with a
note, and non-UTF-8 files are skipped.

An interrupted index is not an error. Already-embedded files are committed and skipped on the next run.

An equal-hash skip still adds the existing chunk count to the statistics, so the totals stay truthful. A reindex
deliberately bypasses the equal-hash short-circuit so a dry-run reindex reports the real work.

{{% notice style="warning" title="Load-bearing decision" %}}
Reconcile is double-guarded: deletion happens only on a full-corpus walk and only when that walk saw at least one file. A
walk that errored early can therefore never wipe the index.
{{% /notice %}}

## Chunking

The chunker is heading-aware and size-packed, and it is pure: no database, no IO.

- A **fenced code block** is collected whole and marked indivisible. Code is never split, even past the maximum chunk
  size, because splitting code hurts retrieval more than an oversized chunk does.
- A **heading** flushes the current section, updates the breadcrumb stack, and starts a fresh packer. The heading line
  itself is not added to the body; the breadcrumb carries it.
- A **paragraph** is the contiguous run of non-blank lines up to the next blank line, fence, or heading, which is what
  keeps a markdown table's rows together.

Blocks accumulate to a 1200-byte target and a 1500-byte maximum, and a divisible block over the maximum is hard-split.
A finished chunk carries its body and its breadcrumb as two separate fields and never folds one into the other. The two
are folded in exactly one place, where the chunk is handed to the embedder, so the section title still travels into the
vector and pulls the on-topic chunk up; the lexical index keeps them apart.

The chunk ordinal is its index within the document. That is exactly why citations shift when a file is edited.

## The schema

```sql
documents(id, path UNIQUE, title, mtime, hash)
chunks(id, document_id REFERENCES documents ON DELETE CASCADE, heading_path, ordinal, body)
chunks_fts        VIRTUAL USING fts5(body, heading_path,
                                     content='chunks', content_rowid='id',
                                     tokenize='porter unicode61')
chunks_fts_exact  VIRTUAL USING fts5(body, heading_path,
                                     content='chunks', content_rowid='id',
                                     tokenize='unicode61')
chunks_vocab      VIRTUAL USING fts5vocab('chunks_fts_exact', 'row')
chunks_vec        VIRTUAL USING vec0(chunk_id INTEGER PRIMARY KEY, embedding FLOAT[<dim>])
rag_meta(key PRIMARY KEY, value)
```

`chunks_fts` is an external-content table, so chunk text is stored exactly once and FTS5 holds only the index.
`heading_path` gets its own FTS column because a section title is often the most search-relevant phrase in a chunk.

`chunks.body` is the body alone. That is worth stating because it was not always so: the breadcrumb used to be stored
folded into the same column, which put the heading text in two columns at once and cost three things. No body-only
question could be asked, since every heading term was also a body term. A phrase could match across the join between a
heading and the body under it, matching text no document contains. And the breadcrumb was rendered twice by every
surface that prints both, and billed twice against the injected-token budget.

{{% notice style="warning" title="Load-bearing decision" %}}
Separating the columns removed an implicit ranking weight. While the breadcrumb sat inside the indexed body, heading
tokens were counted twice and BM25 weighted headings without anyone choosing to. `bm25(chunks_fts, 1.0, 2.0)` puts that
back as a decision rather than a side effect. Both `bm25()` calls in the search statement carry the weights, or the
`ORDER BY` and the returned score disagree, and a spec pins the result against rankings recorded before the split.
{{% /notice %}}

Foreign keys are on for every connection, so deleting a document cascades to chunks and, through triggers, to both the
full-text and vector indexes. The vector table is created only when the vector tier is on and only once the dimension is
known, so a lexical-only index has no vector table at all.

Three rules govern the sync triggers, and two of the three ways to break them corrupt silently. The hidden first column
of a command insert is the target table's own name, so a copy-paste that leaves the wrong name there writes into the
wrong index. Every indexed column must be supplied on a delete, with the `old` value, or terms are left behind against a
rowid that no longer exists and every later delete wedges `SQLITE_CORRUPT`; search hides exactly that, because hydration
drops rows it cannot join. And the FTS5 column names must equal the content-table column names: a mismatch still answers
`MATCH` and still passes a bare integrity check, failing only at `rebuild`.

That last one is why a bare `integrity-check` is not evidence. Only `('integrity-check', 1)`, the rank form, compares
the index against its content table, and that is the form the specs use, after an insert, an update and a delete.

Neither form is available to `knowledge doctor`. Both are command inserts, so both are writes, and every read-only
handle carries `query_only(1)`; a reader gets `attempt to write a readonly database`. Verifying an index against its
content table therefore needs the writer and its advisory lock, which `doctor` does not take.

### Two tokenizers over the same rows

FTS5 sets the tokenizer per table rather than per column, so a second tokenizer means a second table, not a third
column. `chunks_fts_exact` indexes the same rows through the same triggers and stems nothing.

Every query runs against the stemmed table. That is what makes an empty result mean "no document holds this word in any
form" rather than "no document spells it the way you typed it". The unstemmed table never answers a search; it earns its
size three other ways: it holds real words, so a vocabulary dump returns `deprecation` rather than `deprec`; it is the
only table where a prefix search grows monotonically; and it lets a stemmed count say how many of those documents
contain the word as it was written.

{{% notice style="warning" title="Load-bearing decision" %}}
The unstemmed table must not become the matcher. The idea is attractive, since it would make matching literal and would
let a prefix operator ship, and the cost is invisible without measurement. Measured over this repository's own
`docs/content`, 19 files and 389 chunks: of 20 word forms present in the index, **every one loses documents** without
the stemmer. Two drop to zero, and 17 more return strictly fewer. `tooling` matches all 19 documents under the stemmer
and none without it. A command whose output says "this is the complete answer, not a ranking cutoff" cannot be built on
that.
{{% /notice %}}

Prefix behavior is the other half, and it is why no `*` operator is offered against the stemmed table. Against an index
of stems, a prefix longer than the stem but shorter than the word matches nothing, so lengthening a query walks a result
set from non-empty to empty and back. Measured against a document containing `deprecated`:

| Prefix | `chunks_fts` (stemmed) | `chunks_fts_exact` |
|--------|------------------------|--------------------|
| `deprec*` | matches | matches |
| `depreca*` | **empty** | matches |
| `deprecat*` | **empty** | matches |
| `deprecate*` | matches | matches |

`chunks_vocab` is an `fts5vocab` table over the unstemmed index, exposing every term with the number of chunks holding
it. It costs no write time and no bytes of its own. It has to be created by the writer, because a read-only connection
cannot create a virtual table at all, in `main` or in `temp`; once created it is fully readable through every read-only
handle.

{{% notice style="warning" title="Load-bearing decision" %}}
Its `doc` column counts **chunks, not documents**. A word in five chunks of one document and a word in one chunk of each
of five documents both report `doc=5`. `knowledge words` therefore never displays it, never sorts on it and never
filters on it: every document count it shows comes from a `count(DISTINCT document_id)` query per word. Filtering or
ordering on the cheap number while displaying the expensive one produces a list whose own column contradicts the flags
that built it, and it is the kind of wrong that looks like a rendering bug rather than a counting one.
{{% /notice %}}

The exact per-word count is the only option on the read path, which was established by exhausting the alternatives. An
instance-form `fts5vocab` would give every word's true document frequency in one `GROUP BY`, but it cannot be created on
the read-only connection, nor in `temp`, because `query_only` blocks the temp database too, and the schema-qualified
three-argument form does not exist in this SQLite build, so the scratch-database trick `stemSurfaces` uses cannot be
pointed at an attached index either. Adding it to the schema would be a format generation bump and a reindex, for a scan
proportional to total tokens rather than to distinct words.

Narrowing the vocabulary scan is done in Go, against the whole list. SQLite has no `REGEXP`, and the driver's function
registration is process-global, so registering one would put it on the agent's connection too. Taking a literal prefix
from the pattern and range-scanning on it is worse than useless: `LiteralPrefix` describes a prefix of the *match*, not
of the *subject*, so under unanchored matching a scan from `ing` silently drops `testing`. Measured, the alternatives it
would have optimized cost about a third of a second across a five thousand word vocabulary, which is not worth a
silently incomplete answer from a command whose whole promise is completeness.

## The format gate

An index carries a format generation, and every open refuses any generation but this build's: a newer one so an older
binary never misreads a future layout, an older one because nothing migrates it today. The documents on disk are the
source of truth, so the index is rebuilt from them rather than upgraded in place.

Both refusals are the same shape, and the gate is deliberately the whole of the mechanism. Nothing drops and recreates
inside an open, and no writer destroys an index because a flag implied consent. The operator discards the index with
`knowledge reset --force` and rebuilds it with `knowledge index`, in that order, which is what the refusal says.

Rebuilding rather than migrating is a decision the absence of users pays for, not a permanent one. The gate is the
single place that would change: it is the one point that knows a generation is behind, so a migration added later hooks
in there and every open inherits it.

| Situation | Where it is refused | What the message names |
|-----------|---------------------|------------------------|
| Older generation | Every reader and every writer | `reset --force`, then `index` |
| Newer generation | Every reader and every writer | Upgrade fisk-ai; the index is not the thing that is wrong |

`reset --force` is the only path that can act on an index nothing can open, so it removes the file and its WAL sidecars
rather than clearing rows: with no handle there are no rows to clear. That path takes the same advisory write lock as
any writer, so it cannot race a live indexer, and refuses a symlink at any of the three paths rather than deleting
through it.

{{% notice style="warning" title="Load-bearing decision" %}}
Two checks, not one. `CREATE TABLE IF NOT EXISTS` silently no-ops against a table from an older layout, leaving the
stored schema untouched and failing much later as `no such column`, so the gate runs before it. The pinned generation
alone is not enough to catch that: a reset performed by an older build cleared the manifest but kept the table shape, so
there is no generation left to compare, and an unpinned manifest is also what a freshly created schema has. The column
shape of `chunks` settles those cases.
{{% /notice %}}

A spec asserts the named commands actually succeed against the state they are named for, since a message naming a fix
that also refuses is worse than one naming none.

The generation also covers changes that are invisible to every other check. Nothing in the manifest is a function of the
text handed to the embedder, so a chunker change combined with the equal-hash skip would otherwise leave unchanged
files' vectors computed from the old text and touched files' from the new, silently. Bumping the generation is what
forces the full re-embed.

## The pinned vector identity

`rag_meta` holds a manifest: format version, model, dimension, whether vectors are normalized, and both prefixes.
Together those are the vector identity, and mismatches are hard failures rather than degradations.

The invariant is enforced in three places for three reasons. On the write path before any embedding spend, so a changed
model does not cost money before it is refused. At open time for everything except dimension, so opening never contacts
the embeddings server. At query time for dimension only, against a live probe.

Adding the vector tier to a non-empty lexical-only index is also refused, rather than silently searching lexically
forever after an operator asked for hybrid.

Every mismatch message ends by naming the fix: reindex. A stale manifest yields garbage rankings, which is worse than a
refusal.

Normalization lives in exactly one place. The package L2-normalizes every vector regardless of which embedder produced
it, which is what lets the manifest pin normalization to true and lets the default L2 distance stand in for cosine
similarity.

## Search

<figure class="cm-diagram">
  <svg viewBox="0 0 760 330" role="img" aria-label="Hybrid search fusing BM25 and vector results by rank">
    <defs>
      <marker id="kn-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="143" width="130" height="54" rx="8"/>
    <text class="cm-svg-label" x="85" y="166" text-anchor="middle">query</text>
    <text class="cm-svg-sub" x="85" y="183" text-anchor="middle">sanitized</text>
    <rect x="230" y="50" width="210" height="54" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="335" y="73" text-anchor="middle" style="fill:var(--cm-accent)">FTS5 BM25</text>
    <text class="cm-svg-sub" x="335" y="90" text-anchor="middle">always on, fanout 50</text>
    <rect x="230" y="236" width="210" height="54" rx="8" fill="color-mix(in srgb, var(--cm-accent2) 14%, transparent)" stroke="var(--cm-accent2)"/>
    <text class="cm-svg-label" x="335" y="259" text-anchor="middle" style="fill:var(--cm-accent2)">vector KNN</text>
    <text class="cm-svg-sub" x="335" y="276" text-anchor="middle">optional, fanout 50</text>
    <path d="M150,170 L190,170 L190,77 L224,77" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#kn-ah)"/>
    <path d="M150,170 L190,170 L190,263 L224,263" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#kn-ah)"/>
    <rect x="520" y="143" width="200" height="54" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="620" y="166" text-anchor="middle" style="fill:var(--cm-accent)">RRF fusion</text>
    <text class="cm-svg-sub" x="620" y="183" text-anchor="middle">by rank, not by score</text>
    <path d="M440,77 L480,77 L480,170 L514,170" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#kn-ah)"/>
    <path d="M440,263 L480,263 L480,170 L514,170" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#kn-ah)"/>
    <text class="cm-svg-sub" x="620" y="222" text-anchor="middle">truncate to top_k, max 20</text>
    <text class="cm-svg-sub" x="620" y="237" text-anchor="middle">then hydrate into citations</text>
    <text class="cm-svg-sub" x="335" y="316" text-anchor="middle">an unreachable embeddings server degrades to lexical, and always says so</text>
  </svg>
  <figcaption>Fusion is on rank, so BM25 scores and L2 distances never have to be normalized against each other.</figcaption>
</figure>

The query is split on non-alphanumeric runs, terms under two runes are dropped, each term is double-quoted with embedded
quotes doubled, the list is clamped to 40 terms, and they are joined with `OR`. The quoting is both a syntax fix and an
injection guard. The `OR` is deliberate: the lexical tier supplies recall and the vector tier supplies precision.

Both retrievers over-fetch 50 results before truncation to at most 20, so a chunk that is strong in one list and deep in
the other still gets its rank boost before the cut.

Hydration is a single join re-ordered to the fused order. A row that vanished mid-flight, because a reindex was running,
is skipped rather than failing the search.

When the vector tier is off or degraded, no fusion runs at all and the result is straight BM25 order.

The active tier is never ambiguous. One canonical tier line is rendered by every ranking surface and included in the
tool's JSON, and a degraded query renders a distinct line naming the reason. Enumeration overrides it with a line of its
own and its machine-readable modes print none, which is covered below.

## Enumeration

Search answers "what do the documents say about this" and ranks. Enumeration answers "which documents mention this" and
does not rank: it returns a set. The two are different questions, and the second is the one a search cannot answer,
because a search that returns nothing means nothing scored well, which is not the same as nothing being there.

`Store.Enumerate` is the engine. Two surfaces reach it: the `knowledge match` CLI verb in `rag_match.go`, and the
`knowledge_enumerate` tool in `builtin_rag_enumerate.go`.

{{% notice style="warning" title="Load-bearing decision" %}}
`MATCH` is a predicate, so the set is complete by construction. No `bm25()` call takes part, nothing is truncated by
score, and the only limit is a display budget applied after the total is known. That is what lets an empty result be
reported as an answer rather than as a cutoff, and it is the property the whole feature is for.
{{% /notice %}}

{{% notice style="warning" title="Load-bearing decision" %}}
FTS5 booleans evaluate **within one row, which is one chunk**. A document with "retention" in one chunk and "policy" in
the next is found by each term alone and missed entirely by `"retention" AND "policy"`. Composition therefore happens in
Go over document-id sets, one query per term, intersected and subtracted. This is not an optimization detail: a command
that claims completeness cannot be built on chunk-scoped booleans, and a spec asserts the single-MATCH form misses the
document that the composed form finds.
{{% /notice %}}

The syntax is small and closed. Every form composes over sets rather than inside one expression:

| Form | Example | Matches |
|------|---------|---------|
| terms side by side | `deprecated api` | documents holding both, anywhere in the document |
| a quoted phrase | `"retention policy"` | the words adjacent, within one section |
| a leading minus | `api -deprecated` | documents holding the first and not the second |
| a field prefix | `heading:retention` | the section breadcrumb only |
| a field prefix | `body:retention` | the body only |

What is missing is missing deliberately, and each absence is enforced by name rather than passed through to FTS5:

| Typed | What happens | Instead |
|-------|--------------|---------|
| `OR`, `AND`, `NOT`, `NEAR` | rejected, naming the working form | terms side by side; a leading minus to exclude |
| `*` anywhere | rejected | matching is by word stem already |
| only exclusions | rejected | add a term to match |
| an unknown field | rejected, naming the two that exist | `body:` or `heading:` |

`OR` is the one that shows why rejecting by name matters. It is two runes, so it survives the minimum-term-length drop,
and an unguarded compiler turns `foo OR bar` into `"foo" AND "or" AND "bar"`, intersecting with every document
containing the word "or". A wrong answer, not an empty one, from the command whose contract is completeness. `*` is
refused for the reason in the tokenizer section: against an index of stems it is not monotonic.

Every token emitted into `MATCH` is double-quoted with internal quotes doubled, and the only unquoted characters this
package emits are its own operators and the two column names, which are constants it chooses rather than user text. The
doubling is load-bearing here rather than defensive, because a doubled quote inside a phrase is how FTS5 spells a
literal quote, so a term can legitimately contain the character that delimits it.

Per-document body and heading counts are reported separately, which the column split made computable. They are counted
as the OR of the query's terms within each column, not the AND: they are a statistic about how much a document is about
the query, so a document holding one term in one chunk and another in the next has to report both chunks rather than
the zero an AND would give it. A dropped term, one below the length floor, is named in the result rather than discarded,
since a term that was never queried makes the answer complete about a different question.

Each term is also reported against both indexes: how many documents hold it in any form, and how many hold it as
written. The stem is named too, and it comes from SQLite's own porter tokenizer, run over a scratch in-memory FTS5 table
rather than from a second implementation of the algorithm that could drift from the index it describes. The reader
cannot compute it in place, since it is query-only and cannot create a table; a failure there leaves the stem empty
rather than failing the query, because the stem explains the answer and the answer does not depend on it.

### The tool surface

`knowledge_enumerate` returns citations and counts, never document text. Reading is a separate call the model makes with
the citation, which keeps the decision to spend context on text apart from the decision to find out where the text is.
The list is bounded by a quarter of `max_injected_tokens` rather than a constant, because an operator who raised the
budget should get a longer list, and because enumeration precedes the retrieval it exists to inform: spending the whole
budget on filenames would crowd out the text the model reasons over. The budget floors at one document, since rounding a
real match down to an empty list would read as absence.

{{% notice style="warning" title="Load-bearing decision" %}}
The `note` field is never omitted. A model does not read the absence of a warning as a signal, so a complete set has to
say it is complete as loudly as a truncated one says it is not. The note also names any gap between what a term matched
and what is written, in both directions: a count above the literal one because stemming reached other forms, and a zero
that is a genuine absence. Without it the model has counts and no reading of them.
{{% /notice %}}

The tool declares `Expose: &functool.ExposeSpec{MCP: true}`, on the same terms as `knowledge_search`: read-only over the
operator's own index, needing no operator at a terminal. `config.mcpExposableBuiltins` names both, so an operator can
allowlist either or both.

{{% notice style="warning" title="Load-bearing decision" %}}
They are meant to be served together, and that is a note rather than a mechanism. `notePartialKnowledgeSet` tells an
operator who exposed one what the missing half costs their clients; it does not select the other for them. Making one
allowlist entry serve two tools would break the property the whole exposure design rests on, that selection narrows
capability and never widens it, and would silently invalidate the spec that guards it. A client served only
`knowledge_search` is exactly the case enumeration exists to fix, so the note is worth printing, but not at that price.
{{% /notice %}}

Having two servable tools in one group is what finally tests the per-tool filter properly. `mcpSelectedBuiltins` applies
the allowlist per tool rather than once as a boolean, so naming one knowledge tool serves precisely that one. Before
this the guard could only be exercised with a tool that was unservable anyway, which could not distinguish a working
filter from a filter that never mattered.

`RAGSystemNote` gains one routing sentence, not a syntax lesson: the model is told which question each tool answers, and
the tool's own description carries the syntax. Without the routing the model has two tools and no rule for choosing, and
the one it under-reaches for is the one that makes "no" safe to say.

## Soft states and hard failures

The split is consistent and worth stating plainly.

| Situation | Behavior |
|-----------|----------|
| No index file yet | A store opens with a nil database; searches report a status, not an error, so a first agent run still starts |
| Index exists but is empty | A status, not an error |
| Index built by an older or newer generation | A hard error on every path but the two that declare destruction |
| Embeddings server unreachable at query time | Degrade to lexical, set the reason, and report it |
| Embeddings dimension differs from the manifest | A hard error |
| Model or prefixes differ from the manifest | A hard error naming the reindex |
| Embeddings server unreachable at index time | A hard error, because that is when spend was about to happen |

A silent lexical fallback would be indistinguishable from "vectors did not help", which is why the reason is always
surfaced.

Enumeration carries its own status enum rather than reusing the search one, and the reason is the feature itself.
`SearchStatus` folds "the index holds nothing" and "the query reduced to nothing" into a single member, and those are
exactly the two states enumeration exists to tell apart: one says nothing about the documents, the other says nothing
about the query.

| Enumerate status | Means |
|------------------|-------|
| `ok` | The set is complete, and may be empty. An empty `ok` is the answer the feature exists to give |
| `index_not_built` | No index file yet, which is the ordinary first-run state for an agent |
| `corpus_empty` | The index holds no documents, so a zero says nothing about any document |
| `query_empty` | Every term was dropped before anything ran, so a zero is about the query |

A malformed query is an error rather than a status. It carries a fix, there is nothing to report about the index, and
the error is the compiler's own rather than a wrapped FTS5 one, which would quote the user's text back inside a parser's
wording.

## Embedding

The client is hand-rolled against an OpenAI-compatible endpoint, batching 64 inputs at a time. On any batch failure it
binary-splits down to single inputs while preserving order, so a server with a lower batch cap still succeeds.

Vectors are placed by each response object's index field, and the batch fails on an out-of-range index, a duplicate
index, an empty vector, or a count mismatch. That is what guarantees a vector can never land on the wrong chunk. An
error-shaped body returned with HTTP 200 is also treated as a failure.

Inputs are validated before sending: non-empty, valid UTF-8, and under 128 KiB. Responses are read through a 64 MiB
limit. A query is truncated to 8192 characters, backing off any partial trailing rune.

The dimension probe runs once per process under a mutex, because one embedder is shared across concurrent MCP calls, and
it refuses to cache an empty result so a broken server never pins a bogus dimension.

A non-loopback base URL must be https, so a query is never sent in cleartext. The API key is configured as the name of an
environment variable, never as the secret.

## Citations

The canonical form is `<relpath>#<ordinal>`, produced by one function and set during hydration. The path is exactly as
relative or absolute as the root that was indexed.

`knowledge show` parses a citation back and resolves it, and a miss produces a purpose-written hint that citations shift
after a reindex.

## Watching

The watcher prints its tier line from a read-only store first, deliberately, so it never contends with the writer locks
it is about to take.

Missing or unreadable roots are warned about and dropped, and it errors only if none remain. Every eligible directory is
watched, with a file root watched through its parent, and the same skip rules as the index walk apply. A watch-limit
error is translated into a single actionable warning about the inotify limit and the watcher keeps going.

The event loop is a single-timer debounce with a 100 ms floor. Indexing runs in its own goroutine so a long pass never
blocks event draining, which would overflow the kernel queue, and a dirty flag coalesces changes arriving mid-pass into
exactly one follow-up pass. A lock held by a competing writer becomes a warning and a retry.

Events under the store's own directory are rejected outright, which is what breaks the WAL feedback loop.

Deletions are applied stat-guarded: if the path exists again, the delete is skipped. That is what makes an editor's
atomic-save rename harmless.

One subtlety worth knowing: a reactive pass is not a targeted per-file ingest. It re-walks every root and relies on the
content-hash skip for cheapness. The collected event paths are used only for reporting and for the guarded deletes.

## Locking and platform splits

Two independent mechanisms solve two different problems. A single open connection serializes writes within the process.
An advisory file lock serializes writers across processes, which the connection limit cannot do because WAL lets multiple
processes open the same file.

On Unix the lock is a non-blocking `flock`, and a busy lock fails fast rather than interleaving writes under a timeout.
Because it is an flock, the operating system releases it if the process dies, so a crashed writer never wedges future
indexing. On Windows it is an exclusive-create lock file, and the comment states the trade-off plainly: it does not
auto-release on crash, so a stale lock may need removing by hand.

No-follow open is used at exactly two sites, the database and the lock file, so a symlink planted at either path cannot
redirect a write. Permissions are enforced on the database and both WAL sidecars, with any symlink among them refused,
because SQLite creates the sidecars honoring the umask and could leave them world-readable.

Reader connections are opened read-only by the driver and additionally set query-only as defense in depth.

## Performance choices

- Embedding happens outside the transaction, so the slow network call never holds the single writer slot.
- Hash-based incremental indexing means unchanged files cost nothing, which is also what makes an interrupted index cheap
  to resume and the watcher's full re-walk affordable.
- Purge-then-insert per document covers first ingest and update in one path, and the triggers clear ghost chunks when a
  file shrinks.
- The reader pool is kept tiny and short-lived so no pooled connection pins a WAL snapshot long enough to block
  checkpointing and grow the WAL unbounded across a long session.
- Auto-vacuum returns freed pages to the operating system after a removal or reindex. It only takes effect on a freshly
  created empty database, which is precisely why the writer must create the file itself and why that pragma is ordered
  before the journal mode.
- Everything is bounded: 512 KiB source files, 1200 and 1500 byte chunks, 40 query terms, 8192-character queries, 128 KiB
  embed inputs, 64 MiB responses, at most 20 results, and a default 6000-token injection budget.
- A second full-text index is the largest single line item in the store, and it buys tooling around retrieval rather
  than retrieval itself. It is paid at index time and on disk, never per query, since no search reads it. `prefix='2 3'`
  was measured and rejected: it helps only the two-to-three character band, does nothing at four or more, and costs
  again in both size and index time, where a compiler-enforced minimum prefix length costs nothing.
- Reset drops the schema and recreates it rather than deleting rows. It is the cheaper operation, it is the only one
  that works against a corrupt index, and it means the schema has exactly one definition instead of a per-table rebuild
  list that has to be kept in step with the table list forever.

The tool's own budget converts injected tokens to characters at four per token and stops adding hits once the budget
would be exceeded, but always includes at least the first hit so a large first chunk is not silently dropped to nothing.

## The doctor

`fisk knowledge doctor` renders the tier line as its header and then runs a series of checks, of which two are fatal:
FTS5 compiled in, and the search index matching the stored text. Store presence, journal mode, index writability, each
configured path, and the embeddings checks are all reported without failing the command.

The policy is deliberate: an absent or unreachable embeddings server is never fatal, so a lexical-only operator is never
told their setup is broken.

A check is three-valued rather than a boolean, because "did not run" is a real answer that neither of the other two can
carry. Reporting an unrun check as passing is the dishonesty the whole report exists to avoid; reporting it as failing
says something is broken when nothing is. `HasFatal` ignores an unrun check, since not knowing is not the same as
knowing something is wrong, and the report says in words that it verified less than it lists.

{{% notice style="warning" title="Load-bearing decision" %}}
The integrity check is the one that needs the write lock, and it does **not** go through `OpenWriter`. That function is
the index constructor: it creates the directory, creates the database file, sets persistent journal and vacuum modes,
and creates the whole schema. A diagnostic built on it would manufacture the index it was asked to inspect, and the very
check reporting "no index file" would report a built store on the next run. `withWriteAccess` takes the advisory lock
directly and opens a writable handle on a file that already exists, refusing when it does not.
{{% /notice %}}

Only the rank form of `integrity-check` detects the failure worth detecting. The bare form and the rank-0 form both pass
on an index that has drifted from its content table, and a drifted index answers `MATCH` with fewer rows than the corpus
holds, so every search silently under-reports while `knowledge match` keeps promising a complete set. That is why it is
worth taking a write lock for, and why it runs on every `doctor` invocation rather than behind a flag: a check that
guards the completeness contract and is off by default protects nobody.

It is cheap enough to be unconditional. Measured at 0.2s over 600 documents, 4,727 chunks and 11 MiB, against 0.02s for
the rest of a doctor run, and it leaves the database file's mtime alone, touching only the WAL sidecar, so what
`knowledge stats` reports as Modified does not move.

Everything that can prevent the check from running is reported as skipped, not failed: a read-only index file and a
read-only store directory are both supported deployments, a concurrent writer is ordinary, and none of them is a finding
about the index. SQLite reports the real failure as `database disk image is malformed`, which reads as a dying disk and
is not what happened, so it is translated into a sentence naming the stale table and the repair.

`knowledge rebuild` is that repair, and it is a separate verb rather than something the doctor offers. It rebuilds both
FTS indexes from the chunk text, leaving documents, text and vectors untouched, so nothing is re-embedded. The reason it
is not automatic is that it repairs a derived index over intact text and nothing else: given a damaged chunks table it
builds a consistent index over the damage, after which the check passes and reports nothing.

## Store directory resolution is a documented footgun

The directory is `knowledge.directory` or, by default, `knowledge/<identity>`. A relative result is rebased under the
store directory when one is set, and an absolute one is honored verbatim.

The agent and the CLI must pass the same base or they resolve to different directories. That is why `--store-dir` and
`FISK_AI_STORE_DIR` exist and are documented as distinct from `--state-dir`, and why the agent raises
`WarnKnowledgeIndexAbsent` when a base was given but no index is there.

## Security posture

Text is stored unencrypted at mode 0600 inside a 0700 directory, the same posture as memory. The package documentation
says outright not to index secrets.

Retrieved text is framed to the model as untrusted reference data rather than instructions, in the system note and in the
tool description alike. The model-supplied query is sanitized before it reaches an operator's terminal.

`chunks_vocab` deserves its own sentence, because it is the one object whose contents are broader than any query result.
It makes the full vocabulary of the corpus, with per-term frequencies, readable through every read-only handle,
including the agent's. That is acceptable only as long as nothing serves it: `knowledge words` is a CLI verb, which the
agent cannot reach, and that is the control rather than the table's own permissions.

The reason to keep it that way is not confidentiality. The same read-only handle already returns verbatim document text
through `knowledge_search`, so a vocabulary adds no new class of secret. What it adds is a discovery primitive.
Retrieval today requires guessing a word: `*` is refused outright, and the related-forms explanation names at most five
words sharing a stem, and only when the counts disagree. A vocabulary tool removes both limits at once and hands any
caller, including one following injected instructions, the identifiers that make the rest of the corpus searchable. It
is also, more prosaically, thousands of tokens of low signal for a model that asked a question.

## What is deliberately absent

Stating the scope matters as much as stating the features. Hybrid here means BM25 plus KNN plus reciprocal rank fusion,
and nothing else:

- No cross-encoder re-ranking.
- No diversification or per-document capping.
- No neighborhood expansion around a hit.
- No query expansion or rewriting.
- No snippet highlighting; the CLI simply truncates the line.
- No relevance threshold. Scores are internal and never exposed in the tool JSON or the CLI, and truncation to `top_k` is
  the only gate.
- Embedding is per file rather than per chunk. Any content change re-embeds every chunk of that file; there is no
  chunk-level embedding cache.

## Reserved and unused

- `IndexOptions.Extensions` is a real extension point that no caller sets, so the indexable extension set is not
  configurable from YAML or flags today.
- `documents.title` is computed and written on every upsert and read on no path at all, which stays true only because
  the enumerate result carries no title either: adding one would turn a written-and-never-read column into a read one,
  for output nothing renders. Chunk-level heading paths do all the work.
- `IndexStats.FirstBuild` is computed and read by nobody; both first-build previews check the document count themselves.
- `Hit.ChunkID` is populated and never read outside the package.
- `rag.Destroy` has one caller, `knowledge reset --force`, and only on the branch where the index could not be opened.
- `Meta.Normalized` is always written true and any index with it false is a mismatch. It documents the invariant rather
  than selecting a behavior.
- `Store.VectorEnabled()` and `Store.Dir()` have no in-repository callers.
- The search status enum has no degraded member; degradation is a separate flag and the status stays fine.

{{% notice style="tip" title="Next" %}}
[Serving: MCP and A2A]({{% relref "serving" %}}) covers `knowledge_search` in its other role, as the only built-in a
Fisk AI process will hand to an outside client.
{{% /notice %}}
