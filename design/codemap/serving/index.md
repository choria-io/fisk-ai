# Serving

A served agent takes work from channels: peer agents over a2a, a work queue, or people in Slack threads. The server owns the run; a channel owns getting the work in and the answer out.

{{% notice style="note" title="Where it lives" %}}
`internal/serve` is the host: `serve.go` holds the contract only, `server.go` the pool and lifecycle, `resources.go` the process-wide shared stores, `endpoints.go` the configuration-to-endpoint plumbing. The channels are `serve/a2aendpoint`, `serve/asyncjobs` and `serve/slack`. `internal/a2a` is the protocol, `internal/mcpserver` is a separate surface, and `internal/conns` owns NATS connection ownership.
{{% /notice %}}

## Capability by supply

`Channel` is two methods, `Name` and `Next`, and says nothing about transport. What a channel can do is what it fills in on the `Work` it hands over. A channel that can put a question to a human supplies a prompter; one that cannot leaves it nil. There is no separate capability enum to disagree with reality.

<dl class="cm-kv">
  <dt>Work.Done</dt><dd>Required, called exactly once, on a context that is not the run's, so a cancelled run still records what happened.</dd>
  <dt>Work.RunContext</dt><dd>Called once, after the slot is acquired and immediately before the run. It is the only signal a channel gets that its work started.</dd>
  <dt>Work.Budget</dt><dd>May only lower configured limits, because it comes from a caller the server does not control. The tool timeout is left alone however long it is, since both values come from whoever started the server.</dd>
  <dt>Outcome</dt><dd>Reason plus rejected, abandoned, crashed and the deferred calls, none of which a reason can express.</dd>
</dl>

<figure class="cm-diagram">
  <svg viewBox="0 0 760 290" role="img" aria-label="Three channels feeding one server pool that runs the agent and reports an outcome back">
    <defs>
      <marker id="srv-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
      <marker id="srv-ah2" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-faint)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="40" width="180" height="54" rx="8"/>
    <text class="cm-svg-label" x="110" y="63" text-anchor="middle">a2a prompts</text>
    <text class="cm-svg-sub" x="110" y="81" text-anchor="middle">peer agents</text>
    <rect class="cm-svg-box" x="20" y="110" width="180" height="54" rx="8"/>
    <text class="cm-svg-label" x="110" y="133" text-anchor="middle">asyncjobs</text>
    <text class="cm-svg-sub" x="110" y="151" text-anchor="middle">work queue</text>
    <rect class="cm-svg-box" x="20" y="180" width="180" height="54" rx="8"/>
    <text class="cm-svg-label" x="110" y="203" text-anchor="middle">slack</text>
    <text class="cm-svg-sub" x="110" y="221" text-anchor="middle">people in threads</text>
    <line x1="200" y1="67" x2="243" y2="67" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="200" y1="137" x2="243" y2="137" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="200" y1="207" x2="243" y2="207" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="245" y1="67" x2="245" y2="207" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="245" y1="137" x2="274" y2="137" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#srv-ah)"/>
    <rect x="280" y="80" width="180" height="115" rx="10" fill="color-mix(in srgb, var(--cm-accent) 14%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="370" y="115" text-anchor="middle" style="fill:var(--cm-accent)">Server</text>
    <text class="cm-svg-sub" x="370" y="138" text-anchor="middle">a slot pool per channel</text>
    <text class="cm-svg-sub" x="370" y="156" text-anchor="middle">Work in, Outcome out</text>
    <line x1="460" y1="137" x2="534" y2="137" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#srv-ah)"/>
    <rect class="cm-svg-box" x="540" y="105" width="180" height="64" rx="8"/>
    <text class="cm-svg-label" x="630" y="132" text-anchor="middle">agent.Run</text>
    <text class="cm-svg-sub" x="630" y="150" text-anchor="middle">one run per work</text>
    <path d="M630,169 L630,240 L370,240 L370,201" fill="none" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#srv-ah2)"/>
    <text class="cm-svg-sub" x="500" y="232" text-anchor="middle">Outcome, reported exactly once</text>
  </svg>
  <figcaption>Concurrency is per channel, because a channel that claims work before a run starts can only size its claiming to a limit it owns.</figcaption>
