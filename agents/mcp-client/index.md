# MCP client

`mcp_clients` imports the tools of third-party MCP servers into an agent run, alongside the wrapped application's
commands, the built-ins and any remote tools. Each entry names one server and selects a transport by which of `command`
and `url` it sets: `command` starts the server as a child process and speaks stdio to it, `url` reaches an
already-running server over streamable HTTP. Setting both is an error, and so is setting neither. Stdio and streamable
HTTP are the only transports, and an endpoint that speaks the older HTTP+SSE transport is not supported.

```yaml
mcp_clients:
  - name: filesystem
    alias: fs
    command: npx
    args:
      - -y
      - "@modelcontextprotocol/server-filesystem"
      - /srv/data
    env:
      FS_STATE_DIR: ${HOME}/.cache/fs-mcp
    timeout: 30s
    include:
      tools:
        - ^read_
    exclude:
      tools:
        - ^read_media_

  - name: docs
    url: https://mcp.example.net/mcp
    headers:
      Authorization: Bearer ${DOCS_TOKEN}
    timeout: 15s
```

Every field is described in the [configuration reference](../../reference/#mcp-clients).

Two entries sharing a `name` is an error when the file is parsed, and so is two entries whose effective alias is the
same, since that alias prefixes every tool they both expose.

## Naming

Every imported tool is named `<alias>_<tool>`, always, where the alias defaults to the server name. `remote_tools`
prefixes only on a clash. MCP servers use short generic tool names such as `search` and `read`, where a clash is the
common case, and a name derived only from its own server does not move when another server's tool list does.

A collision against a local tool, a remote tool or another server's is still possible. At the start of a run it fails
the run, naming the tools that collided. `fisk info` reports it and carries on. A tool arriving while a run is under
way whose name is taken is left out and reported, and the run continues.

## Variable references

`env`, `headers` and `url` values hold any number of `${VAR}` references, each replaced by the value of that
environment variable, so a credential lives in the variable rather than in the file. A value mixes literal text with
references freely, as in `Bearer ${DOCS_TOKEN}` and `${HOME}/cache`. A `$NAME` without braces is literal text and
references nothing. `command` and `args` are literal throughout.

References resolve when a session connects, not when the file is parsed. Parsing checks their syntax and reads no
variable, so a host holding none of the credentials still runs `fisk info` and `fisk mcp` against the file. A variable
that is not set fails the connect, naming the variable and the server.

Some services authenticate in the endpoint rather than in a header, by query parameter as in
`https://mcp.example.net/mcp/?apiKey=${DOCS_TOKEN}`, or by a path segment as in
`https://mcp.zapier.com/api/mcp/s/${ZAPIER_KEY}/mcp`. `url` takes references for that reason.

## What is printed

Whatever a `url`'s references resolved to is replaced by `REDACTED` wherever it appears in an error or warning about
that server, the endpoint an SDK or HTTP error quotes included, as long as the resolved value is at least eight
characters. A shorter one is never searched for, since replacing a string that short would blank the digits and words
it matches all through an unrelated message. Every endpoint printed anywhere is also redacted on its structure: the
userinfo before the host, the value of every query parameter, and the fragment. The scheme, host, port, path and
parameter names stay, so an operator recognizes the entry from their own file, and a reference is shown as written so
it names the variable rather than its value.

> [!info] Warning
> A credential written into a URL path segment as a literal is printed in full, because nothing can tell a path segment
> holding a token from one naming a route. Put a path credential in a variable and reference it. A token under eight
> characters is printed either way, since the value redaction skips a string that short and the structural redaction
> leaves the path alone.

A stdio entry prints its command line, and each argument goes through the same URL redaction, so an
`npx -y mcp-remote https://host/sse?key=...` bridge does not print its key.

## Timeouts

`timeout` covers everything that happens for one server before the run starts, and it is applied twice: once around
starting or reaching the server and the initialize handshake, and again around listing its tools. An entry that is slow
at both steps takes up to twice the configured value, 60s at the default, before the run gives up on it. Unset it
defaults to 30s.

Servers are connected one after another and listed one after another, so an entry that answers slowly or not at all
delays every entry behind it by up to its own timeout. The timeout keeps that delay finite rather than preventing it:
three entries that answer the handshake and then never return a tool list hold the run for 90 seconds at the default
before it is refused.

A call to an imported tool is limited by `harness.tool_timeout`, like every other tool.

## Trust posture

An imported MCP tool is never confirm-gated. `ai:confirm` and `harness.confirm_tags` reach the wrapped application's
commands, and the server applies no gate on its side either, so a call the model makes to an imported tool runs
unapproved at both ends. `include` and `exclude` are therefore the only control an operator has over what a third
party's server can do in a run.

An imported MCP tool is never served on. Neither `fisk mcp` nor the a2a tool endpoint advertises one to its own
clients, whatever `expose.agent.tools` selects: serving it would re-advertise a third party's tool under this agent's
identity, and a client cannot tell which of the tools it is offered came from where.

A stdio server is a program of someone else's choosing running as the operator's user. It gets this process's
environment with the credential variables removed, the same scrub a command tool's subprocess gets, and the entry's
`env` applied on top.

## Failures and a moving tool set

A server that cannot be started, reached or listed fails the run, since the prompt may depend on tools that are not
there. A tool the run cannot use, one with no description or a schema whose root is not an object, is skipped with a
reason and the run continues on the rest.

A server can tell a live session that its tool list changed. The run re-lists that server, applies the entry's filters
and names the survivors, and the model sees the new set on its next call. A tool batch already dispatched runs against
the set it was dispatched with, and no other server's tools move.

## Seeing what a server offers

`fisk info` connects every configured server and prints an `MCP clients` section: where each is reached and over which
transport, how long it took to answer, how many tools it advertised, how many the filters kept, the name each was
imported under, and any tool left out with the reason. Discovery there is best-effort, so a server that is down is
reported rather than failing the command. Its imported tools appear in the tool table with the alias in the `Source`
column.

`fisk serve` connects the configured servers once at startup and shares those sessions across every run it hosts, so a
long-lived worker is not starting and stopping a stdio child around each job. A server that cannot be reached stops the
worker from starting, and the startup banner names the servers every hosted run imports from. Those runs share the
server's working directory, its authentication and its rate limits.
