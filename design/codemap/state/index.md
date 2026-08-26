# Durable state

A run writes an append-only journal, and the conversation, the counters and the resume position are recomputed from the records, so they cannot drift from what happened.

{{% notice style="note" title="Where it lives" %}}
`internal/runstate` holds the record model (`record.go`), the fold (`state.go`), the store contract (`store.go`), the shared append rules (`validate.go`), the resume gate's fingerprint (`fingerprint.go`) and the embedded JSON schemas. `internal/runstate/file` and `internal/runstate/jetstream` are the backends. `internal/tasks` is a separate store for a different question.
{{% /notice %}}

## Records and sequence

Ten protocol ids, one shape each, with the id in the body rather than in a subject or a filename, so one record read anywhere can be validated without knowing where it came from. A JetStream record body is byte-identical to a file journal line, so a run migrates between backends unchanged.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 250" role="img" aria-label="Journal records folded into a resumable run state">
    <defs>
      <marker id="st-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="30" width="115" height="44" rx="8"/>
    <text class="cm-svg-label" x="77" y="50" text-anchor="middle">meta</text>
    <text class="cm-svg-sub" x="77" y="65" text-anchor="middle">seq 1</text>
    <rect class="cm-svg-box" x="150" y="30" width="115" height="44" rx="8"/>
    <text class="cm-svg-label" x="207" y="50" text-anchor="middle">assistant</text>
    <text class="cm-svg-sub" x="207" y="65" text-anchor="middle">seq 2</text>
    <rect class="cm-svg-box" x="280" y="30" width="115" height="44" rx="8"/>
    <text class="cm-svg-label" x="337" y="50" text-anchor="middle">tool_result</text>
    <text class="cm-svg-sub" x="337" y="65" text-anchor="middle">seq 3</text>
    <rect class="cm-svg-box" x="410" y="30" width="115" height="44" rx="8"/>
    <text class="cm-svg-label" x="467" y="50" text-anchor="middle">decision</text>
    <text class="cm-svg-sub" x="467" y="65" text-anchor="middle">seq 4</text>
    <rect class="cm-svg-box" x="540" y="30" width="115" height="44" rx="8"/>
    <text class="cm-svg-label" x="597" y="50" text-anchor="middle">terminal</text>
    <text class="cm-svg-sub" x="597" y="65" text-anchor="middle">seq 5</text>
    <line x1="77" y1="74" x2="77" y2="100" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="207" y1="74" x2="207" y2="100" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="337" y1="74" x2="337" y2="100" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="467" y1="74" x2="467" y2="100" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="597" y1="74" x2="597" y2="100" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="77" y1="100" x2="597" y2="100" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="380" y1="100" x2="380" y2="129" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#st-ah)"/>
    <rect x="290" y="135" width="180" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="157" text-anchor="middle" style="fill:var(--cm-accent)">Fold</text>
    <text class="cm-svg-sub" x="380" y="174" text-anchor="middle">pure, no IO</text>
    <line x1="470" y1="160" x2="534" y2="160" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#st-ah)"/>
    <rect class="cm-svg-box" x="540" y="135" width="180" height="50" rx="8"/>
    <text class="cm-svg-label" x="630" y="157" text-anchor="middle">RunState</text>
    <text class="cm-svg-sub" x="630" y="174" text-anchor="middle">messages, counters</text>
    <text class="cm-svg-sub" x="360" y="222" text-anchor="middle">seq is the ordering authority: a duplicate is skipped, a gap is an error</text>
  </svg>
  <figcaption>Everything the resume needs is derived. Nothing is stored twice.</figcaption>
</figure>

`CheckAppend` is the shared rule so the two backends cannot drift: at or below the last sequence is a duplicate to skip, one above is next, anything higher is a gap. Its contract says explicitly not to fold the advance into the helper, because the caller must advance only after the record is durably stored, so a torn write re-appends the same sequence instead of losing it. The file backend advances after fsync, the JetStream backend after the ack.

The file backend fsyncs every record and fsyncs the directory on a new journal's first write. It drops an unparsable final line as a torn tail but treats an interior parse failure as corruption, which is only valid because the file is append-only and synced. JetStream enforces append-only at the server with one message per subject and a discard-new policy, and refuses a stream with a maximum age, since stored runs would silently expire.

## Folding

`Fold` is pure. It requires the first record to be meta, requires the version to match exactly in both directions, and requires strictly increasing sequences. It walks the records keeping a current assistant turn, and commits that turn, appending the assistant message plus a synthetic user message of tool results, when the next assistant record begins or a user follow-up arrives. A trailing turn with unanswered tool calls becomes the pending batch instead.

