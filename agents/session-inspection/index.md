# Session inspection

The `session stats`, `session query` and `session search` commands read journaled sessions without resuming them.
`stats` reports what one session cost and where its time went, `query` prints the tool calls it made with their
answers, and `search` finds sessions across the store by agent, model, caller, status, time and prompt.

The commands read the same store as `session ls` and `session show`: the `file` backend under `--state-dir` by default,
or the backend a configuration names with `--config`. They read a journal without taking its lock, so they work
against a session that is still running. See [Session snapshots](../sessions/#file-backend) for where sessions
are stored.

> [!info] Note
> A session journaled before records carried a time and the agent's name reports no timing and no agent, and
> `search --identity` leaves it out. [Older journals](#older-journals) covers both.

```nohighlight
$ fisk session stats 33Fp8QnWb3aZkR1vT0yXcLmE2sD
Session 33Fp8QnWb3aZkR1vT0yXcLmE2sD:

         Agent: nats-ops
         Model: claude-opus-4-8
        Status: completed
       Created: 2026-09-22 11:14:03
         Ended: 2026-09-22 11:14:24
    Iterations: 6 (at most 20 per turn)
    Wall clock: 21.81s (creation to the last journal record)
        Tokens: 41993 of 200000 budget (3790 in / 883 out / 33420 cache read / 3900 cache write)

  Model responses:

    ╭───────────┬───────┬─────┬────────────┬─────────────┬──────────┬───────╮
    │ Iteration │ In    │ Out │ Cache read │ Cache write │ Thinking │ Total │
    ├───────────┼───────┼─────┼────────────┼─────────────┼──────────┼───────┤
    │ 0         │ 2,140 │ 96  │ 0          │ 3,900       │ 40       │ 6,136 │
    │ 1         │ 180   │ 142 │ 6,040      │ 0           │ 60       │ 6,362 │
    │ 2         │ 410   │ 88  │ 6,220      │ 0           │ 0        │ 6,718 │
    │ 3         │ 520   │ 74  │ 6,630      │ 0           │ 0        │ 7,224 │
    │ 4         │ 230   │ 71  │ 7,150      │ 0           │ 0        │ 7,451 │
    │ 5         │ 310   │ 412 │ 7,380      │ 0           │ 120      │ 8,102 │
    ╰───────────┴───────┴─────┴────────────┴─────────────┴──────────┴───────╯

  Tools:

    ╭──────────────────┬─────────────┬───────┬────────────┬─────────┬────────────┬────────┬───────╮
    │ Tool             │ Kind        │ Calls │ Dispatched │ Not run │ Unanswered │ Errors │ Time  │
    ├──────────────────┼─────────────┼───────┼────────────┼─────────┼────────────┼────────┼───────┤
    │ consumer_info    │ application │ 2     │ 2          │ 0       │ 0          │ 1      │ 690ms │
    │ consumer_ls      │ application │ 1     │ 1          │ 0       │ 0          │ 0      │ 550ms │
    │ knowledge_search │ builtin     │ 1     │ 1          │ 0       │ 0          │ 0      │ 2.45s │
    │ stream_info      │ application │ 1     │ 1          │ 0       │ 0          │ 0      │ 400ms │
    │ stream_ls        │ application │ 1     │ 1          │ 0       │ 0          │ 0      │ 1.20s │
    ╰──────────────────┴─────────────┴───────┴────────────┴─────────┴────────────┴────────┴───────╯
```

This session took six model responses and six tool calls over 22 seconds. One `consumer_info` call returned an error,
and `knowledge_search` took the most time. `session query` shows that call and its answer:

```nohighlight
$ fisk session query 33Fp8QnWb3aZkR1vT0yXcLmE2sD --errors
Session 33Fp8QnWb3aZkR1vT0yXcLmE2sD:

  Call 1:

           Tool: consumer_info
        Call ID: toolu_05
      Iteration: 3
         Status: error

    Input:

      {
        "stream": "ORDERS",
        "consumer": "SHIPING"
      }

    Answer:

      nats: error: consumer not found
```

## Cost and timing

`session stats <id>` reports one session: its frame, the tokens of each model response, and a row per tool it called.

| Item         | Description                                                                                  |
|--------------|----------------------------------------------------------------------------------------------|
| `Agent`      | The agent identity that started the session, left out when the journal records none          |
| `Caller`     | Who a channel recorded as asking for the session, left out when there is none               |
| `Model`      | The model the session ran against                                                            |
| `Status`     | `open`, or the reason the session ended                                                      |
| `Created`    | When the session was created                                                                 |
| `Ended`      | When the session ended, left out for an open session                                         |
| `Iterations` | Model responses across the whole session, against the per-turn cap when one was set         |
| `Wall clock` | Creation to the last journal record                                                          |
| `Tokens`     | Total against the token budget, then the split by tier                                       |

The iteration cap applies to each turn on its own, so a conversation of several turns can show more responses than
the cap. The wall clock runs to the last record journaled, so for a chat conversation continued over several days it
includes the time between turns.

The token total counts uncached input, output, cache reads and cache writes, weighed the same, which is what a token
budget counts. The `Thinking` column is the part of `Out` the model spent reasoning and is not added to the total a
second time.

### Tools table

| Column       | Description                                                                                  |
|--------------|----------------------------------------------------------------------------------------------|
| `Tool`       | The tool the model named                                                                     |
| `Kind`       | The provider that answered: `application`, `builtin`, `remote`, `custom`, `mcp` or `unknown` |
| `Calls`      | Calls the model made to the tool                                                             |
| `Dispatched` | Answered calls that were handed to the tool's provider                                       |
| `Not run`    | Answered calls that were not handed to the provider, such as a call a policy denied or the operator refused |
| `Unanswered` | Calls with no result yet                                                                     |
| `Errors`     | Answers that came back as an error                                                           |
| `Time`       | Total time of the tool's timed calls                                                         |

A result journaled before the provider kind was recorded cannot say whether the call ran. It counts in neither
`Dispatched` nor `Not run`, and its kind shows as `unknown`. One marked as a remote call in that older format shows as
`remote` and counts as dispatched.

### Reading the Time column

A call's duration is the gap between the record that ended the call and the record journaled immediately before it. The
agent dispatches tools one at a time, so no other tool ran in that gap.

The gap also holds anything the run did around the call without journaling it, such as policy hooks and waiting for
an operator to answer an approval prompt. A tool behind a confirmation can therefore show the operator's response time
as well as its own.

A call that deferred its answer is timed to the deferral. The time until the answer arrived is not counted, since that
depends on whoever supplies it. An approval given while the run was suspended is not counted either.

The `Time` column sums the durations of a tool's calls. When only some of the calls could be timed, it says how many
it covers, as in `1.10s (1 of 2 calls)`, and a dash means it covers none. Lines below the table say why:

| Line         | Meaning                                                                              |
|--------------|--------------------------------------------------------------------------------------|
| `Untimed`    | Calls that ended with a record carrying no time                                      |
| `Unanswered` | Calls with neither a result nor a deferral, such as one a suspended run left behind      |

### JSON

`--json` renders the same report as one JSON document. The example is trimmed to one model response and one tool.

```json
{
  "run_id": "33Fp8QnWb3aZkR1vT0yXcLmE2sD",
  "agent": "nats-ops",
  "model": "claude-opus-4-8",
  "created": "2026-09-22T09:14:03Z",
  "updated": "2026-09-22T09:14:24.81Z",
  "terminal": "completed",
  "ended": "2026-09-22T09:14:24.81Z",
  "iterations": 6,
  "max_iterations": 20,
  "max_tokens": 200000,
  "tokens": {
    "in_tokens": 3790,
    "out_tokens": 883,
    "cache_read_tokens": 33420,
    "cache_create_tokens": 3900,
    "thinking_tokens": 220
  },
  "responses": [
    {
      "iteration": 0,
      "time": "2026-09-22T09:14:05.1Z",
      "tokens": {
        "in_tokens": 2140,
        "out_tokens": 96,
        "cache_read_tokens": 0,
        "cache_create_tokens": 3900,
        "thinking_tokens": 40
      }
    }
  ],
  "tools": [
    {
      "tool": "consumer_info",
      "kinds": [
        "application"
      ],
      "calls": 2,
      "dispatched": 2,
      "undispatched": 0,
      "unanswered": 0,
      "errors": 1,
      "duration_ns": 690000000,
      "timed": 2,
      "untimed": 0
    }
  ]
}
```

`terminal` is absent for an open session. `duration_ns` is in nanoseconds, `timed` is how many calls it covers, and
`untimed` counts the calls that ended with no time recorded. `undispatched` is the `Not run` column. `agent`, `caller`,
`updated`, `ended` and each response's `time` are left out when the journal does not record them.

## Tool calls

`session query <id>` prints the tool calls of a session in the order they were made, each paired with the answer it
got. With no flags it prints every call.

```nohighlight
$ fisk session query 33Fp8QnWb3aZkR1vT0yXcLmE2sD --iteration 1 --calls-only
Session 33Fp8QnWb3aZkR1vT0yXcLmE2sD:

  Call 1:

           Tool: stream_info
        Call ID: toolu_02
      Iteration: 1

    Input:

      {
        "stream": "ORDERS"
      }

  Call 2:

           Tool: consumer_ls
        Call ID: toolu_03
      Iteration: 1

    Input:

      {
        "stream": "ORDERS"
      }
```

Each call shows the tool, the call id, the loop iteration that made it and, unless `--calls-only` is given, its status:
`answered`, `error` or `no answer yet`. A call that deferred its answer also shows a `Deferred` line with what the tool said it is waiting on.
Input and answers that are JSON are indented, and any other answer, such as a command's output, prints as it is.

| Flag                     | Description                                                                          |
|--------------------------|--------------------------------------------------------------------------------------|
| `--tool NAME`            | Selects the calls to this tool, matched exactly; repeat to select several tools      |
| `--iteration N`          | Selects the calls made at this loop iteration                                        |
| `--errors`               | Selects the calls whose answer is an error                                           |
| `--calls-only`           | Prints the calls without their answers                                               |
| `--results-only`         | Prints only the answers, leaving out calls nothing has answered                      |
| `--input-match PATTERN`  | Selects the calls whose input JSON matches this regular expression                   |
| `--output-match PATTERN` | Selects the calls whose answer matches this regular expression                       |
| `--json`                 | Renders the selected calls as one JSON document                                      |

Every filter given must match. `--calls-only` and `--results-only` cannot be combined. `--input-match` is applied to
the input JSON as journaled, which a model usually sends compact: `"consumer":"SHIP` matches both `consumer_info` calls of this session, and a
pattern holding the spaces of the indented form does not. A session with no matching calls prints `No matching calls`.

### JSON

```json
{
  "run_id": "33Fp8QnWb3aZkR1vT0yXcLmE2sD",
  "calls": [
    {
      "iteration": 3,
      "tool_use_id": "toolu_05",
      "tool": "consumer_info",
      "input": {
        "stream": "ORDERS",
        "consumer": "SHIPING"
      },
      "time": "2026-09-22T09:14:17.1Z",
      "answer": {
        "tool_use_id": "toolu_05",
        "result": {
          "tool_use_id": "toolu_05",
          "content": "nats: error: consumer not found",
          "is_error": true
        },
        "kind": "application",
        "dispatched": true
      },
      "answer_time": "2026-09-22T09:14:17.38Z"
    }
  ]
}
```

`time` is when the model response holding the call was journaled and `answer_time` when the answer was. `answer` is
the result record as journaled, so `content` holds the whole answer. A call that deferred carries a
`deferred` object, and a call with no answer has no `answer`. `--calls-only` leaves out `answer` and `answer_time`, and
`--results-only` leaves out `input`. `calls` is an empty list when nothing matches.

## Finding sessions

`session search` lists the sessions in the store that match every filter given, newest first. It prints the columns of
`session ls`, and adds an `Agent`, `Caller` or `Created` column when a filter on that field is given.

```nohighlight
$ fisk session search --status completed --since 2026-09-01
╭─────────────────────────────┬─────────────────┬───────────┬─────────────────────┬─────────────────────┬─────────────────────────────────────────────────────╮
│ ID                          │ Model           │ Status    │ Created             │ Updated             │ Prompt                                              │
├─────────────────────────────┼─────────────────┼───────────┼─────────────────────┼─────────────────────┼─────────────────────────────────────────────────────┤
│ 33Fp8QnWb3aZkR1vT0yXcLmE2sD │ claude-opus-4-8 │ completed │ 2026-09-22 11:14:03 │ 2026-09-22 11:14:24 │ report on the ORDERS stream and any consumers with… │
│ 2yKd9TqWm4RbX7cNz1LhVs0PfGe │ claude-opus-4-8 │ completed │ 2026-09-13 11:14:03 │ 2026-09-13 11:14:34 │ list the consumers on EVENTS                        │
╰─────────────────────────────┴─────────────────┴───────────┴─────────────────────┴─────────────────────┴─────────────────────────────────────────────────────╯
```

| Flag                 | Description                                                                              |
|----------------------|------------------------------------------------------------------------------------------|
| `--identity AGENT`   | Selects the sessions of this agent; a session journaled before the agent was recorded carries none and is left out |
| `--model MODEL`      | Selects the sessions run against this model                                              |
| `--caller CALLER`    | Selects the sessions a channel recorded this caller for                                  |
| `--status STATUS`    | Selects the sessions with this status: `open`, `completed`, `suspended`, `error`, `budget` or `max_iterations` |
| `--since WHEN`       | Selects the sessions created at or after this time                                       |
| `--until WHEN`       | Selects the sessions created before this time                                            |
| `--match PATTERN`    | Selects the sessions whose prompt matches this regular expression                        |
| `--json`             | Renders the matching sessions as one JSON document                                       |

`--since` and `--until` take a duration counted back from now (`24h`, `7d`), a date taken as local midnight
(`2026-09-01`), or an RFC3339 time. `--status open` selects every session with no ending: one still running, one with a
turn in flight, and one that journaled something after it last ended, such as a chat conversation that was continued.
`--model`, `--caller` and `--identity` compare exactly. A search that matches nothing prints `No sessions matched`.

### JSON

```json
{
  "runs": [
    {
      "run_id": "33Fq1cVx7MhN0bTe4KpWz9RgYuA",
      "agent": "nats-ops",
      "caller": "alice",
      "model": "claude-opus-4-8",
      "status": "open",
      "created": "2026-09-23T11:14:03Z",
      "updated": "2026-09-23T11:14:05.9Z",
      "prompt": "which streams have no consumers?"
    },
    {
      "run_id": "33Fp8QnWb3aZkR1vT0yXcLmE2sD",
      "agent": "nats-ops",
      "model": "claude-opus-4-8",
      "status": "completed",
      "created": "2026-09-22T09:14:03Z",
      "updated": "2026-09-22T09:14:24.81Z",
      "ended": "2026-09-22T09:14:24.81Z",
      "prompt": "report on the ORDERS stream and any consumers with pending messages"
    }
  ],
  "no_agent_excluded": 1
}
```

This is `session search --identity nats-ops --json`. `status` is `open` or the reason the session ended, and `prompt`
holds the whole prompt, which the table trims. `agent`, `caller`, `model` and `ended` are left out when a session
has none. `no_agent_excluded` counts the sessions `--identity` left out only because they carry no agent, and is `0`
without `--identity`. `runs` is an empty list when nothing matches.

## Older journals

A journal written before records carried a time and the agent's name still reads, with less to report.

`session stats` shows no `Agent` line, reports the wall clock as `not recorded in this journal`, and shows `-` in the
`Time` column with an `Untimed` line below the table. Tokens, calls and errors are still reported. In its JSON the
calls count in `untimed` with `duration_ns` at `0`, and the JSON of `session query` leaves out `time` and
`answer_time`. The `file` backend reports such a session's updated time as the journal file's modification time.

`search --identity` leaves out every session that carries no agent and says how many it left out:

```nohighlight
$ fisk session search --identity nats-ops
╭─────────────────────────────┬──────────┬─────────────────┬───────────┬─────────────────────┬─────────────────────────────────────────────────────╮
│ ID                          │ Agent    │ Model           │ Status    │ Updated             │ Prompt                                              │
├─────────────────────────────┼──────────┼─────────────────┼───────────┼─────────────────────┼─────────────────────────────────────────────────────┤
│ 33Fq1cVx7MhN0bTe4KpWz9RgYuA │ nats-ops │ claude-opus-4-8 │ open      │ 2026-09-23 13:14:05 │ which streams have no consumers?                    │
│ 33Fp8QnWb3aZkR1vT0yXcLmE2sD │ nats-ops │ claude-opus-4-8 │ completed │ 2026-09-22 11:14:24 │ report on the ORDERS stream and any consumers with… │
╰─────────────────────────────┴──────────┴─────────────────┴───────────┴─────────────────────┴─────────────────────────────────────────────────────╯

1 more session matched every other filter but carries no agent, so --identity left it out. It was journaled before the agent was recorded.
```

The agent filter a channel applies to its own listing, such as the [web channel](../../channels/web/#sessions)'s,
includes a session that carries no agent. `--identity` excludes it. To see such sessions from the command line, search
without `--identity` or use `session ls`.
