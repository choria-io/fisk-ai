# Architecture

Fisk AI is a single Go module with one binary. The layering is strict enough that reading any one package rarely requires
reading its callers, and the recurring patterns are consistent enough that learning them once covers most of the tree.

## Layers

<figure class="cm-diagram">
  <svg viewBox="0 0 760 384" role="img" aria-label="Package layering from the config package up to the CLI">
    <rect class="cm-svg-box" x="20" y="20" width="720" height="56" rx="8"/>
    <text class="cm-svg-label" x="380" y="44" text-anchor="middle">main: run, session, info, knowledge, mcp, a2a, discover</text>
    <text class="cm-svg-sub" x="380" y="61" text-anchor="middle">owns flags, signals, and every byte of terminal wording</text>
    <rect x="20" y="92" width="720" height="56" rx="8" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="116" text-anchor="middle" style="fill:var(--cm-accent)">internal/agent</text>
    <text class="cm-svg-sub" x="380" y="133" text-anchor="middle">the only package that composes all the others</text>
    <rect class="cm-svg-box" x="20" y="164" width="124" height="56" rx="8"/>
    <text class="cm-svg-label" x="82" y="188" text-anchor="middle">memory</text>
    <text class="cm-svg-sub" x="82" y="205" text-anchor="middle">notes</text>
    <rect class="cm-svg-box" x="154" y="164" width="124" height="56" rx="8"/>
    <text class="cm-svg-label" x="216" y="188" text-anchor="middle">runstate</text>
    <text class="cm-svg-sub" x="216" y="205" text-anchor="middle">journal</text>
    <rect class="cm-svg-box" x="288" y="164" width="124" height="56" rx="8"/>
    <text class="cm-svg-label" x="350" y="188" text-anchor="middle">rag</text>
    <text class="cm-svg-sub" x="350" y="205" text-anchor="middle">index</text>
    <rect class="cm-svg-box" x="422" y="164" width="124" height="56" rx="8"/>
    <text class="cm-svg-label" x="484" y="188" text-anchor="middle">a2a</text>
    <text class="cm-svg-sub" x="484" y="205" text-anchor="middle">peers</text>
    <rect class="cm-svg-box" x="556" y="164" width="184" height="56" rx="8"/>
    <text class="cm-svg-label" x="648" y="188" text-anchor="middle">mcpserver, tui</text>
    <text class="cm-svg-sub" x="648" y="205" text-anchor="middle">surfaces</text>
    <rect x="20" y="236" width="232" height="56" rx="8" fill="color-mix(in srgb, var(--cm-accent2) 14%, transparent)" stroke="var(--cm-accent2)"/>
    <text class="cm-svg-label" x="136" y="260" text-anchor="middle" style="fill:var(--cm-accent2)">internal/llm</text>
    <text class="cm-svg-sub" x="136" y="277" text-anchor="middle">neutral message model</text>
    <rect x="264" y="236" width="232" height="56" rx="8" fill="color-mix(in srgb, var(--cm-accent2) 14%, transparent)" stroke="var(--cm-accent2)"/>
    <text class="cm-svg-label" x="380" y="260" text-anchor="middle" style="fill:var(--cm-accent2)">internal/toolkit</text>
    <text class="cm-svg-sub" x="380" y="277" text-anchor="middle">tool contracts</text>
    <rect x="508" y="236" width="232" height="56" rx="8" fill="color-mix(in srgb, var(--cm-accent2) 14%, transparent)" stroke="var(--cm-accent2)"/>
    <text class="cm-svg-label" x="624" y="260" text-anchor="middle" style="fill:var(--cm-accent2)">internal/util</text>
    <text class="cm-svg-sub" x="624" y="277" text-anchor="middle">shared primitives</text>
    <rect class="cm-svg-box" x="20" y="308" width="720" height="56" rx="8"/>
    <text class="cm-svg-label" x="380" y="332" text-anchor="middle">config</text>
    <text class="cm-svg-sub" x="380" y="349" text-anchor="middle">pure data, no IO beyond reading the file, imports no internal package</text>
  </svg>
  <figcaption>Each band imports only from bands below it. `config` sits at the bottom precisely so everything can read it.</figcaption>
