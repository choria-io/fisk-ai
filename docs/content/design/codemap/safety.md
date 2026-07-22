+++
title = "Safety and Human in the Loop"
weight = 50
description = "How every command runs sandboxed by construction, and how a human gates the ones that matter."
+++

Safety is the reason Fisk AI exists. Two ideas carry it: a command runs with no shell and no credentials so it is bounded by construction, and a tagged command can require a human to approve it before it runs. Both default to the safe outcome.

{{% notice style="note" title="Where it lives" %}}
The confirm gate is `internal/util/confirm.go` and terminal detection and sanitization are `internal/util/terminal.go`. The prompter contract is `internal/toolkit/prompter.go` with its line implementation in `internal/toolkit/survey_prompter.go`; the full-screen prompter is `internal/tui/prompter.go`. The `ask_human_*` tools are `internal/toolkit/builtin/builtin.go`. Command execution safety is `internal/toolkit/fisk/fisk.go`.
{{% /notice %}}

## A command is sandboxed by construction

When the loop runs a tool, `FiskCommandTool.Execute` passes an argument vector to `exec.CommandContext`. No shell is involved, so model-supplied arguments can never be interpreted as shell syntax; `t.Model.ArgsFromJSON` is the trust boundary that bounds input to the command's schema. The child gets no stdin, a ten-second wait delay after cancellation, an environment with credentials stripped, and its combined output capped.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 250" role="img" aria-label="Model JSON becomes an argument vector executed with no shell, a stripped environment, and capped output">
    <defs>
      <marker id="sx" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- model args -->
    <rect class="cm-svg-box" x="16" y="96" width="120" height="48" rx="8"/>
    <text class="cm-svg-label" x="76" y="118" text-anchor="middle">model args</text>
    <text class="cm-svg-sub"   x="76" y="134" text-anchor="middle">JSON</text>
    <!-- argv exec (accent) -->
    <rect x="206" y="96" width="140" height="48" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="276" y="118" text-anchor="middle" style="fill:var(--cm-accent)">argv exec</text>
    <text class="cm-svg-sub"   x="276" y="134" text-anchor="middle">no shell</text>
    <!-- raw output -->
    <rect class="cm-svg-box" x="412" y="96" width="150" height="48" rx="8"/>
    <text class="cm-svg-label" x="487" y="118" text-anchor="middle">raw output</text>
    <text class="cm-svg-sub"   x="487" y="134" text-anchor="middle">stdout+stderr</text>
    <!-- cap -->
    <rect class="cm-svg-box" x="618" y="96" width="124" height="48" rx="8"/>
    <text class="cm-svg-label" x="680" y="118" text-anchor="middle">cap 64 KiB</text>
    <text class="cm-svg-sub"   x="680" y="134" text-anchor="middle">head + tail</text>
    <!-- child env callout -->
    <rect class="cm-svg-box" x="196" y="188" width="160" height="46" rx="8"/>
    <text class="cm-svg-label" x="276" y="209" text-anchor="middle">child env</text>
    <text class="cm-svg-sub"   x="276" y="225" text-anchor="middle">no secrets</text>
    <!-- arrows -->
    <text class="cm-svg-sub" x="171" y="86" text-anchor="middle">ArgsFromJSON</text>
    <line x1="136" y1="120" x2="204" y2="120" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sx)"/>
    <line x1="346" y1="120" x2="410" y2="120" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sx)"/>
    <line x1="562" y1="120" x2="616" y2="120" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#sx)"/>
    <line x1="276" y1="144" x2="276" y2="186" stroke="var(--cm-faint)" stroke-width="1.5" marker-end="url(#sx)"/>
  </svg>
  <figcaption>A tool call becomes an argument vector, never a shell command. The child environment has its credentials removed and gains `LLMFORMAT=1`.</figcaption>
</figure>

