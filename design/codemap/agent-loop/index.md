# The agent loop

One iteration calls the model once, runs whatever tools it asked for, and feeds the results back.

{{% notice style="note" title="Where it lives" %}}
`internal/agent` splits in two. `agent.go` is setup and teardown: load tools, resolve the provider, build stores, resolve the session, install hooks, and own the panic barrier. `runner.go` is the loop itself. Supporting files: `toolset.go`, `hooks.go`, `events.go`, `approvals.go`, `pii.go`, `mcplive.go`.
{{% /notice %}}

## One iteration

<ol class="cm-steps">
  <li><b>Poll for suspend</b> Only at the loop boundary, and before the iteration index is consumed, so a suspend does not burn one.</li>
  <li><b>Take the tool set once</b> A snapshot serves the model call and its whole tool batch. A tool removed mid-batch cannot strand a call the model already made.</li>
  <li><b>Call the model</b> Under the per-call timeout the provider owns.</li>
  <li><b>Journal the assistant turn before running any tool</b> A crash mid-batch resumes without paying for the model call a second time.</li>
  <li><b>Execute each tool call</b> Validate arguments, apply the confirm gate, trace, dispatch, journal the result.</li>
  <li><b>Feed the results back</b> All results become one user message and the loop iterates. If anything deferred, nothing is appended and the run suspends.</li>
</ol>

<figure class="cm-diagram">
  <svg viewBox="0 0 760 300" role="img" aria-label="One iteration of the agent loop, from suspend poll through the model call to the tool batch and back">
    <defs>
      <marker id="loop-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
      <marker id="loop-ah2" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="40" width="150" height="50" rx="8"/>
    <text class="cm-svg-label" x="95" y="62" text-anchor="middle">suspend poll</text>
    <text class="cm-svg-sub" x="95" y="79" text-anchor="middle">loop boundary</text>
    <line x1="170" y1="65" x2="189" y2="65" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#loop-ah)"/>
    <rect class="cm-svg-box" x="195" y="40" width="150" height="50" rx="8"/>
    <text class="cm-svg-label" x="270" y="62" text-anchor="middle">tool snapshot</text>
    <text class="cm-svg-sub" x="270" y="79" text-anchor="middle">one set per call</text>
    <line x1="345" y1="65" x2="364" y2="65" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#loop-ah)"/>
    <rect x="370" y="40" width="150" height="50" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="445" y="62" text-anchor="middle" style="fill:var(--cm-accent)">model call</text>
    <text class="cm-svg-sub" x="445" y="79" text-anchor="middle">under call timeout</text>
    <line x1="520" y1="65" x2="539" y2="65" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#loop-ah)"/>
    <rect class="cm-svg-box" x="545" y="40" width="150" height="50" rx="8"/>
    <text class="cm-svg-label" x="620" y="62" text-anchor="middle">journal turn</text>
    <text class="cm-svg-sub" x="620" y="79" text-anchor="middle">before any tool</text>
    <line x1="620" y1="90" x2="620" y2="119" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#loop-ah)"/>
    <polygon points="620,125 700,160 620,195 540,160" fill="none" stroke="var(--cm-accent)" stroke-width="2"/>
    <text class="cm-svg-label" x="620" y="165" text-anchor="middle">tool calls?</text>
    <line x1="540" y1="160" x2="486" y2="160" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#loop-ah)"/>
    <text class="cm-svg-sub" x="512" y="152" text-anchor="middle">yes</text>
    <rect class="cm-svg-box" x="300" y="135" width="180" height="50" rx="8"/>
    <text class="cm-svg-label" x="390" y="157" text-anchor="middle">execute batch</text>
    <text class="cm-svg-sub" x="390" y="174" text-anchor="middle">gate, run, journal</text>
    <line x1="620" y1="195" x2="620" y2="224" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#loop-ah)"/>
    <text class="cm-svg-sub" x="636" y="214" text-anchor="start">no</text>
    <rect class="cm-svg-box" x="545" y="230" width="150" height="50" rx="8"/>
    <text class="cm-svg-label" x="620" y="252" text-anchor="middle">answer</text>
    <text class="cm-svg-sub" x="620" y="269" text-anchor="middle">terminal turn</text>
    <path d="M300,160 L95,160 L95,96" fill="none" stroke="var(--cm-faint)" stroke-width="2" stroke-dasharray="5 4" marker-end="url(#loop-ah2)"/>
    <text class="cm-svg-sub" x="197" y="152" text-anchor="middle">results as one user message</text>
  </svg>
  <figcaption>The dashed edge is where the tool results re-enter the conversation. Nothing crosses it while a call is unanswered.</figcaption>
</figure>

## What one tool call passes through

`executeTool` has eight exits and one deferred span covering all of them. In order:

<ol class="cm-steps">
  <li><b>Registry lookup</b> An unknown name is counted under its own kind and answered with an error result the model can adapt to, and the run warns. The call is never dispatched.</li>
  <li><b>Describe and check gating on the original call</b> Done before any hook, so <code>PreToolUse</code> sees what the model actually asked for.</li>
  <li><b>PreToolUse</b> May deny, or rewrite the tool and its arguments. A rewrite targeting an unregistered tool or carrying invalid JSON aborts the run rather than dispatching a malformed call.</li>
  <li><b>Argument validation</b> Runs before the gate, so an operator is never asked to approve a structurally invalid call. A fisk command drops a missing required flag silently, so without this the failure would only surface as the command's own exit.</li>
  <li><b>Confirm gate</b> Fires if either the original or the effective tool is gated.</li>
  <li><b>Trace and dependencies</b> The tool receives a prompter or a work directory only if it said it needed one.</li>
  <li><b>Takeover check</b> The last check before an irreversible effect. A run taken over on a shared store stops here rather than at its next append.</li>
  <li><b>Execute, then PostToolUse</b> A timeout message is substituted before the hook runs, so hook, journal, event sink and model all see the same output.</li>