</figure>

Two placements are worth calling out.

`config` is the lowest layer and imports nothing internal, which is why it can define constants that other packages need
to agree on. The knowledge tool's name lives there so the MCP allowlist can be validated without importing the package
that implements the tool.

`internal/agent` is the only package that composes all the others. Its own doc states the boundary: it owns no CLI
concerns, so flags, signals, and terminal rendering stay with the caller. That is what makes it embeddable.

The subsystem band packages do not import each other. Memory does not know about knowledge, and the journal does not know
about tools. They meet only in the agent.

## Implementations are linked in, never named in code

Five things are selected by a string in the config file: the model provider, the A2A transport, the memory backend, the
session backend, and the tool kinds. All but the last use the same registry pattern.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 270" role="img" aria-label="A config string resolved through a registry populated by blank imports">
    <defs>
      <marker id="ar-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="30" width="210" height="54" rx="8"/>
    <text class="cm-svg-label" x="125" y="53" text-anchor="middle">config names it</text>
    <text class="cm-svg-sub" x="125" y="70" text-anchor="middle">harness.memory.backend</text>
    <rect x="290" y="30" width="190" height="54" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="385" y="53" text-anchor="middle" style="fill:var(--cm-accent)">registry lookup</text>
    <text class="cm-svg-sub" x="385" y="70" text-anchor="middle">by name only</text>
    <rect class="cm-svg-box" x="540" y="30" width="200" height="54" rx="8"/>
    <text class="cm-svg-label" x="640" y="53" text-anchor="middle">factory</text>
    <text class="cm-svg-sub" x="640" y="70" text-anchor="middle">fails at construction</text>
    <line x1="230" y1="57" x2="284" y2="57" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ar-ah)"/>
    <line x1="480" y1="57" x2="534" y2="57" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ar-ah)"/>
    <rect class="cm-svg-box" x="290" y="160" width="190" height="54" rx="8"/>
    <text class="cm-svg-label" x="385" y="183" text-anchor="middle">blank import</text>
    <text class="cm-svg-sub" x="385" y="200" text-anchor="middle">init registers itself</text>
    <line x1="385" y1="160" x2="385" y2="90" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ar-ah)"/>
    <text class="cm-svg-sub" x="640" y="180" text-anchor="middle">an unknown name lists</text>
    <text class="cm-svg-sub" x="640" y="195" text-anchor="middle">exactly what is linked in</text>
    <text class="cm-svg-sub" x="130" y="188" text-anchor="middle">no host code ever</text>
    <text class="cm-svg-sub" x="130" y="203" text-anchor="middle">names a backend</text>
  </svg>
  <figcaption>Every registry error message enumerates the linked implementations, which turns a forgotten import into a self-diagnosing failure.</figcaption>
</figure>

The blank imports all live in `internal/agent/agent.go`, each with an inline comment saying what it enables. Adding a
second implementation is a package plus one import line.

Every registry follows the same rules:

- `Register` panics on an empty name, a nil factory, or a duplicate, mirroring `database/sql.Register`. Each is a
  programming error resolvable at compile time.
- Factories decode their options strictly, with unknown fields rejected, so an operator's typo fails at run start.
- Construction failures are errors, so a missing bucket, an unwritable directory, or a misconfigured stream surfaces
  before the agent runs.
- A backend declares its own requirements rather than being special-cased. The JetStream memory and session backends
  register a "requires NATS" flag, which is the only reason the host knows to dial a connection without naming any
  backend.

## Invariants that repeat everywhere

Recognizing these once makes the rest of the tree read quickly.