</figure>

The puller takes work first and acquires a slot second, deliberately not the reverse, so a slot is never parked on an idle channel. If the context dies while waiting for one, the work is reported abandoned rather than dropped.

## The three channels

| | a2a prompts | asyncjobs | Slack |
|---|---|---|---|
| Intake | A micro request, acked and handed over on a goroutine | The engine's handler, which blocks because returning is the ack | A socket envelope, decided in memory before the ack because of a three-second redelivery rule |
| Prompter | Only when elicitation is enabled | None. A queue has nobody to ask, so every gated tool is refused | Buttons and inputs in the thread |
| Events | Protocol blocks on the reply stream | None | Recorded as state, written by a rate-limited publisher |
| Faults | Reported, which drains the server | Not reported | Reported |
| Default workers | 1 | 1 | 5, because a Slack turn is a person waiting |

Session ids are always derived and never taken from a caller. All three hash their own inputs with a distinguishing prefix: a session id is not a secret, since it is logged and a deferred run's terminal message carries it, so handing the store caller-chosen bytes would put every journal within reach of anyone who learned an id. The conversation field on an incoming request is dropped rather than passed through for the same reason.

Follow-up mode may only be set by a channel whose deliveries never repeat. Slack asks the store whether it already holds the thread rather than keeping a map, because a map gives the wrong answer after a restart, and a follow-up mistaken for an opening turn discards the person's message.

## Asking a person

The server substitutes a default-deny prompter for a nil one, because the run and the confirm gate both call the can-prompt method unguarded.

The a2a channel and Slack ask in the same way. The question goes out, the answer comes back on a subject or a thread the channel already watches, and a minted question id routes it to the call waiting for it. Each keepalive from the answering side restarts the waiting window rather than extending it by a fixed amount, so a caller can hold a question open for as long as somebody is typing.

Silence resolves differently by tool: the three question tools get a deferral, and the confirm gate gets an abort. A deferred call is never dispatched again, while an abort leaves the gated call to be re-dispatched on resume. Both channels therefore hold the operator's approval separately, so the gate's second question on that resume is answered from what was already decided.

A Slack question outlives the turn that asked it, so a click days later becomes a resume turn rather than reaching a dead run. Delivery and giving up are one transition under one mutex, so a click landing exactly as the window closes is reported as one or the other and never both.

## Draining

Drain and stop are the same call at different times: close the hold once, then close every releasable channel and every service. The package registers no signal handler; the command layers the two-stage contract on top, where the first interrupt drains and the second cancels.

Each channel closes in an order chosen for what it must not lose. The a2a channel closes its shutdown gate first, so intake refuses rather than acking something nothing will run.

Slack waits for its intake to end, then edits the status message of every turn admitted but never started, since the server reports no outcome for work it never took. It waits for its posting goroutines so refusals still land, and closes the socket last, because turns already running are still receiving clicks.

The queue channel leaves one window open: the engine stores the answer after the handler returns, so a close landing in that gap costs one redelivery cycle. Not the work, since the session is already journaled.

A fault on any channel or service drains the server, because the drain unblocks a channel sitting in `Next`, and the error is returned so a supervisor restarts the worker.

## One protocol, one discriminator

Every a2a message is a flat, self-describing JSON object, and nothing in a body says what kind of message it is: the protocol id is the discriminator, and one id names exactly one shape with one schema. Each schema names its own required fields and refuses its siblings' by name.

Messages are validated on both sides in both directions. The client validates an outgoing message against the schema the receiver will use, so a message this agent could not have answered is refused here rather than arriving as a peer's validation failure. The size cap runs first, before any decode or allocation.

Unknown properties are accepted and discarded, an unknown stop reason is carried rather than refused, and an unknown event kind validates against a framing-only fallback and decodes into a block that keeps the peer's raw bytes. An unknown protocol id is rejected outright, because it decides what the message means and a receiver ignoring it would not know what it was ignoring.

