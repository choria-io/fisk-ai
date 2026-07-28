# Configuration

A Fisk AI agent is one YAML file. The same file drives `run`, `mcp`, and `a2a`, and each command validates only the parts
it needs. The design goal throughout is that an operator mistake fails at startup with a message naming the fix, never at
the first tool call.

{{% notice style="note" title="Where it lives" %}}
`config` is a single file, `config.go`. It is pure data with no IO beyond reading the file, and it imports no internal
package. That is deliberate: `config` is the lowest layer, so `main` and every `internal` package can import it.
{{% /notice %}}

## One file, three modes

<figure class="cm-diagram">
  <svg viewBox="0 0 760 300" role="img" aria-label="One config file parsed under three validation modes">
    <defs>
      <marker id="cf-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="120" width="160" height="56" rx="8"/>
    <text class="cm-svg-label" x="100" y="144" text-anchor="middle">agent.yaml</text>
    <text class="cm-svg-sub" x="100" y="161" text-anchor="middle">literal, no interpolation</text>
    <rect x="240" y="120" width="230" height="56" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="355" y="144" text-anchor="middle" style="fill:var(--cm-accent)">parse, prepare, validate</text>
    <text class="cm-svg-sub" x="355" y="161" text-anchor="middle">unknown keys are fatal</text>
    <line x1="180" y1="148" x2="234" y2="148" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cf-ah)"/>
    <rect class="cm-svg-box" x="530" y="26" width="210" height="56" rx="8"/>
    <text class="cm-svg-label" x="635" y="50" text-anchor="middle">ModeAgent</text>
    <text class="cm-svg-sub" x="635" y="67" text-anchor="middle">run: model and prompt</text>
    <rect class="cm-svg-box" x="530" y="120" width="210" height="56" rx="8"/>
    <text class="cm-svg-label" x="635" y="144" text-anchor="middle">ModeMCP</text>
    <text class="cm-svg-sub" x="635" y="161" text-anchor="middle">mcp, info: tools only</text>
    <rect class="cm-svg-box" x="530" y="214" width="210" height="56" rx="8"/>
    <text class="cm-svg-label" x="635" y="238" text-anchor="middle">ModeServer</text>
    <text class="cm-svg-sub" x="635" y="255" text-anchor="middle">a2a: app and nats</text>
    <path d="M470,148 L500,148 L500,54 L524,54" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cf-ah)"/>
    <line x1="470" y1="148" x2="524" y2="148" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cf-ah)"/>
    <path d="M470,148 L500,148 L500,242 L524,242" fill="none" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cf-ah)"/>
  </svg>
  <figcaption>The mode selects which required fields apply. Structural checks run in all three.</figcaption>
</figure>

`ModeMCP` is the most permissive: it serves tools and needs neither a prompt nor a model. That is exactly why `fisk info`
parses in it. Requiring a model would reject a valid MCP-only config that `info` exists to inspect.

## Loading is strict on purpose

Parsing is one call with three consequential options:

```go
yaml.UnmarshalWithOptions(data, cfg, yaml.DisallowUnknownField(), yaml.UseJSONUnmarshaler())
```

`DisallowUnknownField` makes every unknown key a hard parse error, including a harness setting mistakenly left at the top
level. That case has its own test.

`UseJSONUnmarshaler` makes the parser populate the `json.RawMessage` fields, `harness.memory.options` and
`harness.sessions.options`, with canonical JSON. A backend can then decode its own sub-block against a typed schema
regardless of the source format. It is also why an unknown key inside an options block fails just as loudly, only at store
construction time rather than at parse time.

Every field carries both `json` and `yaml` tags even though only YAML is read today, and the parse path is routed through
canonical JSON deliberately, so a JSON config or a schema-driven editor is a small step rather than a rewrite.

## Defaulting and normalization

`prepare()` runs before validation, in a fixed order.

<ol class="cm-steps">
  <li><b>Identity fallback</b> Explicit value, else the basename of <code>application_path</code>, else <code>fisk-ai</code>.</li>
  <li><b>Normalize confirm tags</b> Trim, drop empties, de-duplicate, preserve first-seen order. Trimming is a safety fix: a trailing space would silently fail to match a real command tag.</li>
  <li><b>Normalize global flags</b> Trim, strip leading dashes so <code>--context</code> and <code>context</code> both work, drop empties, de-duplicate.</li>
  <li><b>Prepare the MCP block</b> Validate <code>confirm_over_mcp</code>, validate the <code>builtins</code> allowlist, parse the tool timeout.</li>
  <li><b>Prepare the a2a block</b> The same limits pass, with its own path in every error message.</li>
  <li><b>Prepare the budget</b> Reject negatives, then default max tokens to 200000, iterations to 50, and the call timeout to 120s.</li>
  <li><b>Prepare embeddings</b> Default the timeout to 30s, else parse it and require a positive value.</li>
