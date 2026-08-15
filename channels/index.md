# Channels

A channel is a calling surface an agent is hosted behind. A work queue, a NATS binding, an HTTP listener and a caller
in the same process are all channels, and they differ in what they can do rather than in kind.

The `fisk serve` command hosts an agent behind whatever channels its configuration enables. Each channel supplies work,
the agent runs it, and the outcome goes back the way it came. How work reaches a channel is the channel's own business:
one polls a queue, another might answer a request on a subject, and the agent that runs it never learns which.

> [!info] Note
> Channels are a young part of Fisk AI. The queued-jobs channel is the only one that ships today, and the shape of the
> configuration around them will grow as more arrive.
>
> Channels and the `fisk serve` command are available since {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}.

## What a channel decides

Channels differ in what they can offer a run, and the differences are real rather than cosmetic:

| Capability          | Description                                                      |
|---------------------|-------------------------------------------------------------------|
| Streaming           | whether a caller sees output as it is produced                     |
| Elicitation         | whether a run can put a question to a person part way through      |
| Follow-up turns     | whether a conversation continues after the first answer            |
| Caller identity     | what the channel knows about who asked                             |

A queue offers none of the first three. Nobody is waiting on the other end, so there is no one to stream to, no one to
answer a question, and no second turn. A run served over a queue is therefore one shot: it starts with a prompt, and it
ends with an answer.

This has a consequence worth stating plainly, because it applies to every channel that cannot reach a person.
Confirmation-gated tools are dropped at the start of such a run. There is no operator to approve them, so a run that
would need one is told the tool is unavailable rather than left waiting for an approval that cannot arrive.

## Where to go next

* [Serving](serving/) covers the `fisk serve` command and the settings every channel shares
* [Queued jobs](asyncjobs/) covers the asyncjobs channel: submitting work, reading answers, and its own configuration
