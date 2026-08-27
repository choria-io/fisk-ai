# Configuration

Every command starts by reading `agent.yaml`. One file drives `run`, `serve`, `mcp`, `info`, `knowledge`, `discover` and `session`, so a bad entry fails the same way whichever command reads it.

{{% notice style="note" title="Where it lives" %}}
`config` is a single file, `config/config.go`, holding the `Config` struct and every nested block, the parser, the defaults, the validator and about seventy accessors. It imports the standard library, `fisk` for duration parsing, and a YAML library. Nothing else from the tree.
{{% /notice %}}

## Three stages, in order

`ParseConfigForMode` is the single funnel.

<ol class="cm-steps">
  <li><b>Unmarshal strictly</b> <code>DisallowUnknownField</code> makes a typo a hard error. <code>UseJSONUnmarshaler</code> fills every <code>json.RawMessage</code> block with canonical JSON, so a per-backend options block decodes identically whether the file was written as YAML or as JSON.</li>
  <li><b>Prepare</b> Derive the identity, normalize enumerated strings, parse every duration into a matching <code>...Parsed</code> field, and apply defaults. This step mutates in place.</li>
  <li><b>Validate for a mode</b> The checks that always run, then the ones the calling command needs.</li>
</ol>

<figure class="cm-diagram">
  <svg viewBox="0 0 760 240" role="img" aria-label="agent.yaml parsed, prepared and validated for one of three modes">
    <defs>
      <marker id="cfg-ah" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto"><path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/></marker>
    </defs>
    <rect class="cm-svg-box" x="20" y="50" width="140" height="56" rx="8"/>
    <text class="cm-svg-label" x="90" y="74" text-anchor="middle">agent.yaml</text>
    <text class="cm-svg-sub" x="90" y="92" text-anchor="middle">bytes on disk</text>
    <line x1="160" y1="78" x2="189" y2="78" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cfg-ah)"/>
    <rect class="cm-svg-box" x="195" y="50" width="150" height="56" rx="8"/>
    <text class="cm-svg-label" x="270" y="74" text-anchor="middle">unmarshal</text>
    <text class="cm-svg-sub" x="270" y="92" text-anchor="middle">unknown key fails</text>
    <line x1="345" y1="78" x2="374" y2="78" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cfg-ah)"/>
    <rect class="cm-svg-box" x="380" y="50" width="150" height="56" rx="8"/>
    <text class="cm-svg-label" x="455" y="74" text-anchor="middle">prepare</text>
    <text class="cm-svg-sub" x="455" y="92" text-anchor="middle">defaults, durations</text>
    <line x1="530" y1="78" x2="559" y2="78" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#cfg-ah)"/>
    <rect x="565" y="50" width="175" height="56" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="652" y="74" text-anchor="middle" style="fill:var(--cm-accent)">ValidateForMode</text>
    <text class="cm-svg-sub" x="652" y="92" text-anchor="middle">per command</text>
    <path d="M652,106 L652,140 L185,140" fill="none" stroke="var(--cm-faint)" stroke-width="2"/>
    <line x1="185" y1="140" x2="185" y2="164" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#cfg-ah)"/>
    <line x1="380" y1="140" x2="380" y2="164" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#cfg-ah)"/>
    <line x1="575" y1="140" x2="575" y2="164" stroke="var(--cm-faint)" stroke-width="2" marker-end="url(#cfg-ah)"/>
    <rect class="cm-svg-box" x="100" y="170" width="170" height="50" rx="8"/>
    <text class="cm-svg-label" x="185" y="190" text-anchor="middle">ModeMCP</text>
    <text class="cm-svg-sub" x="185" y="207" text-anchor="middle">no model, no prompt</text>
    <rect class="cm-svg-box" x="295" y="170" width="170" height="50" rx="8"/>
    <text class="cm-svg-label" x="380" y="190" text-anchor="middle">ModeAgent</text>
    <text class="cm-svg-sub" x="380" y="207" text-anchor="middle">identity, prompt, model</text>
    <rect class="cm-svg-box" x="490" y="170" width="170" height="50" rx="8"/>
    <text class="cm-svg-label" x="575" y="190" text-anchor="middle">ModeServe</text>
    <text class="cm-svg-sub" x="575" y="207" text-anchor="middle">checked per endpoint</text>
  </svg>
  <figcaption>One parser, three validation modes. The mode is chosen by the command, not written in the file.</figcaption>
</figure>

## What each mode requires

| Mode | Used by | Requires |
|---|---|---|
| `ModeMCP` | `mcp`, `info`, `knowledge`, `discover`, `session`, and `run` against a remote worker | The always-on checks and nothing more |
| `ModeAgent` | `run` hosting its own agent | `llm.model`; identity and system prompt unless the run is MCP-only; a NATS context alongside `remote_tools` or jobs |
| `ModeServe` | `serve` | Per endpoint. Jobs, Slack and a2a prompts each need a named identity, a prompt and a model; `a2a.serve_tools` needs an application path and a named identity |

