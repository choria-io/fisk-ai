//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/choria-io/fisk-ai/internal/toolkit"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/util"
)

// runner drives the agentic loop. Its fields split cleanly into two groups: the
// infrastructure rebuilt from configuration on every start or resume (client,
// tool registries, prompt, budget) and the mutable run state that a snapshot must
// carry (messages). This separation is what makes a run resumable: the state is a
// plain value, the infrastructure is reconstructed.
type runner struct {
	cfg      *config.Config
	provider llm.Provider
	stats    *util.RunStats

	system          []string
	toolDefs        []llm.ToolDef
	toolSearch      bool
	thinking        bool
	maxOutputTokens int64
	maxIter         int64
	maxTokens       int64

	// tools is the single dispatch registry: every model-facing tool, whatever its
	// kind (local command, in-process built-in, remote), keyed by the unique name the
	// model addresses it by. executeTool looks a call up here once and runs it through
	// the uniform Tool contract, consulting narrow capability interfaces for the
	// kind-specific policy (argument validation, confirmation) and the Describer
	// interface for the kind-specific call trace and execution dependencies.
	tools       map[string]toolkit.Tool
	confirmTags []string
	gate        *util.ConfirmGate

	verbose bool

	// promptCache turns on Anthropic prompt caching: two cache_control breakpoints per
	// request (one after tools+system, one on the conversation tail). Resolved once at
	// setup from cfg.PromptCacheEnabled(); kept out of the fingerprint so toggling it
	// never refuses a resume. interactive selects the cache TTL (1h for a chat run whose
	// think-time between turns exceeds 5m, 5m for an autonomous loop).
	promptCache bool
	interactive bool

	// events receives the run's narration, tool traces and warnings so the caller
	// owns all wording and rendering.
	events Events

	// prompter puts the run's interactive decisions (confirm-gate approval and the
	// human-in-the-loop questions) to the operator. It is used only from this single
	// run goroutine, never from the concurrent MCP path.
	prompter toolkit.Prompter

	// toolWorkDir is the directory local command tools run in, handed to each tool
	// execution so concurrent runs sharing one process do not collide on relative-path
	// writes. Empty inherits the process working directory.
	toolWorkDir string

	// messages is the conversation, grown as the loop runs. It is the core of the
	// resumable state.
	messages []llm.Message

	// journal records every event for durable suspend/resume. It is nil when
	// snapshotting is disabled, in which case the run behaves exactly as before.
	journal runstate.Journal
	// seq is the last journal seq written; the next append is seq+1. On a fresh
	// snapshotted run it starts at 1 (the meta record). On resume it is seeded
	// from the journal's last seq.
	seq uint64
	// startIter is the loop index to begin at: 0 for a fresh run, the resumed
	// NextIteration otherwise, so the iteration cap stays cumulative.
	startIter int64
	// iter is the current loop index, seeded from startIter. It lives on the runner so
	// it stays monotonic across interactive turns (each re-entry of loop continues the
	// numbering rather than restarting it), keeping AssistantRecord.Iteration and the
	// trace iteration unique per turn.
	iter int64
	// pending is an in-flight tool batch restored on resume: its unanswered tools
	// are run before the loop proceeds.
	pending *runstate.PendingTurn
	// suspendRequested reports that a graceful suspend was asked for; it is polled
	// at the loop boundary, never mid-tool. Nil when suspension is not wired.
	suspendRequested func() bool

	// nextPrompt continues the run interactively: after a boundary the operator can act
	// on it gathers the next decision (a follow-up, a context reset, or an end). Nil for
	// a one-shot run. Called only from this single run goroutine.
	nextPrompt func(context.Context) Continuation

	// sessionID is the current checkpoint session; empty for a non-checkpointed run. It
	// changes when a context reset rotates to a fresh session, so the caller reads it back
	// after the run to report the final session on-screen.
	sessionID string
	// newSession starts a fresh checkpoint session with the given first prompt, returning
	// its journal and id. It carries the store, fingerprint and meta the runner does not
	// otherwise hold, and is nil for a non-checkpointed run (which resets in memory only).
	newSession func(prompt string) (runstate.Journal, string, error)
	// resetPending marks a deferred context reset for a checkpointed run: a bare /clear
	// clears nothing until the operator supplies a prompt, so the fresh session is created
	// with a real first prompt rather than an empty one (which would fail to resume).
	resetPending bool

	// resumeAtInputBoundary is set when resuming a chat session whose last turn is
	// complete: the conversation already rests at an input boundary, so the initial
	// loop() is skipped (a fresh LLM call on a completed conversation would be wrong)
	// and control drops straight to the input bar. A resume with an in-flight turn
	// leaves this false so loop() finishes that turn first.
	resumeAtInputBoundary bool
}

