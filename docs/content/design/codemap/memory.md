+++
title = "Memory"
weight = 70
description = "A small key/value store that lets the model keep durable notes across separate runs."
+++

Memory gives the model a small key/value store that persists across runs, so it can keep durable notes and pick them up next time rather than rediscovering them. It is opt-in, agent-mode only, and never exposed over MCP.

{{% notice style="note" title="Where it lives" %}}
`internal/memory` holds the backend-agnostic core: the `Store` interface and the `New` factory in `store.go`, the backend registry in `registry.go`, key validation in `key.go`, and the shared write validation in `write.go`. The file backend lives in its own `internal/memory/file` package, with the backend and its options in `file.go`, the on-disk format in `frontmatter.go`, and the symlink defense split across `nofollow.go` and `windows.go`. The four model-facing tools and the system-prompt index are in `internal/toolkit/builtin/builtin_memory.go`.
{{% /notice %}}

## Four tools over one interface

When memory is enabled the model gets four tools: `memory_list` returns keys and descriptions, `memory_read` fetches one entry, `memory_write` saves an entry, and `memory_delete` removes one. All four go through the `Store` interface, so the storage backend is pluggable behind them.

Each backend registers itself under a name in `registry.go`, so a backend links into the binary by being imported and `New` builds whichever one `agent.yaml` selects. An unknown backend name fails at the start of a run and lists the backends that are linked in. Backends share the same key and write rules through `ValidateKey` and `ValidateWrite`, so a value written by one is legal in another. The file backend is the only one today; a NATS KV backend is the planned second.

<figure class="cm-diagram">
  <svg viewBox="0 0 760 210" role="img" aria-label="Four memory tools call one Store interface backed by a file backend of markdown files">
    <defs>
      <marker id="mm" markerWidth="9" markerHeight="9" refX="7" refY="3" orient="auto">
        <path d="M0,0 L7,3 L0,6 Z" fill="var(--cm-accent)"/>
      </marker>
    </defs>
    <!-- tool boxes -->
    <rect class="cm-svg-box" x="16" y="28" width="150" height="36" rx="7"/>
    <text class="cm-svg-label" x="91" y="51" text-anchor="middle">memory_list</text>
    <rect class="cm-svg-box" x="16" y="72" width="150" height="36" rx="7"/>
    <text class="cm-svg-label" x="91" y="95" text-anchor="middle">memory_read</text>
    <rect class="cm-svg-box" x="16" y="116" width="150" height="36" rx="7"/>
    <text class="cm-svg-label" x="91" y="139" text-anchor="middle">memory_write</text>
    <rect class="cm-svg-box" x="16" y="160" width="150" height="36" rx="7"/>
    <text class="cm-svg-label" x="91" y="183" text-anchor="middle">memory_delete</text>
    <!-- store (accent) -->
    <rect x="260" y="70" width="150" height="88" rx="8" fill="color-mix(in srgb, var(--cm-accent) 12%, transparent)" stroke="var(--cm-accent)"/>
    <text class="cm-svg-label" x="335" y="110" text-anchor="middle" style="fill:var(--cm-accent)">memory.Store</text>
    <text class="cm-svg-sub"   x="335" y="128" text-anchor="middle">one interface</text>
    <!-- file backend -->
    <rect class="cm-svg-box" x="470" y="70" width="170" height="88" rx="8"/>
    <text class="cm-svg-label" x="555" y="104" text-anchor="middle">file backend</text>
    <text class="cm-svg-sub"   x="555" y="122" text-anchor="middle">one .md per key</text>
    <text class="cm-svg-sub"   x="555" y="138" text-anchor="middle">under memory/id</text>
    <!-- arrows: tools to store -->
    <line x1="166" y1="46"  x2="258" y2="104" stroke="var(--cm-accent)" stroke-width="1.5" marker-end="url(#mm)"/>
    <line x1="166" y1="90"  x2="258" y2="112" stroke="var(--cm-accent)" stroke-width="1.5" marker-end="url(#mm)"/>
    <line x1="166" y1="134" x2="258" y2="120" stroke="var(--cm-accent)" stroke-width="1.5" marker-end="url(#mm)"/>
    <line x1="166" y1="178" x2="258" y2="128" stroke="var(--cm-accent)" stroke-width="1.5" marker-end="url(#mm)"/>
    <!-- store to backend -->
    <line x1="410" y1="114" x2="468" y2="114" stroke="var(--cm-accent)" stroke-width="2" marker-end="url(#mm)"/>
  </svg>
  <figcaption>The tools depend only on the interface. The file backend is the one implementation today.</figcaption>
</figure>

## Keys, files, and races

A key is letters, digits, and `. _ = -`, with no leading or trailing dot and no `..`, enforced by `ValidateKey` in `key.go`. The slash that a NATS KV key also permits is deliberately excluded, so a key maps one-to-one to a flat filename with no separator to traverse. `ValidateKey` guards every operation and filters directory entries, which makes it a path-traversal defense as well as a format rule.

The file backend stores each entry as a markdown file with a YAML frontmatter description under `memory/<identity>`, where the identity defaults to the application binary's base name. Two agents pointed at the same directory share a memory; the default keeps each separate. Create and overwrite use the filesystem for atomicity: a create links a staged temp file into place and fails if the name exists, giving a race-safe create guard, while an overwrite renames over the target. Content is capped at 64 KiB, sized to what a future NATS KV backend would accept.

## The start-of-run index

At the start of a run, if the index is enabled, the stored keys and descriptions are injected into the system prompt inside a `<memory-index>` block, so the model knows what it has saved. `memory_list` is the live view during the run. The index is turned off with `no_index`.

{{% notice style="warning" title="Load-bearing decision" %}}
Memory is untrusted data, not instructions. The system note, the index framing, and the description sanitization all reinforce that a stored value is data the model saved, never an instruction to follow. On unix a read opens with `O_NOFOLLOW` and then rejects any non-regular file, so a planted symlink cannot redirect a read. Windows has no `O_NOFOLLOW`, so that build keeps only the second check: the file is opened and then rejected if it is not regular. That is a weaker guarantee, and it leans on the fact that creating a symlink on Windows needs privilege.

A create is atomic for a different reason. The backend stages into a temp file in the same directory and then links it into place, so an existing key fails at the link rather than after a separate existence check. The 1024-entry capacity check that precedes it is not part of that atomic step.
{{% /notice %}}

{{% notice style="tip" title="Next" %}}
Continue to [Knowledge and RAG]({{% relref "knowledge" %}}) for the other opt-in, agent-only data store: a searchable document index.
{{% /notice %}}
