# Sessions and Resume

By default a run lives only in memory. With `--checkpoint` it is journaled to disk as an append-only record stream, so it can be suspended and resumed later, in a fresh process or on another machine. The journal is also the foundation for the durability guarantees the agent loop relies on.

{{% notice style="note" title="Where it lives" %}}
`internal/runstate` holds the backend-agnostic core: the record schema in `record.go`, the pure fold in `state.go`, the `Store`/`Journal` interfaces and the `New` factory in `store.go`, the backend registry in `registry.go`, the shared id and append validation in `validate.go`, and the resume guard in `fingerprint.go`. The file backend lives in its own `internal/runstate/file` package, with the backend and its options in `file.go` and per-run locking in `lock_unix.go` and `lock_other.go`. The transcript replay is `resume_replay.go`; the `session ls|show|rm` subcommands are in `session_command.go`.
{{% /notice %}}

## A run is a record stream

A run is an ordered sequence of self-describing records. A `MetaRecord` is always first at sequence one. Each model turn is an `AssistantRecord` holding a neutral `llm.Message`, each tool result a `ToolResultRecord` holding an `llm.ToolResultBlock`, each interactive follow-up a `UserRecord`, and the run ends with a single `TerminalRecord` carrying its reason. Because the records store the provider-neutral model described in [Providers and the Neutral Model]({{% relref "llm-providers" %}}), the journal names no vendor type.

The schema is at `Version = 3`, and `Fold` accepts that version only. This is a break rather than the usual additive bump: earlier journals hold the Anthropic wire form, which does not round-trip through neutral records, so they are refused rather than mis-folded. There is no converter.

The refusal is louder in some commands than others. `run --resume` and `session show` both fail with a message naming the unsupported version, but `session ls` folds each run to summarize it and skips any that fails, so an older journal does not appear in the listing at all. `session rm` never folds, so removing one still works.

`Fold` in `state.go` replays a record set into a resumable `RunState` with no IO. The state carries the committed conversation prefix, cumulative counters derived from the journal so they can never drift, the next iteration number, and any in-flight tool batch. It deliberately holds no client, credentials, or config; those are rebuilt on resume.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 224" role="img" aria-label="An append-only record stream from Meta to Terminal is folded into a resumable RunState">
    <defs>
      <marker id="sj" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- record stream -->
    <rect class="cm-svg-box" x="60" y="44" width="120" height="44" rx="8"/>
    <text class="cm-svg-label" x="120" y="66" text-anchor="middle">Meta</text>
    <text class="cm-svg-sub"   x="120" y="81" text-anchor="middle">seq 1</text>
    <rect class="cm-svg-box" x="190" y="44" width="120" height="44" rx="8"/>
    <text class="cm-svg-label" x="250" y="66" text-anchor="middle">Assistant</text>
    <text class="cm-svg-sub"   x="250" y="81" text-anchor="middle">LLM turn</text>
    <rect class="cm-svg-box" x="320" y="44" width="120" height="44" rx="8"/>
    <text class="cm-svg-label" x="380" y="66" text-anchor="middle">Result</text>
    <text class="cm-svg-sub"   x="380" y="81" text-anchor="middle">tool</text>
    <rect class="cm-svg-box" x="450" y="44" width="120" height="44" rx="8"/>
    <text class="cm-svg-label" x="510" y="66" text-anchor="middle">Result</text>
    <text class="cm-svg-sub"   x="510" y="81" text-anchor="middle">tool</text>
    <rect class="cm-svg-box" x="580" y="44" width="120" height="44" rx="8"/>
    <text class="cm-svg-label" x="640" y="66" text-anchor="middle">Terminal</text>
    <text class="cm-svg-sub"   x="640" y="81" text-anchor="middle">reason</text>
    <!-- record connectors -->
    <line x1="180" y1="66" x2="189" y2="66" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sj)"/>
    <line x1="310" y1="66" x2="319" y2="66" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sj)"/>
    <line x1="440" y1="66" x2="449" y2="66" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sj)"/>
    <line x1="570" y1="66" x2="579" y2="66" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sj)"/>
    <!-- fold and runstate -->
    <rect class="cm-svg-box" x="200" y="152" width="140" height="48" rx="8"/>
    <text class="cm-svg-label" x="270" y="174" text-anchor="middle">Fold</text>
    <text class="cm-svg-sub"   x="270" y="190" text-anchor="middle">pure, no IO</text>
    <rect x="420" y="152" width="190" height="48" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="515" y="174" text-anchor="middle" style="fill:var(--cm-accent)">RunState</text>
    <text class="cm-svg-sub"   x="515" y="190" text-anchor="middle">messages + counters</text>
    <!-- fold arrows -->
    <line x1="380" y1="90" x2="300" y2="150" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#sj)"/>
    <line x1="340" y1="176" x2="418" y2="176" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sj)"/>
  </svg>
  <figcaption>Records append left to right; resume folds them into `RunState`. Records are fsynced, so only the last line can ever be torn.</figcaption>