`commandEnv` removes the secret-bearing variables so a model-chosen command cannot read the agent's own credentials from its environment. The set is not a list maintained here: it is the union of the names every provider linked into the build declared when it registered, plus the operator's own `sensitive_env_vars`. The anthropic provider contributes five, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_IDENTITY_TOKEN`, `ANTHROPIC_WEBHOOK_SIGNING_KEY`, and `ANTHROPIC_CUSTOM_HEADERS`. Because the union is over linked providers rather than the configured one, a build that links two backends strips both regardless of which is selected. `commandEnv` also appends `LLMFORMAT=1` so a fisk application can render output suited to a model rather than a terminal, and output is capped at 64 KiB, keeping the head and tail with a truncation marker, so a chatty command cannot flood the model's context.

Both executions of the operator's binary are covered: the tool call and the introspection subprocess that reads the command tree. Between them they are the only places Fisk AI executes it.

Selector variables such as `ANTHROPIC_PROFILE`, `ANTHROPIC_CONFIG_DIR`, and `XDG_CONFIG_HOME` are deliberately left in place. They hold no secret, and the files they point at are already protected by filesystem permissions.

{{% notice style="warning" title="Caveat" %}}
The guarantee is name-based and scoped to the environment. It removes the variables a provider identified as secrets, so a tool cannot read them from its environment. It does not reach a secret the operator also exported under a second, unnamed variable, nor a key passed on the parent's command line, nor a file the agent itself wrote that the tool can read as the same user.
{{% /notice %}}

{{% notice style="warning" title="Load-bearing decision" %}}
The built-in tools run in-process with the agent's own, unstripped environment. Their handlers must never hand that environment to a subprocess. Only the `exec` path through `commandEnv` in `internal/toolkit/fisk/fisk.go` is sanitized.
{{% /notice %}}

## Two ways to put a human in the loop

The two mechanisms are distinct and address different needs.

<dl class="cm-kv">
  <dt>ai:confirm</dt><dd>An application author gates a specific command. The operator must approve it before it runs; the command itself is unchanged. Always active, no configuration flag.</dd>
  <dt>human_in_the_loop</dt><dd>A configuration flag that gives the model three `ask_human_*` tools so it can ask the operator a question it chose to ask.</dd>
</dl>

Reach for `ai:confirm` when a normal command should run only with the operator's say-so. Reach for `human_in_the_loop` when the model should decide when to check in. The `harness.confirm_tags` key extends the gate to any tag an application already uses, for example `impact:rw`.

## The confirm gate defaults to deny

When the model calls a gated command, the gate runs the checks in a fixed order, and every failure is a denial.

<ol class="cm-steps">
  <li><b>Missing parameters first</b> A structurally invalid call is rejected before the gate, so the operator is never asked to approve a broken command. The runner reaches this through the `toolkit.ArgumentValidator` capability interface, which is why the check applies uniformly across tool kinds.</li>
  <li><b>Session-allow short-circuit</b> If the operator earlier chose "allow for the session" for this command, it runs without asking again.</li>
  <li><b>No operator, no run</b> When the prompter reports it cannot reach an operator (`CanPrompt` is false), or the context is canceled, `ConfirmGate.Approve` denies before any prompt is shown. Reachability is the prompter's own report, not a raw terminal check, so an operator reachable through a non-terminal channel can still approve while a run with no operator fails closed.</li>
  <li><b>Prompt the operator</b> The sanitized full command line is shown. Any prompter error, an interrupt, an end-of-input, or an Escape, is a denial.</li>
</ol>

A denial is returned to the model as an authoritative, non-error result whose reason ends with a note not to retry, so the model reasons about the refusal rather than routing around a tool failure. An "allow for the session" answer is remembered by command name, regardless of arguments, and lasts only for that run.

## The ask_human tools

When `human_in_the_loop` is enabled the model gets three tools: `ask_human_confirm` (yes or no), `ask_human_select` (choose one option), and `ask_human_input` (a free-text value). Each denies by default: no reachable operator (the prompter's `CanPrompt` is false), an interrupt, an end-of-input, or a canceled context yields a negative answer rather than a guess, returned as a normal result with a reason so the model does not retry around it. `ask_human_select` caps the option list at 25.

Model-supplied prompt text is stripped of terminal control sequences before any truncation, so a cut can never leave a dangling escape. The two prompter implementations differ in where they draw: the line prompter renders on stderr so a piped final answer stays clean, while the full-screen prompter draws a modal inside the terminal UI.

The sanitization caps are deliberately asymmetric. Prompt text is capped at 500 runes, but a command line shown for approval is capped at 2000, so truncation can never hide the very arguments the operator is being asked to approve.

{{% notice style="warning" title="Load-bearing decision" %}}
The confirm gate and the `ask_human_*` tools are agent-loop only. Neither served face can prompt, but they handle a gated command differently. Over MCP the command is still served and requested through elicitation, which a client may auto-approve, so `ai:deny` is the only way to keep it off the MCP surface entirely. Over A2A a confirm-gated tool is dropped from the served set outright by `Server.selectExposed`, with the reason logged. An author who adds `ai:confirm` for agent-mode safety therefore also removes that tool from A2A. See [MCP and A2A]({{% relref "interop" %}}).
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [Sessions and Resume]({{% relref "sessions" %}}) for how a run survives a suspend or a crash.
{{% /notice %}}
