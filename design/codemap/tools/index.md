# Tools and Introspection

The tool model is the center of the whole system. Fisk AI never lets a model run arbitrary commands. It introspects a fisk application once, turns its runnable commands into named tools, and then decides which of those tools a model is even allowed to see.

{{% notice style="note" title="Where it lives" %}}
`internal/util`: introspection and execution in `fisk.go`, translation to the Anthropic API in `anthropic.go`, the load pipeline in `util.go`. The `agent.yaml` schema is in `config/config.go`. The `fisk-ai info` command in `info_command.go` prints the resolved set without calling the model.
{{% /notice %}}

## From a command tree to a tool list

`ToolsForApp` execs the target binary with `--fisk-introspect` and unmarshals the emitted `fisk.ApplicationModel` (`fisk.go:696`, `fisk.go:714`). `ApplicationTools` then walks the command tree with `commandTools` (`fisk.go:558`, `fisk.go:584`). The walk keeps only runnable leaf commands: a hidden command is skipped, and a grouping node with subcommands is recursed into but is not itself a tool. Every surviving leaf becomes one `Tool` bound to its `fisk.CmdModel`, the source of truth for the command's schema and tags.

A tool's name is its command path joined with underscores. The command `auth user add` becomes the tool `auth_user_add` (`Tool.Name`, `fisk.go:103`). That name is what the model addresses, what include and exclude rules match against, and what a remote import prefixes with its alias.

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
  <figcaption>The load pipeline in `util.LoadTools`. The deny strip runs before any rule, so a denied tool can never be selected back in.</figcaption>
</figure>

## Reserved tags

Three tags carry meaning to Fisk AI itself. The rest are free-form and can only be referenced by include and exclude rules. Tags are set in the fisk command definition, or in YAML for an App Builder application.

| Tag           | Effect                                                                                                                  |
|---------------|-------------------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never exposed. Stripped before any include or exclude rule, so it can never be added back (`fisk.go:638`).              |
| `ai:no_defer` | Always sent to the model directly rather than deferred behind tool search (`anthropic.go:91`).                          |
| `ai:confirm`  | Requires operator approval before the command runs. Covered in [Safety and Human in the Loop]({{% relref "safety" %}}). |

## Selecting tools

`LoadTools` is the pipeline (`util.go:221`). It always runs one filter pass first with a nil filter, which strips `ai:deny` and nothing else, so deny is enforced even when no rules are configured. Then, if `include` names any tools or tags, only matching tools are kept; if `exclude` names any, matching tools are dropped.

A rule's `tools` entries are regular expressions matched against the tool name. Its `tags` entries are exact tags, where an empty string matches a tool with no tags at all. A tool matches a rule if any name regex matches or any tag is present (`matchesFilter`, `fisk.go:666`). Include and exclude can be combined: include `^cow` but exclude `^cow_think`.

## Tool search deferral

Recent Anthropic models can search a large tool set on demand rather than receive every definition up front. Fisk AI uses this above a threshold. `BuildToolParams` counts local, remote, and built-in tools together; at ten or more (`toolSearchThreshold`, `anthropic.go:21`) it marks tools for deferred loading and appends the BM25 tool-search server tool. Below the threshold every tool is sent directly and no search tool is added.

A tool tagged `ai:no_defer` is always sent directly even in a deferred set, which keeps the handful of commands a model needs on most requests immediately available. Because the model discovers deferred tools by their description, `ModelDescription` appends a trailing `Tags: ...` line so a tag search can find them (`fisk.go:126`).

{{% notice style="warning" title="Load-bearing decision" %}}
Tool schemas are sent but deliberately not marked strict. Grammar mode caps a tool at 24 optional parameters, which a broad command tree exceeds. The schema still constrains the model; it is just not enforced as a grammar (`anthropic.go:30`).
{{% /notice %}}

## Running a tool

`Tool.Execute` turns the model's JSON arguments into a command line with `t.Model.ArgsFromJSON`, the trust boundary that bounds input to the command's schema, then runs `exec.CommandContext` with the argument vector (`fisk.go:449`). No shell is involved, so model input can never be interpreted as a shell command. A non-zero exit is a normal result carrying the exit code, not an error, so the model can react to it. Output handling and credential stripping are detailed in [Safety and Human in the Loop]({{% relref "safety" %}}).

{{% notice style="tip" title="Next" %}}
Continue to [The Agent Loop]({{% relref "agent-loop" %}}) to see how these tools are called under a budget.
{{% /notice %}}
