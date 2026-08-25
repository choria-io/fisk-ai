# Tool selection

Run the `fisk info` command to verify what tools the agent has access to:

```nohighlight
$ fisk info
╭───────────────────┬────────┬───────────────────────────────────────────────────────┬──────╮
│ TOOL              │ SOURCE │ DESCRIPTION                                           │ TAGS │
├───────────────────┼────────┼───────────────────────────────────────────────────────┼──────┤
│ say               │ local  │ Say something using the configured command            │      │
│ think             │ local  │ Think something using a cow                           │      │
│ ask_human_confirm │ local  │ Ask the human operator a yes/no question at the te... │      │
│ ask_human_select  │ local  │ Ask the human operator to choose one option from a... │      │
│ ask_human_input   │ local  │ Ask the human operator to type a free-text value a... │      │
╰───────────────────┴────────┴───────────────────────────────────────────────────────┴──────╯

Prompt:

  Tell short jokes using Cows!
...
```

The output shows the `say` and `think` tools and some Human in the Loop tools. When the configuration sets a model,
`fisk info` also prints a Model section first, listing the resolved model and provider, whether thinking is enabled,
and how tool search will behave, so you can confirm the backend and feature gates without starting a run.

## Application tags

The application can declare that the LLM never gets the `think` tool:

```yaml
  - name: think
    description: Think something using a cow
    type: exec
    tags: [ ai:deny ]
    # ...
```

Adding the `ai:deny` tag to a command means Fisk AI never exposes that tool to the LLM. `fisk info` confirms the LLM
only gets the `say` tool now.

## Agent configuration

The `agent.yaml` can also include only certain tools:

```yaml
include:
  tools:
    - ^say
```

Or exclude certain tools specifically:

```yaml
exclude:
  tools:
    - ^think
```

This uses regular expressions over the tool name, and both can be used together. For example, include `^cow` but exclude
`^cow_think`.

A tool's name is its command path joined with underscores, so a nested command like `cow think` becomes the tool
`cow_think`. Grouping commands and hidden commands are skipped and never become tools.

Tools can also be included or excluded by tag:

```yaml
exclude:
  tags:
    - scope:system
```

This excludes any command that has the `scope:system` tag.

## Global flags

A wrapped binary often has application-level global flags that apply to every subcommand. `nats`, for example, has
`--context` to select a stored connection profile, alongside sensitive globals such as `--user` and `--password`. By
default none of these are exposed to the model. `global_flags` is an allowlist of the globals you want the model to be
able to set per command:

```yaml
global_flags:
  - context
```

Each named global becomes an argument on every leaf command tool, so the model can run `nats stream ls` against a chosen
context without you hard-wiring one. Names are the long flag name, with or without the leading dashes, and are validated
against the binary's real global flags at load; a name matching none is an error. Hidden and framework flags (like
`--help`) cannot be exposed, and a global that clashes with a command's own flag or argument is skipped for that command.
A global the application marks required is always exposed, whether or not it is listed, since the command cannot run
without it.

Run `fisk info` to see which globals a binary exposes; it lists the application's global flags and marks the ones you
have allowlisted.

## Command tags

Fisk commands can carry tags, set in their fisk definition (or, for App Builder
applications, in YAML). Tags can be referenced by the `include`/`exclude` rules
to select commands by group, and the `ai:` prefix is reserved for the tags fisk
interprets. The full vocabulary is listed under
[Command tags](../../reference/#command-tags) in the Reference guide; the tags that
control how a command is exposed to the model are:

| Tag           | Description                                                                                                                                                           |
|---------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| `ai:deny`     | Never expose the command to the model; it is dropped before include/exclude and can never be added back.                                                              |
| `ai:no_defer` | Always send the command directly instead of deferring it behind the tool-search tool.                                                                                 |
| `ai:confirm`  | Require the operator to approve the command at the terminal before it runs; an "allow for the conversation" answer is remembered for that command for the rest of the conversation, across resumes of the conversation. |

The behavior tags (`ai:read_only`, `ai:destructive`, `ai:additive`,
`ai:idempotent`) describe what a command does rather than controlling it. They
reach the model and, over MCP, the client; they gate nothing.

A tag under the `ai:` prefix that fisk does not recognize does nothing, so it is
reported as a warning at startup and by `fisk info`.

`ai:deny` is the reliable way to keep a command the agent should never call out of
reach, since it applies before any `include`/`exclude` rule. `ai:no_defer` keeps the
handful of commands the model needs on most requests immediately available rather
than discoverable only through tool search.

`ai:confirm` gates a command behind the operator's explicit permission. When the
model calls a command tagged `ai:confirm`, fisk pauses before running it and
prompts the operator at the terminal, showing the resolved command line with its
arguments, and offers three choices: run it once, run it and stop asking for that
command for the rest of the conversation, or decline. Declining returns an
authoritative result to the model (the command is not run and the model is told
the decision is final), so it stops rather than working around the refusal. An
"allow for the conversation" answer is remembered **by command, regardless of its
arguments**: once you bless `stream rm`, every later `stream rm` call runs without
asking again, so reserve that choice for a command you trust the agent to repeat.

The conversation records the answer, so continuing it honors the answer rather than
asking again, and `fisk session show` lists what it holds. They are dropped by
`/clear` and by a `--force` resume across a changed configuration, and a resume with
no terminal attached declines a gated command rather than honoring one. The
prompt is rendered on stderr (so a piped final answer stays clean), the displayed
command line is stripped of terminal control sequences so model-supplied argument
values cannot spoof what you see, and it denies by default: no interactive terminal,
or a prompt that cannot be shown, declines rather than runs. An interrupt or an
end-of-input at the prompt ends the run rather than declining, since the operator did
not answer; the conversation stays continuable and asks again. Unlike
`human_in_the_loop`, the tag is always active: there is no configuration flag to
enable it.

The same gate can be extended to other tags with the `harness.confirm_tags`
configuration key: any tag listed there gates its commands exactly as `ai:confirm` does, which
lets an operator require confirmation for a tag the application already uses (for
example `ai:destructive`, or an application's own `impact:rw`) without editing the application. It is additive to the
always-on `ai:confirm` tag and matching is exact rather than a regex. A
`confirm_tags` entry that matches no loaded command is reported as a warning at
startup, since a typo would otherwise leave a command ungated. The approval prompt
names the tag that gated the command, so you can tell why you are being asked. Run
`fisk info` to see each command's tags and which commands a run would gate. Like
`ai:confirm`, a `confirm_tags` tag gates both the agent loop and MCP, where it is
requested through elicitation.

Any other tags are free-form: they have no built-in meaning to fisk but can be
matched by the `tags` field of an `include` or `exclude` rule.

All of a command's tags, reserved and free-form alike, are also included in the
tool description fisk sends the model, as a trailing `Tags: ...` line, in both
the agent and over MCP. This lets your prompt reference them, for example "always
use `ask_human_confirm` before running any command
tagged `impact:rw`". The human-facing `fisk info` listing keeps the plain
description.
