# Tools and introspection

Fisk AI writes no glue code for the application it drives. It runs the binary once with `--fisk-introspect`, reads the
command tree, and turns leaf commands into tools. Everything that makes this safe rather than merely convenient happens
in the filtering, the gating, and the execution.

{{% notice style="note" title="Where it lives" %}}
`internal/toolkit` holds the neutral contracts and imports no tool kind. Key files: `tool.go`, `kind.go`, `describer.go`,
`confirm.go`, `prompter.go`. `internal/toolkit/fisk` is the application-command kind, `internal/toolkit/functool` the
generic function kind, and `internal/toolkit/builtin` the harness-owned tools.
{{% /notice %}}

## One interface, narrow capabilities

`toolkit.Tool` is four methods: `Name`, `Description`, `InputSchema`, `Definition(deferLoading bool)`, and
`ExecuteUse`. The runner never type-switches on a tool. Policy is opted into through three narrow interfaces:

<dl class="cm-kv">
  <dt>Describer</dt><dd>Supplies the trace line, the provider kind, and which dependencies the tool needs. Must be side-effect free, because it runs before execution and before hooks.</dd>
  <dt>Confirmable</dt><dd>Reports whether a call needs approval, which tag triggered it, and how to render the command.</dd>
  <dt>ArgumentValidator</dt><dd>Reports missing required arguments so the runner can reject a malformed call before an operator ever sees it.</dd>
</dl>

A tool implementing none of these is still runnable. It traces by name, accounts as `KindUnknown`, receives no
dependencies, and is not treated as remote.

### Two axes that are never derived from each other

| Axis | Values | Used for |
|------|--------|----------|
| `Kind` | `unknown`, `application`, `builtin`, `remote`, `custom` | Accounting and the `kind=` log token |
| `Presentation` | `PresentCommand`, `PresentRemote`, `PresentSelfRendered`, `PresentTraced` | Whether and how a renderer shows the call |

One kind spans two presentations: the human-in-the-loop tools are self-rendered, while the memory and knowledge tools are
traced. Remoteness is read from `Present == PresentRemote` explicitly, never inferred from a possibly-empty agent name.

## From a command tree to a tool set

<figure class="cm-diagram">
  <svg viewBox="0 0 760 400" role="img" aria-label="The narrowing funnel from a Fisk command tree to model-visible tools">
    <defs>
      <marker id="tk-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="180" y="18" width="400" height="44" rx="8"/>
    <text class="cm-svg-label" x="380" y="45" text-anchor="middle">fisk binary, --fisk-introspect</text>
    <rect class="cm-svg-box" x="200" y="76" width="360" height="44" rx="8"/>
    <text class="cm-svg-label" x="380" y="103" text-anchor="middle">leaf commands become tools</text>
    <rect x="220" y="134" width="320" height="44" rx="8" fill="color-mix(in srgb, var(--cm-accent3) 12%, transparent)" stroke="var(--cm-accent3)"/>
    <text class="cm-svg-label" x="380" y="161" text-anchor="middle" style="fill:var(--cm-accent3)">ai:deny dropped</text>
    <rect x="240" y="192" width="280" height="44" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="219" text-anchor="middle" style="fill:var(--cm-accent)">include filter</text>
    <rect x="260" y="250" width="240" height="44" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="380" y="277" text-anchor="middle" style="fill:var(--cm-accent)">exclude filter</text>
    <rect class="cm-svg-box" x="280" y="308" width="200" height="44" rx="8"/>
    <text class="cm-svg-label" x="380" y="335" text-anchor="middle">expose.agent.tools</text>
    <line x1="380" y1="62" x2="380" y2="70" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tk-ah)"/>
    <line x1="380" y1="120" x2="380" y2="128" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tk-ah)"/>
    <line x1="380" y1="178" x2="380" y2="186" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tk-ah)"/>
    <line x1="380" y1="236" x2="380" y2="244" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tk-ah)"/>
    <line x1="380" y1="294" x2="380" y2="302" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tk-ah)"/>
    <text class="cm-svg-sub" x="606" y="158" text-anchor="start">unconditional</text>
    <text class="cm-svg-sub" x="606" y="216" text-anchor="start">optional</text>
    <text class="cm-svg-sub" x="606" y="274" text-anchor="start">optional</text>
    <text class="cm-svg-sub" x="606" y="332" text-anchor="start">MCP and a2a only</text>
    <text class="cm-svg-sub" x="130" y="225" text-anchor="middle">every stage can only</text>
    <text class="cm-svg-sub" x="130" y="240" text-anchor="middle">narrow, never re-add</text>
    <text class="cm-svg-sub" x="380" y="378" text-anchor="middle">the survivors share one flat namespace, and a collision aborts the run</text>
  </svg>
  <figcaption>The deny pass runs with a nil filter before include and exclude, which is what makes it unconditional.</figcaption>