</ol>

A non-zero command exit is deliberately not an error outcome. It is an answer the model should reason about, and counting it would make the error rate meaningless.

{{% notice style="warning" title="Load-bearing decision" %}}
The gate fires on the union of the original and effective calls, so a `PreToolUse` hook cannot strip a gate by redirecting a gated call to an ungated tool.
{{% /notice %}}

## Budgets and timeouts

| Limit | Scope | Zero means |
|---|---|---|
| `llm.budget.max_output_tokens` | One model reply. Defaults to 8192, raised to 16384 when thinking is on | The built-in default |
| `llm.budget.max_tokens` | Cumulative across the conversation: input, output, cache reads and cache writes | Unbounded |
| `llm.budget.max_iterations` | An absolute position, grown by the configured amount on each accepted follow-up | Refused |
| `llm.budget.call_timeout` | One provider call, enforced inside the provider | Refused |
| `harness.tool_timeout` | One tool call, as a context deadline | No limit |

The cumulative check runs before a tool batch and at the head of a follow-up turn, but deliberately after a completed answer is returned, because those tokens are already spent.

The tool timeout has two exemptions: an operator who asked for none, and a tool marked operator-paced, where the deadline would cancel the operator's own question rather than a runaway command. A command tool is really killed with its process group; an in-process tool stops only when its handler observes the context.

## Approval, deferral and suspend

`human_in_the_loop` adds the `ask_human_*` tools to the run. The confirm gate is independent of it: it stands in front of a tagged command and is default-deny, so with no prompter it refuses before asking anything.

The gate checks for a prompter, then for an already-expired context, and only then consults standing and one-shot grants. A grant restored from a journal therefore cannot run a gated command with nobody present.

A grant is honored from the moment it is given but staged, and written only after the triggering call is answered or deferred. A crash in between loses the grant, and the resume asks again for a command it is about to re-run. There is no standing denial, so a run that ended before the operator answered cannot persist a decision they never made.

A deferral is a tool saying it will answer later. The call is journaled as deferred, grants are flushed, and the run ends suspended. A deferred call is never dispatched again; its turn finishes only when an answer is supplied through the checkpoint.

{{% notice style="warning" title="Load-bearing decision" %}}
A turn is never committed while a `tool_use` has no result. If anything in a batch defers, nothing is appended to the conversation and the run suspends with the batch intact.
{{% /notice %}}

## Hooks

In loop order. All run on the single run goroutine.

| Hook | Fires | Can |
|---|---|---|
| `RunStart` | Once, before a session is created or opened | Abort |
| `UserPromptSubmit` | On each prompt entering the conversation | Deny, rewrite |
| `PreModelCall` | Before each model call, above the provider | Abort |
| `PostModelCall` | After each reply, including a truncated one | Abort, but not durably: the turn is already journaled |
| `PreToolUse` | Before validation, gate, trace and execution | Deny, rewrite the tool and its arguments |
| `PostToolUse` | After execution, before the trace and journal | Replace the output |
| `TurnEnd` | At an interactive continuation boundary | Abort |
| `RunEnd` | At teardown, after stores close, including on a crash | Nothing |

`PreToolUse` is the only reliable place to block a tool.

## PII scanning

The guard wraps `UserPromptSubmit` and `PostToolUse`, and the run installs it itself so every path into the loop is covered. It composes with the caller's hooks rather than replacing them: the caller's hook runs first and the scan reads whatever it left behind, including its rewrite.

Under `redact` a hit rewrites the text. Under `reject` a prompt is denied and a tool output is replaced with a fixed withholding message. A scan that fails is treated as a hit, and the cause reaches the operator through an advisory and the log rather than the model. The operator warning is raised once per run, because a chat redacting on forty tool calls would bury its own answer.

A mode other than off whose scanner will not build fails the run, because carrying on would send unscanned text to the model with no sign that scanning had stopped.

## Setup and teardown

Tool assembly runs in a fixed order into one flat namespace, and every collision aborts the run rather than shadowing, because shadowing a confirm-gated command would strip its gate.

The panic barrier is registered after the telemetry spans so it unwinds first. It captures the stack before running any caller code, fires `RunEnd` exactly once, delivers the stack to the event sink inside its own recover, and substitutes a `PanicError` for the returned error. The stack stays off that error because the error may cross to a remote peer. It covers this goroutine only, not fatal runtime errors.

MCP advisories arrive on another goroutine and land in a mutex-guarded queue the loop drains where it takes tools for a model call, so an advisory arrives with the call that carries the set it is about. The tool set itself is an atomic pointer to a whole immutable set, so a reader never sees one change under it.

{{% notice style="tip" title="Next" %}}
Continue to [Tools and introspection]({{% relref "tools" %}}) for how the set the loop dispatches against is built, or [Durable state]({{% relref "state" %}}) for what the journal holds and how a resume reads it.
{{% /notice %}}
