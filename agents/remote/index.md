# Remote agents

`--nats-context` points a terminal at an agent somebody else is running rather than starting one in this process:

```nohighlight
$ fisk run --nats-context production "how many streams are there"
```

`identity` in the configuration names which agent to talk to, and you must set it. A default or a name derived from the
application binary is shared by every agent built the same way, so the run could reach any of them.

The worker calls the model, runs the tools and writes the journal. Flags that describe that work are refused rather than
ignored: `--api-key`, `--base-url`, `--trace`, `--http-debug`, `--verbose`, `--state-dir` and `--no-telemetry`. Setting
one of these through an environment variable is ignored without an error, since you did not type it.