The always-on checks cover what any command pays for getting wrong: identity charset, `global_flags` without an `application_path`, remote tool hosts, and every `mcp_clients` entry.

`ModeMCP` is the most lenient on purpose. `fisk info` parses in it so it can report on a configuration it could not run.

## Zero is not one answer

A duration of `0s` means something different in each block.

| Key | `0s` means |
|---|---|
| `harness.tool_timeout` | Unbounded. The operator asked for no limit |
| `mcp_clients[].timeout` | Rejected. An unlimited startup holds the whole run against a server that never answers |
| `expose.agent.a2a.request_timeout` | Rejected. The transport reads a non-positive value as its own shorter default, so `0s` would shorten the wait rather than remove it |
| `expose.agent.slack.answer_grace` | Rejected. It would defer every question the instant it was asked |
| `llm.budget.call_timeout` | Rejected. It reaches the provider as a deadline already in the past, and every call fails with a context error that names nothing |

`telemetry.sample_ratio` is a `*float64` because an explicit `0` would otherwise arrive as the Go zero value, be defaulted back to 1.0, and send every trace to a paid backend. `llm.thinking` is a pointer for three states: absent says nothing to the provider, `{enabled: true}` asks for thinking, `{enabled: false}` asks for it off.

Switches that default on are spelled negatively for the same reason: `no_tui`, `no_bell`, `no_prompt_cache`, `no_tool_search`, `no_metrics`, `no_index`, `no_progress`. An absent bool unmarshals to `false`, so a positive `metrics: true` could not be told apart from an unset one.

## Where a mistake lands

| Stage | Examples |
|---|---|
| Parse | An unknown key, a malformed or out-of-range duration, a negative budget, an illegal identity or alias, a duplicate MCP server name, an `mcp_clients` entry with neither or both transports, a `${VAR}` syntax error, a `builtins` entry that is not exposable |
| Command startup | `fisk mcp` with no `expose.agent.mcp` block, `fisk serve` with no endpoint enabled, a `knowledge` subcommand with knowledge disabled, telemetry endpoint resolution, `${VAR}` resolution at connect, a missing NATS stream or bucket |
| Run | A provider name that no linked backend answers to, a reasoning effort the model rejects on the first call, embeddings settings validated when the knowledge store opens |

A `confirm_tags` entry that matches no loaded tool is the one case that produces a warning rather than a failure, because the tool set is only known after introspection.

{{% notice style="warning" title="Load-bearing decision" %}}
The package never reads the environment. `ExpandEnvReferences` takes a lookup function instead of calling `os.LookupEnv`, because the commands that parse a configuration are not the commands that connect. `${VAR}` syntax is checked at parse time and resolved at connect time, so `fisk info` can describe a file whose secrets are not present.
{{% /notice %}}

{{% notice style="warning" title="Load-bearing decision" %}}
Identity is the name other agents send traffic to. When it is derived from the application basename rather than written by an operator, `identityDerived` records that and `IdentityIsNamed()` reports it. Every serving path requires a named identity, because a fleet of unrelated agents built from one shared executable would otherwise register under a single name and answer each other's traffic. For Slack, identity is also the first field hashed into a thread's journal.
{{% /notice %}}

## Credentials

Secrets stay out of the file and out of tool subprocesses.

<dl class="cm-kv">
  <dt><code>${VAR}</code> references</dt><dd>Recognized in <code>mcp_clients</code> <code>env</code>, <code>headers</code> and <code>url</code>. A value in <code>command</code> or <code>args</code> is taken literally.</dd>
  <dt><code>CredentialEnvNames()</code></dt><dd>Strips OTLP credential variables from every tool subprocess whether or not this agent enables telemetry. These are ambient operator variables, a tool never needs them, and gating on config would mean <code>--no-telemetry</code> puts the token back into every tool subprocess.</dd>
  <dt><code>RedactURL</code></dt><dd>Applied to anything that prints an MCP endpoint. It leaves the path intact, and some hosted MCP providers put the credential in the path.</dd>
</dl>

Slack credentials are absent from the file entirely and come from `SLACK_APP_TOKEN` and `SLACK_BOT_TOKEN`.

## Two blocks to read carefully

`expose.agent.tools` narrows only MCP and `a2a.serve_tools`, the two endpoints that hand a caller this agent's tools. It does not narrow `jobs`, `slack` or `a2a.prompts`, which hand the agent a whole unit of work and run the full loop over the top-level tool selection.

`llm.budget.max_tokens` counts tokens processed. It weights a cache read the same as an uncached input token although the two are priced differently, and its 500000 default was chosen to stay out of the way of ordinary use. It does not cap spend.

## Not yet wired

`remote_agents` is declared on `Config` and read by nothing. Working remote-agent functionality is `remote_tools`. `A2ATransport()` returns the constant `nats`, with the config key deferred until a second transport exists.

{{% notice style="tip" title="Next" %}}
Configuration decides what the run may do. Continue to [The agent loop]({{% relref "agent-loop" %}}) for what it then does, or [Tools and introspection]({{% relref "tools" %}}) for how the selection becomes a tool set.
{{% /notice %}}