</figure>

<ol class="cm-steps">
  <li><b>Introspect</b> <code>FetchFiskAppModel</code> runs the binary as a subprocess with a 30 second deadline when the caller supplies none, in its own process group, with credentials scrubbed from the environment.</li>
  <li><b>Walk the tree</b> <code>ApplicationTools</code> skips hidden commands and their whole subtree. A node with subcommands is a grouping node and produces no tool. Every leaf becomes one tool whose name is its path joined with underscores.</li>
  <li><b>Require real schemas</b> A leaf with no precomputed restricted schema is an error telling the operator to introspect with a current fisk, so an empty schema is never shipped silently.</li>
  <li><b>Drop <code>ai:deny</code></b> <code>LoadTools</code> calls <code>FilterTools</code> with a nil filter first. That pass exists solely to enforce the deny tag.</li>
  <li><b>Apply include, then exclude</b> Each is honored only if it carries patterns or tags. Patterns are regexes over the underscore name; tags are exact matches, where an empty string matches an untagged command.</li>
  <li><b>Narrow for serving</b> <code>ServedTools</code> applies <code>expose.agent.tools</code> on top for MCP and a2a.</li>
</ol>

Introspection deliberately does not run in the per-run working directory. A binary that reads a relative config at
introspect time therefore sees the same one the operator would, so the exposed tool set cannot differ between runs.

An `application_path` that is unset is not an error. An agent may run on built-ins and remote tools alone.

## Schemas

The per-command JSON schema is computed by fisk during introspection, not here. Fisk emits an
`additionalProperties: false` object schema and a restricted variant trimmed to the strict subset the model API accepts.
`InputSchema` returns that schema as-is, unless global flags are exposed.

`mergeGlobalFlags` clones the schema, the properties map, and the required list before touching anything. The model
schema is shared and reused on every request, so mutating it in place would leak across calls and, worse, would break the
resume fingerprint. A global flag's description is prefixed with `Global flag:` so the model knows the setting is
application-wide, and string-typed completions become an `enum`.

Optional parameters are annotated on the wire rather than in the neutral schema. `toolkit.AnnotateOptional` copies the
property map and appends `(optional)` to each description, because models under-weight mere absence from `required` and
will interrogate the operator for a parameter the command would have defaulted.

`global_flags` failures are loud rather than silent. An unknown, framework, or hidden entry is an error, and the two
cases are distinguished so an operator is not hunting for a typo that does not exist. A framework flag deny-list keeps
`help`, `completion`, `fisk-introspect`, and `version` from ever becoming parameters, since exposing them would make a
tool print usage instead of running. A required global is exposed whether or not it was allowlisted, because the command
cannot run without it. A global that collides with a command's own flag is skipped for that command, so a call is never
ambiguous.

## Tags

Tags come from the Fisk command definition and survive introspection. Three are interpreted; everything else is
free-form and matched only by the filters and by `confirm_tags`.

