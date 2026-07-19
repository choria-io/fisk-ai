//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/builtin"
	"github.com/choria-io/fisk-ai/internal/toolkit/fisk"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/util"
)

// runner drives the agentic loop. Its fields split cleanly into two groups: the
// infrastructure rebuilt from configuration on every start or resume (client,
// tool registries, prompt, budget) and the mutable run state that a snapshot must
// carry (messages). This separation is what makes a run resumable: the state is a
// plain value, the infrastructure is reconstructed.
type runner struct {
	cfg    *config.Config
	client anthropic.Client
	stats  *util.RunStats

	system          []anthropic.TextBlockParam
	toolParams      []anthropic.ToolUnionParam
	thinking        anthropic.ThinkingConfigParamUnion
	maxOutputTokens int64
	maxIter         int64
	maxTokens       int64

	// tools is the single dispatch registry: every model-facing tool, whatever its
	// kind (local command, in-process built-in, remote), keyed by the unique name the
	// model addresses it by. executeTool looks a call up here once and runs it through
	// the uniform Tool contract, consulting narrow capability interfaces for the
	// kind-specific policy (argument validation, confirmation) and a type switch for
	// the kind-specific call trace.
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

	// callLLM performs one LLM request. It defaults to util.CallLLM and is a field
	// so tests can drive the loop with scripted responses.
	callLLM func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error)

	// messages is the conversation, grown as the loop runs. It is the core of the
	// resumable state.
	messages []anthropic.MessageParam

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

