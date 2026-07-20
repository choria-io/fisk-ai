+++
title = "Tools and Introspection"
weight = 30
description = "How a fisk command tree becomes the tool set an LLM may call, filtered by tags and rules."
+++

The tool model is the center of the whole system. Fisk AI never lets a model run arbitrary commands. It introspects a fisk application once, turns its runnable commands into named tools, and then decides which of those tools a model is even allowed to see.

{{% notice style="note" title="Where it lives" %}}
`internal/toolkit` defines the contracts: `tool.go` holds the interfaces, `schema.go` maps a JSON schema to the Anthropic form, `result.go` the shape a command tool returns. `internal/toolkit/fisk` introspects and runs the wrapped application: the command model and execution in `fisk.go`, the load pipeline in `load.go`, the interface methods in `fisk_tool.go`, the Anthropic wire form in `fisk_present.go`. `internal/toolkit/builtin` holds the tools Fisk AI implements itself. The `agent.yaml` schema is in `config/config.go`, and `info_command.go` prints the resolved set without calling the model.
{{% /notice %}}

## Three kinds, one interface

A tool is anything the model can call by name. Three kinds exist, and they are not interchangeable in origin, only in contract.

| Kind | Type | Comes from |
|------|------|------------|
| Command tool | `fisk.FiskCommandTool` | A leaf command in the wrapped fisk application |
| Built-in | `builtin.BuiltinTool` | Implemented in-process by Fisk AI: the `ask_human_*`, memory, and `knowledge_search` tools |
| Remote | `a2a.RemoteTool` | Imported from a peer agent over A2A |

All three satisfy `toolkit.Tool`, so the runner holds them in one `map[string]toolkit.Tool` and dispatches without knowing which kind it has. Kind-specific policy is deliberately kept out of that contract. Two narrow capability interfaces carry it instead, and the runner type-asserts on them: `ArgumentValidator` pre-checks required parameters, and `Confirmable` drives the approval gate. Only command tools implement either. A remote tool is not `Confirmable` because it carries no local tags, and the serving agent declines confirm-gated tools at its own end.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 216" role="img" aria-label="Three tool kinds satisfy one interface, with two optional capability interfaces beside it">
    <defs>
      <marker id="at" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- three kinds -->
    <rect class="cm-svg-box" x="18" y="16" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="108" y="38" text-anchor="middle">fisk.FiskCommandTool</text>
    <text class="cm-svg-sub"   x="108" y="54" text-anchor="middle">wrapped CLI command</text>
    <rect class="cm-svg-box" x="18" y="70" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="108" y="92" text-anchor="middle">builtin.BuiltinTool</text>
    <text class="cm-svg-sub"   x="108" y="108" text-anchor="middle">in-process</text>
    <rect class="cm-svg-box" x="18" y="124" width="180" height="44" rx="8"/>
    <text class="cm-svg-label" x="108" y="146" text-anchor="middle">a2a.RemoteTool</text>
    <text class="cm-svg-sub"   x="108" y="162" text-anchor="middle">peer agent</text>
    <!-- the interface -->
    <rect x="300" y="60" width="170" height="64" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="385" y="84" text-anchor="middle" style="fill:var(--cm-accent)">toolkit.Tool</text>
    <text class="cm-svg-sub"   x="385" y="102" text-anchor="middle">Name, InputSchema,</text>
    <text class="cm-svg-sub"   x="385" y="116" text-anchor="middle">ToolParam, ExecuteUse</text>
    <!-- capability interfaces -->
    <rect class="cm-svg-box" x="566" y="34" width="176" height="44" rx="8" stroke-dasharray="4 3"/>
    <text class="cm-svg-label" x="654" y="56" text-anchor="middle">ArgumentValidator</text>
    <text class="cm-svg-sub"   x="654" y="72" text-anchor="middle">command tools only</text>
    <rect class="cm-svg-box" x="566" y="106" width="176" height="44" rx="8" stroke-dasharray="4 3"/>
    <text class="cm-svg-label" x="654" y="128" text-anchor="middle">Confirmable</text>
    <text class="cm-svg-sub"   x="654" y="144" text-anchor="middle">command tools only</text>
    <!-- arrows into the interface -->
    <line x1="198" y1="38"  x2="298" y2="76"  stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#at)"/>
    <line x1="198" y1="92"  x2="298" y2="92"  stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#at)"/>
    <line x1="198" y1="146" x2="298" y2="108" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#at)"/>
    <!-- dashed links to capabilities -->
    <line x1="470" y1="80" x2="564" y2="58"  stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3"/>
    <line x1="470" y1="104" x2="564" y2="126" stroke="var(--cm-faint)" stroke-width="1.5" stroke-dasharray="4 3"/>
    <!-- caption of the type-assert -->
    <text class="cm-svg-sub" x="517" y="190" text-anchor="middle">runner type-asserts, absent capability means the step is skipped</text>
  </svg>
  <figcaption>One contract for dispatch, two optional capabilities for policy. The runner never switches on a concrete tool type to decide whether to validate or to ask.</figcaption>
