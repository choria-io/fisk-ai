# Telemetry

Every other package reaches OpenTelemetry through one facade. `internal/telemetry` imports the standard library and OpenTelemetry and nothing else from this repository, which keeps it free of import cycles and forces its API to take primitives rather than domain types.

{{% notice style="note" title="Where it lives" %}}
`internal/telemetry` holds the provider and lifecycle (`telemetry.go`), configuration resolution (`config.go`), every span kind (`span.go`), the attribute catalogue and closed vocabularies (`attrs.go`), instruments (`metrics.go`), content capture (`content.go`) and propagation (`propagation.go`). `bootstrap` maps configuration in; `genai` renders content documents.
{{% /notice %}}

## Enablement is absolute

Resolution applies one precedence chain: an explicit off variable, then the caller's own disable label, then the SDK's disable variable, then on. The OTLP exporter variables never turn export on by themselves, so a host-wide collector endpoint cannot silently make every process on the box an exporter.

Validation runs only when export is enabled, so a stale endpoint in a file with telemetry off never fails a run. Content capture is forced off when export is off, because a privacy marker that overstates is as broken as one that understates.

<dl class="cm-kv">
  <dt>Settings</dt><dd>Primitives only. <code>SampleRatio</code> is a pointer because zero is meaningful, and <code>DisabledBy</code> is a label rather than a bool so the library never names a flag it does not own.</dd>
  <dt>Setting[T]</dt><dd>A value plus a display-only origin string, so <code>fisk info</code> can report every effective value alongside where it came from. Nothing branches on the origin.</dd>
  <dt>Provider</dt><dd>Nil-safe on every method. A disabled run holds a nil pointer and call sites never branch on it. It registers nothing globally: no global tracer provider, no global propagator.</dd>
  <dt>Delivery</dt><dd>Attempts against completions, tallied by exporters that wrap the real ones. OTLP is fire-and-forget, so a run whose every span was rejected would otherwise shut down with a nil error.</dd>
</dl>

The shutdown context is built fresh with its own flush timeout rather than derived from the caller's, so an interrupt cannot cancel the flush and discard the run worth reading.

## Spans around a run

The root span is started with its finish deferred immediately, before the panic barrier, so that defers unwind last-in-first-out and the root observes the error the barrier substituted. The startup span keeps a context of its own that is never assigned back over the run's, because doing so nested the whole run inside a span that ended at handoff.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 280" role="img" aria-label="Span nesting across the a2a boundary from a caller into a peer agent">
    <defs>
      <marker id="tel-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect x="20" y="30" width="345" height="200" rx="10" fill="none" stroke="var(--cm-faint)" stroke-width="2" stroke-dasharray="6 5"/>
    <text class="cm-svg-sub" x="192" y="52" text-anchor="middle">caller process</text>
    <rect class="cm-svg-box" x="45" y="62" width="295" height="44" rx="8"/>
    <text class="cm-svg-label" x="192" y="82" text-anchor="middle">invoke_agent</text>
    <text class="cm-svg-sub" x="192" y="97" text-anchor="middle">the run</text>
    <path d="M60,106 L60,138 L70,138" fill="none" stroke="var(--cm-faint)" stroke-width="2"/>
    <rect class="cm-svg-box" x="70" y="118" width="245" height="40" rx="8"/>
    <text class="cm-svg-label" x="192" y="136" text-anchor="middle">execute_tool alias</text>
    <text class="cm-svg-sub" x="192" y="151" text-anchor="middle">caller side</text>
    <path d="M85,158 L85,190 L95,190" fill="none" stroke="var(--cm-faint)" stroke-width="2"/>
    <rect class="cm-svg-box" x="95" y="170" width="220" height="40" rx="8"/>
    <text class="cm-svg-label" x="205" y="188" text-anchor="middle">invoke_agent peer</text>
    <text class="cm-svg-sub" x="205" y="203" text-anchor="middle">client kind</text>
    <line x1="315" y1="190" x2="419" y2="190" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#tel-ah)"/>
    <text class="cm-svg-sub" x="367" y="182" text-anchor="middle">traceparent</text>
    <rect x="400" y="30" width="340" height="200" rx="10" fill="none" stroke="var(--cm-faint)" stroke-width="2" stroke-dasharray="6 5"/>
    <text class="cm-svg-sub" x="570" y="52" text-anchor="middle">peer agent</text>
    <rect x="425" y="170" width="290" height="44" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="570" y="190" text-anchor="middle" style="fill:var(--cm-accent)">execute_tool name</text>
    <text class="cm-svg-sub" x="570" y="205" text-anchor="middle">server kind</text>
    <text class="cm-svg-sub" x="380" y="262" text-anchor="middle">a remote parent claiming not sampled is not followed</text>
  </svg>
  <figcaption>The message body carries the trace context, so a delegation reads as one trace.</figcaption>
</figure>

The sampler follows a remote parent that claims sampled, so a delegation stays one trace, but deliberately does not follow one claiming not sampled. The a2a body is unauthenticated, and any peer able to reach the subject could otherwise switch this process's recording off. Trace ids are adopted either way, so linkage is unaffected, and nothing reads the incoming trace id as evidence of who called.

