# Memory

Memory gives the model a small key/value store that persists across runs, so it
can keep durable notes (a layout it worked out, a convention, the outcome of an
investigation) and pick them up next time rather than rediscovering them. It is
opt-in and agent-mode only; like the human-in-the-loop tools it is never exposed
over MCP.

> [!info] Warning
> Memory is shared state. Treat what a memory contains as data the model saved, not as trusted instructions.

Enable it under `harness.memory`. The `backend` field selects where memories are
kept; it defaults to `file`, so the minimal configuration is just:

```yaml
harness:
  memory:
    enabled: true
```

When enabled the model is offered four tools: `memory_list` (keys and their
descriptions), `memory_read` (one memory by key), `memory_write` (save a memory
with a key, a one-line description, and a body), and `memory_delete`. A key uses
letters, digits and `.`, `_`, `=` or `-` (no slashes or spaces), which keeps it
valid both as a filename and as a NATS KV key. `memory_write` creates by default
and refuses to overwrite an existing key unless called with `overwrite: true`, so
the model does not silently clobber a note; the create still fails cleanly if two
writers race for the same new key.

`read_only: true` serves `memory_list` and `memory_read` and withholds the other two, for a run that should use what
earlier runs saved without changing it. The store itself is unaffected, so anything else writing to it still does.

At the start of a run the stored keys and descriptions are injected into the
system prompt as an index so the model knows what it has saved; `memory_list` is
the live view during the run. Turn the index off with `no_index: true`.

A memory body is capped at 64 KB and a store holds at most 1024 entries. Both
limits are shared by every backend, and a write that would exceed them fails
cleanly. The on-disk format is shared too, so a value written by one backend
migrates to another unchanged.

`fisk info` shows a `Memory` section with the resolved backend and, for the
jetstream backend, the bucket, NATS context and key prefix, so you can confirm
where memory is stored without starting a run.

Two backends ship today: `file` (the default) and `jetstream`  {{% badge style="primary" title="Version" %}}0.0.3{{% /badge %}}.

## File backend

The `file` backend keeps each memory as a markdown file named for its key under
the configured directory, which defaults to `memory/<identity>`.

```yaml
harness:
  memory:
    enabled: true
    backend: file
    options:
      directory: memory
```

A relative directory, including that default, resolves under the store base when a
deployment sets one and against the working directory otherwise; an absolute
directory is used as-is. The `identity` is the agent's name, set with the
`identity` configuration field and defaulting to the application binary's base
name; the [configuration reference](../../reference/) covers it in detail. Point two
agents at the same directory and they share a memory; leave the default and each
agent keeps its own.

## JetStream backend

The `jetstream` backend keeps memories in a NATS JetStream KV bucket instead of on
disk, so a fleet of agents can share durable memory over a broker. It uses the
connection from the configured `nats_context`, the same one remote tools use, and
binds to a bucket that must already exist: the agent never creates it, so you own
the bucket's durability policy.

```yaml
nats_context: production

harness:
  memory:
    enabled: true
    backend: jetstream
    options:
      bucket: agent-memory
```

Create the bucket first, without a TTL so memories do not silently expire and with a
max value size that fits a full entry (the 64 KB body cap plus the small frontmatter
header stored with it), up to 1024 entries:

```
nats --context production kv add agent-memory --history=1 --max-value-size=69600
```

The backend fails at run start, rather than degrading silently, if the bucket does
not exist, has a TTL set, or caps values below that full-entry size.

By default each agent's keys are namespaced under a prefix equal to its `identity`
(stored as `<identity>.<key>`), mirroring the file backend's per-identity directory
so two agents pointed at one bucket do not collide. Set `options.prefix` to a shared
value for agents that deliberately share memory, or to `""` for a flat, unprefixed
keyspace:

```yaml
harness:
  memory:
    enabled: true
    backend: jetstream
    options:
      bucket: agent-memory
      prefix: fleet-shared   # agents with the same prefix share memory; "" is flat
```

### Read-before-update

The jetstream backend adds a safety guard the file backend cannot: an overwrite
must follow a read of the current value, and is refused if the memory was not read
or has changed since it was read. The model then reads the current value and
retries. This is the same read-before-edit discipline that keeps an editor from
clobbering a file it has not seen.

The read counts for the whole conversation rather than one turn. A memory read on
Monday and edited on Friday in the same conversation is overwritten without a fresh
read, as long as nothing else wrote to it in between; if something did, the write is
refused and the model reads again before retrying. One conversation's reads never
authorize another's overwrite.

The check uses the KV entry's revision, which makes it an atomic
compare-and-swap: when two agents share a bucket and both try to update the same
memory, the second write is rejected rather than silently overwriting the first.
The file backend's last-writer-wins overwrite would quietly drop that change, so a
shared or concurrent deployment wants this backend.

The guard is on by default. Set `no_require_read_before_update: true` to allow blind
overwrites, matching the file backend's behavior:

```yaml
harness:
  memory:
    enabled: true
    backend: jetstream
    options:
      bucket: agent-memory
      no_require_read_before_update: true
```

We can use memory to ensure our agent never repeats jokes; change the`system_prompt` as follows:

```yaml
harness:
  memory:
    enabled: true

system_prompt: |
  Tell short jokes using Cows!

  You have tools that can render a cow saying short sentences, when asked 
  use the tools to tell funny jokes.
  
  You tell cow jokes, no other kinds of jokes, strictly jokes about cows. 
  If asked to tell non cow jokes, refuse and show no joke.

  Do not use emoji, keep general narration short, just stick to the jokes

  Save the jokes you told to a single memory file with all the past jokes 
  and make sure you dont repeat jokes you previously told.

  Finish your turn by making a funny quip related to the joke or cows or similar
```

We will get a new joke every time - be ready to get some awful jokes after a while :)
