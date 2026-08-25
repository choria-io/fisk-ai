# Basic agent

This example builds an AI agent that speaks in `cowsay` bubbles.

The steps make a quick CLI application using App Builder and then drive it in various ways using the LLM.

The example needs an Anthropic API key, the `cowsay` application (try `brew install cowsay`) and `fisk-ai` installed.

## Creating a CLI tool

This example uses [Choria Application Builder](https://choria-io.github.io/appbuilder/) to create a basic CLI tool that
can say and think. Any command line tool built with Fisk works.

First create an `ABTaskFile`:

```yaml
name: cowsay
description: Tools for the Cowsay LLM Agent
author: fisk-ai@choria.io

commands:
  - name: say
    description: Say something using a talking cow, does not accept emoji
    type: exec
    arguments:
      - name: message
        description: The message to send to the terminal
        required: true
        validate: is_shellsafe(value)
    command: |
      {{ default .Config.Cowsay "cowsay" }} {{ .Arguments.message | escape }}

  - name: think
    description: Think something using a thinking cow, does not accept emoji
    type: exec
    arguments:
      - name: message
        description: The message to send to the terminal
        required: true
        validate: is_shellsafe(value)
    command: |
      cowthink {{ .Arguments.message | escape }}
```

Now install `appbuilder`:

```nohighlight
$ brew tap choria-io/tap
$ brew install appbuilder
```

Then confirm the CLI tool works:

```nohighlight
$ abt
usage: abt [<flags>] <command> [<args> ...]

Tools for the Cowsay LLM Agent

Help: https://choria-io.github.io/appbuilder

Commands:
  help [<command>...]
  say <message>
  think <message>
```

```nohighlight
$ abt say 'Hello AI'
 __________
< Hello AI >
 ----------
        \   ^__^
         \  (oo)\_______
            (__)\       )\/\
                ||----w |
                ||     ||
```

## Creating an LLM agent

Turning this CLI into an LLM agent needs an `agent.yaml` file.

```yaml
# Command to introspect and expose as an agent
application_path: /opt/homebrew/bin/abt

harness:
  # Allow the LLM to prompt us for information if needed
  human_in_the_loop:
    enabled: true

llm:
  # Choose a Model and set safety budgets
  model: claude-haiku-4-5-20251001
  budget:
    max_tokens: 100000
    max_iterations: 50

# We want a cow joke machine!
system_prompt: |
  Tell jokes using Cows!

  You have tools that can render a cow saying < 120 character sentences, when asked use the tools to tell funny jokes.

  You tell cow jokes, no other kinds of jokes, strictly jokes about cows. If asked to tell non cow jokes, refuse and show no joke.

  Keep narration short, just stick to the jokes, don't say what you are doing or planning to do, just do it and don't repeat the joke
```

Run the agent after setting the API key:

```nohighlight
$ export ANTHROPIC_API_KEY="....."
$ fisk run --tool-output --no-tui 'tell me a joke '
-> say -- Why did th...space?
<-  
 ______________________________
< Why did the cow go to space? >
 ------------------------------
        \   ^__^
         \  (oo)\_______
            (__)\       )\/\
                ||----w |
                ||     ||

-> think -- To visit t...oooon!
<- 
 ______________________
( To visit the Moooon! )
 ----------------------
        o   ^__^
         o  (oo)\_______
            (__)\       )\/\
                ||----w |
                ||     ||


  There you go! A classic cow joke for you!

Run summary: model=claude-haiku-4-5-20251001 llm_calls=2 tool_calls=2 tokens=3536/113 thinking=0 latency=3.613s
```

The default is a running TUI. To make the output easy to show here, the run passes `--no-tui` and shows the tool call
output with `--tool-output`.

Now ask about a cat joke:

```nohighlight
$ fisk run 'tell me a joke about a cat'

  I appreciate the request, but I only tell jokes about cows! I'm strictly a cow joke specialist.

  If you'd like to hear some funny cow jokes instead, I'd be happy to moo-ve right into those for you!

Run summary: model=claude-haiku-4-5-20251001 llm_calls=1 tool_calls=0 tokens=1632/54 thinking=0 latency=1.341s
```

## Running the agent

The agent runs in one of these modes:

* A shell script style output, plain text to STDOUT with an exit at the end of the task
* A TUI for interaction, optionally continuing to chat with the agent after the main task completes
* Hosted behind a [channel](../../channels/), taking work from a queue or serving its tools to other agents

### TUI

The TUI mode is the default: a visual runner with hot-keys to show or hide thinking and tool output, and scrolling up
and down the session history. A chat box can optionally be enabled to continue a session.

In the TUI press the `?` key to get interactive help.

#### Chat after turn

In the TUI mode the chat bar opens once the prompt is processed, instead of exiting, for follow up questions related to
the session. Every full-screen run works this way; `--no-tui` answers one prompt and exits, since it has no bar to
open.

Type a follow-up and press Enter to send it; `Ctrl-D` ends the session, `Ctrl-C` aborts it. Up/Down recall this
session's earlier follow-ups. Alt-Enter (Option-Enter) moves to the next line rather than send. `Ctrl-L` empties the
transcript on screen and leaves the conversation and any half-typed follow-up alone, where `/clear` does the opposite
and drops the conversation while leaving the scrollback.

### Shell mode

The TUI is turned off with `--no-tui`, and the system falls back to a simple terminal output format suitable for
scripting.

The model's prose is markdown: both the final answer and any mid-conversation updates. When stdout is a terminal it is
rendered for readability with a style matched to the terminal background; when stdout is piped or redirected, the raw
markdown is written so the result stays free of ANSI escape codes. Rendering can also be disabled with `--no-color`, or
the standard `NO_COLOR` environment variable.

Output is separated by kind. Only the final answer goes to stdout; everything else goes to stderr: the commands being
run, mid-conversation updates, a final run summary (LLM calls, tool calls, tokens, latency), and, with `--thinking`,
the model's reasoning (each line prefixed with a thought bubble). This keeps stdout safe to pipe into other tools.

#### One-shot runs

The common use case gives a `system_prompt` that describes the goals and approach (think of it as a one-file SKILL) and
a user prompt that provides the question to solve.

The LLM runs through the prompt and, once it reaches the end of its turn, finishes processing, and the session cannot
continue later. This resembles a shell utility.

#### HTTP debugging

As a debug or learning aid, all the HTTP requests can be logged to `http-debug.log` using the `--http-debug` flag.