// cacheControl is the cache_control marker to place on this run's breakpoints. A chat
// run uses a 1h TTL because an operator's think-time between turns commonly exceeds the
// default 5m (a 5m TTL there would pay a write with no read, a net cost increase); an
// autonomous loop uses the 5m default, which its tight turn-to-turn cadence stays within.
func (r *runner) cacheControl() anthropic.CacheControlEphemeralParam {
	if r.interactive {
		return anthropic.CacheControlEphemeralParam{TTL: anthropic.CacheControlEphemeralTTLTTL1h}
	}
	return anthropic.NewCacheControlEphemeralParam()
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
				Message: anthropic.NewUserMessage(anthropic.NewTextBlock(cont.Text)),
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
	block := anthropic.NewTextBlock(text)

	n := len(r.messages)
	if n > 0 && r.messages[n-1].Role == anthropic.MessageParamRoleUser {
		r.messages[n-1].Content = append(r.messages[n-1].Content, block)
		return
	}

	r.messages = append(r.messages, anthropic.NewUserMessage(block))
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
	closeJournal(r.journal)

	r.journal = newJournal
	r.sessionID = newID
	r.seq = newJournal.LastSeq()
	r.iter = 0
	r.maxIter = 0
	r.messages = []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(prompt))}

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

		// Prompt caching places two cache_control breakpoints without mutating any
		// persistent state, so the fingerprint (which hashes r.system and r.toolParams)
		// and the journal (r.messages) stay marker-free and toggling the kill switch never
		// refuses a resume. The tools+system breakpoint marks the last block of a value
		// copy of r.system (a real element clone, never a reslice, or the marker would
		// write through to r.system and poison the fingerprint). The conversation-tail
		// breakpoint is params.CacheControl, a request-level field that never touches
		// r.messages, so no marker is journaled or accumulates across turns.
		system := r.system
		if r.promptCache && len(system) > 0 {
			cc := r.cacheControl()
			system = slices.Clone(r.system)
			system[len(system)-1].CacheControl = cc
		}

		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(r.cfg.LLM.Model),
			MaxTokens: r.maxOutputTokens,
			System:    system,
			Tools:     r.toolParams,
			Messages:  r.messages,
			Thinking:  r.thinking,
		}
		if r.promptCache {
			params.CacheControl = r.cacheControl()
		}

		if r.verbose {
			r.events.LLMRequest(util.LLMRequestSummary(r.messages))
		}

		resp, err := r.callLLM(util.WithTraceIteration(ctx, int(i)), r.client, params, r.cfg.LLM.Budget.CallTimeoutParsed)
		if err != nil {
			var apiErr *anthropic.Error
			if r.cfg.ThinkingEnabled() && errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusBadRequest {
				return runstate.ReasonError, fmt.Errorf("llm call: %w; model %q may not support thinking, set llm.thinking.enabled to false", err, r.cfg.LLM.Model)
			}
			return runstate.ReasonError, fmt.Errorf("llm call: %w", err)
		}
		r.stats.LlmCalls++
		r.stats.InTokens += resp.Usage.InputTokens
		r.stats.OutTokens += resp.Usage.OutputTokens
		r.stats.CacheReadTokens += resp.Usage.CacheReadInputTokens
		r.stats.CacheCreateTokens += resp.Usage.CacheCreationInputTokens

		// Round-trip the assistant turn back into the conversation. This keeps
		// any server-side tool_search blocks intact alongside text and tool_use.
		asst := resp.ToParam()
		r.messages = append(r.messages, asst)

		// Journal the assistant turn before executing any tools, so a crash mid
		// batch resumes without re-paying for this LLM call.
		err = r.emit(runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: &runstate.AssistantRecord{
			Iteration:         i,
			Message:           asst,
			StopReason:        string(resp.StopReason),
			InTokens:          resp.Usage.InputTokens,
			OutTokens:         resp.Usage.OutputTokens,
			CacheReadTokens:   resp.Usage.CacheReadInputTokens,
			CacheCreateTokens: resp.Usage.CacheCreationInputTokens,
		}})
		if err != nil {
			return runstate.ReasonError, err
		}

		var toolUses []anthropic.ToolUseBlock
		for _, block := range resp.Content {
			use, ok := block.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			toolUses = append(toolUses, use)
		}

		// The turn is terminal when the model neither asked to run a tool nor
		// paused a long-running turn it intends to continue.
		terminal := len(toolUses) == 0 && resp.StopReason != anthropic.StopReasonPauseTurn

		// Text on a terminal turn is the answer; text on an intermediate turn is
		// narration. The caller decides where each goes.
		r.events.Message(resp, terminal)

		// A terminal turn is the final answer; deliver it regardless of remaining
		// budget since no further spend follows.
		if terminal {
			if resp.StopReason == anthropic.StopReasonRefusal {
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
			results := make([]anthropic.ContentBlockParamUnion, 0, len(toolUses))
			for _, use := range toolUses {
				block, remote := r.executeTool(ctx, use)
				err = r.emit(runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: &runstate.ToolResultRecord{
					ToolUseID: use.ID,
					Result:    block,
					Remote:    remote,
				}})
				if err != nil {
					return runstate.ReasonError, err
				}
				results = append(results, block)
			}
			r.messages = append(r.messages, anthropic.NewUserMessage(results...))
		}
	}

	return runstate.ReasonMaxIterations, fmt.Errorf("max iterations (%d) reached without a final answer", r.maxIter)
}

// completePending runs the not-yet-answered tools of a restored in-flight turn,
// reusing the results already journaled for the answered ones, then commits the
// assistant turn and its full result set to the conversation.
func (r *runner) completePending(ctx context.Context) error {
	p := r.pending

	results := make([]anthropic.ContentBlockParamUnion, 0, len(p.Assistant.Content))
	results = append(results, p.Results...)

	for _, block := range p.Assistant.Content {
		if block.OfToolUse == nil {
			continue
		}
		id := block.OfToolUse.ID
		if p.Answered[id] {
			continue
		}

		use, err := toolUseFromParam(block.OfToolUse)
		if err != nil {
			return fmt.Errorf("resuming tool %q: %w", id, err)
		}

		resBlock, remote := r.executeTool(ctx, use)
		err = r.emit(runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: &runstate.ToolResultRecord{
			ToolUseID: id,
			Result:    resBlock,
			Remote:    remote,
		}})
		if err != nil {
			return err
		}
		results = append(results, resBlock)
	}

	r.messages = append(r.messages, p.Assistant, anthropic.NewUserMessage(results...))

	return nil
}

