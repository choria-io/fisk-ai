# Tool selection

Fisk turns an application's commands into tools for the model, alongside the built-in tools the configuration enables.
`fisk info` shows the resulting set:

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

The output shows the `say` and `think` tools and the Human in the Loop tools. When the configuration sets a model,
`fisk info` also prints a Model section listing the resolved model and provider, whether thinking is enabled, and how
tool search will behave.

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

The filters cover every tool the agent gets, not only the application's commands: a [built-in](#built-in-tools) is
matched by its name, such as `memory_write`, and a tool imported from a [remote agent](../remote/) or an
[MCP server](../mcp-client/) by its final name, which is `alias_tool` when the importer prefixed it. Only a command
carries tags, so an `include` that lists tags alone removes every built-in and import; a pattern beside the tags names
one back.

## Built-in tools

Fisk has a set of built-in tools that are enabled under `harness.tools`:

```yaml
harness:
  tools:
    - name: base64_encode
    - name: read_file
      confirm: true
      options:
        root: /srv/corpus
        max_bytes: 1048576
```

Only listed tools are enabled. `confirm: true` requires user confirmation before each call. `options` holds the tool's
own settings; an unknown key is an error.

### read_file

`read_file` reads one regular file under a configured root. Its argument is `path`. It returns:

| Field               | Description                                                                          |
|---------------------|--------------------------------------------------------------------------------------|
| `path`              | the path as requested                                                                |
| `found` (boolean)   | `false` when the file does not exist                                                 |
| `size` (integer)    | the file size in bytes                                                               |
| `encoding`          | `utf-8` for text, `base64` for content that is not valid UTF-8                      |
| `content`           | the file content in the form `encoding` names                                        |

The `root` option defaults to `root_directory`, or the working directory when that is unset; a relative `root` is
resolved under `root_directory`. Paths are confined to the root: a leading slash is ignored, `..` cannot leave it, and
a symlink is followed only when its target is relative and stays inside. Only regular files are read.

`max_bytes` is the largest file the tool returns, 1 MiB when unset. A larger file is refused rather than truncated.

### base64_encode

`base64_encode` encodes its `text` argument as standard base64 and returns `{"encoded": "<base64>"}`. It takes no
options.

### Serving

Both tools are served over [MCP](../../mcp/) whenever `harness.tools` lists them and the `include`, `exclude` and
`expose.agent.tools` filters leave them in; `exclude: {tools: [^read_file$]}` under `expose.agent.tools` serves
`base64_encode` alone. [Confirmation over MCP](../../mcp/#confirmation-over-mcp) describes how `confirm: true` behaves
with MCP clients. Neither tool is served over a2a.

## Global flags

A wrapped binary often has application-level global flags that apply to every subcommand. `nats`, for example, has
`--context` to select a stored connection profile, alongside sensitive globals such as `--user` and `--password`. By
default none of these are exposed to the model. `global_flags` is an allowlist of the globals the model may set per
command:

```yaml
global_flags:
  - context
```

Each named global becomes an argument on every leaf command tool. Names are the long flag name, with or without the
leading dashes, and are validated against the binary's global flags at load; a name matching none is an error. Hidden
and framework flags such as `--help` cannot be exposed. A global that clashes with a command's own flag or argument is
skipped for that command. A global the application marks required is always exposed, whether or not it is listed.

`fisk info` lists the application's global flags and marks the allowlisted ones.

## Command tags

Fisk commands carry tags, set in their fisk definition or, for App Builder applications, in YAML. Any tag can be
matched by `include`/`exclude`. The `ai:` prefix is reserved for the tags fisk interprets; the full vocabulary is under
[Command tags](../../reference/#command-tags) in the Reference guide. The tags that control how a command is exposed
are:

| Tag           | Description                                                                              |
|---------------|------------------------------------------------------------------------------------------|
| `ai:deny`     | never expose the command; dropped before include/exclude and cannot be added back        |
| `ai:no_defer` | always send the command directly instead of deferring it behind the tool-search tool     |
| `ai:confirm`  | require the operator to approve the command before it runs                               |

The behavior tags `ai:read_only`, `ai:destructive`, `ai:additive` and `ai:idempotent` describe what a command does.
They reach the model and, over MCP, the client; they gate nothing. An `ai:` tag fisk does not recognize is reported as
a warning at startup and by `fisk info`.

When the model calls a command tagged `ai:confirm`, fisk prompts the operator at the terminal with the resolved command
line and offers three choices: run it once, run it and stop asking for that command for the rest of the conversation,
or decline. Declining tells the model the decision is final. An "allow for the conversation" answer is remembered by
command name regardless of arguments: once `stream rm` is allowed, every later `stream rm` call runs without asking.

The conversation records each answer and honors it on resume; `fisk session show` lists what it holds. `/clear` and a
`--force` resume across a changed configuration drop the answers. A resume with no terminal attached declines a gated
command. The prompt is rendered on stderr, so a piped final answer stays clean, and the displayed command line is
stripped of terminal control sequences. With no interactive terminal, or a prompt that cannot be shown, the command is
declined. An interrupt or end-of-input at the prompt ends the run; the conversation stays continuable and asks again.
The tag is always active; there is no configuration flag.

`harness.confirm_tags` extends the same gate to other tags, for example `ai:destructive` or an application's own
`impact:rw`. Matching is exact, not a regex. An entry that matches no loaded command is reported as a warning at
startup. The approval prompt names the tag that gated the command. `fisk info` shows each command's tags and which
commands a run would gate. Over MCP a gated command is requested through elicitation.

Any other tag is free-form: it has no built-in meaning and can be matched by the `tags` field of an `include` or
`exclude` rule.

All of a command's tags are included in the tool description fisk sends the model, as a trailing `Tags: ...` line, in
the agent and over MCP, so a prompt can reference them, for example "always use `ask_human_confirm` before running any
command tagged `impact:rw`". The `fisk info` listing shows the plain description.