</figure>

The rest of this page is about command tools, which are the only kind derived from something outside the program.

## From a command tree to a tool list

`ToolsForApp` execs the target binary with `--fisk-introspect` and unmarshals the emitted `fisk.ApplicationModel` through `FetchFiskAppModel`. `ApplicationTools` then walks the command tree with `commandTools`. The walk keeps only runnable leaf commands: a hidden command is skipped, and a grouping node with subcommands is recursed into but is not itself a tool. Every surviving leaf becomes one `FiskCommandTool` bound to its `fisk.CmdModel`, the source of truth for the command's schema and tags.

A tool's name is its command path joined with underscores. The command `auth user add` becomes the tool `auth_user_add`. That name is what the model addresses, what include and exclude rules match against, and what a remote import prefixes with its alias.

`ApplicationTools` fails outright if a leaf carries no precomputed schema, so an older fisk that cannot emit one produces an error rather than a set of silently empty tools.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 200" role="img" aria-label="A binary is introspected into leaf tools, the deny tag is stripped, rules select, and the result becomes API tool params">
    <defs>
      <marker id="ap" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- b1 -->
    <rect class="cm-svg-box" x="8" y="76" width="118" height="48" rx="8"/>
    <text class="cm-svg-label" x="67" y="98"  text-anchor="middle">fisk binary</text>
    <text class="cm-svg-sub"   x="67" y="114" text-anchor="middle">on disk</text>
    <!-- b2 -->
    <rect class="cm-svg-box" x="156" y="76" width="118" height="48" rx="8"/>
    <text class="cm-svg-label" x="215" y="98"  text-anchor="middle">leaf tools</text>
    <text class="cm-svg-sub"   x="215" y="114" text-anchor="middle">runnable cmds</text>
    <!-- b3 -->
    <rect class="cm-svg-box" x="304" y="76" width="118" height="48" rx="8"/>
    <text class="cm-svg-label" x="363" y="98"  text-anchor="middle">strip deny</text>
    <text class="cm-svg-sub"   x="363" y="114" text-anchor="middle">unconditional</text>
    <!-- b4 -->
    <rect class="cm-svg-box" x="452" y="76" width="118" height="48" rx="8"/>
    <text class="cm-svg-label" x="511" y="98"  text-anchor="middle">select</text>
    <text class="cm-svg-sub"   x="511" y="114" text-anchor="middle">include/exclude</text>
    <!-- b5 core -->
    <rect x="600" y="76" width="118" height="48" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="659" y="98"  text-anchor="middle" style="fill:var(--cm-accent)">tool params</text>
    <text class="cm-svg-sub"   x="659" y="114" text-anchor="middle">to the API</text>
    <!-- introspect label -->
    <text class="cm-svg-sub" x="141" y="66" text-anchor="middle">introspect</text>
    <!-- arrows -->
    <line x1="126" y1="100" x2="154" y2="100" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ap)"/>
    <line x1="274" y1="100" x2="302" y2="100" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ap)"/>
    <line x1="422" y1="100" x2="450" y2="100" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ap)"/>
    <line x1="570" y1="100" x2="598" y2="100" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#ap)"/>
  </svg>
  <figcaption>The load pipeline in `fisk.LoadTools`. The deny strip runs before any rule, so a denied tool can never be selected back in.</figcaption>
</figure>

## Reserved tags

Three tags carry meaning to Fisk AI itself. The rest are free-form and can only be referenced by include and exclude rules. Tags are set in the fisk command definition, or in YAML for an App Builder application.

