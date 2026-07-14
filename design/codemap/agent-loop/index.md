# The Agent Loop

The agent loop is what `fisk-ai run` drives. It calls the Anthropic model, runs the tools the model asks for, feeds the results back, and repeats until the model gives a final answer or a budget stops it.

{{% notice style="note" title="Where it lives" %}}
`internal/agent`: one-time setup in `agent.go`, the loop itself in `runner.go`, the reporting contract in `events.go`. The single model call with its per-call timeout is `util.CallLLM` in `internal/util/llm.go`.
{{% /notice %}}

## Setup, then loop

`agent.Run` does all the one-time work before the loop starts (`agent.go:119`): load and validate tools, inject the built-in tools, import any remote tools, build the Anthropic tool params, construct the confirm gate, build the client, seed the conversation, compute the resume fingerprint, and open or resume the journal. It then constructs a `runner` and calls its loop. The `runner` splits its state in two: infrastructure that is rebuilt from config on every start or resume, and mutable conversation state that is resumable. That split is what makes a run suspendable.

## How one iteration runs

The loop runs while the iteration count is below `max_iterations` (`runner.go:368`).

<ol class="cm-steps">
  <li><b>Check for suspend</b> Only at the loop boundary, before the iteration index is consumed, never mid-tool, so the conversation is always left coherent (`runner.go:384`).</li>
  <li><b>Call the model</b> Under a per-call timeout derived from `call_timeout`. `util.CallLLM` wraps the single request in its own context (`llm.go:16`).</li>
  <li><b>Journal the turn</b> The assistant response is appended to the conversation and journaled before any tool runs, so a crash mid-batch resumes without re-paying for the call (`runner.go:444`).</li>
  <li><b>Decide terminality</b> A turn with no tool-use blocks, and not a paused turn, is the final answer and ends the run. Otherwise the tool calls are executed (`runner.go:470`).</li>
  <li><b>Run tools and feed back</b> Each tool result is journaled as it completes, then all results are appended as one user message that becomes the next iteration's input (`runner.go:494`).</li>
</ol>

<figure class="cm-diagram">
  <svg viewBox="0 0 760 300" role="img" aria-label="Call the model, branch on whether it asked for tools, run tools and loop back, or return the final answer">
    <defs>
      <marker id="al" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- call model (accent) -->
    <rect x="40" y="110" width="150" height="54" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="115" y="134" text-anchor="middle" style="fill:var(--cm-accent)">call model</text>
    <text class="cm-svg-sub"   x="115" y="151" text-anchor="middle">under timeout</text>
    <!-- decision diamond -->
    <path d="M330,97 L410,137 L330,177 L250,137 Z" fill="var(--cm-box-bg)" stroke="var(--cm-accent)" stroke-width="1.5"/>
    <text class="cm-svg-label" x="330" y="142" text-anchor="middle">tool_use?</text>
    <!-- final answer -->
    <rect class="cm-svg-box" x="470" y="110" width="150" height="54" rx="8"/>
    <text class="cm-svg-label" x="545" y="134" text-anchor="middle">final answer</text>
    <text class="cm-svg-sub"   x="545" y="151" text-anchor="middle">return</text>
    <!-- run tools -->
    <rect class="cm-svg-box" x="255" y="222" width="150" height="48" rx="8"/>
    <text class="cm-svg-label" x="330" y="250" text-anchor="middle">run tools</text>
    <!-- feed back -->
    <rect class="cm-svg-box" x="40" y="222" width="150" height="48" rx="8"/>
    <text class="cm-svg-label" x="115" y="250" text-anchor="middle">feed results back</text>
    <!-- edges -->
    <line x1="190" y1="137" x2="248" y2="137" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#al)"/>
    <line x1="412" y1="137" x2="468" y2="137" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#al)"/>
    <text class="cm-svg-sub" x="440" y="128" text-anchor="middle">no</text>
    <line x1="330" y1="177" x2="330" y2="220" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#al)"/>
    <text class="cm-svg-sub" x="346" y="202" text-anchor="middle">yes</text>
    <line x1="253" y1="246" x2="192" y2="246" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#al)"/>
    <line x1="115" y1="222" x2="115" y2="166" stroke="var(--cm-accent)" stroke-width="1.5" stroke-dasharray="4 3" marker-end="url(#al)"/>
    <text class="cm-svg-sub" x="130" y="196" text-anchor="middle">loop</text>
  </svg>
  <figcaption>One iteration. The dashed edge feeds tool results back into the next call; the token budget is checked on that edge before any further tools run.</figcaption>
</figure>

## Budgets

Two different token caps apply. `defaultMaxOutputTokens` (8192, or 16384 with thinking) bounds a single response so a call stays under the non-streaming ceiling (`agent.go:39`). The configured `llm.budget.max_tokens` bounds the whole run. The run budget is a soft cap checked after each call but before that turn's tools run, so an over-budget turn incurs no further tool side effects (`runner.go:490`). All four token tiers, input, output, cache read, and cache create, are summed so the cap measures total throughput.

`max_iterations` bounds the loop count and `call_timeout` bounds each call. The defaults are 200000 tokens, 50 iterations, and 120 seconds.

{{% notice style="warning" title="Load-bearing decision" %}}
The assistant turn is journaled before any tool executes, and each tool result is journaled the instant that tool finishes. This ordering is what gives the durability guarantee in [Sessions and Resume]({{% relref "sessions" %}}): a clean suspend is exactly-once and a crash repeats at most one tool call.
{{% /notice %}}

## Reporting, not rendering

The loop never draws anything. It emits typed callbacks through the `Events` interface, and the caller decides how they look. The same callbacks back both the line UI and the full-screen UI, which keeps the loop free of terminal concerns. Tool dispatch distinguishes local, remote, built-in, and memory tools so each is traced correctly.

{{% notice style="tip" title="Next" %}}
Continue to [Safety and Human in the Loop]({{% relref "safety" %}}) for the guardrails around each tool call.
{{% /notice %}}