| Tag | Effect |
|-----|--------|
| `ai:deny` | Never exposed to any model over any transport. Dropped before include and exclude, so no selection can re-add it. It also beats `ai:confirm`: a doubly-tagged command is never exposed at all. |
| `ai:no_defer` | Always sent directly rather than hidden behind tool search, even inside an otherwise deferred set. Inert over MCP and a2a, which do not defer. |
| `ai:confirm` | Always requires operator approval. Matched unconditionally, so leaving it out of `confirm_tags` cannot weaken it. |

`harness.confirm_tags` lists additional tags that gate exactly as `ai:confirm` does: exact match, no regex, additive.
The common use is an existing application convention such as `impact:rw`. That is an example of an operator-supplied
tag, not something Fisk AI understands.

`ConfirmTrigger` prefers `ai:confirm` when present, since it is the strongest and mode-independent signal, and otherwise
takes the first matching tag in the tool's own tag order, so prompt wording is deterministic.

All surviving tags are appended to the model-facing description as a single `Tags:` line. That is deliberate: operator
prompts can key off tag names, and the terms stay searchable when the tool set is deferred behind tool search.

{{% notice style="warning" title="Load-bearing decision" %}}
Approval is never expressed as an MCP annotation, because annotations are advisory hints a client may ignore rather than
a control channel. That keeps the exclusion story single-valued: for anything that must be unreachable the answer is
`ai:deny`, which drops the command before any filter or gate can reach it, not `ai:confirm`, which depends on a client
choosing to honor it. See [Serving: MCP and A2A]({{% relref "serving" %}}).
{{% /notice %}}

Nothing rejects or warns about an unrecognized `ai:*` tag. The prefix reads as reserved but is not enforced, and such a
tag reaches the model in the `Tags:` line like any other.

## Two ways a human enters the loop

These are distinct mechanisms and are worth keeping apart.

**The confirm gate** puts the operator on the path of a command the model chose. `util.ConfirmGate.Approve` checks the
per-run allow list, then default-denies if no operator is reachable, then default-denies on a canceled context, all
before any prompt is drawn. Only then does it call the prompter. An "always" answer is remembered for the run, keyed by
tool name, so it covers that command with any arguments.

A denial returns a normal result, not an error, whose reason states that the decision is final and not to retry. That
wording is the point: the model treats the refusal as authoritative rather than as a transport failure to route around.

**The human-in-the-loop tools** let the model ask its own question: `ask_human_confirm`, `ask_human_select`, and
`ask_human_input`. Each handler decodes, sanitizes, validates non-empty, checks that an operator is reachable, checks the
context, and only then prompts. Every non-affirmative path is a normal result carrying a reason. Only malformed input is
an error.

`ask_human_input` treats an empty answer as a real value, distinguishing "typed nothing" from "no answer". `ask_human_select`
starts at no selection so a cancel never silently picks the first option, and `ask_human_confirm` lists the safe option
first so a reflexive Enter declines.

These tools exist because the loop ends on a text-only turn. A model that asks a question in prose silently ends the run.
`HITLSystemNote` names the enabled tools in the system prompt so the capability is discoverable and the note cannot drift
from what is actually wired.

Three startup advisories cover the ways this can be misconfigured: HITL enabled with no reachable operator, gated tools
with no reachable operator, and each `confirm_tags` entry that matches no loaded tool.

## Execution

**No shell, ever.** Model arguments become argv and nothing else. `Model.ArgsFromJSON` is the trust boundary: it bounds
arguments to the command's schema and rejects unknown properties. Exposed globals are placed after the command path and
before the `--` separator so they always parse as flags.

**Credentials are scrubbed by name.** The environment handed to a command drops the union of every linked provider's
credential variables and the operator's own `api_key_env` names. The union covers all linked providers, not just the
active one. The limit is stated honestly in the source: it cannot catch the same secret exported under a second,
unnamed variable.