Consecutive user turns are merged, because the API rejects two user messages in a row.

An `optional` flag carries forward compatibility, and may be set only where a reader that skips the record behaves more conservatively rather than differently. A record whose absence would change a decision in the permissive direction needs a version bump. A deferral record must never carry the flag. New fields are added with `omitempty` and a documented fold-as-zero rule instead.

## The resume gate

The fingerprint records the configuration a stored conversation was written under, and each field is classed hard, blocking, tools or budget.

| Class | Fields | Behavior |
|---|---|---|
| Hard | Provider | Refused, and `--force` cannot cross it |
| Blocking | Model, system prompt hash, thinking mode, reasoning effort | Refused unless forced. Each can leave a history the provider will not accept |
| Tools | Tool set hash | Never refused. It drops standing approvals and warns, because a moved tool set invalidates the grants rather than the stored conversation |
| Budget | Max tokens, max iterations | Reported only. A served conversation's caller may lower both per request |

The system prompt is stored as a hash, never verbatim. The resume reminder is appended to the prompt after the fingerprint is computed, so it can never perturb the comparison.

## Claiming a run

A resume appends a claim record before it does anything else. The payload is diagnostic; the append moves the journal's tail, so any worker that still believes it holds the run is refused at its own next append.

The claim lands before this worker causes any effect, and the last sequence is read after the claim, because reading it first would make the runner's first record collide with the claim's sequence and be folded away as a duplicate. A claim that fails is fatal to the resume, since skipping it when the store is briefly unreachable would leave a second worker free to append.

The fold treats a claim as completely inert: a claim written on resume lands between an assistant turn and the tool results answering it, so touching the current turn there would commit it early and destroy the pending batch the resume exists to finish.

The backends enforce it differently. The file backend takes an exclusive `flock` on a lock file for the journal's lifetime, released by the kernel on exit, so a crash leaves no stale lock. JetStream publishes with an expected-last-sequence condition and disambiguates a rejection by reading the target subject: the same message id means a lost ack to adopt, sequence one means a concurrent creator, anything else means another writer.

{{% notice style="warning" title="Load-bearing decision" %}}
Standing grants survive a suspend, which is what they are for, but one-shot call approvals are cleared by a terminal record, so an approval the run never reached is spent rather than authorizing a later dispatch nobody approved. Neither record type carries a denial.
{{% /notice %}}

{{% notice style="warning" title="Load-bearing decision" %}}
No credential of this process reaches the journal. The system prompt is only ever a hash. The one credential stored is the caller's conversation token: reading it requires store access that already grants writing the journal, it is never logged, and it is worth nothing without the identity that minted it.
{{% /notice %}}

## Answering a deferred call

Supplying a result loads and validates before opening the journal, so a refusal costs no lock. It then writes one ordinary tool result record and nothing else, which makes the next resume an ordinary resume.

The check looks at the committed conversation first. Answering a deferral completes and commits its turn, so reporting it as never deferred would tell somebody answering twice that their first answer never landed.

## The task record

`internal/tasks` stores what was asked and what came back. The journal holds how the work was done, which is private working state, and a caller is never sent there for an answer: not because it is hidden, but because depending on it would make every internal change a breaking one.

| | Journal | Task record |
|---|---|---|
| Shape | Append-only, folded over N entries | Rewritten in place, two observable states |
| Contents | Neutral messages and this process's working state | The a2a messages verbatim |
| Id | Minted or derived | Taken from the request, so one identifier threads request, record, trace and session |
| Audience | This software | The caller |

The state vocabulary reports only what the store can observe. Queued, claimed, running and retrying belong to the queue, and duplicating them would give two answers to one question. Completing refuses a second write, because with at-least-once delivery the loser is usually the failed worker and last-write-wins would let a failure replace an answer that succeeded.

## Reserved and not yet wired

Nothing outside its own file backend imports `internal/tasks` yet, and there is no stream backend, though the registry hooks for one exist and the package doc names it.

The schema validator is exercised only by tests. Neither backend validates before writing and neither validates before folding, so an entry that violates its schema is written and folded without complaint.

Call approval records are read and spent but never written here: they are journaled by whatever supplied the operator's answer while the run was suspended, and that out-of-band approval channel does not exist yet.

The two backends disagree in one documented case. The file backend folds every run to build its listing; JetStream rejected that as too expensive and summarizes from two records, so a run with a turn in flight is reported open by one and can be reported completed by the other.

{{% notice style="tip" title="Next" %}}
Continue to [Serving]({{% relref "serving" %}}) for the surfaces that create these sessions, or [The agent loop]({{% relref "agent-loop" %}}) for what writes each record.
{{% /notice %}}
