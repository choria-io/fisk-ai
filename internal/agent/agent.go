//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package agent turns a parsed configuration into a running agent: it loads the
// tools, imports any remote ones, builds the LLM client, sets up checkpointing
// and resume, and drives the agentic loop to a terminal state. It owns no CLI
// concerns: flags, signals and terminal rendering stay with the caller, which
// receives the run's narration, tool traces and advisories as structured Events
// and gets a Result to render at the end.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/builtin"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	// Link the NATS a2a transport in so it registers itself; a2a.NewTransport
	// resolves the configured transport from the registry, and this is the sole
	// transport today.
	_ "github.com/choria-io/fisk-ai/internal/a2a/nats"
	"github.com/choria-io/fisk-ai/internal/conns"
	"github.com/choria-io/fisk-ai/internal/llm"
	// Link the anthropic provider in so it registers itself; llm.NewProvider resolves
	// the configured provider from the registry, and this is the sole provider today.
	_ "github.com/choria-io/fisk-ai/internal/llm/anthropic"
	"github.com/choria-io/fisk-ai/internal/memory"
	// Link the file memory backend in so it registers itself; memory.New resolves
	// the configured backend from the registry, and this is the sole backend today.
	_ "github.com/choria-io/fisk-ai/internal/memory/file"
	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	// Link the file session backend in so it registers itself; runstate.New resolves
	// the configured backend from the registry, and this is the sole backend today.
	_ "github.com/choria-io/fisk-ai/internal/runstate/file"
	"github.com/choria-io/fisk-ai/internal/util"
)

// defaultMaxOutputTokens caps the tokens generated per LLM call. It is distinct
// from the cumulative llm.budget.max_tokens spend cap: this bounds a single
// response and must stay within every supported model's per-response limit,
// while the budget bounds the whole run.
const defaultMaxOutputTokens = 8192

// thinkingMaxOutputTokens raises the per-call output cap when thinking is enabled
// so the reasoning summary and the answer both fit; thinking output counts toward
// this per-response limit. It stays within the non-streaming ceiling that keeps
// responses clear of SDK HTTP timeouts.
const thinkingMaxOutputTokens = 16384

// resolveMaxOutputTokens picks the per-call output cap. An explicit
// llm.budget.max_output_tokens wins so an operator can fit an endpoint whose
// per-response limit is below the default; otherwise the built-in default is used,
// raised when thinking is on so the reasoning summary and the answer both fit.
func resolveMaxOutputTokens(cfg *config.Config, thinking bool) int64 {
	if n := cfg.LLM.Budget.MaxOutputTokens; n > 0 {
		return n
	}
	if thinking {
		return thinkingMaxOutputTokens
	}
	return defaultMaxOutputTokens
}

// toolSearchDegradation returns the advisory to raise when totalTools crosses the
// tool-search threshold but tool search cannot run, so every tool is sent to the
// model directly. It returns nil when there is nothing to warn about (tool search is
// available, or the set is small enough to send directly anyway). The remedy differs
// by cause, so the kind names it: the provider cannot do tool search, or the operator
// disabled it with no_tool_search.
func toolSearchDegradation(totalTools int, caps llm.Caps, toolSearchAllowed bool) *Warning {
	if toolSearchAllowed || totalTools < util.ToolSearchThreshold {
		return nil
	}

	kind := WarnToolSearchDisabled
	if !caps.SupportsToolSearch {
		kind = WarnToolSearchUnsupported
	}

	return &Warning{Kind: kind, Count: totalTools}
}

// resumeReminder is appended to the system prompt of a resumed run so the model
// re-verifies external state before acting on results captured before the
// suspension.
const resumeReminder = "This session was suspended and has now resumed. Tool results earlier in the conversation may be stale; re-verify current state before taking any state-changing action."

// Checkpoint carries the resumable-run options. Enabled starts a new checkpointed
// run; ResumeID continues an existing one; the two are mutually exclusive, which
// the caller validates before calling Run.
type Checkpoint struct {
	Enabled  bool
	Name     string
	ResumeID string
	Force    bool
}

