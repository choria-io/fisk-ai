# Human in the loop

When enabled, fisk gives the model built-in tools to ask the operator a question at the terminal and wait for the
answer. They are off by default and only available when running the agent:

```yaml
harness:
  human_in_the_loop:
    enabled: true
```

Enabling it offers these tools:

* `ask_human_confirm` - a yes/no question. Returns `{"confirmed": true}` or `{"confirmed": false}`
* `ask_human_select` - choose one of a list of options the model provides. Returns `{"selected": "<option>"}`, or
  `{"selected": null}` if no choice was made
* `ask_human_input` - a free-text value, optionally pre-filled with a default the operator can accept or edit. Returns
  `{"value": "<text>"}`, or `{"value": null}` if none was given

## Optional communication from the agent

The model decides when to call the HITL tools, shaped through the prompt. They suit decisions the model should not make
alone: confirming a destructive action, choosing between options that depend on operator intent, or supplying a value it
cannot derive. The question is rendered on the terminal (stderr, so a piped final answer stays clean), and the
model-supplied text is stripped of terminal control sequences first so it cannot spoof what is shown. Each tool denies
by default: with no terminal attached the call returns a negative answer (no confirmation, no selection, no value) and a
reason rather than hanging on a prompt no one can answer, and they are never exposed over MCP, where there is no
operator. Tool calls within a turn run one at a time, so a prompt has the terminal to itself.

If you interrupt a question, or close the input, fisk does not treat that as a reply. The run stops there and the
conversation is kept, and when you continue it the same question is asked again. No answer is recorded, so a run you
interrupt never carries a decision you did not make.

## Required tool use confirmations

These mechanisms put a human in the loop:

* `human_in_the_loop` (a configuration flag) lets the model ask its own question through a fisk-provided
  `ask_human_*` tool, with no application command involved. The human answers a question the model chose to ask.
* `ai:confirm` (a command tag) lets the application author gate an ordinary, non-interactive command so the operator
  must approve it before it runs. The human is a checkpoint on a command the model wanted to run anyway; nothing about
  the command itself changes.

Reach for `human_in_the_loop` when the model should decide when to check in; reach for `ai:confirm` when a normal
command should run only with the operator's say-so, typically something destructive or irreversible.
[Command tags](../tools/#command-tags) covers the tag in full.
