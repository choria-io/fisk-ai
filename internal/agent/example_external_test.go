//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// These examples live in the external agent_test package on purpose: they can reach
// only agent's exported API, so they are proof that agent.Run is drivable from
// outside the package, which is the whole premise of the concurrent-caller work.
// They are standard testing.T tests rather than Ginkgo specs because the harness
// constructors take testing.TB, which only *testing.T satisfies; each reads as
// documentation of one supported composition built from the internal/agenttest
// harness. Cases needing a broker or a TCP bind are Integration-tagged and land
// elsewhere.
package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/choria-io/fisk"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/agenttest"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// exampleConfirmApp is exampleApp with its command gated behind ai:confirm, so a run
// must have the call approved before the command executes.
func exampleConfirmApp() *fisk.Application {
	app := fisk.New("app", "an app")
	do := app.Command("do", "do a thing").Tag("ai:confirm")
	do.Flag("level", "log level").Enum("debug", "info", "warn")
	do.Arg("subject", "the subject").Required().String()
	return app
}

// exampleApp is a small fisk application with one runnable command carrying a flag
// and a required argument, so the tool it becomes has a genuine input schema.
func exampleApp() *fisk.Application {
	app := fisk.New("app", "an app")
	do := app.Command("do", "do a thing")
	do.Flag("level", "log level").Enum("debug", "info", "warn")
	do.Arg("subject", "the subject").Required().String()
	return app
}

// TestExample_MinimalOneShot is setup 1: application tools, a scripted provider that
// answers once with a final text turn, asserting the terminal result and stats.
func TestExample_MinimalOneShot(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	provider := agenttest.NewScriptedProvider(t, agenttest.TextResponse("all done"))
	events := agenttest.NewRecordingEvents()

	cfg := agenttest.Config(t, app)
	opts := agent.Options{
		Config:     cfg,
		ConfigFile: "agent.yaml",
		Prompt:     []string{"summarize the widget inventory"},
		Provider:   provider,
	}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	g.Expect(res.Stats.LlmCalls).To(BeNumerically("==", 1))

	final, ok := events.FinalMessage()
	g.Expect(ok).To(BeTrue())
	g.Expect(final.Content[0].Text.Text).To(Equal("all done"))

	// The operator's prompt is the first user turn the model is asked to act on, and the
	// application tool is offered alongside it.
	reqs := provider.Requests()
	g.Expect(reqs).To(HaveLen(1))
	g.Expect(reqs[0].Messages[0].Role).To(Equal(llm.RoleUser))
	g.Expect(reqs[0].Messages[0].Content[0].Text.Text).To(Equal("summarize the widget inventory"))
	g.Expect(reqs[0].Tools).NotTo(BeEmpty())
}

// TestExample_ToolRoundTrip is setup 2: the provider returns a tool_use, the tool
// executes, its result feeds back, and a second turn completes the run.
func TestExample_ToolRoundTrip(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	input := json.RawMessage(`{"level":"info","subject":"hello"}`)
	provider := agenttest.NewScriptedProvider(t,
		agenttest.ToolUseResponse("call-1", "do", input),
		agenttest.TextResponse("finished"),
	)
	events := agenttest.NewRecordingEvents()

	cfg := agenttest.Config(t, app)
	opts := agent.Options{
		Config:     cfg,
		ConfigFile: "agent.yaml",
		Prompt:     []string{"run do at info level against hello"},
		Provider:   provider,
	}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	g.Expect(res.Stats.LlmCalls).To(BeNumerically("==", 2))

	// The fake application echoes its arguments, so the result carries the subject.
	results := events.ToolResults()
	g.Expect(results).To(HaveLen(1))
	g.Expect(results[0].IsError).To(BeFalse())
	g.Expect(results[0].Output).To(ContainSubstring("hello"))

	// The first request opened with the operator's prompt; the second carried the tool
	// result back as a user turn, keyed to the tool_use id the model chose.
	reqs := provider.Requests()
	g.Expect(reqs).To(HaveLen(2))
	g.Expect(reqs[0].Messages[0].Content[0].Text.Text).To(Equal("run do at info level against hello"))
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	g.Expect(last.Role).To(Equal(llm.RoleUser))
	g.Expect(last.Content[0].ToolResult).NotTo(BeNil())
	g.Expect(last.Content[0].ToolResult.ToolUseID).To(Equal("call-1"))
}

