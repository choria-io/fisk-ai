# The terminal

The terminal owns no run. Even a local `fisk run` hosts an agent behind an embedded broker and talks to it over a2a, so a terminal reaches its own agent the same way it reaches somebody else's.

{{% notice style="note" title="Where it lives" %}}
`internal/tui` holds the full-screen surface: `viewer.go` is the shared viewport, `live.go` drives a running agent, `prompter.go` is the native prompter, `callline.go` renders a tool call, `splash.go` the startup card. `package main` holds command registration and the two client surfaces, `run_chat.go` and `run_client.go`, with `run_render.go` as the single rendering point.
{{% /notice %}}

## From the command to the screen

<ol class="cm-steps">
  <li><b>Decide the shape</b> A NATS context turns the process into a pure client. Without one it hosts. That single switch decides how much configuration the run needs.</li>
  <li><b>Resolve telemetry before anything is opened</b> A bad endpoint then fails on a readable terminal rather than behind the alternate screen.</li>
  <li><b>Open one session store</b> Shared by the hosted agent that journals into it and the channel that reads back from it.</li>
  <li><b>Start the agent before taking the screen</b> A worker that cannot start says so on a terminal somebody can read.</li>
  <li><b>Probe the agent card</b> One round trip with a short deadline. No responders is fatal, since nothing is serving that identity; any other failure returns no card and no error.</li>
  <li><b>Run the view</b> Full screen when stdin and stdout are both terminals and nothing disabled it, otherwise the line surface.</li>
</ol>

On exit the terminal is restored and everything is reprinted to the normal buffer so it survives in scrollback: warnings, the handles of conversations a reset walked away from, the answer to stdout, and the resume hint, usage and trace lines to stderr.

## One rendering, three sources

<figure class="cm-diagram">
  <svg viewBox="0 0 760 230" role="img" aria-label="A live reply set and a stored session rendered through one function into two surfaces">
    <defs>
      <marker id="tui-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="40" width="180" height="50" rx="8"/>
    <text class="cm-svg-label" x="110" y="63" text-anchor="middle">live reply set</text>
    <text class="cm-svg-sub" x="110" y="81" text-anchor="middle">a2a blocks</text>
    <rect class="cm-svg-box" x="20" y="110" width="180" height="50" rx="8"/>
    <text class="cm-svg-label" x="110" y="133" text-anchor="middle">stored session</text>
    <text class="cm-svg-sub" x="110" y="151" text-anchor="middle">same blocks</text>
    <line x1="200" y1="65" x2="274" y2="93" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tui-ah)"/>
    <line x1="200" y1="135" x2="274" y2="117" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tui-ah)"/>
    <rect x="280" y="70" width="190" height="70" rx="10" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="375" y="100" text-anchor="middle" style="fill:var(--cm-accent)">blockRenderer</text>
    <text class="cm-svg-sub" x="375" y="120" text-anchor="middle">one appearance</text>
    <line x1="470" y1="93" x2="544" y2="65" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tui-ah)"/>
    <line x1="470" y1="117" x2="544" y2="135" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tui-ah)"/>
    <rect class="cm-svg-box" x="550" y="40" width="190" height="50" rx="8"/>
    <text class="cm-svg-label" x="645" y="63" text-anchor="middle">live viewport</text>
    <text class="cm-svg-sub" x="645" y="81" text-anchor="middle">full screen</text>
    <rect class="cm-svg-box" x="550" y="110" width="190" height="50" rx="8"/>
    <text class="cm-svg-label" x="645" y="133" text-anchor="middle">printTranscript</text>
    <text class="cm-svg-sub" x="645" y="151" text-anchor="middle">plain output</text>
    <text class="cm-svg-sub" x="380" y="200" text-anchor="middle">one line list drives both, so a stored run reads like a live one</text>
  </svg>
  <figcaption>Every conversation this program draws goes through the block renderer.</figcaption>
</figure>

Line kinds carry the prefixes that make the line surface and the full-screen viewport read the same: the arrow for a tool call, the reversed arrow for a result, the plain word for a warning. A tool result unwraps its command envelope, keeps a non-zero exit, and renders a silent success explicitly rather than as a blank.

With no terminal the plain surface drops tool output rather than folding it, since a text dump has nothing to unfold with.

Similarly, a tool call line is rendered from what the call is, not from what any one surface received, so live, streamed and journal-replayed runs produce the same line. Arguments are sorted, because a decoded object has no order and a line that changed between renderings would read as two calls.

{{% notice style="warning" title="Load-bearing decision" %}}
Model text goes through two steps before it can be drawn: terminal escapes are stripped, then literal brackets are neutralized so the text cannot open a color or region tag. Only after that are the trusted per-kind tags wrapped around it.
{{% /notice %}}