// emit appends a record to the journal, advancing the seq. It is a no-op when
// snapshotting is disabled.
func (r *runner) emit(rec runstate.Record) error {
	if r.journal == nil {
		return nil
	}

	r.seq++
	err := r.journal.Append(r.seq, rec)
	if err != nil {
		return fmt.Errorf("journaling run: %w", err)
	}

	return nil
}

// run executes the agentic loop to a terminal state, returning the reason it
// stopped and, for the failure reasons, the error to surface to the caller. A
// nil error with ReasonCompleted is a successful run. When journaling is enabled
// it records why the run ended (including a graceful suspend) so a resume can
// tell a clean stop from a crash.
func (r *runner) run(ctx context.Context) (runstate.TerminalReason, error) {
	// Seed the loop index once, here, so it is correct for a fresh run (0) and a resume
	// (the restored NextIteration) regardless of how the runner was constructed, and so
	// it then stays monotonic across the interactive re-entries below.
	r.iter = r.startIter

	// A resumed chat that is already at a completed boundary skips the initial loop:
	// the conversation ends on an assistant turn awaiting a follow-up, so a fresh LLM
	// call would be wrong. Treat it as a just-completed turn and fall straight into the
	// continuation loop, which opens the input bar.
	var (
		reason runstate.TerminalReason
		err    error
	)
	if r.resumeAtInputBoundary {
		reason = runstate.ReasonCompleted
	} else {
		reason, err = r.loop(ctx)
	}

	// Interactive continuation: after a turn the operator can act on, hand back for a
	// follow-up. A completed turn and a turn that hit the iteration cap are both
	// recoverable (the operator steers with another prompt); an infra error, an
	// exhausted budget and a cancellation end the session. Each accepted follow-up
	// extends the iteration cap by a fresh turn's worth, so one long turn does not
	// starve the next. The single terminal record below is written once, at true end.
	for r.nextPrompt != nil && ctx.Err() == nil && continuable(reason) {
		switch reason {
		case runstate.ReasonMaxIterations:
			r.events.Warn(Warning{Kind: WarnMaxIterInteractive, Count: int(r.cfg.LLM.Budget.MaxIterations)})
		case runstate.ReasonError:
			// A turn that failed (an LLM call timeout, an API error) does not end an
			// interactive session: surface the cause and hand back to the input bar so
			// the operator can retry or steer, rather than stalling with no way back. An
			// abort (a canceled ctx) is filtered by the loop condition above, so it still
			// ends the session.
			r.events.Warn(Warning{Kind: WarnTurnErrorInteractive, Err: err})
		}

		cont := r.nextPrompt(ctx)
		if !cont.Continue {
			// A cancellation while the prompt was up (an abort) is surfaced as the error
			// it is, so the caller classifies it as aborted. A clean end (the operator
			// left) completes a plain chat; a checkpointed chat instead suspends, since
			// the journal keeps it resumable and there is no free-standing "user turn
			// completed the run" state to record - the operator returns with --resume.
			switch {
			case ctx.Err() != nil:
				reason, err = runstate.ReasonError, ctx.Err()
			case r.journal != nil:
				reason, err = runstate.ReasonSuspended, nil
			default:
				reason, err = runstate.ReasonCompleted, nil
			}
			break
		}

		if cont.Reset {
			// The cleared context has no prior-turn outcome, so drop the stale reason
			// before looping back or running the fresh turn; otherwise the max-iteration
			// or turn-error warning above would re-fire at the next boundary.
			reason = runstate.ReasonCompleted
			if r.journal == nil {
				// A non-checkpointed run clears in memory at once: a bare reset reopens the
				// input, a reset with a prompt runs against the empty context.
				r.resetContext()
			} else {
				// A checkpointed run defers the clear until a prompt is in hand, so the fresh
				// session is created with a real first prompt (an empty Meta.Prompt would fail
				// to resume). Rotation happens below, once cont.Text is known.
				r.resetPending = true
			}
		}

		// A bare context reset (no prompt) reopens the input without running a turn; loop
		// back to gather the fresh prompt.
		if cont.Text == "" {
			continue
		}

		// A deferred checkpoint reset now has its prompt: rotate to a fresh session so the
		// clear is durable and the previous session stays resumable. On failure the reset is
		// abandoned and the turn runs on in the current session, which stays consistent
		// because its messages were never cleared.
		rotated := false
		if r.resetPending {
			r.resetPending = false
			rerr := r.rotateSession(cont.Text)
			if rerr != nil {
				r.events.Warn(Warning{Kind: WarnSessionRotate, Err: rerr})
			} else {
				rotated = true
			}
		}

		// A rotation already seeded the fresh session with this prompt (it is the new
		// session's Meta.Prompt, so it needs no separate user record). Otherwise add the
		// prompt to the conversation and journal it before the turn so a resume reconstructs
		// the same conversation. On a journal failure end the session here, before the turn:
		// the journal then stops at the last coherent boundary (resumable) rather than
		// recording assistant turns with no preceding user message. The in-memory prompt is
		// discarded with the run; only the journal is the source of truth.
		if !rotated {
			r.appendUserPrompt(cont.Text)

			jerr := r.emit(runstate.Record{Protocol: runstate.UserProtocol, User: &runstate.UserRecord{
				Message: llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: cont.Text}}}},
			}})
			if jerr != nil {
				r.events.Warn(Warning{Kind: WarnJournalUser, Err: jerr})
				reason, err = runstate.ReasonError, jerr
				break
			}
		}

		r.maxIter += r.cfg.LLM.Budget.MaxIterations
		reason, err = r.loop(ctx)
	}

	if r.journal != nil {
		tr := &runstate.TerminalRecord{Reason: reason}
		if err != nil {
			tr.Message = err.Error()
		}
		jerr := r.emit(runstate.Record{Protocol: runstate.TerminalProtocol, Terminal: tr})
		if jerr != nil {
			r.events.Warn(Warning{Kind: WarnJournalTerminal, Err: jerr})
		}
	}

	return reason, err
}