// TestExample_SharedRAGStore drives a run against a caller-owned read-only knowledge
// store (Options.RAGStore) and proves Run borrows it: the run completes and the store
// is still open afterward, since Run did not close a store it did not open.
func TestExample_SharedRAGStore(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	// A one-document corpus, indexed once into a read-only store the caller owns.
	corpus := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(corpus, "note.md"), []byte("the widget inventory is managed here"), 0o600)).To(Succeed())

	app := agenttest.NewFakeApp(t, exampleApp())
	cfg := agenttest.Config(t, app)
	cfg.Harness.RAG = &config.RAGConfig{Enabled: true, Directory: filepath.Join(t.TempDir(), "kb"), Paths: []string{corpus}}

	writer, err := rag.OpenWriter(cfg, "")
	g.Expect(err).NotTo(HaveOccurred())
	_, err = writer.Index(ctx, []string{corpus}, rag.IndexOptions{})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(writer.Close()).To(Succeed())

	store, err := rag.Open(cfg, "")
	g.Expect(err).NotTo(HaveOccurred())
	defer store.Close()
	g.Expect(store.Built()).To(BeTrue())

	provider := agenttest.NewScriptedProvider(t, agenttest.TextResponse("done"))
	events := agenttest.NewRecordingEvents()
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"go"}, Provider: provider, RAGStore: store}

	res, err := agent.Run(ctx, opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))

	// The borrowed store survives the run: a close would have dropped the db handle and
	// flipped Built to false.
	g.Expect(store.Built()).To(BeTrue())
}

// TestExample_ConcurrentRunsNoCrosstalk is setup 11, the acceptance gate: N runs
// execute concurrently in one process, each with its own ToolWorkDir and StoreDir, and
// none sees another's tool working directory or store. This is the composition the whole
// concurrent-caller effort exists to make safe (items 1, 2 and 5), driven entirely
// through the exported Run API.
func TestExample_ConcurrentRunsNoCrosstalk(t *testing.T) {
	g := NewWithT(t)

	const n = 6
	// One shared fake application, as a server holds one byName tool set across runs.
	app := agenttest.NewFakeApp(t, exampleApp())

	// Everything that touches testing.T is built on the test goroutine up front; the
	// goroutines below only call agent.Run.
	toolDirs := make([]string, n)
	storeDirs := make([]string, n)
	providers := make([]*agenttest.ScriptedProvider, n)
	prompters := make([]*agenttest.ScriptedPrompter, n)
	events := make([]*agenttest.RecordingEvents, n)
	cfgs := make([]*config.Config, n)
	for i := 0; i < n; i++ {
		toolDirs[i] = t.TempDir()
		storeDirs[i] = t.TempDir()
		providers[i] = agenttest.NewScriptedProvider(t,
			agenttest.ToolUseResponse("c1", "do", json.RawMessage(`{"subject":"x"}`)),
			agenttest.TextResponse("done"),
		)
		prompters[i] = agenttest.NewScriptedPrompter(t)
		events[i] = agenttest.NewRecordingEvents()
		cfgs[i] = agenttest.Config(t, app, agenttest.WithMemory())
	}

	results := make([]*agent.Result, n)
	errs := make([]error, n)
	outputs := make([]string, n)

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			opts := agent.Options{
				Config:      cfgs[i],
				ConfigFile:  "agent.yaml",
				Prompt:      []string{"go"},
				Provider:    providers[i],
				ToolWorkDir: toolDirs[i],
				StoreDir:    storeDirs[i],
			}
			results[i], errs[i] = agent.Run(context.Background(), opts, events[i], prompters[i])
			if r := events[i].ToolResults(); len(r) == 1 {
				outputs[i] = r[0].Output
			}
		}()
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		g.Expect(errs[i]).NotTo(HaveOccurred(), "run %d", i)
		g.Expect(results[i].Reason).To(Equal(runstate.ReasonCompleted), "run %d", i)

		// The tool ran in this run's own working directory (commandEnv sets PWD to it),
		// and in none of the siblings'.
		g.Expect(outputs[i]).To(ContainSubstring("PWD=" + toolDirs[i]))
		for j := 0; j < n; j++ {
			if j != i {
				g.Expect(outputs[i]).NotTo(ContainSubstring("PWD=" + toolDirs[j]))
			}
		}

		// This run's memory store landed under its own StoreDir, isolated from the others.
		g.Expect(filepath.Join(storeDirs[i], "memory", "agent")).To(BeADirectory())
	}
}

