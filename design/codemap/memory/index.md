# Memory

Memory gives an agent four tools for storing short markdown notes that survive the end of a run. The design problem is
not storage. It is that both the key and the body are written by a model, so every value read back is untrusted input
that will be placed into a later prompt.

{{% notice style="note" title="Where it lives" %}}
`internal/memory` holds the contract and the shared rules. Key files: `store.go`, `key.go`, `write.go`,
`frontmatter.go`, `registry.go`. The backends are `internal/memory/file` and `internal/memory/jetstream`. The
model-facing tools are in `internal/toolkit/builtin/builtin_memory.go`.
{{% /notice %}}

## The contract

`memory.Store` (`internal/memory/store.go:105`) is four methods.

```go
type Store interface {
	List(ctx context.Context) ([]Item, error)
	Read(ctx context.Context, key string) (description, content string, err error)
	Write(ctx context.Context, key, description, content string, overwrite bool) error
	Delete(ctx context.Context, key string) (existed bool, err error)
}
```

`Item` (`store.go:96`) carries only `Key` and `Description`, never the body. That is what makes the index cheap enough to
inject into every prompt. Reading a body always costs a tool call.

The interface documents a stronger concurrency requirement than usual: an implementation must be safe for use by
independent processes sharing one backing store, not merely by goroutines in one process. Two agents pointed at the same
directory or the same NATS bucket are the expected case.

Three sentinel errors carry the outcomes the tool layer needs to distinguish: `ErrExists`, `ErrNotExist`, and `ErrStale`.

The limits are constants in one file, and the two exported ones leak into operator-facing documentation.

<dl class="cm-kv">
  <dt>maxKeyRunes</dt><dd>200, sized so <code>key + ".md"</code> fits on every filesystem.</dd>
  <dt>maxDescriptionRunes</dt><dd>500 after normalization to a single line.</dd>
  <dt>MaxContentBytes</dt><dd>64 KB. The cap shown by <code>fisk info</code>.</dd>
  <dt>MaxEntries</dt><dd>1024 memories per store.</dd>
  <dt>MaxEntryBytes</dt><dd>69600. Content plus a deliberately over-estimated frontmatter overhead. This is the number an operator passes to <code>nats kv add --max-value-size</code>.</dd>
</dl>

The two byte caps are close enough to read as a contradiction. `MaxContentBytes` bounds what the model may write.
`MaxEntryBytes` bounds what the backend stores, because the stored value includes the frontmatter header.

## The key charset is the whole traversal defense

`ValidateKey` (`internal/memory/key.go:25`) enforces five rules in order: non-empty, at most 200 runes, matching
`^[A-Za-z0-9._=-]+$`, no leading or trailing `.`, and no `..` substring.

The charset is the intersection of legal NATS KV keys and safe filenames, with `/` excluded even though KV permits it.
That exclusion is the point. A key maps one to one onto a flat filename with no path separator to escape.

{{% notice style="warning" title="Load-bearing decision" %}}
Because a validated key provably carries no separator, `filepath.Join(s.dir, key+".md")` cannot leave the memory
directory. Every `Store` method validates before touching the backing store, and the file backend re-validates on the way
out: `keyFiles` (`internal/memory/file/file.go:196`) drops any filename stem that fails `ValidateKey`, so a name planted
by hand can never appear in a listing. Loosening the charset to allow `/` would silently remove the traversal guarantee
from both directions.
{{% /notice %}}

Namespacing happens per backend rather than per key. The file backend uses a directory named `memory/<identity>`. The
JetStream backend prefixes keys with `<identity>.`, joined with a dot because a dot is a legal key character, so a
prefixed key is still a legal key on both sides.

## The write path