`PWD` is rewritten so the child never sees two. `HOME` and `TMPDIR` are deliberately left inherited: repointing `TMPDIR`
into the work directory would collide with the tool's own output there and let a cleaning tool delete it, and repointing
`HOME` breaks tools that read credentials or config from it.

**The whole process group is killed on cancel.** `exec.CommandContext` signals only the direct child, and `WaitDelay`
bounds only the parent's wait for I/O, so a wrapped CLI that forks would leak orphaned grandchildren. That is invisible
in a short-lived CLI and cumulative in a long-lived server, so both execution paths set their own process group and kill
the group. On non-Unix platforms this is an explicit documented no-op.

**Output is bounded, differently on each path, for a reason.** Tool output keeps the first 64 KiB and the most recent
32 KiB with a truncation marker in between, because a head-and-tail view is usually enough to act on. Introspection
output is capped at 16 MiB and then rejected rather than truncated, because the document has to parse whole. Both writers
report full consumption so the `os/exec` copier keeps draining the pipe.

A single writer is set on both stdout and stderr, so `os/exec` serializes them and the interleaving order a terminal
would show is preserved without buffering the whole stream.

A canceled or timed-out context is an error. A non-zero exit is a normal result carrying `exit_code` and output. A
failure to start the binary is an error.

**Everything an operator sees is sanitized before truncation**, so a cut can never leave a dangling escape sequence.
Command lines get a 2000-rune cap, because truncating shorter could hide the very argument being approved. Short trace
lines elide argument values only, never the command path or flag names, and only for display: the gate and the executor
always see the full value.

## Validation before approval

Fisk silently drops a missing required flag and fails only on the command's own exit. So `MissingRequired` runs on the
effective call before the gate. The operator is never asked to approve a structurally invalid call, and nothing runs.

"Supplied" means the key is present regardless of value, so `false`, `0`, `""`, and `[]` all count. A non-object input
passes through for fisk's own error message. The invariant relied on is that fisk forbids a parameter that is both
required and defaulted.

`MissingRequiredMessage` returns the full required and optional roster, with optional parameters sorted since properties
are an unordered map, so the model can fix the call in a single turn.

{{% notice style="warning" title="Load-bearing decision" %}}
A tool's `Definition` JSON must be byte-stable across process restarts, because the tool set is hashed into the resume
fingerprint. That is why `AnnotateOptional` copies rather than mutates, `mergeGlobalFlags` clones, parameters are sorted,
custom tools are appended in name order, and `defer_loading` is emitted unconditionally including a present `false`. Any
non-determinism here turns into a spurious resume refusal. See
[Sessions and replay]({{% relref "state" %}}).
{{% /notice %}}

## The built-in tools

All in-process, all `KindBuiltin`, all never deferred.

Each declares the serving surfaces that may ever carry it. The declaration is the ceiling; an operator's allowlist
narrows it further and can never widen past it, and the zero value serves nowhere.

| Tool | Group | MCP | A2A | Notes |
|------|-------|-----|-----|-------|
| `ask_human_confirm` | HITL | no | no | Yes or no, deny by default |
| `ask_human_select` | HITL | no | no | Up to 25 options, labels sanitized, never auto-picks |
| `ask_human_input` | HITL | no | no | Free text with an editable default; the description says explicitly it is not for secrets |
| `memory_list` | memory | no | no | The live view, contrasted with the start-of-run index |
| `memory_read` | memory | no | no | A miss is a soft result, not an error |
| `memory_write` | memory | no | no | Create by default; an existing-key reply names the colliding description |
| `memory_delete` | memory | no | no | Idempotent |
| `knowledge_search` | knowledge | yes | no | Read-only ranked retrieval |
| `knowledge_enumerate` | knowledge | yes | no | Read-only complete-set matching; returns paths and counts, no text |