// TestExample_MemoryWrite is setup 3: with memory enabled, the model calls the
// memory_write built-in, and the memory it writes lands in this run's store under its
// StoreDir.
func TestExample_MemoryWrite(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	write := json.RawMessage(`{"key":"build.notes","description":"how the build works","content":"run abt t u"}`)
	provider := agenttest.NewScriptedProvider(t,
		agenttest.ToolUseResponse("c1", "memory_write", write),
		agenttest.TextResponse("saved"),
	)
	events := agenttest.NewRecordingEvents()

	storeDir := t.TempDir()
	cfg := agenttest.Config(t, app, agenttest.WithMemory())
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"remember the build"}, Provider: provider, StoreDir: storeDir}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))

	data, err := os.ReadFile(filepath.Join(storeDir, "memory", "agent", "build.notes.md"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(data)).To(ContainSubstring("run abt t u"))
}

// TestExample_HumanInTheLoopConfirm is setup 7: with human_in_the_loop enabled, the
// model calls ask_human_confirm, which routes to the scripted prompter, and the run
// completes with the operator's answer.
func TestExample_HumanInTheLoopConfirm(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	provider := agenttest.NewScriptedProvider(t,
		agenttest.ToolUseResponse("c1", "ask_human_confirm", json.RawMessage(`{"question":"Proceed?"}`)),
		agenttest.TextResponse("done"),
	)
	events := agenttest.NewRecordingEvents()

	prompter := agenttest.NewScriptedPrompter(t)
	var asked string
	prompter.ConfirmFn = func(q string) (bool, error) {
		asked = q
		return true, nil
	}

	cfg := agenttest.Config(t, app, agenttest.WithHITL())
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"ask me first"}, Provider: provider}

	res, err := agent.Run(context.Background(), opts, events, prompter)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	g.Expect(asked).To(Equal("Proceed?"))

	results := events.ToolResults()
	g.Expect(results).To(HaveLen(1))
	g.Expect(results[0].Output).To(ContainSubstring(`"confirmed":true`))
}

// TestExample_InteractiveContinuation is setup 9: a run driven past its first completed
// turn by Options.NextPrompt, which supplies one follow-up and then ends the session.
func TestExample_InteractiveContinuation(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	provider := agenttest.NewScriptedProvider(t,
		agenttest.TextResponse("first answer"),
		agenttest.TextResponse("second answer"),
	)
	events := agenttest.NewRecordingEvents()

	calls := 0
	next := func(context.Context) agent.Continuation {
		calls++
		if calls == 1 {
			return agent.Continuation{Text: "and then?", Continue: true}
		}
		return agent.Continuation{Continue: false}
	}

	cfg := agenttest.Config(t, app)
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"start"}, Provider: provider, NextPrompt: next}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	// Two turns ran: the initial prompt and the one follow-up before the session ended.
	g.Expect(res.Stats.LlmCalls).To(BeNumerically("==", 2))
	g.Expect(calls).To(Equal(2))
}

// TestExample_CheckpointSuspendResume is setup 8: a checkpointed run does one tool
// call, suspends at the next loop boundary, and a second Run resumes the saved session
// to completion. It exercises suspend/resume through the exported Run entry point (the
// runner-level path is tested elsewhere).
func TestExample_CheckpointSuspendResume(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	storeDir := t.TempDir()
	app := agenttest.NewFakeApp(t, exampleApp())

	// Run 1: one tool call, then a suspend is requested at the next boundary.
	suspendPolls := 0
	opts1 := agent.Options{
		Config:           agenttest.Config(t, app),
		ConfigFile:       "agent.yaml",
		Prompt:           []string{"start work"},
		Provider:         agenttest.NewScriptedProvider(t, agenttest.ToolUseResponse("c1", "do", json.RawMessage(`{"subject":"x"}`))),
		StoreDir:         storeDir,
		Checkpoint:       agent.Checkpoint{Enabled: true},
		SuspendRequested: func() bool { suspendPolls++; return suspendPolls > 1 },
	}
	res1, err := agent.Run(ctx, opts1, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res1.Reason).To(Equal(runstate.ReasonSuspended))
	g.Expect(res1.SessionID).NotTo(BeEmpty())

	// Run 2: resume the saved session (same StoreDir) to a final answer.
	opts2 := agent.Options{
		Config:     agenttest.Config(t, app),
		ConfigFile: "agent.yaml",
		Provider:   agenttest.NewScriptedProvider(t, agenttest.TextResponse("finished")),
		StoreDir:   storeDir,
		Checkpoint: agent.Checkpoint{ResumeID: res1.SessionID},
	}
	res2, err := agent.Run(ctx, opts2, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res2.Reason).To(Equal(runstate.ReasonCompleted))
	g.Expect(res2.SessionID).To(Equal(res1.SessionID))
}