{{% notice style="warning" title="Load-bearing decision" %}}
The engine dispatches from the protocol id in the body and never from the subject a message arrived on. Each subject carries exactly one message type, so a permission grant can cover one path without the others: tools without tasks, cancels without answers. An answer can approve a confirmation-gated command where a cancel only ends the run.
{{% /notice %}}

The request id is part of the cancel and elicit subjects, which is why its character set and length are constrained: a caller choosing those bytes freely would be shaping a subscription.

## The reply set

A task's reply is a set of messages, numbered from one, gap-free and monotonic per direction. The sequence advances only after a successful publish, so a message the sink refused reuses its number and the set stays gap-free. The size cap is enforced before the sink sees anything, which is why block text is trimmed rather than dropped: a dropped block would leave no gap for a caller to notice.

The ack is sequence one and goes out through the single-reply path, synchronously on the serving goroutine, so the transport measures the accept. Everything after it is published to the captured inbox. A refusal is always a negative ack followed by a terminal message, because the ack closes nothing and carries no code, and a caller holding only a refusing ack would wait to its own deadline.

Ownership passes between goroutines rather than being shared: the ack on the intake goroutine, events on the run goroutine, the terminal message from whoever reports the outcome. One owner at a time, no lock.

The cancel and elicit subscriptions are opened before the ack, because a cancel arriving at nobody is unfixable where a cancel arriving early merely waits.

A cancel does not cancel the run's context. Somebody asking to stop is asking for a conversation they can continue rather than a turn that ended half done, so the run parks at its next boundary and ends suspended. Closing the stop gate does give up any question in flight, since a question is not a boundary.

The limit is on idle time rather than on the call, because a run may think for a long time and not be stuck. A configured value is floored at three keepalive intervals.

## The MCP server

`internal/mcpserver` is not part of the serve tree and is not a channel. It is its own command with its own listener, its own concurrency limit and its own confirm policy, serving the same tool values with no model in the loop. It has no prompter: a tool that asks a question is denied, and a deferral is refused as a call the surface cannot carry. Human approval there is MCP elicitation, which fails closed on anything that is not an explicit accept.

The outbound MCP client runs on the serve path. Sessions are built once in the shared resources and reused by every run, so a stdio child starts once per worker rather than once per job. A run never opens or closes them, the resource close releases them first, and a drain deliberately leaves them alone.

## The local run takes the same path

A terminal running `fisk run` without a NATS context still hosts an agent behind an embedded in-process broker and talks to it over a2a. A terminal reaches its own agent the same way it reaches somebody else's, which makes the local path the one everybody exercises.

The hosted configuration is synthesized: exposure is replaced with a prompts-only block at one worker with elicitation on, so hosting a run at a terminal registers no micro service and takes no jobs. Only the channel gets the in-process connection; the stores and remote tools keep the connection the configuration named.

Nothing provisions storage. The queue, the task store, the session stream and the memory bucket are the operator's to create, so a cluster nobody prepared fails at startup rather than being laid out by whichever worker started first.

## Reserved

Several fields are carried and deliberately not acted on. `Outcome.Rejected` is always false because the caller name is recorded rather than enforced, and both consumers already handle it. `Work.PromptWait` and the bounded prompter have no in-repo user; they stay for an embedder's channel that can reach an operator but cannot tell whether one is still there. `Caller.Verified` exists because no binding authenticates a publisher yet: NATS authenticates the connection to the server, not the publisher to the subscriber, so subject permissions are the whole of the access control.

On the protocol side, the must-understand flag, the parent header for multi-hop delegation, the instance half of an identity and the agent-call block are all defined, schema'd and unproduced. Two transport operations exist only to be named, and the second transport binding the interface exists for is not present.

No served run reaches an interactive turn boundary, because the serve path supplies no continuation. The startup banner is the one place the endpoint-agnostic layering is broken on purpose: it reaches each channel's description by concrete type assertion, since the three return types were never unified.

{{% notice style="tip" title="Next" %}}
Continue to [Telemetry]({{% relref "telemetry" %}}) for how a trace crosses these processes, or [The terminal]({{% relref "terminal" %}}) for the client on the other end.
{{% /notice %}}