## Chat and the line surface

| | Full screen | `--no-tui` or no terminal |
|---|---|---|
| Turns | A loop: prompt, turn, input row, next turn | Exactly one |
| Empty prompt | Opens the input row | An error naming the two ways out |
| Resume replay | 500 blocks, at or above the worker's cap | 40, because nobody reads scrollback upwards |
| Interrupt | A key event, since the view holds raw mode; first press suspends at a boundary, second leaves | First sends a cancel message, second stops |
| Thinking | Folded by default | Printed inline with `--thinking` |

There is no `--chat` flag. Chat is implicit whenever the full-screen view runs.

Cancellation is a message rather than a signal, which is why the stop request is sent from a goroutine: waiting for the ack on the draw loop would freeze the view and swallow the second press.

An answer held for a question the run outlived is delivered between the turn and the input row, never riding on the next prompt.

## The viewport

Lines, their plain text and their rendered markup stay index-aligned one to one, so search can address a line by index and rendering can replace only the new one. Search is authoritative over folding: a match inside folded content reveals it. Copying is not: folded content is left out rather than sent as its placeholder, because folding it says the reader is not reading it.

Folding applies to thinking only above a row estimate, and to tool output unconditionally when it is on. Tail-follow re-arms for the mouse, End and G but not for Down or Page Down, so reaching the bottom by any means behaves like following a file.

The key binding drops the scrollback and keeps the conversation; the slash command drops the conversation and keeps the scrollback.

The startup card is a single opaque text view rather than a flex layout, because a flex leaves its background unfilled and the transcript would bleed through. Its telemetry row has three states, since an agent that did not answer must not look like one that exports nothing.

## The live view

The status bar is a small state machine: running, blocked, suspending, suspended, complete, aborted, error, awaiting input. Awaiting input is green and distinct from the amber block, and the state word survives on a monochrome terminal. Elapsed time is deliberately absent, because it kept climbing through idle input waits.

Four token counters are kept apart because a caller reports them apart and a resume seeds them; the bar renders their sum, since the budget counts cache reads and writes at full weight. Thinking tokens are tracked and never shown.

Standard error is redirected into a buffer for the whole run and flushed to the restored terminal afterwards, so SDK and library logging cannot draw onto the alternate screen. That is also why the debug flags write fixed-name files rather than to stderr, each created exclusively after an unlink so a planted symlink is dropped rather than followed.

Teardown ordering is owned by one goroutine so nothing marshals onto a stopped loop, and the screen restore is idempotent, because the framework calls it on stop and again from its own panic recovery while the viewer defers one of its own.

{{% notice style="warning" title="Load-bearing decision" %}}
An aborted prompt records no decision. A checkpointed run would otherwise replay an answer the operator never gave, on every resume.
{{% /notice %}}

Only one question is put at a time, whichever surface asks, because the contended resource is the terminal rather than either widget. In the full-screen prompter the modal owns its keys, but the interrupt still reaches the run, so leaving is always possible with a prompt up. In the line surface the approval list puts No first, so a reflexive Enter declines.

## Telling a multiplexer what is happening

`internal/multiplex` reports whether the run is working, idle, or blocked on a decision, so a multiplexer arranging agents in panes can show which one wants a person. It cannot tell any of that from the pane's output, so the agent says it.

Reporting is best effort and never fails a run. Every call returns before the report is sent, a newer report supersedes an unsent older one, a failed delivery is dropped, and the sender recovers its own panics, because a panic there would take the process down with the terminal still in raw mode.

The pane is labeled with the agent's identity rather than the program name, since somebody watching six panes is watching six agents. Detection reads the environment and claims the process for the first multiplexer that named itself. Outside a pane no multiplexer claims it, and the caller installs the same option either way.

The hooks are driven from what the a2a client already sees. Working fires on prompt submission rather than on the turn being accepted, because an agent under load can take seconds to ack and the pane would ask for a person while the work is already on its way.

## Reserved

The short form of a tool call line is infrastructure waiting for a wire field. The renderer sets it equal to the full text, so the fit test can never choose it; the pre-elided form exists inside the agent but the protocol's tool-call block carries only the name and input.

The live status bar's session segment never renders, because the run command sets no title. The header's chat marker described in the source does not exist. Stale references to the removed `--chat` flag survive in three comments. The multiplexer detector table has one entry, and the blocked-reason plumbing is general but fed only by the question path.

{{% notice style="tip" title="Next" %}}
Continue to [Reference]({{% relref "reference" %}}) for the command surface and the source map, or [Serving]({{% relref "serving" %}}) for the agent on the other end of the wire.
{{% /notice %}}