</ol>

Duration strings always come in pairs: a string field from YAML and a parsed `time.Duration` twin tagged `json:"-"` and
`yaml:"-"`. Parsing happens once, in `prepare`.

### Defaults that do not live in the config package

This matters when reading `config.go` and finding no default for something the documentation promises.

<dl class="cm-kv">
  <dt>top_k</dt><dd>Defaults to 5 with a ceiling of 20, in <code>internal/rag/store.go</code>.</dd>
  <dt>max_injected_tokens</dt><dd>Defaults to 6000, also in the rag store.</dd>
  <dt>knowledge.directory</dt><dd>Defaults to <code>knowledge/&lt;identity&gt;</code>, resolved by the rag store.</dd>
  <dt>memory directory</dt><dd>Defaults to <code>memory/&lt;identity&gt;</code>, resolved by the file backend.</dd>
  <dt>max_output_tokens</dt><dd>Defaults to 8192, raised to 16384 when thinking is on, in <code>internal/agent</code>.</dd>
  <dt>MCP port and address</dt><dd>Default to 8080 and 127.0.0.1, resolved in <code>mcp_command.go</code>.</dd>
</dl>

## Validation

Structural checks run in every mode. Mode-specific required fields come after.

| Rule | Reason |
|------|--------|
| `global_flags` with no `application_path` is an error | Global flags belong to the wrapped application and have nothing to attach to |
| A non-empty `identity` must match `^[a-zA-Z0-9_-]+$` | It doubles as a NATS queue group and appears in subjects, so whitespace, `.`, `*`, or `>` would form an invalid or wildcard-bearing subject |
| A remote tool host needs a name matching the same pattern | The name keys the NATS subjects |
| A remote host `alias` must match the pattern too | It prefixes imported tool names |
| A remote host `exclude.tags` filter is rejected outright | Discovery carries no tags, so the exclude could never be honored and would silently leave an unwanted tool imported |

The identity pattern is checked in every mode whenever the value is non-empty, not only where the field is required,
because the value may have been derived from a binary basename carrying a dot or a space.

An include-by-tag on a remote host is treated more gently than an exclude-by-tag: the import path warns and ignores it
rather than refusing, since over-inclusion is visible and under-exclusion is not.

Mode-specific requirements:

| Mode | Requires |
|------|----------|
| `ModeAgent` | `llm.model` always; `identity` and `system_prompt` unless also exposed over MCP; `nats_context` when `remote_tools` is set |
| `ModeMCP` | nothing beyond the structural checks |
| `ModeServer` | `application_path` and `nats_context` |

A2A requires an application because no built-in declares a2a exposure, so an application-less a2a server would start
with an empty tool set. The server itself can carry any tool kind; the requirement expires when a built-in first opts in.

Several rejections happen during `prepare` rather than validation, so they fire in all modes:

- `confirm_over_mcp` must be `auto`, `always`, or `never` after trimming and lowercasing. A typo must not silently select a
  weaker gate than intended.
- `expose.agent.mcp.builtins` may contain only `knowledge_search` and `knowledge_enumerate`, and the error names the
  accepted set and why the others are excluded. This is the selection, not the capability: the tool's own declaration is
  the ceiling and this can only narrow it, and naming one knowledge tool never selects the other. A non-empty allowlist
  with knowledge disabled is also rejected, since there would be nothing to serve.
- `max_concurrent_tools` rejects a negative value and anything above 1024. Zero is treated as unset rather than rejected,
  because an omitted YAML key unmarshals to zero and the server applies its own default.
- `tool_timeout` must parse and be non-negative, and the error names which block it came from since MCP and a2a share the
  helper.

## Overrides and precedence

There is no environment interpolation inside the YAML. The file is literal. Overrides are layered by the CLI.

| Setting | Precedence |
|---------|-----------|
| MCP port and address | flag, then config, then built-in default |
| TUI | Config `no_tui` is an absolute veto. The flag can only turn the TUI off, never on |
| `--state-dir` | Folded into the config object after parsing, so it wins over a configured directory |

`--state-dir` is the one flag written back into the config. Against a non-file session backend it is a hard error rather
than a silently ignored flag.

Flag-or-environment bindings such as `ANTHROPIC_API_KEY`, `FISK_AI_MCP_PORT`, and `FISK_AI_STORE_DIR` never enter the
config object at all. They travel alongside it into the agent's options or are resolved in the command.

