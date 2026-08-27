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

The `base_url` is validated only as a well-formed `http` or `https` URL naming a host, with no embedded userinfo
credentials. Plain `http` is accepted for any host, since a local runner, a host gateway and a service on a private
network all serve over it.
