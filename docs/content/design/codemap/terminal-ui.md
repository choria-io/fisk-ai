+++
title = "The Terminal UI"
weight = 80
description = "The full-screen runner, its event-driven render, and the single-writer discipline that keeps it safe."
+++

The default `run` presentation is a full-screen terminal UI built on tview and tcell. It shows the run as it happens, folds thinking and tool output on a keypress, and can keep a chat bar open to continue after a turn. The same widget model backs the read-only transcript viewer.

{{% notice style="note" title="Where it lives" %}}
`internal/tui`: the shared widget model and transcript viewer in `viewer.go`, the live-run wrapper in `live.go`, the native prompts in `prompter.go`, and the identity card in `splash.go`. The seam from the run loop is `tcellEvents` in `run_tui_events.go`.
{{% /notice %}}

## Two output surfaces

The full-screen UI is one of two ways output reaches an operator, and only `run` uses it. Everything else, `info`, `session ls`, `knowledge doctor`, `discover`, prints line output instead.

That line output is not hand-rolled. Tables and key-value column layout come from the external `github.com/choria-io/ui` library: `ui/table` builds tables, and `ui/columns` builds the sectioned key-value blocks the commands print. The commands hand it a `columns.Document`, add sections and items, and let the library decide widths and time formatting rather than computing them per command. `internal/tui` does not use either; it owns its own widgets.

## Two goroutines, one writer

A live run has two goroutines. One runs `agent.Run` and emits `Events`; the other is the tview event loop that owns the screen. The run goroutine never touches widgets directly. Every cross-goroutine mutation is marshaled onto the tview loop with `QueueUpdateDraw`, so view state has a single writer. `tcellEvents` is the only producer, and it always goes through `Live.Append`.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 270" role="img" aria-label="The run goroutine emits events marshaled onto the tview loop, which owns the terminal, while the prompter returns operator answers">
    <defs>
      <marker id="tu" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
      <marker id="tf" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/>
      </marker>
    </defs>
    <!-- agent loop -->
    <rect class="cm-svg-box" x="90" y="40" width="160" height="52" rx="8"/>
    <text class="cm-svg-label" x="170" y="62" text-anchor="middle">agent loop</text>
    <text class="cm-svg-sub"   x="170" y="80" text-anchor="middle">run goroutine</text>
    <!-- tcellEvents -->
    <rect class="cm-svg-box" x="320" y="40" width="160" height="52" rx="8"/>
    <text class="cm-svg-label" x="400" y="62" text-anchor="middle">tcellEvents</text>
    <text class="cm-svg-sub"   x="400" y="80" text-anchor="middle">maps to Lines</text>
    <!-- viewer (accent) -->
    <rect x="320" y="186" width="160" height="52" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="400" y="208" text-anchor="middle" style="fill:var(--cm-accent)">viewer</text>
    <text class="cm-svg-sub"   x="400" y="226" text-anchor="middle">tview loop</text>
    <!-- terminal -->
    <rect class="cm-svg-box" x="540" y="186" width="160" height="52" rx="8"/>
    <text class="cm-svg-label" x="620" y="208" text-anchor="middle">terminal</text>
    <text class="cm-svg-sub"   x="620" y="226" text-anchor="middle">alt-screen</text>
    <!-- edges -->
    <text class="cm-svg-sub" x="285" y="56" text-anchor="middle">Events</text>
    <line x1="250" y1="66" x2="318" y2="66" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tu)"/>
    <line x1="400" y1="92" x2="400" y2="184" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tu)"/>
    <text class="cm-svg-sub" x="486" y="140" text-anchor="middle">QueueUpdateDraw</text>
    <line x1="480" y1="212" x2="538" y2="212" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tu)"/>
    <line x1="350" y1="186" x2="205" y2="94" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3" marker-end="url(#tf)"/>
    <text class="cm-svg-sub" x="238" y="150" text-anchor="middle">Prompter</text>
  </svg>
  <figcaption>Events flow down onto the single-writer tview loop; the prompter carries an operator's answer back up to the blocked run goroutine.</figcaption>
</figure>

## Rendering and hot-keys

Each `Events` callback maps to one or more `Line` values and appends them. Narration is rendered as markdown, then escaped; everything else is sanitized and wrapped in trusted per-kind color tags, so model text can never open a color or region tag. Tool output starts collapsed. The `z` and `Z` keys toggle folding of thinking and tool output as a global view mode, since a flat text view has no per-block cursor. The view tails the run like `tail -f`, re-arming follow when a scroll reaches the bottom, and `?` opens interactive help.

## The chat bar

With `--chat`, an input row is added above the status bar. At each turn boundary the agent calls `NextPrompt`, which activates the field and blocks the run goroutine until the operator submits or leaves. Enter sends, Alt-Enter inserts a newline, `Ctrl-D` ends cleanly, and `Ctrl-C` aborts. Slash commands like `/clear` and `/restart` are resolved before the text is sent.

## Keeping stdout pipeable

The full-screen UI collapses answers, narration, and traces into one viewport, so it must protect the alt-screen. `muzzleStderr` redirects `os.Stderr` into a buffer for the whole run so library logging cannot corrupt the display, then flushes it to the restored terminal on exit. The raw answer, warnings, rotated-session ids, and a crash stack when a run panics are captured as they arrive and re-printed to real stdout and stderr after teardown, so a piped answer still lands on stdout exactly as the line UI would deliver it and a stack is never lost to the alt-screen.

{{% notice style="warning" title="Load-bearing decision" %}}
When a run blocks on an operator decision, the prompter rings the terminal bell and recolors the status bar amber, unless `no_bell` is set. It rings only on the block transition, not while simply awaiting chat input, so an unattended run is noticed the moment it needs a human.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [MCP and A2A]({{% relref "interop" %}}) for serving the same tools to other clients and agents.
{{% /notice %}}