Where the config does name an environment variable, it names it rather than holding a value. `api_key_env` is a variable
name, and `CredentialEnvNames()` returns those names so a tool subprocess's environment can be scrubbed of them.

## What `identity` silently controls

`identity` is more load-bearing than its one-line description suggests. It is the discovery name, the NATS queue group,
so multiple agents sharing it share work, and the namespace for on-disk state: `memory/<identity>` and
`knowledge/<identity>`.

Changing it orphans an existing memory store and knowledge index. Nothing warns about that, because nothing can tell an
intentional rename from a typo.

## From config to a running agent

`fisk run` does the following before `agent.Run` sees anything:

<ol class="cm-steps">
  <li><b>Validate flag combinations</b> <code>--resume</code> with <code>--checkpoint</code>, <code>--resume</code> with a query, <code>--name</code> without <code>--checkpoint</code>, and <code>--force</code> without <code>--resume</code> are all refused before any work happens.</li>
  <li><b>Resolve whether the run is checkpointed</b> This changes the interrupt contract, so it is decided first.</li>
  <li><b>Parse in ModeAgent</b> Then fold in <code>--state-dir</code>.</li>
  <li><b>Open the HTTP debug file</b> The CLI owns it: mode 0600, removed then reopened with <code>O_EXCL</code> to defeat a symlink planted at the fixed name.</li>
  <li><b>Peek the session's chat flag when resuming</b> So <code>--chat</code> need not be re-passed.</li>
  <li><b>Pick a UI</b> Chat with <code>--no-tui</code> is refused loudly rather than silently degraded.</li>
</ol>

The naming split in the knowledge feature is intentional and worth stating rather than fixing. The YAML key and the
user-facing noun are `knowledge`; the Go type is `RAGConfig`, the field is `Harness.RAG`, and the package is
`internal/rag`. Knowledge is the feature, RAG is the technique. The CLI mirrors it: the command is `knowledge` with `rag`
and `k` as aliases.

## The command tree

`main.go` is 78 lines and loads no configuration. Every command parses the file itself, in the mode it needs.

| Command | Mode | Purpose |
|---------|------|---------|
| `run` | `ModeAgent` | Runs the agent. Owns the largest flag set |
| `session ls`, `show`, `rm` | `ModeMCP`, or none | Inspects checkpointed journals. `--config` is optional |
| `info` | `ModeMCP` | Explains a config without contacting a model |
| `knowledge` and its thirteen subcommands | reads `harness.knowledge` | Builds and inspects the local index |
| `mcp` | `ModeMCP` | Serves tools over MCP |
| `a2a` | `ModeServer` | Serves tools to other agents over NATS |
| `discover` | reads `nats_context` only | Prints a remote agent's card |

`interruptContext` is the shared one-shot contract: SIGINT plus SIGTERM, so a server stops cleanly under systemd or a
container stop, and a second signal falls through to the default disposition. `run` deliberately does not use it, because
it layers a graceful-suspend contract on top.

`main.go` blank-imports the file session backend so the `session` subcommands, which construct a store directly, are
self-sufficient. The run path picks it up transitively through the agent package.

## Reserved and unused

- **`remote_agents` is entirely unused.** The field and its type are referenced nowhere outside `config.go` and its test.
  Nothing reads them and nothing validates them, unlike `remote_tools`, which is checked in every mode. `remote_tools` is
  the working feature.
- **`A2ATransport()` is a stub** returning the constant `"nats"`. A `transport` config field is deferred until a second
  transport exists, but the value is still routed through the transport registry, so adding one is a config field plus a
  blank import.
- **`ThinkingConfig` is a one-field struct on purpose**, so an effort knob can land without a breaking config change.
- **`Config.Harness` carries `omitempty` on a non-pointer struct**, which is a no-op for struct values. It is cosmetic.
- **MCP and a2a each get their own concurrency and timeout knobs** rather than sharing one, because the two bound
  different trust boundaries: anything that can reach a TCP port, possibly on a non-loopback address, versus NATS peers.
  Both are config-only, with no flag or environment override.
- **`tool_timeout` is named to avoid colliding** with `llm.budget.call_timeout`, which bounds a different unit of work.
- **`llm.budget.max_tokens` is a soft, deliberately over-counting cap.** It sums uncached input, cache reads, cache
  writes, and output, so with prompt caching on it over-states dollar cost by design. A cost-weighted budget is named as
  separate future work. It is also soft in timing, since the total is checked after each call.

{{% notice style="tip" title="Next" %}}
[The agent loop]({{% relref "agent-loop" %}}) picks up where parsing ends, and turns the validated config into behavior.
{{% /notice %}}