The HITL tools need an operator at a terminal and the memory tools carry operator state, so neither is offered on a
served surface. Both knowledge tools are read-only and need no prompt, so both declare MCP. Neither declares A2A
exposure because there is no a2a `builtins` allowlist: without a selection mechanism, declaring it would serve it the
moment an operator enables a2a, with nothing to narrow it.

The two knowledge tools are meant to be served together, since a client that can rank but cannot enumerate has the
defect enumeration exists to fix. That is carried by a note from `notePartialKnowledgeSet`, never by one allowlist entry
selecting the other: selection stays strictly per tool, so naming `knowledge_search` serves exactly `knowledge_search`.
The per-tool filter now has two genuinely servable tools to keep apart, which tests it harder than a tool that could not
be served either way.

`mustNew` is the single chokepoint that stamps `KindBuiltin`, and it panics on a bad spec. A compiled-in static spec is
correct by construction, so a failure there is a programming error, surfaced the way `regexp.MustCompile` does rather
than threaded through every factory.

Every group ships a system-prompt note, on the stated theory that without discovery text a model under-reaches for the
capability. Every group can also be enumerated with a nil store so `fisk info` can list names offline; the handler then
returns an error rather than panicking.

## What functool provides

`functool` is the generic backend. Declare a `Spec` with a name, description, JSON schema, and a Go handler, plus
optional `Confirm`, `ValidateRequired`, `Trace`, `NoDefer`, `Remote`, and `Kind`, and get a full `Tool` that also
satisfies `Describer`, `Confirmable`, and `ArgumentValidator`, with each capability inert unless the spec enables it.

Presentation is derived rather than declared: `Remote` implies remote presentation and kind, a `Trace` function implies
traced presentation, and neither implies self-rendered. Kind defaults to `KindCustom`, so an embedder's tool is accounted
correctly with no extra work.

`New` refuses incoherent specs: a missing name, description, schema, or handler; `ValidateRequired` with no required
parameters, which would silently validate nothing; and remote combined with confirm-gating, since a remote tool is gated
by the agent serving it and never locally.

Three consumers exist today: the harness built-ins, a2a remote tools, and caller-injected custom tools.

## What is not a sandbox

The source is repeatedly explicit about this, and the code map should be too.

- The work directory is collision avoidance, not confinement.
- A `functool` handler is trusted code running with the agent's own privileges and an unscrubbed ambient environment. It
  must never read a secret from the environment or pass that environment to a subprocess.
- Over MCP there is no local operator, so `confirm_over_mcp: never` and clients that do not support elicitation run
  ungated.
- Credential scrubbing is name-based, so it cannot catch a secret exported under a name nobody declared.

## Reserved and unused

- `ConfirmSpec` and `ValidateRequired` on `functool` have no production users. No shipped built-in or remote tool sets
  them; they exist for embedder-supplied tools and are exercised only in tests and the documentation examples.
  `Spec.NoDefer` is likewise unused, since the fisk path uses the tag instead.
- `Presentation` has no unknown member. Its zero value is `PresentCommand`, so a tool without a `Describer` gets a real
  `KindUnknown` sentinel but command presentation. The asymmetry is worth knowing when reading trace output.
- The unrestricted `Schema` from introspection is populated and never consumed; only `RestrictedSchema` is used. The
  same goes for `LLMExtraInfo`, `Cheats`, `CheatTags`, and per-command `Cheat`.
- `functool.Result` is used only by the remote-tool constructor; the built-ins use a local helper for the same job.
- The survey prompter ignores its context argument in all four methods. Survey cannot select on a context, which is
  exactly why the authoritative checks happen before the prompt and why the full-screen prompter exists.
- The introspection timeout, the wait delay, and the output cap are fixed constants rather than config. The escape hatch
  for the timeout is a caller-supplied deadline.

{{% notice style="tip" title="Next" %}}
[Model providers]({{% relref "providers" %}}) covers the other side of the loop: how a request reaches a model, and what
makes a local Anthropic-compatible endpoint work.
{{% /notice %}}
