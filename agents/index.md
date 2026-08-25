# Agents

The main feature of Fisk AI is creating AI agents from CLI tools written with [Fisk](https://github.com/choria-io/fisk).

Any tool built with Fisk, such as the `nats` or `choria` CLI, or an application made
with [Choria Application Builder](https://choria-io.github.io/appbuilder/), can be turned into an AI agent.

Fisk AI creates capable systems that use the abilities LLMs have, such as reasoning and text interpretation, in a safe
and deterministic manner.

Building an agent resembles building a CLI tool: describe the goals, give broad guidance, supply tools to interact with
the world deterministically, then run it on a shell like any other utility.

## Installation

On a Mac you can install `fisk-ai` using homebrew:

```nohighlight
brew tap choria-io/tap
brew install choria-io/tap/fisk-ai
```

Other Operating System users can download the latest release from the [releases page](https://github.com/choria-io/fisk-ai/releases).

## Where to go next

{{< subpages >}}