// continuable reports whether a loop outcome is a boundary the operator can steer
// from with a follow-up. A completed turn is the normal case; a turn that hit the
// iteration cap is recoverable (interactive mode extends the budget on the next prompt),
// and so is a failed turn (a transient LLM/API error must not stall the chat, and the
// operator can always Ctrl-D to end). An abort is filtered separately by the run loop's
// ctx check; an exhausted token budget and a suspend end the session.
func continuable(reason runstate.TerminalReason) bool {
	switch reason {
	case runstate.ReasonCompleted, runstate.ReasonMaxIterations, runstate.ReasonError:
		return true
	default:
		return false
	}
}

// appendUserPrompt adds an interactive follow-up to the conversation. Normally the prior
// turn ended with an assistant message, so the follow-up becomes a new user turn. When the
// prior turn failed before replying, it leaves a dangling trailing user message; the
// follow-up is folded into that turn instead, so the roles keep alternating rather than
// sending two user messages in a row, which the API rejects.
func (r *runner) appendUserPrompt(text string) {
	block := llm.ContentBlock{Text: &llm.TextBlock{Text: text}}

	n := len(r.messages)
	if n > 0 && r.messages[n-1].Role == llm.RoleUser {
		r.messages[n-1].Content = append(r.messages[n-1].Content, block)
		return
	}

	r.messages = append(r.messages, llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{block}})
}