<dl class="cm-kv">
  <dt>One flat tool namespace</dt><dd>Application, built-in, remote, and custom tools share one name set. A collision aborts the run rather than shadowing, because shadowing a confirm-gated command would strip its gate.</dd>
  <dt>Borrowed versus owned</dt><dd>Anything injected by a caller, whether a NATS connection, a store, a transport, or a provider, is used and never closed. Only what the run itself dialed or opened gets a deferred close. The contract is restated at every field and every use site.</dd>
  <dt>Fail at construction</dt><dd>Unknown backend, unknown option key, unwritable directory, missing bucket, bad stream config, illegal prefix, unreachable remote host. All are startup errors, never a surprise at the first tool call.</dd>
  <dt>Determinism for resume</dt><dd>Anything hashed into the run fingerprint must be byte-stable across restarts, which is why schemas are cloned rather than mutated, lists are sorted, and optional fields are emitted unconditionally.</dd>
  <dt>Data, not instructions</dt><dd>Memory entries and retrieved knowledge are wrapped and labeled as data the agent saved or found, never as instructions. The framing appears in the system note, the injected block, and the tool description.</dd>
  <dt>Bounded everything</dt><dd>Message sizes, output capture, source files, chunk sizes, query terms, batch sizes, response bodies, concurrency, and timeouts all have explicit constants with the reason in a comment.</dd>
  <dt>Soft states versus hard failures</dt><dd>A missing index, an absent memory, a declined confirmation, and a non-zero exit are all normal results. Only a harness failure is an error.</dd>
  <dt>Sanitize before truncate</dt><dd>Everything reaching an operator's terminal is stripped of escape sequences before any length cap, so a cut can never leave a dangling escape.</dd>
</dl>

## The single run goroutine

The agent spawns no goroutines of its own. Several design decisions rest on that and say so:

- The events sink needs no locking for a per-run consumer.
- The run statistics counters need no lock.
- The confirm gate is explicitly not concurrency-safe and must never be wired into the concurrent MCP path.
- Hooks run on that goroutine, in loop order.

The serving modes are the exception, and both bound their concurrency with a semaphore for the same stated reason: an
external caller has no iteration budget, so an ungated path could spawn unbounded concurrent commands.

## Three entry points, one selection

`run`, `mcp`, and `a2a` read the same file and share the same tool-selection path. What differs is which validation mode
applies and what happens to a confirm-gated tool.

| | `run` | `mcp` | `a2a` |
|---|-------|-------|-------|
| Needs a model and prompt | yes | no | no |
| Built-in tools | all enabled ones | `knowledge_search` only, if allowlisted | none declares a2a exposure |
| Confirm-gated tools | prompted locally | exposed and gated by elicitation | dropped entirely |
| Concurrency | one loop | semaphore | semaphore |
| Human reachable | yes, on a terminal | maybe, through the client | no |

## Where the trust boundaries are

Being specific about this is more useful than a general claim of safety.

The model is untrusted. It chooses tool names and arguments, and everything it produces is treated as data: arguments are
bounded to a schema and become argv with no shell, text is sanitized before display, and stored or retrieved text is
framed as data rather than instruction.

The wrapped application is trusted to run but not to behave. Its introspection output is size-capped and rejected if it
overflows, its process group is killed on cancel, and its environment is scrubbed of credentials by name.

A remote peer is untrusted at the protocol level. Messages are size-capped before decoding and schema-validated in both
directions, and a reply is only ever sent to the inbox the transport supplied, never to an address taken from the message.
Authentication and authorization are delegated entirely to NATS.

The operator is trusted. Custom tools, hooks, and an injected provider all run with the agent's own privileges, and the
source says so rather than implying a sandbox that does not exist.

{{% notice style="warning" title="Load-bearing decision" %}}
None of the execution paths is a sandbox, and the source repeatedly refuses to imply otherwise. The per-run working
directory is described as collision avoidance, not confinement. Credential scrubbing is name-based and cannot catch a
secret exported under an undeclared name. The reliable exclusion mechanism is `ai:deny`, which drops a command before any
filter or gate can reach it.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
[Configuration]({{% relref "configuration" %}}) is where a reader following the flow should go, since every entry point
starts there.
{{% /notice %}}