// panicProvider panics on every model call, to exercise Run's panic barrier.
type panicProvider struct{}

func (panicProvider) Call(context.Context, llm.Request) (*llm.Response, error) {
	panic("boom in the model call")
}
func (panicProvider) Capabilities() llm.Caps { return llm.Caps{Provider: "anthropic"} }

// TestExample_PanicRecovered proves the panic barrier: a panic on the run goroutine is
// recovered and returned as a distinguishable *PanicError (not a terminal outcome), the
// stack reaches the Events sink, and the returned error carries no stack.
func TestExample_PanicRecovered(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	events := agenttest.NewRecordingEvents()

	cfg := agenttest.Config(t, app)
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"go"}, Provider: panicProvider{}}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))

	// The crash is distinguishable from an outcome, and res.Reason stays unset.
	var panicErr *agent.PanicError
	g.Expect(errors.As(err, &panicErr)).To(BeTrue())
	g.Expect(res).NotTo(BeNil())
	g.Expect(res.Reason).To(BeEmpty())

	// The stack was delivered to the Events sink, and not leaked onto the returned error.
	panics := events.Panics()
	g.Expect(panics).To(HaveLen(1))
	g.Expect(panics[0].Stack).NotTo(BeEmpty())
	g.Expect(panicErr.Error()).NotTo(ContainSubstring("boom"))
}

// panickyEvents panics inside Panicked, to prove the barrier's inner recover keeps a
// misbehaving Events sink from escaping and crashing the process it protects.
type panickyEvents struct{ *agenttest.RecordingEvents }

func (panickyEvents) Panicked(any, []byte) { panic("boom in the events sink") }

// TestExample_PanickingEventsSinkDoesNotEscape drives a crash whose Events.Panicked
// itself panics; Run must still return a PanicError rather than letting the second panic
// take down the process.
func TestExample_PanickingEventsSinkDoesNotEscape(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	cfg := agenttest.Config(t, app)
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"go"}, Provider: panicProvider{}}

	res, err := agent.Run(context.Background(), opts, panickyEvents{agenttest.NewRecordingEvents()}, agenttest.NewScriptedPrompter(t))

	var panicErr *agent.PanicError
	g.Expect(errors.As(err, &panicErr)).To(BeTrue())
	g.Expect(res).NotTo(BeNil())
}

// TestExample_ConfirmGateApprovedOverNonTTYChannel is setup 5 plus item 5's core
// property: a scripted prompter that reports it can prompt (a stand-in for a
// non-terminal operator channel like Slack) approves a confirm-gated tool, so the tool
// runs even though the test has no TTY. Interactivity follows the prompter's
// CanPrompt, not the terminal, and the no-operator advisory does not fire.
func TestExample_ConfirmGateApprovedOverNonTTYChannel(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleConfirmApp())
	input := json.RawMessage(`{"level":"info","subject":"hello"}`)
	provider := agenttest.NewScriptedProvider(t,
		agenttest.ToolUseResponse("call-1", "do", input),
		agenttest.TextResponse("done"),
	)
	events := agenttest.NewRecordingEvents()

	prompter := agenttest.NewScriptedPrompter(t)
	approved := false
	prompter.ApproveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
		approved = true
		return toolkit.ConfirmOnce, nil
	}

	cfg := agenttest.Config(t, app)
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"do it"}, Provider: provider}

	res, err := agent.Run(context.Background(), opts, events, prompter)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	g.Expect(approved).To(BeTrue())
	// The gated tool ran (its result came back) and no "no operator" advisory fired.
	g.Expect(events.ToolResults()).To(HaveLen(1))
	g.Expect(events.HasWarning(agent.WarnConfirmNoTerminal)).To(BeFalse())
}