// resetContext clears the conversation for a fresh start within the same run, keeping the
// system prompt, tools and the session's confirm-gate approvals. The iteration budget is
// re-baselined to the current position so the cleared context gets a full turn's allowance
// from the next prompt rather than inheriting the budget the prior turns already spent.
// It is only reached for a non-checkpointed run; clearing a journaled session needs session
// rotation, which the interactive caller gates until that path is wired.
func (r *runner) resetContext() {
	r.messages = nil
	r.maxIter = r.iter
}

// rotateSession starts a fresh checkpoint session for a context reset and swaps the runner
// onto it, seeding the conversation with prompt as the new session's first turn. The new
// journal is created first so a failure leaves the current session untouched (the caller
// then runs the turn on in the existing session). On success the outgoing session is
// finalized as suspended, never completed, so the operator can return to it with --resume;
// it rests on an assistant boundary, the shape a chat normally suspends at. The per-session
// counters (seq, iteration, budget) reset for the new journal, while the run's cumulative
// stats keep climbing so the live totals reflect the whole sitting; the new session's own
// counters are derived from its journal on any later resume.
func (r *runner) rotateSession(prompt string) error {
	newJournal, newID, err := r.newSession(prompt)
	if err != nil {
		return err
	}

	prevID := r.sessionID

	// Finalize the outgoing session on its own journal before swapping. A failed terminal
	// write is not fatal: the journal still ends on an assistant turn, so the session stays
	// resumable, only unmarked; warn and proceed with the swap.
	terr := r.emit(runstate.Record{Protocol: runstate.TerminalProtocol, Terminal: &runstate.TerminalRecord{Reason: runstate.ReasonSuspended}})
	if terr != nil {
		r.events.Warn(Warning{Kind: WarnJournalTerminal, Err: terr})
	}
	closeJournal(r.journal, r.events)

	r.journal = newJournal
	r.sessionID = newID
	r.seq = newJournal.LastSeq()
	r.iter = 0
	r.maxIter = 0
	r.messages = []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: prompt}}}}}

	r.events.SessionRotated(prevID)

	return nil
}

