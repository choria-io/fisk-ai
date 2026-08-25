# Safety

When Fisk AI runs a command in a CLI tool it passes a slice of arguments to the `exec` system call. No shell is involved
that can be escaped or influenced.

App Builder is often involved and calls shell scripts, so App Builder commands need to be written defensively.

* Use type hints on arguments for ints, floats and so on
* Use `is_shellsafe(value)` on string input arguments
* Use escaping when passing arguments to commands, for example `{{ .Arguments.message | escape }}`
* Tag commands with the various helper tags so the harness understands the intent
* Mark every mandatory argument as `required`

Fisk AI has no tools that can interact with arbitrary files on the system. The only way it interacts with the system is
through the supplied tools or the Memory feature.

Every command the agent runs gets the same protections:

* Its output combines stdout and stderr, preserving order, and is capped at 64 KiB so a chatty command cannot flood the model's context
* The `ANTHROPIC_API_KEY` is stripped from its environment, so a tool can never read the agent's own credentials
* `LLMFORMAT=1` is set, signalling fisk applications to render output suited to an LLM rather than a terminal