// toolUseFromParam rebuilds a response ToolUseBlock from a stored tool_use param
// block so a restored in-flight call can be executed by the same code path as a
// live one. Only the id, name and input are needed downstream.
func toolUseFromParam(p *anthropic.ToolUseBlockParam) (anthropic.ToolUseBlock, error) {
	input, err := json.Marshal(p.Input)
	if err != nil {
		return anthropic.ToolUseBlock{}, fmt.Errorf("marshaling tool input: %w", err)
	}

	return anthropic.ToolUseBlock{ID: p.ID, Name: p.Name, Input: input}, nil
}

// executeTool dispatches a single tool call. It looks the tool up once in the
// unified registry, then runs it through the uniform Tool contract: kind-specific
// policy (argument validation, confirmation) is consulted through narrow capability
// interfaces, and the kind-specific call trace is built by a type switch, so a tool
// of any kind executes the same way. The second return reports whether the call was
// dispatched to a remote agent, for the journal and stats.
func (r *runner) executeTool(ctx context.Context, use anthropic.ToolUseBlock) (anthropic.ContentBlockParamUnion, bool) {
	r.stats.ToolCalls++

	tool, ok := r.tools[use.Name]
	if !ok {
		r.events.Warn(Warning{Kind: WarnUnknownTool, Name: use.Name})
		return anthropic.NewToolResultBlock(use.ID, fmt.Sprintf("unknown tool %q", use.Name), true), false
	}

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
			return anthropic.NewToolResultBlock(use.ID, v.MissingRequiredMessage(missing), true), false
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
	kind, deps, remote := r.traceCall(use)
	if remote {
		r.stats.RemoteToolCalls++
	}

	block := tool.ExecuteUse(ctx, use, deps)
	r.events.ToolResult(toolResultTrace(kind, block))
	return block, remote
}

// traceCall emits the ToolCall trace for a dispatched call and returns the kind
// (for the matching result trace), the execution dependencies the kind needs, and
// whether it is a remote call. The trace shapes are per kind: a built-in shows its
// own call line (a human-in-the-loop tool is distracting to name and is shown only
// under verbose downstream, a memory tool is traced like a command); a remote tool
// names the agent it runs on; a local command tool carries the full call line and a
// short form with long argument values elided, so a width-aware surface can fall
// back to the short one only when the full line would overflow.
func (r *runner) traceCall(use anthropic.ToolUseBlock) (ToolKind, toolkit.ExecDeps, bool) {
	switch t := r.tools[use.Name].(type) {
	case *builtin.BuiltinTool:
		kind := ToolBuiltin
		if t.Traced() {
			kind = ToolMemory
		}
		r.events.ToolCall(ToolTrace{Name: use.Name, Display: t.TraceLine(use.Input), Kind: kind})
		return kind, toolkit.ExecDeps{Prompter: r.prompter}, false

	case *a2a.RemoteTool:
		r.events.ToolCall(ToolTrace{Name: use.Name, Kind: ToolRemote, Agent: t.Agent()})
		return ToolRemote, toolkit.ExecDeps{}, true

	case *fisk.FiskCommandTool:
		r.events.ToolCall(ToolTrace{Name: use.Name, Display: t.TraceLine(use.Input), DisplayShort: t.TraceLineShort(use.Input), Kind: ToolLocal})
		return ToolLocal, toolkit.ExecDeps{}, false

	default:
		// A model-facing tool of an unforeseen kind still runs uniformly; it is traced
		// by name alone rather than dropped.
		r.events.ToolCall(ToolTrace{Name: use.Name, Kind: ToolLocal})
		return ToolLocal, toolkit.ExecDeps{}, false
	}
}

// toolResultTrace extracts the display fields from a tool result block: its text
// content joined in order, and whether the tool reported a failure. It nil-guards
// a block that is not a tool result, yielding an empty output rather than
// panicking, so a future dispatch branch returning another block shape is safe.
func toolResultTrace(kind ToolKind, block anthropic.ContentBlockParamUnion) ToolResultTrace {
	tr := ToolResultTrace{Kind: kind}
	if block.OfToolResult == nil {
		return tr
	}

	tr.IsError = block.OfToolResult.IsError.Or(false)

	var out strings.Builder
	for _, c := range block.OfToolResult.Content {
		if c.OfText != nil {
			out.WriteString(c.OfText.Text)
		}
	}
	tr.Output = out.String()

	return tr
}