func (r *runner) loop(ctx context.Context) (runstate.TerminalReason, error) {
	// On resume, finish the in-flight tool batch before proceeding so the
	// conversation reaches a coherent boundary. Its already-run tools are reused
	// from the journal; only the unanswered ones execute.
	if r.pending != nil {
		err := r.completePending(ctx)
		if err != nil {
			return runstate.ReasonError, err
		}
		r.pending = nil
	}

	for r.iter < r.maxIter {
		// Poll for a graceful suspend only here, at a boundary where the
		// conversation is coherent and nothing is mid-flight. Checked before the index
		// is consumed so a suspend does not burn an iteration number.
		if r.suspendRequested != nil && r.suspendRequested() {
			return runstate.ReasonSuspended, nil
		}

		// i is this turn's index; advance the runner's counter now so it points at the
		// next index, keeping numbering monotonic across a terminal return and the
		// interactive re-entry that follows it.
		i := r.iter
		r.iter++

		req := llm.Request{
			Model:           r.cfg.LLM.Model,
			SystemBlocks:    r.system,
			Messages:        r.messages,
			Tools:           r.toolDefs,
			ToolSearch:      r.toolSearch,
			ThinkingEnabled: r.thinking,
			MaxOutputTokens: r.maxOutputTokens,
			PromptCache:     r.promptCache,
			Interactive:     r.interactive,
		}

		if r.verbose {
			r.events.LLMRequest(util.LLMRequestSummary(r.messages))
		}

		resp, err := r.provider.Call(util.WithTraceIteration(ctx, int(i)), req)
		if err != nil {
			return runstate.ReasonError, fmt.Errorf("llm call: %w", err)
		}
		r.stats.LlmCalls++
		r.stats.InTokens += resp.Usage.In
		r.stats.OutTokens += resp.Usage.Out
		r.stats.CacheReadTokens += resp.Usage.CacheRead
		r.stats.CacheCreateTokens += resp.Usage.CacheCreate

		// Append the assistant turn to the conversation. The neutral blocks preserve
		// any server-side tool_search blocks intact alongside text and tool_use.
		asst := llm.Message{Role: llm.RoleAssistant, Content: resp.Content}
		r.messages = append(r.messages, asst)

		// Journal the assistant turn before executing any tools, so a crash mid
		// batch resumes without re-paying for this LLM call.
		err = r.emit(runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: &runstate.AssistantRecord{
			Iteration:         i,
			Message:           asst,
			StopReason:        string(resp.StopReason),
			InTokens:          resp.Usage.In,
			OutTokens:         resp.Usage.Out,
			CacheReadTokens:   resp.Usage.CacheRead,
			CacheCreateTokens: resp.Usage.CacheCreate,
		}})
		if err != nil {
			return runstate.ReasonError, err
		}

		var toolUses []llm.ToolUseBlock
		for _, block := range resp.Content {
			if block.ToolUse == nil {
				continue
			}
			toolUses = append(toolUses, *block.ToolUse)
		}

		// A turn truncated at the output token cap may carry a partial tool_use whose
		// input is incomplete, so it must never be executed. Treat it as the run's end
		// with a clear cause rather than running malformed input or silently completing;
		// the caller surfaces the error and, in a chat, hands back to the operator.
		if resp.StopReason == llm.StopMaxTokens {
			r.events.Message(*resp, true)
			return runstate.ReasonError, fmt.Errorf("model reply truncated at the output token limit; the answer is incomplete")
		}

		// The turn is terminal when the model neither asked to run a tool nor
		// paused a long-running turn it intends to continue.
		terminal := len(toolUses) == 0 && resp.StopReason != llm.StopPauseTurn

		// Text on a terminal turn is the answer; text on an intermediate turn is
		// narration. The caller decides where each goes.
		r.events.Message(*resp, terminal)

		// A terminal turn is the final answer; deliver it regardless of remaining
		// budget since no further spend follows.
		if terminal {
			if resp.StopReason == llm.StopRefusal {
				return runstate.ReasonError, fmt.Errorf("model refused to respond")
			}
			return runstate.ReasonCompleted, nil
		}

		// Stop before executing this turn's tools once the token budget is
		// exhausted, so an over-budget turn does not incur further tool spend or
		// side effects. The cache tiers are counted alongside InTokens/OutTokens so the
		// cap measures total throughput, keeping its magnitude the same as the pre-cache
		// world (where the cache fields were zero) and resume-consistent.
		if r.maxTokens > 0 && r.stats.InTokens+r.stats.OutTokens+r.stats.CacheReadTokens+r.stats.CacheCreateTokens >= r.maxTokens {
			return runstate.ReasonBudget, fmt.Errorf("token budget (%d) exhausted after %d iterations", r.maxTokens, i+1)
		}

		if len(toolUses) > 0 {
			results := make([]llm.ContentBlock, 0, len(toolUses))
			for _, use := range toolUses {
				result, remote := r.executeTool(ctx, use)
				err = r.emit(runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: &runstate.ToolResultRecord{
					ToolUseID: use.ID,
					Result:    result,
					Remote:    remote,
				}})
				if err != nil {
					return runstate.ReasonError, err
				}
				results = append(results, llm.ContentBlock{ToolResult: &result})
			}
			r.messages = append(r.messages, llm.Message{Role: llm.RoleUser, Content: results})
		}
	}

	return runstate.ReasonMaxIterations, fmt.Errorf("max iterations (%d) reached without a final answer", r.maxIter)
}

