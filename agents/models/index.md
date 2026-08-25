# Model settings

The `agent.yaml` sets which model runs the agent, the budget a run may spend, and whether the model exposes its
reasoning. The [Basic agent](../basic/) example shows these together. The full set of configuration fields is in
the [configuration reference](../../reference/).

## Model

`llm.model` selects the model and is required. It accepts any model identifier the Anthropic API accepts:

```yaml
llm:
  model: claude-sonnet-5
```

Larger models reason better on complex, long-horizon tasks; smaller models like Haiku are faster and cheaper for narrow
ones. When the agent exposes ten or more tools it relies on the model's server-side tool search, which recent models
support and older ones (Claude Opus 4.1 and earlier and local models) do not. Set `llm.no_tool_search` to send every tool
directly on an endpoint that does not implement tool search; when a large tool set cannot use it the run warns that all
tools are being sent directly. The [configuration reference](../../reference/) lists the known models and their trade-offs.

## Budget

`llm.budget` limits the agent loop so it cannot run without end:

```yaml
llm:
  budget:
    max_tokens: 500000
    max_iterations: 50
    call_timeout: 120s
```

| Setting          | Description                                                       |
|------------------|-------------------------------------------------------------------|
| `max_tokens`     | tokens a whole conversation may process, default 500000           |
| `max_iterations` | agent loop iterations one turn may take, default 50               |
| `call_timeout`   | per-call timeout as a duration string, default `120s`             |

**The two caps have different scopes.** `max_iterations` applies to a single turn, and
every turn of a conversation gets the same allowance. `max_tokens` applies to the whole
conversation, so every turn draws on one allowance. Start a new conversation to get a
fresh one.

When a turn reaches the iteration cap, it stops, says so, and you can carry on with
another prompt. When a conversation reaches its token cap, it is finished: the next prompt
is refused before it runs. Start a new conversation, or raise `llm.budget.max_tokens` on
the machine running the agent.

**`max_tokens` counts tokens, not money.** It adds up the uncached input, the output, and
both prompt-cache tiers. A cache read counts the same as an uncached input token here even
though it costs a fraction as much, so two conversations with the same token count can
cost very different amounts. Set this value against your own usage rather than treating it
as a spending limit.

## Thinking

Extended thinking lets the model expose its reasoning before it answers. Some providers call this reasoning rather
than thinking; it is the same setting.

```yaml
llm:
  thinking:
    enabled: true
```

Reasoning is never displayed unless asked for. `--thinking` (or `THINKING=1`) shows it, on `fisk run` and on
`fisk session show --transcript`.

`thinking=N` on the run summary and in the TUI status bar reports the tokens spent reasoning, shown whether or not
reasoning is displayed. It is part of the output half of `tokens=in/out`, not extra.

> [!info] Note
> Older models that predate adaptive thinking, such as Sonnet 4.5 and Haiku 4.5, reject the parameter. Both explicit
> states send one, so for those models remove the `thinking` block rather than setting `enabled: false`. The same
> applies to an endpoint behind `ANTHROPIC_BASE_URL` whose proxy does not implement it.

## Terminal UI

These harness settings govern the full-screen UI for an agent, independent of the per-run `--no-tui` flag:

```yaml
harness:
  no_tui: true
  no_bell: true
```

* `no_tui` is a persistent off switch: the agent always uses the line-by-line output, even on an interactive terminal, and the command line cannot turn the UI back on. Use `--no-tui` instead for a one-off run.
* `no_bell` silences the terminal bell. By default the full-screen UI rings the bell each time a run blocks on an approval gate or an `ask_human_*` prompt, so a waiting run is noticed even when unattended.

Both are negative switches and have no effect in the line UI.