</figure>

## Suspend, crash, and resume

The ordering within a turn is deliberate: the assistant turn is journaled before any tool runs, and each tool result is journaled the instant that tool completes. That gives two semantics.

- A clean suspend is exactly-once. The terminal record marks the boundary, and resume never repeats a tool call or a model call.
- A crash resumes from the last recorded event, so at most one tool call is repeated: the one in flight when the process died, whose side effect completed but whose result never reached disk. On resume, `completePending` reuses journaled results for answered tools and re-runs only the unanswered ones.

Every append is a write followed by an fsync, and the first append also fsyncs the directory, so a new run survives a crash. Reading tolerates exactly one torn tail line, which is safe because on an append-only fsynced file only the last record can be incomplete.

## The resume guard

Continuing a conversation against a changed model, prompt, or tool set can be incoherent, so a resume is refused when the configuration no longer matches. The `Fingerprint` in `fingerprint.go` covers the model, a hash of the system prompt, a hash of the tool set, the thinking mode, and both budgets. Hashes are stored, never the prompt itself, so nothing leaks. A mismatch names exactly what changed and refuses unless `--force` is given.

The provider is the exception, and it is a harder gate. `Fingerprint.Provider` records the resolved backend's own id rather than the configured selector, so the record names the backend the journal was actually written against. A resume across a provider change is refused unconditionally, and `--force` does not reach it. That is why the field is deliberately excluded from `Equal` and `Diff`, which govern only the drift `--force` is allowed to override: the check runs first, so a provider change gives its own message instead of being folded into a generic diff.

{{% notice style="warning" title="Load-bearing decision" %}}
Prompt caching, the memory index, and the resume reminder are all appended after the fingerprint is computed, so none of them can perturb the comparison. Memory drift between suspend and resume never blocks a resume; memory is data, not configuration.
{{% /notice %}}

## Storage and locking

The store is pluggable behind the `Store` interface. Each backend registers itself under a name in `registry.go`, so a backend links into the binary by being imported, and `New` builds whichever one is selected. An unknown backend name fails at the start of a run and lists the backends that are linked in. Backends share the same id and append rules through `ValidateID` and `CheckAppend`, so a run journaled by one is legal in another and the sequence contract cannot drift. The file backend is the only one today; a JetStream stream (subject `<prefix>.<run>.<seq>`, one message per subject for an unbounded dedup window) is the intended second. The backend is not configured in `agent.yaml` yet: `config.SessionConfigFromStateDir` synthesizes a `config.SessionConfig` at boot from `--state-dir` and its default, so the construction path is already the one a future config block will use. Both `run` and the `session` subcommands go through it, which is why they agree on where a run lives without sharing a resolver of their own.

The file backend stores sessions under the XDG state directory, `$XDG_STATE_HOME/fisk-ai/runs` or `~/.local/state/fisk-ai/runs`, never the working directory, so runs do not leak into repositories, and never namespaced by identity, so a resume finds its run regardless of the active identity. `DefaultDir` and that never-in-CWD contract live in the core, not the backend, so every backend that resolves a default location honors them. Each run is a `<id>.json` journal plus an `<id>.lock` file; the directory is `0700` and journals are `0600`. On unix a per-run advisory `flock` held for the life of an open journal prevents two processes appending to the same run, and the kernel releases it on exit so a crash leaves no stale lock. `Load` and `List` do not lock, since they only read.

{{% notice style="warning" title="Load-bearing decision" %}}
That mutual exclusion is unix-only. `lock_other.go` is a deliberate no-op: on other platforms nothing prevents two processes appending to the same run, and the append-only reasoning above assumes a single writer. Resuming the same session twice concurrently off unix interleaves records and corrupts the journal. Those platforms rely on the operator not doing it.
{{% /notice %}}

{{% notice style="note" title="Naming" %}}
The user-facing term is "session" (`session ls`, `--name`), while the internal package and types say "run" (`RunID`, `RunState`, `runstate`). They are the same value.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [Memory]({{% relref "memory" %}}) for durable notes that persist across separate runs.
{{% /notice %}}