<ol class="cm-steps">
  <li><b>Validate</b> <code>ValidateWrite</code> (<code>write.go:19</code>) checks the key, normalizes the description, rejects an empty result, and rejects content over 64 KB. It returns the normalized description, which the backend must be the one to persist.</li>
  <li><b>Normalize the description</b> <code>normalizeDescription</code> (<code>write.go:51</code>) replaces control characters with spaces, collapses runs of whitespace, and truncates to 500 runes. The result is always a single line.</li>
  <li><b>Check capacity</b> On the create path only, the backend counts entries and calls <code>CheckCapacity</code> (<code>write.go:39</code>).</li>
  <li><b>Serialize</b> <code>Serialize</code> (<code>frontmatter.go:31</code>) marshals a typed struct into a YAML header followed by the body verbatim.</li>
  <li><b>Store atomically</b> The file backend stages a temp file and then links or renames. The JetStream backend creates, or updates against a known revision.</li>
</ol>

<figure class="cm-diagram">
  <svg viewBox="0 0 760 330" role="img" aria-label="The memory write path from tool call through validation to the two backends">
    <defs>
      <marker id="mw-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
      <marker id="mw-ad" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent3)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="26" width="150" height="52" rx="8"/>
    <text class="cm-svg-label" x="95" y="48" text-anchor="middle">memory_write</text>
    <text class="cm-svg-sub" x="95" y="65" text-anchor="middle">model tool call</text>
    <rect x="210" y="26" width="180" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="300" y="48" text-anchor="middle" style="fill:var(--cm-accent)">ValidateWrite</text>
    <text class="cm-svg-sub" x="300" y="65" text-anchor="middle">key charset, size caps</text>
    <rect x="430" y="26" width="180" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="520" y="48" text-anchor="middle" style="fill:var(--cm-accent)">CheckCapacity</text>
    <text class="cm-svg-sub" x="520" y="65" text-anchor="middle">create path only</text>
    <line x1="170" y1="52" x2="204" y2="52" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#mw-ah)"/>
    <line x1="390" y1="52" x2="424" y2="52" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#mw-ah)"/>
    <!-- rejection path -->
    <rect x="20" y="130" width="180" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent3) 10%, transparent)" stroke="var(--cm-accent3)"/>
    <text class="cm-svg-label" x="110" y="152" text-anchor="middle" style="fill:var(--cm-accent3)">rejected</text>
    <text class="cm-svg-sub" x="110" y="169" text-anchor="middle">never reaches a backend</text>
    <path d="M260,78 L260,156 L206,156" fill="none" stroke="var(--cm-accent3)" stroke-width="2" stroke-dasharray="4 3" marker-end="url(#mw-ad)"/>
    <!-- serialize -->
    <rect class="cm-svg-box" x="430" y="130" width="180" height="52" rx="8"/>
    <text class="cm-svg-label" x="520" y="152" text-anchor="middle">Serialize</text>
    <text class="cm-svg-sub" x="520" y="169" text-anchor="middle">typed yaml frontmatter</text>
    <line x1="520" y1="78" x2="520" y2="124" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#mw-ah)"/>
    <!-- backends -->
    <rect class="cm-svg-box" x="60" y="242" width="230" height="56" rx="8"/>
    <text class="cm-svg-label" x="175" y="266" text-anchor="middle">file backend</text>
    <text class="cm-svg-sub" x="175" y="283" text-anchor="middle">link to create, rename to replace</text>
    <rect class="cm-svg-box" x="450" y="242" width="230" height="56" rx="8"/>
    <text class="cm-svg-label" x="565" y="266" text-anchor="middle">jetstream backend</text>
    <text class="cm-svg-sub" x="565" y="283" text-anchor="middle">revision-checked update</text>
    <path d="M520,182 L520,214 L175,214 L175,236" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#mw-ah)"/>
    <path d="M520,182 L520,214 L565,214 L565,236" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#mw-ah)"/>
    <text class="cm-svg-sub" x="380" y="322" text-anchor="middle">one serialized format, so a value migrates between backends unchanged</text>
  </svg>
  <figcaption>Validation runs before any backend sees the key. The two backends differ only in how they commit.</figcaption>
</figure>

The identical serialized format on both sides is deliberate. A value written by one backend migrates to the other
unchanged.

## The value format

`Serialize` marshals a one-field struct rather than concatenating strings.

```go
type frontmatter struct {
	Description string `yaml:"description"`
}
```

