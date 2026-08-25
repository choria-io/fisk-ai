# Local LLMs

Local LLM hosting tools like `ollama`, `LM Studio` and others support exposing an Anthropic-compatible API. Fisk AI can
communicate with those tools.

To support a large number of tools, Fisk AI uses the
[Tool Search Tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool), which these local
runners do not support. When targeting a locally hosted model, the total tool count may need to stay around 15.

I set these environment variables before invoking `fisk` to access my local Anthropic API instead of reaching to the internet.

```nohighlight
$ export ANTHROPIC_BASE_URL=http://localhost:1234
$ export ANTHROPIC_API_KEY=lmstudio
```

The `base_url` is validated: a non-loopback host must use `https`, so the API key and conversation are never sent in
cleartext. Plain `http` is allowed only for a loopback address (`127.0.0.1`, `::1`, `localhost`) as used by the local
runners above.