// completePending runs the not-yet-answered tools of a restored in-flight turn,
// reusing the results already journaled for the answered ones, then commits the
// assistant turn and its full result set to the conversation.
func (r *runner) completePending(ctx context.Context) error {
	p := r.pending

	results := make([]llm.ContentBlock, 0, len(p.Assistant.Content))
	for i := range p.Results {
		res := p.Results[i]
		results = append(results, llm.ContentBlock{ToolResult: &res})
	}

	for _, block := range p.Assistant.Content {
		if block.ToolUse == nil {
			continue
		}
		id := block.ToolUse.ID
		if p.Answered[id] {
			continue
		}

		result, remote := r.executeTool(ctx, *block.ToolUse)
		err := r.emit(runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: &runstate.ToolResultRecord{
			ToolUseID: id,
			Result:    result,
			Remote:    remote,
		}})
		if err != nil {
			return err
		}
		results = append(results, llm.ContentBlock{ToolResult: &result})
	}

	r.messages = append(r.messages, p.Assistant, llm.Message{Role: llm.RoleUser, Content: results})

	return nil
}

// executeTool dispatches a single tool call. It looks the tool up once in the
// unified registry, then runs it through the uniform Tool contract: kind-specific
// policy (argument validation, confirmation) is consulted through narrow capability
// interfaces, and the kind-specific call trace is built by a type switch, so a tool
// of any kind executes the same way. The second return reports whether the call was
// dispatched to a remote agent, for the journal and stats.
func (r *runner) executeTool(ctx context.Context, use llm.ToolUseBlock) (llm.ToolResultBlock, bool) {
	r.stats.ToolCalls++

	tool, ok := r.tools[use.Name]
	if !ok {
		r.stats.CountToolKind(toolkit.KindUnknown)
		r.events.Warn(Warning{Kind: WarnUnknownTool, Name: use.Name})
		return llm.ToolResultBlock{ToolUseID: use.ID, Content: fmt.Sprintf("unknown tool %q", use.Name), IsError: true}, false
	}

	// The tool describes its own call once, up front. Its Kind partitions the by-kind
	// tool accounting across every exit path below, including the rejections that
	// return before the tool runs, so the buckets sum to tool_calls on a fresh run;
	// the rest of the CallInfo drives the call trace on the path that does run the
	// tool. Describe must not run the tool or mutate state, so calling it before the
	// argument and confirm gates is safe.
	info := describeCall(tool, use.Input)
	r.stats.CountToolKind(info.Kind)

	// fisk does not enforce a command's required flags or arguments: a missing one
	// is silently dropped or skipped, so the command runs incomplete and fails only
	// on its own non-zero exit. When the model omits a required parameter, reject the
	// call before it runs and return the missing parameters so the model can correct
	// and retry. Only the tool kinds that can check (local command tools) implement
	// ArgumentValidator. This runs before the confirm gate so the operator is never
	// asked to approve a structurally invalid call, and nothing executed, so it is
	// reported as a warning rather than a call-and-result pair whose command line
	// would be shown missing the very parameter that was absent.
	if v, ok := tool.(toolkit.ArgumentValidator); ok {
		if missing := v.MissingRequired(use.Input); len(missing) > 0 {
			r.events.Warn(Warning{Kind: WarnMissingRequired, Name: use.Name, Params: missing})
			return llm.ToolResultBlock{ToolUseID: use.ID, Content: v.MissingRequiredMessage(missing), IsError: true}, false
		}
	}

	// A confirm-tagged tool must be approved by the operator before it runs. Only
	// local command tools are Confirmable; a remote tool carries no local tags (its
	// serving agent declines confirmation-gated tools at its own end) and a built-in
	// has no command to gate. The gate is shown the full, faithful command line
	// (TraceLine) so the operator approves exactly what will run; a denial returns an
	// authoritative result to the model and the command is not run, so it emits no
	// trace or result. The line is sanitized because its argument values come from
	// the model and must not be able to rewrite or spoof the operator's terminal.
	if c, ok := tool.(toolkit.Confirmable); ok && c.NeedsConfirm(r.confirmTags) {
		allowed, reason := r.gate.Approve(ctx, tool.Name(), c.Command(), c.TraceLine(use.Input), c.ConfirmTrigger(r.confirmTags))
		if !allowed {
			return util.ConfirmDeniedResult(use.ID, reason), false
		}
	}

	// The call trace shape and the execution dependencies are kind-specific; the
	// result trace and the ExecuteUse call are uniform. A call line is emitted for
	// every tool that runs, including an approved confirm-gated one whose approval
	// modal has since closed, so its result always has a visible command above it.
	deps, remote := r.traceCall(use, info)
	if remote {
		r.stats.RemoteToolCalls++
	}

	result := tool.ExecuteUse(ctx, use, deps)
	r.events.ToolResult(toolResultTrace(info.Present, info.Kind, result))
	return result, remote
}

