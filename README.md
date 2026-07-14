![Fisk Agent and MCP Creation Toolkit](https://raw.githubusercontent.com/choria-io/fisk-ai/refs/heads/main/docs/static/logo.png)

## Overview

Fisk AI turns any fisk-based command-line application into an LLM agent. It introspects the app’s command tree, exposes the allowed commands as tools, and runs an agent loop against the Anthropic API that calls those commands to satisfy a prompt.

No glue code — if your CLI is built with Fisk, Fisk AI can turn it into a purpose built agentic harness.

The main focus is on safety and determinism, every features is carefully considered and designed. What sets this Harness apart is what it does not have rather than a long list of features.

It is designed in particular to complement Choria App Builder. App Builder lets you define a command line application declaratively in YAML, and because it is built on fisk, any App Builder application can be introspected and driven by fisk-ai. Together they let you define a strict, purpose-built set of tools in a YAML file and expose exactly those to an agent, without writing or compiling any code: App Builder describes the commands, fisk-ai’s configuration selects which of them the agent may use and how it should behave.

* [Documentation](https://choria-io.github.io/fisk-ai/)
* [Discussions](https://github.com/choria-io/fisk-ai/discussions)
* [Slack #choria](https://short.voxpupu.li/puppetcommunity_slack_signup)