// Options is everything Run needs to execute a run. Config is already parsed so
// Run does no file IO; the caller owns flags, signals and rendering.
type Options struct {
	Config     *config.Config
	ConfigFile string
	Prompt     []string

	APIKey  string
	BaseURL string
	// HTTPDebugOut, when non-nil, receives a dump of every Anthropic API request and
	// response body. The caller owns the writer's lifecycle (for example a file it
	// opens and closes); os.Stderr reproduces the old stderr-dump behavior.
	HTTPDebugOut io.Writer
	TraceFile    string
	Verbose      bool

	Checkpoint Checkpoint

	// SuspendRequested reports that a graceful suspend was asked for; it is polled
	// at a loop boundary. Nil when suspension is not wired.
	SuspendRequested func() bool

	// NextPrompt continues the run interactively: after a turn reaches a boundary the
	// operator can act on (a completed turn, or one that hit the iteration cap), it is
	// called to gather the operator's next decision (see Continuation). Nil disables
	// interactive continuation, the default one-shot behavior. It is called only from
	// the single run goroutine, like the prompter.
	NextPrompt func(context.Context) Continuation
}

// Continuation is the operator's decision at an interactive turn boundary. Continue
// false ends the session. Reset clears the conversation context before the next turn,
// keeping the system prompt, tools and the session's confirm-gate approvals; with an
// empty Text it reopens the input for a fresh prompt without running a turn. Text is
// the next user prompt when the turn proceeds.
type Continuation struct {
	Text     string
	Reset    bool
	Continue bool
}

// Result is the outcome of a run, for the caller to render.
type Result struct {
	Reason    runstate.TerminalReason
	Stats     *util.RunStats
	SessionID string
}