// describeCall asks a tool to describe one call, from the CallInfo the runner uses
// for both accounting and tracing. A tool that does not implement toolkit.Describer
// yields the zero CallInfo, so it is accounted under toolkit.KindUnknown and traced
// by name alone, with no dependencies and not as a remote call: the safe default for
// a tool of an unforeseen kind.
func describeCall(tool toolkit.Tool, input json.RawMessage) toolkit.CallInfo {
	d, ok := tool.(toolkit.Describer)
	if !ok {
		return toolkit.CallInfo{}
	}

	return d.Describe(input)
}

// traceCall emits the ToolCall trace for a dispatched call from the CallInfo the
// runner already obtained, and returns the execution dependencies the call's kind
// needs and whether it is a remote call. The tool described its own call rather than
// the runner switching on its concrete type, so the presentation and dependency needs
// travel with the tool on info.Present: a built-in shows its own call line (a
// human-in-the-loop tool is distracting to name and is shown only under verbose
// downstream, a memory or knowledge tool is traced like a command); a remote tool
// names the agent it runs on; a command tool carries the full call line and a short
// form with long argument values elided, so a width-aware surface can fall back to
// the short one only when the full line would overflow.
func (r *runner) traceCall(use llm.ToolUseBlock, info toolkit.CallInfo) (toolkit.ExecDeps, bool) {
	r.events.ToolCall(ToolTrace{
		Name:         use.Name,
		Display:      info.Display,
		DisplayShort: info.DisplayShort,
		Agent:        info.Agent,
		Present:      info.Present,
		ProviderKind: info.Kind,
	})

	// A kind receives only the dependencies it asked for: a command tool the per-run
	// working directory, a built-in the operator prompter (and a working directory it
	// ignores), a remote tool neither. The remote flag is taken from the presentation
	// explicitly and never inferred from the agent name, which a remote tool may leave
	// empty.
	var deps toolkit.ExecDeps
	if info.NeedsPrompter {
		deps.Prompter = r.prompter
	}
	if info.NeedsWorkDir {
		deps.WorkDir = r.toolWorkDir
	}

	return deps, info.Present == toolkit.PresentRemote
}

// toolResultTrace extracts the display fields from a tool result: its presentation
// (carried through so the result renderer suppresses it by the same rule as its
// call), its provider kind for the log token, its text content, and whether the tool
// reported a failure.
func toolResultTrace(present toolkit.Presentation, provider toolkit.Kind, result llm.ToolResultBlock) ToolResultTrace {
	return ToolResultTrace{Present: present, ProviderKind: provider, IsError: result.IsError, Output: result.Content}
}
