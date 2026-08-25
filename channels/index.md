# Channels

A channel supplies work to an agent and returns the answer. A work queue and a NATS request subject are channels
today; an HTTP listener or a caller in the same process would be channels too.

The `fisk serve` command hosts an agent behind the channels. The queued-jobs channel polls a work queue. The prompts 
channel answers a request on a NATS subject. The Slack channel answers people who mention a bot in a thread. The agent
loop is the same in each case and does not see the difference.

`fisk serve` also hosts endpoints that produce no work. [Serving tools](a2a/) answers another agent's tool call
directly, running one tool. It starts no agent loop, so the behavior on this page does not apply to it.

> [!info] Note
> Queued jobs, prompts from other agents and Slack are the channels that ship today.
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

| Channel     | Streaming | Elicitation | Follow-up turns | Caller identity           |
|-------------|-----------|-------------|-----------------|---------------------------|
| Queued jobs | no        | no          | no              | unverified `sender` field |
| a2a prompts | yes       | optional    | yes             | unverified `sender` field |
| Slack       | no        | yes         | yes             | the Slack user who spoke  |

No caller waits for a queued job, so that channel does not stream output and does not take a second turn.

The prompts channel sends output to the caller as the worker produces it. It returns a conversation token with every
prompt it accepts. Send that token on a later request to continue the conversation.

To let a run ask the caller a question, set `expose.agent.a2a.prompts.elicit`. Leave it unset and the agent asks
nobody.

### Confirmation-gated tools

An agent with nobody to ask still offers every tool to the model, including the confirmation-gated ones. The model can
call one. The confirm gate then refuses the call and tells the model why.

## Where to go next

{{< subpages >}}