// Run loads the tools and prompt from opts.Config, sets up checkpointing and
// resume as requested, and drives the agentic loop to a terminal state. It emits
// the run's narration, tool traces and advisories through events and returns a
// Result the caller renders. Interactive decisions (confirm-gate approval and the
// human-in-the-loop questions) are put to the operator through prompter, injected
// per run so the concurrent MCP path never receives it. The returned Result is
// non-nil even on error so the caller can always print the stats. The context
// governs cancellation; a graceful suspend is requested via opts.SuspendRequested.
func Run(ctx context.Context, opts Options, events Events, prompter toolkit.Prompter) (*Result, error) {
	cfg := opts.Config
	res := &Result{}

	tools, err := fisk.LoadTools(cfg)
	if err != nil {
		return res, err
	}

	byName := make(map[string]*fisk.FiskCommandTool, len(tools))
	for _, t := range tools {
		byName[t.Name()] = t
	}

	// taken tracks every tool name already claimed (local tools, then built-ins,
	// then imported remote tools), so a clash across those namespaces is caught
	// rather than silently shadowing one with another, since the model addresses
	// every tool by a single flat name.
	taken := make(map[string]bool, len(tools))
	for name := range byName {
		taken[name] = true
	}

	// Built-in human-in-the-loop tools are injected only here, in the agent run
	// path, so they are never reachable over MCP where there is no operator. They
	// are never deferred, so enabling them neither hides them behind tool search
	// nor changes how the application tools are presented.
	builtins := builtin.HITLTools(cfg)
	builtinByName := make(map[string]*builtin.BuiltinTool, len(builtins))
	for _, b := range builtins {
		if taken[b.Name()] {
			return res, fmt.Errorf("human_in_the_loop adds a built-in tool %q but the application already exposes a tool with that name; exclude or rename it", b.Name())
		}
		builtinByName[b.Name()] = b
		taken[b.Name()] = true
	}

	if len(builtins) > 0 && !util.StdinIsTerminal() {
		events.Warn(Warning{Kind: WarnHITLNoTerminal})
	}

	// Built-in memory tools are added here in the agent run path too, but tracked
	// in their own slice so they never perturb the human-in-the-loop system note or
	// its no-terminal warning. They are pure (no operator), and like the HITL tools
	// they are not reachable over MCP. The store is built now so a misconfiguration
	// (unknown backend, bad options, an unwritable directory) fails before the loop.
	var memStore memory.Store
	var memBuiltins []*builtin.BuiltinTool
	if cfg.MemoryEnabled() {
		memStore, err = memory.New(cfg)
		if err != nil {
			return res, err
		}

		memBuiltins = builtin.MemoryTools(cfg, memStore)
		for _, b := range memBuiltins {
			if taken[b.Name()] {
				return res, fmt.Errorf("memory adds a built-in tool %q but the application already exposes a tool with that name; exclude or rename it", b.Name())
			}
			builtinByName[b.Name()] = b
			taken[b.Name()] = true
		}
	}

	// The built-in knowledge_search tool is added here in the agent run path too,
	// tracked in its own slice like the memory tools. rag.Open validates the config
	// (a bad embeddings block fails before the loop) but treats a missing index file
	// as a soft empty state, so a first run never fails to start. The store is opened
	// read-only; knowledge index is the writer.
	var ragStore *rag.Store
	var ragBuiltins []*builtin.BuiltinTool
	if cfg.RAGEnabled() {
		ragStore, err = rag.Open(cfg)
		if err != nil {
			return res, err
		}
		defer ragStore.Close()

		ragBuiltins = builtin.RAGTools(cfg, ragStore)
		for _, b := range ragBuiltins {
			if taken[b.Name()] {
				return res, fmt.Errorf("knowledge adds a built-in tool %q but the application already exposes a tool with that name; exclude or rename it", b.Name())
			}
			builtinByName[b.Name()] = b
			taken[b.Name()] = true
		}
	}

	// Import remote tools, if any, before building the request tool set. A run is
	// strict: a named remote agent that cannot be reached or imported aborts the
	// run rather than silently dropping tools the prompt may depend on. The
	// connection is held open for the whole run since each remote tool call uses it.
	var remoteTools []*a2a.RemoteTool
	remoteByName := map[string]*a2a.RemoteTool{}
	if len(cfg.RemoteTools) > 0 {
		provider, err := conns.Connect(cfg.NatsContext, cfg.Identity)
		if err != nil {
			return res, fmt.Errorf("connecting to NATS for remote tools: %w", err)
		}
		defer provider.Close()

		transport, err := a2a.NewTransport(cfg.A2ATransport(), provider, a2a.TransportConfig{Identity: cfg.Identity, Timeout: cfg.LLM.Budget.CallTimeoutParsed})
		if err != nil {
			return res, err
		}

		client, err := a2a.NewClient(transport, cfg.Identity)
		if err != nil {
			return res, err
		}

		var imports []remotetools.HostImport
		remoteTools, remoteByName, imports, err = remotetools.ImportForRun(ctx, client, cfg, taken)
		if err != nil {
			return res, err
		}
		events.RemoteHostNotes(imports)
	}

	// The run needs at least one callable tool, counting every source the model can
	// address: filtered application tools, the built-in HITL/memory/knowledge_search
	// tools, and imported remote tools. Checking only the application tools would
	// abort a run whose sole tools are native (e.g. knowledge_search) or remote.
	if len(tools)+len(builtins)+len(memBuiltins)+len(ragBuiltins)+len(remoteTools) == 0 {
		if cfg.ApplicationPath == "" {
			return res, fmt.Errorf("no tools available: this agent wraps no application (application_path unset) and enables no built-in or remote tools; set application_path, or enable harness.knowledge, harness.memory, human_in_the_loop, or remote_tools in %q", opts.ConfigFile)
		}
		return res, fmt.Errorf("no tools available after filtering; check include/exclude in %q", opts.ConfigFile)
	}

	// The confirm gate enforces confirmation tags: a tool carrying ai:confirm (always
	// on) or any tag listed in confirm_tags must be approved by the operator before
	// each run, with an "allow for the session" answer remembered for the rest of the
	// run. It is independent of human_in_the_loop. With no terminal there is no
	// operator to ask, so a gated tool can never be approved and will always be
	// declined; warn loudly, naming the count, since otherwise those commands would
	// silently fail mid-run.
	gate := util.NewConfirmGate(prompter)
	confirmTags := cfg.ConfirmTags()
	confirmTools := 0
	for _, t := range tools {
		if t.NeedsConfirm(confirmTags) {
			confirmTools++
		}
	}
	if confirmTools > 0 && !util.StdinIsTerminal() {
		events.Warn(Warning{Kind: WarnConfirmNoTerminal, Count: confirmTools})
	}

	// A configured confirm tag that matches no loaded tool is almost always a typo;
	// left unreported it gives a false sense of safety, since the operator believes a
	// command is gated when nothing actually carries the tag. Warn per unmatched tag.
	for _, tag := range confirmTags {
		matched := false
		for _, t := range tools {
			if slices.Contains(t.Tags(), tag) {
				matched = true
				break
			}
		}
		if !matched {
			events.Warn(Warning{Kind: WarnConfirmTagUnmatched, Name: tag})
		}
	}

	prompt := opts.Prompt
	if len(prompt) == 0 {
		prompt = []string{"assist the user"}
	}

	stats := &util.RunStats{Start: time.Now(), Model: cfg.LLM.Model}
	res.Stats = stats

	if opts.BaseURL != "" {
		if err := util.ValidateBaseURL("--base-url / ANTHROPIC_BASE_URL", opts.BaseURL); err != nil {
			return res, err
		}
	}

	// The provider owns the wire call. Its cross-cutting request hooks (the HTTP debug
	// dump and the request tracer) are assembled here, where their lifecycle lives: the
	// tracer's summary and close are deferred against this run's stats and exit paths.
	var middlewares []llm.Middleware
	if opts.HTTPDebugOut != nil {
		middlewares = append(middlewares, util.HttpDebugMiddleware(opts.HTTPDebugOut))
	}

	if opts.TraceFile != "" {
		tracer, err := util.NewTracer(opts.TraceFile)
		if err != nil {
			return res, err
		}
		// Close runs last; the summary line is written just before it. Both are
		// deferred so they fire on every exit path, including errors.
		defer func() {
			if cerr := tracer.Close(); cerr != nil {
				fmt.Fprintf(os.Stderr, "warning: closing trace file: %v\n", cerr)
			}
		}()
		defer tracer.RecordSummary(stats)

		tracer.RecordSession(cfg.LLM.Model, opts.ConfigFile, util.Version())
		middlewares = append(middlewares, tracer.Middleware)
	}

	// The provider is resolved from the registry by name rather than constructed
	// directly, so a second backend is linked in the same way the a2a, memory and
	// session backends are. The name comes from llm.provider, which defaults to
	// anthropic, the only provider linked in today.
	provider, err := llm.NewProvider(cfg.LLMProvider(), llm.Config{
		APIKey:      opts.APIKey,
		BaseURL:     opts.BaseURL,
		Timeout:     cfg.LLM.Budget.CallTimeoutParsed,
		Middlewares: middlewares,
	})
	if err != nil {
		return res, err
	}

	// Large tool sets are deferred and discovered via the tool search tool; small
	// ones are sent directly. Deferral is decided over the combined local and remote
	// set. Built-in tools are appended after, never deferred. Deferral is offered only
	// when the resolved provider supports tool search and the operator has not disabled
	// it, so a backend that cannot honor deferred loading always gets every tool direct.
	caps := provider.Capabilities()
	toolSearchAllowed := caps.SupportsToolSearch && cfg.ToolSearchEnabled()
	deferrable := make([]toolkit.Tool, 0, len(tools)+len(remoteTools))
	for _, t := range tools {
		deferrable = append(deferrable, t)
	}
	for _, rt := range remoteTools {
		deferrable = append(deferrable, rt)
	}
	toolDefs, toolSearch := util.BuildToolParams(deferrable, len(builtins)+len(memBuiltins)+len(ragBuiltins), toolSearchAllowed)
	for _, b := range builtins {
		toolDefs = append(toolDefs, b.Definition(false))
	}
	for _, b := range memBuiltins {
		toolDefs = append(toolDefs, b.Definition(false))
	}
	for _, b := range ragBuiltins {
		toolDefs = append(toolDefs, b.Definition(false))
	}

	// A tool set that crosses the tool-search threshold but cannot use tool search is
	// sent to the model in full every request, spending context the search tool exists
	// to save. That is a silent degradation worth surfacing.
	totalTools := len(deferrable) + len(builtins) + len(memBuiltins) + len(ragBuiltins)
	if w := toolSearchDegradation(totalTools, caps, toolSearchAllowed); w != nil {
		events.Warn(*w)
	}

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: strings.Join(prompt, " ")}}}},
	}

	maxIter := cfg.LLM.Budget.MaxIterations
	maxTokens := cfg.LLM.Budget.MaxTokens

	// The system prompt is the user's prompt, plus a note about reaching the
	// operator when the human-in-the-loop tools are enabled: the agent loop ends on
	// a text-only turn, so without it the model tends to "ask the user" in prose and
	// silently end the run instead of calling a tool. It is constant across
	// iterations, so build it once.
	system := []string{cfg.SystemPrompt}
	if note := builtin.HITLSystemNote(builtins); note != "" {
		system = append(system, note)
	}
	if note := builtin.MemorySystemNote(cfg); note != "" {
		system = append(system, note)
	}
	if note := builtin.RAGSystemNote(cfg); note != "" {
		system = append(system, note)
	}

	// Thinking is requested only when enabled; the provider omits it otherwise so the
	// model and backend use their default behavior. The per-call output cap is constant
	// across iterations, so resolve it once.
	thinking := cfg.ThinkingEnabled()
	maxOutputTokens := resolveMaxOutputTokens(cfg, thinking)

	checkpointing := opts.Checkpoint.Enabled || opts.Checkpoint.ResumeID != ""
	interactive := opts.NextPrompt != nil

	info := RunInfo{
		Tools:           len(tools),
		ThinkingEnabled: cfg.ThinkingEnabled(),
		ConfirmTools:    confirmTools,
		ConfirmTags:     confirmTags,
		TraceFile:       opts.TraceFile,
		NoApplication:   cfg.ApplicationPath == "",
	}

	var (
		journal               runstate.Journal
		seq                   uint64
		startIter             int64
		pending               *runstate.PendingTurn
		sessionID             string
		resumeAtInputBoundary bool
		newSession            func(prompt string) (runstate.Journal, string, error)
	)

	if checkpointing {
		fp, err := computeFingerprint(cfg, provider.Capabilities().Provider, system, toolDefs)
		if err != nil {
			return res, err
		}

		store, err := runstate.New(cfg.SessionBackend(), cfg.SessionRawOptions())
		if err != nil {
			return res, err
		}

		if opts.Checkpoint.ResumeID != "" {
			sessionID = opts.Checkpoint.ResumeID

			rs, err := store.Load(sessionID)
			if err != nil {
				return res, err
			}
			if rs.Completed() {
				return res, fmt.Errorf("session %q has already completed and cannot be resumed", sessionID)
			}
			// The stored session and the caller must agree on interactivity: a chat
			// session needs the input bar wired (or it would make a spurious LLM call at
			// its resting boundary and never take a follow-up), and a one-shot session
			// has no free-standing user-turn journaling and must not be handed a prompter.
			// The CLI reconciles this before calling Run (it reads the flag from the
			// session), so these are defense in depth.
			if rs.Interactive && !interactive {
				return res, fmt.Errorf("session %q is an interactive chat session; resume it with the full-screen chat UI", sessionID)
			}
			if !rs.Interactive && interactive {
				return res, fmt.Errorf("session %q was not started as a chat session and cannot be resumed as one", sessionID)
			}
			// Provider is a hard gate: a turn from another provider cannot be folded
			// coherently, so --force (which is for configuration drift) must not cross
			// it. Checked before the forceable drift so the message is unambiguous.
			if rs.Fingerprint.Provider != fp.Provider {
				return res, fmt.Errorf("cannot resume %q: it was started with provider %q but the current configuration uses %q; a run cannot change provider, and --force does not apply",
					sessionID, rs.Fingerprint.Provider, fp.Provider)
			}
			if !rs.Fingerprint.Equal(fp) && !opts.Checkpoint.Force {
				return res, fmt.Errorf("cannot resume %q, the configuration changed since it was saved:\n  %s\nre-run against the original configuration, or pass --force to continue with the current one",
					sessionID, strings.Join(rs.Fingerprint.Diff(fp), "\n  "))
			}

			j, err := store.Open(sessionID)
			if err != nil {
				return res, err
			}
			defer closeJournal(j)

			journal = j
			seq = j.LastSeq()
			startIter = rs.NextIteration
			pending = rs.Pending
			messages = rs.Messages

			// A chat session's iteration cap grows one turn's worth per accepted
			// follow-up; on resume that grown cap is not stored, only the position, so
			// give the resumed turn a fresh per-turn budget from where it left off. A
			// one-shot resume keeps the absolute cap (cumulative across the whole run).
			if interactive {
				maxIter = startIter + cfg.LLM.Budget.MaxIterations
			}

			// A chat session that rests at a completed boundary (no in-flight turn, the
			// conversation ends on an assistant turn that was not a server-side pause)
			// resumes straight to the input bar. With an in-flight turn, or a paused turn
			// the model means to continue, the loop runs first to finish it.
			resumeAtInputBoundary = rs.Interactive &&
				rs.Pending == nil &&
				endsOnAssistant(messages) &&
				rs.LastStopReason != string(llm.StopPauseTurn)

			stats.LlmCalls = rs.Counters.LlmCalls
			stats.ToolCalls = rs.Counters.ToolCalls
			stats.RemoteToolCalls = rs.Counters.RemoteToolCalls
			stats.InTokens = rs.Counters.InTokens
			stats.OutTokens = rs.Counters.OutTokens
			stats.CacheReadTokens = rs.Counters.CacheReadTokens
			stats.CacheCreateTokens = rs.Counters.CacheCreateTokens

			// Tell the model it resumed so it re-verifies external state before
			// acting on possibly-stale results. Appended after the fingerprint was
			// computed so it never perturbs the fingerprint comparison, and it is
			// never persisted.
			system = append(system, resumeReminder)

			info.SessionID = sessionID
			info.Resumed = true
			events.Starting(info)
			for _, w := range resumeHazards(rs) {
				events.Warn(w)
			}
			events.ResumeTranscript(rs, byName)
		} else {
			sessionID = opts.Checkpoint.Name
			if sessionID == "" {
				sessionID = a2a.NewID()
			}

			meta := runstate.MetaRecord{
				Version:     runstate.Version,
				RunID:       sessionID,
				Created:     time.Now(),
				Fingerprint: fp,
				Prompt:      strings.Join(prompt, " "),
				Interactive: interactive,
			}
			j, err := store.Create(sessionID, meta)
			if err != nil {
				return res, err
			}
			defer closeJournal(j)

			journal = j
			seq = 1

			info.SessionID = sessionID
			events.Starting(info)
		}

		// newSession lets a context reset rotate to a fresh session mid-run: it creates a new
		// journal with the same fingerprint and a new id, seeded with the reset prompt as its
		// Meta.Prompt. It closes over the store and fingerprint the runner does not hold.
		newSession = func(prompt string) (runstate.Journal, string, error) {
			id := a2a.NewID()
			meta := runstate.MetaRecord{
				Version:     runstate.Version,
				RunID:       id,
				Created:     time.Now(),
				Fingerprint: fp,
				Prompt:      prompt,
				Interactive: interactive,
			}
			j, err := store.Create(id, meta)
			if err != nil {
				return nil, "", err
			}
			return j, id, nil
		}

		stats.Session = sessionID
	} else {
		events.Starting(info)
	}

	res.SessionID = sessionID

	// The memory index lists the stored memories for the model. It is appended after
	// the fingerprint was computed so that memory changing between a suspend and a
	// resume never blocks the resume: memory is data, not configuration, and the
	// resume reminder already tells the model to re-verify state. It is a start-of-run
	// snapshot; memory_list is the live view during the run. A read failure is an
	// advisory, not fatal, since the tools still reach the store.
	if cfg.MemoryIndexEnabled() {
		entries, lerr := memStore.List(ctx)
		if lerr != nil {
			events.Warn(Warning{Kind: WarnMemoryIndex, Err: lerr})
		} else {
			system = append(system, builtin.MemoryIndexBlock(entries))
		}
	}

	// The runner dispatches over a single registry keyed by the unique name the model
	// addresses each tool by. Names were made unique across the kinds as each was
	// added (the taken set), so merging local, built-in and remote tools cannot
	// shadow one another. The per-kind maps above are kept only where a consumer still
	// needs the concrete kind: byName feeds the resume transcript renderer.
	allTools := make(map[string]toolkit.Tool, len(byName)+len(builtinByName)+len(remoteByName))
	for name, t := range byName {
		allTools[name] = t
	}
	for name, b := range builtinByName {
		allTools[name] = b
	}
	for name, rt := range remoteByName {
		allTools[name] = rt
	}

	r := &runner{
		cfg:             cfg,
		provider:        provider,
		stats:           stats,
		system:          system,
		toolDefs:        toolDefs,
		toolSearch:      toolSearch,
		thinking:        thinking,
		maxOutputTokens: maxOutputTokens,
		maxIter:         maxIter,
		maxTokens:       maxTokens,
		tools:           allTools,
		confirmTags:     confirmTags,
		gate:            gate,
		verbose:         opts.Verbose,
		promptCache:     cfg.PromptCacheEnabled(),
		interactive:     interactive,
		events:          events,
		prompter:        prompter,
		messages:        messages,
		journal:         journal,
		seq:             seq,
		startIter:       startIter,
		pending:         pending,
		nextPrompt:      opts.NextPrompt,
		sessionID:       sessionID,
		newSession:      newSession,

		resumeAtInputBoundary: resumeAtInputBoundary,
	}
	if checkpointing {
		r.suspendRequested = opts.SuspendRequested
	}

	reason, err := r.run(ctx)
	res.Reason = reason
	// A context reset may have rotated to a fresh session mid-run, so report the session the
	// run ended on (the one an operator resumes) rather than the one it started with.
	res.SessionID = r.sessionID
	stats.Session = r.sessionID
	if reason == runstate.ReasonSuspended {
		stats.Suspended = true
	}

	return res, err
}