| Tag           | Effect                                                                                                                  |
|---------------|-------------------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never exposed. Stripped in `FilterTools` before any include or exclude rule, so it can never be added back.              |
| `ai:no_defer` | Always sent to the model directly rather than deferred behind tool search, honored in `FiskCommandTool.Definition`.       |
| `ai:confirm`  | Requires operator approval before the command runs. Covered in [Safety and Human in the Loop]({{% relref "safety" %}}). |

## Selecting tools

`LoadTools` is the pipeline. It always runs one `FilterTools` pass first with a nil filter, which strips `ai:deny` and nothing else, so deny is enforced even when no rules are configured. Then, if `include` names any tools or tags, only matching tools are kept; if `exclude` names any, matching tools are dropped.

A rule's `tools` entries are regular expressions matched against the tool name. Its `tags` entries are exact tags, where an empty string matches a tool with no tags at all. A tool matches a rule if any name regex matches or any tag is present, decided by `matchesFilter`. Include and exclude can be combined: include `^cow` but exclude `^cow_think`.

`ServedTools` layers on top for the MCP and A2A faces. It starts from the loaded set and can only narrow it further through `expose.agent.tools`, never widen it, so a served surface is always a subset of what the agent itself may call.

Application global flags are resolved separately. `resolveExposedGlobals` fails loudly when an allowlisted name matches no global, or resolves to a framework or hidden flag, so a typo errors at load rather than quietly exposing nothing. A global that collides with a command's own flag or argument is dropped for that command, so the local one always wins.

## Tool search deferral

Recent Anthropic models can search a large tool set on demand rather than receive every definition up front. Fisk AI uses this above a threshold. `BuildToolParams` in `internal/util/anthropic.go` counts local, remote, and built-in tools together; at ten or more (`ToolSearchThreshold`) it marks tools for deferred loading and appends the BM25 tool-search server tool. Below the threshold every tool is sent directly and no search tool is added. Built-ins count toward the threshold but are never themselves deferred.

Deferral is offered only when the resolved provider supports tool search (`Caps.SupportsToolSearch`) and the operator has not disabled it with `llm.no_tool_search`; `BuildToolParams` takes that combined gate as its `toolSearchAllowed` argument and, when it is false, sends every tool directly regardless of count. When a set crosses the threshold but tool search cannot run, the run raises a warning (`WarnToolSearchUnsupported` or `WarnToolSearchDisabled`) naming the tool count and the cause, since sending the whole set on every request spends the context tool search exists to save.

A tool tagged `ai:no_defer` is always sent directly even in a deferred set, which keeps the handful of commands a model needs on most requests immediately available. Because the model discovers deferred tools by their description, `ModelDescription` appends a trailing `Tags: ...` line so a tag search can find them.

{{% notice style="warning" title="Load-bearing decision" %}}
Tool schemas are sent but deliberately not marked strict. Strict mode compiles every strict tool's schema into a grammar and caps the combined optional parameters across all of them at 24, which a broad command tree exceeds. The schema still constrains the model; only the grammar-enforced conformance guarantee is given up. See `AnthropicTool` in `internal/toolkit/fisk/fisk_present.go`.
{{% /notice %}}

A second schema decision sits beside it. `annotateOptional` in `internal/toolkit/schema.go` appends `(optional)` to the description of every non-required property, because models under-weight absence from `required` and tend to interrogate the operator for a parameter the command would have defaulted. It copies the schema rather than mutating it, since the same schema is reused on every request.

## Running a tool

`FiskCommandTool.Execute` turns the model's JSON arguments into a command line with `t.Model.ArgsFromJSON`, the trust boundary that bounds input to the command's schema, then runs `exec.CommandContext` with the argument vector. No shell is involved, so model input can never be interpreted as a shell command. A non-zero exit is a normal result carrying the exit code, not an error, so the model can react to it. Only a failure to start the binary or a canceled context is an error. Output handling and credential stripping are detailed in [Safety and Human in the Loop]({{% relref "safety" %}}).

{{% notice style="tip" title="Next" %}}
Continue to [The Agent Loop]({{% relref "agent-loop" %}}) to see how these tools are called under a budget.
{{% /notice %}}
