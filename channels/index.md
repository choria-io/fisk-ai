# Channels

A channel supplies work to an agent and returns the answer. A work queue and a NATS request subject are channels
today; an HTTP listener or a caller in the same process would be channels too.

The `fisk serve` command hosts an agent behind the channels its configuration enables. The queued-jobs channel polls a
work queue. The prompts channel answers a request on a NATS subject. The agent loop is the same either way and does
not see the difference.

`fisk serve` also hosts surfaces that produce no work. [Serving tools](a2a/) answers another agent's tool call
directly, running one tool. It starts no agent loop, so the behavior on this page does not apply to it.

> [!info] Note
> Queued jobs and prompts from other agents are the channels that ship today.
>
> Channels and `fisk serve` are available since {{% badge style="primary" title="Version" %}}0.0.5{{% /badge %}}.

## Channel capabilities

Channels differ in what they can offer a run:

| Capability      | Description                                                 |
|-----------------|-------------------------------------------------------------|
| Streaming       | whether a caller sees output as it is produced               |
| Elicitation     | whether a run can ask a person a question mid-run            |
| Follow-up turns | whether a conversation continues after the first answer      |
| Caller identity | what the channel reports about the caller                    |

What each shipped channel offers:

| Channel     | Streaming | Elicitation | Follow-up turns | Caller identity          |
|-------------|-----------|-------------|-----------------|--------------------------|
| Queued jobs | no        | no          | no              | unverified `sender` field |
| a2a prompts | yes       | no          | no              | unverified `sender` field |

Nobody waits on a queued job, so the queued-jobs channel neither streams nor takes a second turn. The prompts channel
streams output to the waiting caller as the worker produces it, then ends the run after the first answer.

No shipped channel can reach a person, so the worker removes confirmation-gated tools from the tool set at the start of
every served run. The model never sees them.

## Where to go next

* [Serving](serving/) covers the `fisk serve` command and the settings every channel shares
* [Queued jobs](asyncjobs/) covers the asyncjobs channel: submitting work, reading answers, and its own configuration
* [Answering prompts](prompts/) covers taking prompts from other agents and streaming the run back to them
* [Serving tools](a2a/) covers offering this agent's tools to other agents without running an agent loop