// closeJournal closes a session journal, warning rather than failing the run if
// the close errors, since the run's own outcome is already decided.
func closeJournal(j runstate.Journal) {
	err := j.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: closing session journal: %v\n", err)
	}
}

// endsOnAssistant reports whether the conversation's last message is an assistant
// turn, which for an interactive session means it rests at an input boundary (the
// operator's turn) rather than mid-flight awaiting the next LLM call.
func endsOnAssistant(messages []llm.Message) bool {
	n := len(messages)
	return n > 0 && messages[n-1].Role == llm.RoleAssistant
}

// SessionInteractive reports whether a stored session was started as an interactive
// chat run, reading only its Meta record so the CLI can reopen the input bar on
// resume without the operator re-passing the flag. It does not lock the session (the
// subsequent resume takes the lock), so it is a cheap pre-flight read.
func SessionInteractive(cfg *config.Config, id string) (bool, error) {
	store, err := runstate.New(cfg.SessionBackend(), cfg.SessionRawOptions())
	if err != nil {
		return false, err
	}

	rs, err := store.Load(id)
	if err != nil {
		return false, err
	}

	return rs.Interactive, nil
}

// resumeHazards reports the resume situation that can misbehave: a pause at a
// server-tool boundary whose state may have expired.
func resumeHazards(rs *runstate.RunState) []Warning {
	var out []Warning

	if rs.Pending == nil && rs.LastStopReason == string(llm.StopPauseTurn) {
		out = append(out, Warning{Kind: WarnResumePausedTurn})
	}

	return out
}