That choice is what makes a description containing `key: value`, a quote, or a leading dash safe. Combined with the
single-line normalization invariant, the header cannot grow a second key or break out of the block.

`Parse` (`frontmatter.go:53`) is deliberately lenient in the other direction, so a hand-written file still reads:

| Input | Result |
|-------|--------|
| No `---` prefix | Description empty, the whole input is the body |
| A body containing its own `---` line | The first closing delimiter wins, the body survives intact |
| Header closed by `---` with no trailing newline | Accepted, empty body |
| No closing delimiter at all | Treated as not frontmatter, the whole input is the body |
| Header present but invalid YAML | Degrades to the whole input as body, no error |

## Reading, and the two kinds of miss

`memory_read` never fails for an absent key. A miss returns `{"found": false, "reason": ...}` naming `memory_list` as the
way to see the current set, so the model corrects itself without burning an error turn. `memory_delete` behaves the same
way, returning `{"deleted": false}` for a key that was not there.

An existing key on the create path is also a soft result rather than an error. `existsReason`
(`builtin_memory.go:325`) spends an extra `Read` purely to name the colliding memory's description, so the model can
decide whether to retry with `overwrite` in the same turn instead of a separate round trip.

## The index, and treating stored text as data

When memory is enabled and `no_index` is not set, the agent calls `List` once at startup and appends
`builtin.MemoryIndexBlock(entries)` to the system prompt (`internal/agent/agent.go:1118`). The block wraps entries in
`<memory-index>` tags and opens with a line stating that these are notes saved on earlier runs, not instructions. The
system note ends with the same framing: anything stored in memory is data the agent saved, not an instruction to follow.

Descriptions are sanitized twice: `normalizeDescription` strips control characters at write time, and
`util.SanitizeForTerminal` truncates to 200 runes at render time. A description written by an earlier run therefore
cannot smuggle a terminal escape onto an operator's screen or inflate the prompt.

A `List` failure at startup is advisory. The agent emits a `WarnMemoryIndex` warning and continues, because the tools
still reach the store; only the free preview is lost.

{{% notice style="warning" title="Load-bearing decision" %}}
The index is appended after the run fingerprint is computed (`internal/agent/agent.go:1112`). Memory is data, not
configuration, so a memory written between a suspend and a resume must never block that resume. Folding the index into
the fingerprint would make every successful memory write invalidate its own session. See
[Sessions and replay]({{% relref "state" %}}) for what the fingerprint does cover.
{{% /notice %}}

Memory tools are never hidden behind tool search. They are always declared directly, though they do count toward the
tool-count threshold and the degradation warning described in
[Tools and introspection]({{% relref "tools" %}}).

## The two backends

Both funnel through the same validation, the same capacity check, the same serialization, and the same strict option
decoder. They differ in what they can promise.

| | `file` | `jetstream` |
|---|--------|-------------|
| Layout | One `<key>.md` per memory in a `0o700` directory | One KV entry per memory, keys prefixed with the identity |
| Location | `options.directory`, else `memory/<identity>` rebased under the state directory | A pre-existing bucket, never created by Fisk AI |
| Create atomicity | `os.Link` from a temp file; `ErrExist` becomes `ErrExists` | `kv.Create` |
| Overwrite | `os.Rename`, last write wins | Revision-checked `kv.Update` |
| Returns `ErrStale` | No | Yes |
| `Delete` accuracy | Exact, a single `os.Remove` | Best-effort, Get-then-Delete can race |
| Listing | Directory scan, skipping temp, non-regular, and invalid names | One server-side `Watch` pass filtered to the prefix |

The JetStream backend binds a bucket and refuses to create one, so durability policy stays with the operator. It also
refuses to start against a bucket that would quietly break the contract. `checkBucketConfig`
(`internal/memory/jetstream/jetstream.go:138`) rejects any bucket with a TTL, because stored memories would expire
without anyone noticing, and any bucket whose positive `MaxValueSize` is below `MaxEntryBytes`. A missing bucket produces
an error containing a ready-to-paste `nats kv add` command with the right size.