// TestExample_ConfirmGateDeniedWithoutOperator is setup 6: with no operator reachable,
// a confirm-gated tool is declined without running and the no-operator advisory fires.
// This is the composition the job system uses.
func TestExample_ConfirmGateDeniedWithoutOperator(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleConfirmApp())
	input := json.RawMessage(`{"level":"info","subject":"hello"}`)
	provider := agenttest.NewScriptedProvider(t,
		agenttest.ToolUseResponse("call-1", "do", input),
		agenttest.TextResponse("done"),
	)
	events := agenttest.NewRecordingEvents()

	// NoOperator makes CanPrompt report false; the ApproveFn is left unset because the
	// gate must deny before ever reaching it.
	prompter := agenttest.NewScriptedPrompter(t).NoOperator()

	cfg := agenttest.Config(t, app)
	opts := agent.Options{Config: cfg, ConfigFile: "agent.yaml", Prompt: []string{"do it"}, Provider: provider}

	res, err := agent.Run(context.Background(), opts, events, prompter)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	// The advisory fired at start, and the gated tool never ran, so no tool result was
	// traced (the model receives an authoritative denial in the conversation instead).
	g.Expect(events.HasWarning(agent.WarnConfirmNoTerminal)).To(BeTrue())
	g.Expect(events.ToolResults()).To(BeEmpty())
}

// TestExample_KnowledgeStoreBaseWithoutIndex is setup 4 (the empty-index case): with
// knowledge enabled and a StoreDir in effect but nothing indexed under it, the run
// completes but warns loudly rather than silently starting with an empty knowledge
// base (the silent-mismatch trap StoreDir could otherwise introduce).
func TestExample_KnowledgeStoreBaseWithoutIndex(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	provider := agenttest.NewScriptedProvider(t, agenttest.TextResponse("done"))
	events := agenttest.NewRecordingEvents()

	cfg := agenttest.Config(t, app, agenttest.WithRAG())
	opts := agent.Options{
		Config:     cfg,
		ConfigFile: "agent.yaml",
		Prompt:     []string{"search the knowledge base"},
		Provider:   provider,
		StoreDir:   t.TempDir(),
	}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	g.Expect(events.HasWarning(agent.WarnKnowledgeIndexAbsent)).To(BeTrue())
}

// TestExample_MaxIterations is setup 10 (part one): the model never returns a final
// answer, so the single permitted iteration is spent and the run ends on the
// max-iterations outcome.
func TestExample_MaxIterations(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	input := json.RawMessage(`{"subject":"x"}`)
	provider := agenttest.NewScriptedProvider(t,
		agenttest.ToolUseResponse("call-1", "do", input),
	)
	events := agenttest.NewRecordingEvents()

	cfg := agenttest.Config(t, app, agenttest.WithMaxIterations(1))
	opts := agent.Options{
		Config:     cfg,
		ConfigFile: "agent.yaml",
		Prompt:     []string{"keep working on the task"},
		Provider:   provider,
	}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).To(MatchError(ContainSubstring("max iterations")))
	g.Expect(res.Reason).To(Equal(runstate.ReasonMaxIterations))
}

// TestExample_TokenBudget is setup 10 (part two): a single turn reports enough token
// usage to cross the run budget, so the run ends on the budget outcome before the
// tool even executes.
func TestExample_TokenBudget(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, exampleApp())
	input := json.RawMessage(`{"subject":"x"}`)
	resp := agenttest.ToolUseResponse("call-1", "do", input)
	resp.Usage = llm.Usage{In: 100, Out: 100}
	provider := agenttest.NewScriptedProvider(t, resp)
	events := agenttest.NewRecordingEvents()

	cfg := agenttest.Config(t, app, agenttest.WithMaxTokens(50))
	opts := agent.Options{
		Config:     cfg,
		ConfigFile: "agent.yaml",
		Prompt:     []string{"keep working on the task"},
		Provider:   provider,
	}

	res, err := agent.Run(context.Background(), opts, events, agenttest.NewScriptedPrompter(t))
	g.Expect(err).To(MatchError(ContainSubstring("token budget")))
	g.Expect(res.Reason).To(Equal(runstate.ReasonBudget))
}