// computeFingerprint captures the configuration a checkpointed run depends on, so
// a resume against a changed model, prompt, tool set or budget is caught. The
// system prompt is hashed, never stored. providerID is the resolved provider's own
// id (Capabilities().Provider), not the config selector, so the fingerprint records
// the backend the journal was actually written against.
func computeFingerprint(cfg *config.Config, providerID string, system []string, toolDefs []llm.ToolDef) (runstate.Fingerprint, error) {
	sys, err := json.Marshal(system)
	if err != nil {
		return runstate.Fingerprint{}, fmt.Errorf("hashing system prompt: %w", err)
	}
	tools, err := json.Marshal(toolDefs)
	if err != nil {
		return runstate.Fingerprint{}, fmt.Errorf("hashing tool set: %w", err)
	}

	mode := "off"
	if cfg.ThinkingEnabled() {
		mode = "summarized"
	}

	return runstate.Fingerprint{
		Provider:      providerID,
		Model:         cfg.LLM.Model,
		SystemHash:    runstate.HashHex(sys),
		ToolsHash:     runstate.HashHex(tools),
		ThinkingMode:  mode,
		MaxTokens:     cfg.LLM.Budget.MaxTokens,
		MaxIterations: cfg.LLM.Budget.MaxIterations,
	}, nil
}