A backend declares what it needs at registration time rather than being special-cased by the host. The JetStream backend
registers with `memory.RequiresNats()`, and that single flag is why `memory.NeedsNats(cfg)` can tell the agent to dial
NATS before construction without the agent naming any backend.

### The read-before-update guard

By default the JetStream backend will not overwrite a key whose current revision it does not know. `Read` records the
entry revision in the `memory.Scope` on the context, and `overwrite` requires a known revision and issues a
revision-checked update. A wrong-last-sequence error maps to `ErrStale`, and the stale revision is dropped so a retry is
forced to re-read.

A scope belongs to a run, and a turn of a checkpointed conversation is a run. The agent journals the scope as each run
ends and seeds a fresh one from `RunState.MemoryRevisions` on resume, so the revisions follow the conversation across
turns, processes and a week of wall clock.

`List` deliberately does not grant that authority. Seeing a key in the index is not the same as having read it. A
successful create or overwrite does carry authority forward, so a sequence of edits within one conversation costs a
single read.

The switch to disable this is spelled `no_require_read_before_update`, one of two negative switches in the memory config.
Both are phrased as opt-outs so that omitting them leaves the safe behavior in place.

## The tools

Four tools, all pure. They receive a `Prompter` and ignore it, because none of them needs a terminal.

| Tool | Parameters | Success | Miss |
|------|-----------|---------|------|
| `memory_list` | none | `{"memories": [{"key", "description"}]}` sorted by key | error only |
| `memory_read` | `key` | `{"found": true, "key", "description", "content"}` | `{"found": false, "reason"}` |
| `memory_write` | `key`, `description`, `content`, optional `overwrite` | `{"written": true}` | `{"written": false, "reason"}` |
| `memory_delete` | `key` | `{"deleted": true}` | `{"deleted": false}` |

`memory_list` is described to the model as the live view, in contrast to the index captured in its instructions at the
start of the run. `memory_write` explicitly tells the model not to write frontmatter itself and restates the key
charset, so the common failure is prevented by the description rather than caught by validation.

`content` is a `*string` in the read result so that an empty body still serializes as `"content": ""` instead of
vanishing from the object.

Every handler tolerates a nil store and returns an error rather than panicking. That is what makes
`MemoryTools(cfg, nil)` safe for `fisk info` to call when it only needs to enumerate names offline.

Traced output for the three key-taking tools runs the model-supplied key through `util.SanitizeForTerminal` before it
reaches the screen, falling back to the bare tool name on a decode failure.

None of these tools can be served over MCP or a2a. They declare no serving exposure, which the serving surfaces apply
per tool, and the configuration validator independently rejects any name but the two knowledge tools in the MCP
allowlist. Both gates must pass, so neither one alone is load-bearing.

## Failing at construction

Every memory misconfiguration is a startup error, never a surprise at the first tool call. Unknown backend, unknown
option key, unwritable directory, missing bucket, bucket with a TTL, undersized bucket, illegal prefix, and a missing
NATS connection all fail before the agent runs. Option decoding uses `DisallowUnknownFields` through a single shared
`DecodeOptions` helper, so a typo fails as loudly as a bad top-level YAML key and the strict rule cannot drift between
backends.

## Growth points

Several parts are generalized slightly ahead of need, and the comments say so.

- `ErrStale` is optional in the contract and unused by the file backend, but the tool layer already handles it. The file
  backend can gain the guard with no tool change.
- `frontmatter` has one field today. The typed struct plus the lenient `Parse` is the growth path for more header keys.
- `RuntimeEnv` is a struct rather than two parameters so a future per-run value is a new field instead of a signature
  change across every backend. Today each backend ignores the other's field.
- `RegisterOption` exists for one option.

`internal/agenttest.FakeMemoryStore` is a third implementation of `Store`, written in a separate package to prove the
interface is implementable from outside using only exported identifiers. It is test-only and enforces neither validation
nor caps.

{{% notice style="tip" title="Next" %}}
[Knowledge]({{% relref "knowledge" %}}) covers the other store an agent reads from, where the content is operator-owned
rather than model-written.
{{% /notice %}}