A tool span covers the whole handling, from before the registry lookup through every one of the exits, so a call that never ran is still a span. A model call span is finished from one place on both paths. The HTTP middleware creates no span at all: it adds one event per attempt to the chat span it was handed by identity, which is why the caller appends it last, so it sits innermost.

## Two hard rules

{{% notice style="warning" title="Load-bearing decision" %}}
Nothing model-controlled reaches a span name or a metric attribute, and the error type stays inside a closed vocabulary. Span attributes cannot be set from outside the package; each span kind has one constructor owning its name and attribute set. `ErrorClass` is a struct rather than a string type, because a string type is convertible from any string and passing an error's own text would compile and put absolute paths on a span.
{{% /notice %}}

`span.RecordError` is never called anywhere, because it records a stack trace. A failing span gets a class from the calling package's own sentinels instead, and an unset class falls back to a generic value so a failure is never unfindable.

The same reasoning shapes the MCP server info type: it has two fields and no way to express a third, because a URL or a command argument carries credentials.

Conversation id, session id, tool call id, the model's requested tool name and anything derived from content are on no instrument. Exit code, memory location and the served-call sender are span attributes only.

## Metrics

The GenAI token and duration instruments come from the generated semantic-convention helpers, whose record calls take the required attributes positionally so the set cannot drift. Bucket boundaries are explicit for every instrument including those, because the SDK defaults are shaped for milliseconds and top out at ten thousand, which put every seconds-valued and token-valued observation into the first two buckets. That defect was invisible to every span assertion and was found by reading a real collector's decoded output.

Token usage is recorded as input and output only. The cache and reasoning tiers are already part of the totals they sit under.

A degraded knowledge search gets a counter rather than relying on its span, because spans are head-sampled and metrics are not. Session appends report a caller-measured duration rather than opening a span each, since a per-append span would double a run's span count.

## Content capture

Four gates stand between a conversation and a collector: the config block, which is off unless it says otherwise; validation, which refuses plain HTTP to a non-loopback host while capture is on because the payload is now the secret; the provider, which carries the resolved setting; and the span, which is the only place a content builder is ever invoked and returns before invoking any when capture is off.

Five attributes carry content: the system instructions on the startup span, input and output messages on a chat span, and tool arguments and result on a tool span. The system instructions are recorded as the very last thing before the loop starts, because the prompt is not final where it looks final.

Messages are exported as a delta from a moving index rather than the whole conversation each call, since capturing everything per call is quadratic and a thirty-iteration run would ship thirty copies. When trimming, the newest suffix is kept and leading tool responses whose calls were dropped go with them, because orphaned ids render as tool output attributed to calls that never happened. The budget counts JSON-escaped cost, six bytes for a control character or an invalid UTF-8 byte, since a limit measured on the Go string does not limit the attribute.

Two payloads never leave the process: a thinking block's signature and a provider block's raw JSON. Both are replaced with an explicit omitted marker, so their absence does not read as an instrumentation gap.

Capture also changes the export shape: the batch size is derived from the per-attribute limit and gzip is enabled, neither of which applies when capture is off.

## Conventions

GenAI span and metric attributes follow the last semantic-convention version that shipped them; the resource alone is built at a newer version, because merging refuses differing schema URLs and the SDK's own detectors use the newer one. Convention keys are imported at the use site and never transcribed, so a version bump breaks the build rather than drifting quietly. Everything else lives under a `fisk.` prefix rather than squatting on the GenAI namespace.

Some conventions are declined deliberately. The startup span is not `create_agent`, enumeration is not `retrieval`, and the retrieval query and document attributes are permanently unused: the query attribute is flagged sensitive in the conventions, the document attribute is corpus paths, and capture already exports the same data one span up.

## What an operator sees

With telemetry off, the bootstrap still returns a usable handle so callers never branch on nil, and if any exporter variable is set the command prints a note naming the variables and the switch responsible.

With telemetry on and nothing listening, OTLP is connectionless, so the run finishes normally. The counting exporters record attempts against deliveries and the first error, and the command prints how many of how many spans were delivered. An empty metric collection is dropped before the wire and not counted, because a collector answers 400 for a quiet run's periodic export and counting it would report an attempt for a run that recorded nothing.

The startup card and the run summary read capture off the provider rather than the configuration, because a veto, a rejected endpoint or an embedder's own provider makes those two disagree.

## Reserved

The embedder-facing constructor and its options have no caller in this repository; they exist for a caller that already runs OpenTelemetry. One metric label, the session backend name, is a variable rather than a closed vocabulary. Clamping the name against the registered backends and dropping an empty one would close it.

Only `run`, `mcp` and `serve` bootstrap telemetry. The indexing commands start none.

{{% notice style="tip" title="Next" %}}
Continue to [Serving]({{% relref "serving" %}}) for where the trace crosses a process, or [The agent loop]({{% relref "agent-loop" %}}) for what the spans wrap.
{{% /notice %}}
