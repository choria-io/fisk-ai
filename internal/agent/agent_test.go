//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/choria-io/fisk"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	fisk2 "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	"github.com/segmentio/ksuid"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	"github.com/choria-io/fisk-ai/internal/llm"
	llmanthropic "github.com/choria-io/fisk-ai/internal/llm/anthropic"
	"github.com/choria-io/fisk-ai/internal/remotetools"
	"github.com/choria-io/fisk-ai/internal/runstate"
	runstatefile "github.com/choria-io/fisk-ai/internal/runstate/file"
	"github.com/choria-io/fisk-ai/internal/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent")
}

// nopEvents discards every event, for tests that exercise the loop rather than
// its rendering.
type nopEvents struct{}

func (nopEvents) Warn(Warning)                                                           {}
func (nopEvents) Starting(RunInfo)                                                       {}
func (nopEvents) RemoteHostNotes([]remotetools.HostImport)                               {}
func (nopEvents) ResumeTranscript(*runstate.RunState, map[string]*fisk2.FiskCommandTool) {}
func (nopEvents) LLMRequest(string)                                                      {}
func (nopEvents) ToolCall(ToolTrace)                                                     {}
func (nopEvents) ToolResult(ToolResultTrace)                                             {}
func (nopEvents) Message(llm.Response, bool)                                             {}
func (nopEvents) SessionRotated(string)                                                  {}
func (nopEvents) Panicked(any, []byte)                                                   {}

// captureEvents records the tool traces so a test can assert what was emitted; it
// inherits the no-op behavior for every other event.
type captureEvents struct {
	nopEvents
	calls   []ToolTrace
	results []ToolResultTrace
	warns   []Warning
}

func (c *captureEvents) ToolCall(t ToolTrace)         { c.calls = append(c.calls, t) }
func (c *captureEvents) ToolResult(t ToolResultTrace) { c.results = append(c.results, t) }
func (c *captureEvents) Warn(w Warning)               { c.warns = append(c.warns, w) }

// warnRecorder records the warnings a run emits, inheriting the no-op behavior for
// every other event.
type warnRecorder struct {
	nopEvents
	warns []Warning
}

func (w *warnRecorder) Warn(x Warning) { w.warns = append(w.warns, x) }

func (w *warnRecorder) has(kind WarningKind) bool {
	for _, x := range w.warns {
		if x.Kind == kind {
			return true
		}
	}
	return false
}

// rotateRecorder records the previous session ids reported when a context reset rotates
// to a fresh session, inheriting the no-op behavior for every other event.
type rotateRecorder struct {
	nopEvents
	prevIDs []string
}

func (r *rotateRecorder) SessionRotated(prevID string) { r.prevIDs = append(r.prevIDs, prevID) }

// stubInvoker is a canned a2a.RemoteInvoker for driving a RemoteTool through the
// runner without a transport: every call returns the same reply.
type stubInvoker struct {
	reply *a2a.ToolReply
}

func (s stubInvoker) InvokeTool(context.Context, string, string, json.RawMessage) (*a2a.ToolReply, error) {
	return s.reply, nil
}

func mustMessage(j string) *anthropic.Message {
	GinkgoHelper()
	var m anthropic.Message
	Expect(json.Unmarshal([]byte(j), &m)).To(Succeed())
	return &m
}

// mustResponse builds a neutral llm.Response from an Anthropic message JSON, so the
// test data stays the wire form the model returns while the loop consumes neutral.
func mustResponse(j string) *llm.Response {
	GinkgoHelper()
	resp, err := llmanthropic.ResponseToNeutral(mustMessage(j))
	Expect(err).NotTo(HaveOccurred())
	return &resp
}

// providerFunc adapts a plain function to the llm.Provider interface so a test can
// drive the loop with scripted responses in place of a live API call.
type providerFunc func(context.Context, llm.Request) (*llm.Response, error)

func (f providerFunc) Call(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return f(ctx, req)
}

func (providerFunc) Capabilities() llm.Caps {
	return llm.Caps{Provider: "anthropic", SupportsToolSearch: true}
}

// userMsg and assistantTextMsg build the neutral turns a test conversation seeds.
func userMsg(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: text}}}}
}

func assistantTextMsg(text string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Text: &llm.TextBlock{Text: text}}}}
}

// failOnUserJournal wraps a real journal but rejects the interactive user record, so a
// test can exercise the "journaling a follow-up failed" path without corrupting a real
// store. Every other record is appended normally.
type failOnUserJournal struct {
	runstate.Journal
}

func (j failOnUserJournal) Append(seq uint64, rec runstate.Record) error {
	if rec.Protocol == runstate.UserProtocol {
		return errors.New("disk full")
	}
	return j.Journal.Append(seq, rec)
}

var _ = Describe("runner", func() {
	Describe("resumeHazards", func() {
		It("warns when resuming at a paused-turn boundary", func() {
			rs := &runstate.RunState{LastStopReason: "pause_turn"}
			ws := resumeHazards(rs)
			Expect(ws).To(HaveLen(1))
			Expect(ws[0].Kind).To(Equal(WarnResumePausedTurn))
		})

		It("stays quiet for an ordinary resume", func() {
			rs := &runstate.RunState{LastStopReason: "tool_use"}
			Expect(resumeHazards(rs)).To(BeEmpty())
		})
	})

	Describe("suspend then resume across runners", func() {
		It("journals a run, suspends at a boundary, and resumes in a fresh runner to completion", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			id := ksuid.New().String()

			cfg := &config.Config{}
			cfg.LLM.Model = "test-model"
			cfg.LLM.Budget.CallTimeoutParsed = time.Second

			toolMsg := `{"id":"m1","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"missing","input":{}}],"usage":{"input_tokens":10,"output_tokens":5}}`
			finalMsg := `{"id":"m2","type":"message","role":"assistant","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"all done"}],"usage":{"input_tokens":3,"output_tokens":2}}`

			emptyTools := func(r *runner) {
				r.tools = map[string]toolkit.Tool{}
			}

			// Runner A: one tool-using turn, then a suspend request lands, so the
			// loop stops at the next boundary before calling the LLM again.
			jA, err := store.Create(id, runstate.MetaRecord{Version: runstate.Version, RunID: id, Prompt: "go"})
			Expect(err).NotTo(HaveOccurred())

			var suspendNow atomic.Bool
			rA := &runner{
				cfg: cfg, stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages:         []llm.Message{userMsg("go")},
				journal:          jA,
				seq:              1,
				suspendRequested: suspendNow.Load,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					suspendNow.Store(true)
					return mustResponse(toolMsg), nil
				}),
			}
			emptyTools(rA)

			reason, err := rA.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonSuspended))
			Expect(jA.Close()).To(Succeed())

			// The suspended session is resumable, its one tool turn recorded.
			mid, err := store.Load(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(mid.Completed()).To(BeFalse())
			Expect(mid.NextIteration).To(Equal(int64(1)))
			Expect(mid.Counters.LlmCalls).To(Equal(int64(1)))

			// Runner B: a fresh runner seeded from the store finishes the run.
			jB, err := store.Open(id)
			Expect(err).NotTo(HaveOccurred())

			rB := &runner{
				cfg: cfg, stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages:  mid.Messages,
				journal:   jB,
				seq:       jB.LastSeq(),
				startIter: mid.NextIteration,
				pending:   mid.Pending,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					return mustResponse(finalMsg), nil
				}),
			}
			emptyTools(rB)

			reason, err = rB.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			Expect(jB.Close()).To(Succeed())

			done, err := store.Load(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(done.Completed()).To(BeTrue())
			Expect(done.Counters.LlmCalls).To(Equal(int64(2)))
		})
	})

	Describe("executeTool", func() {
		It("emits neither a call nor a result for a tool that never ran", func() {
			ev := &captureEvents{}
			r := &runner{
				stats:  &util.RunStats{},
				events: ev,
				tools:  map[string]toolkit.Tool{},
			}

			block, remote := r.executeTool(context.Background(), llm.ToolUseBlock{ID: "t1", Name: "nope"})
			Expect(remote).To(BeFalse())
			Expect(block.ToolUseID).To(Equal("t1"))
			Expect(block.IsError).To(BeTrue())

			// An unknown tool is reported as a warning only; it never ran, so it leaves
			// no call or result line in the transcript.
			Expect(ev.calls).To(BeEmpty())
			Expect(ev.results).To(BeEmpty())
		})

		It("rejects a local tool call missing a required parameter without running it", func() {
			ev := &captureEvents{}
			tool := &fisk2.FiskCommandTool{
				Path:    []string{"do"},
				AppPath: filepath.Join(GinkgoT().TempDir(), "never-run"),
				Model: &fisk.CmdModel{RestrictedSchema: map[string]any{
					"type":     "object",
					"required": []string{"subject"},
					"properties": map[string]any{
						"subject": map[string]any{"type": "string"},
						"level":   map[string]any{"type": "string"},
					},
				}},
			}
			r := &runner{
				stats:  &util.RunStats{},
				events: ev,
				tools:  map[string]toolkit.Tool{"do": tool},
			}

			block, remote := r.executeTool(context.Background(), llm.ToolUseBlock{ID: "t1", Name: "do", Input: json.RawMessage(`{"level":"info"}`)})
			Expect(remote).To(BeFalse())
			Expect(block.ToolUseID).To(Equal("t1"))
			Expect(block.IsError).To(BeTrue())

			// Like an unknown tool, a rejected call never ran, so it emits only a
			// warning naming the missing parameters and no call or result line.
			Expect(ev.calls).To(BeEmpty())
			Expect(ev.results).To(BeEmpty())
			Expect(ev.warns).To(HaveLen(1))
			Expect(ev.warns[0].Kind).To(Equal(WarnMissingRequired))
			Expect(ev.warns[0].Name).To(Equal("do"))
			Expect(ev.warns[0].Params).To(Equal([]string{"subject"}))
		})

		It("dispatches a local command tool: traces the full call line and runs it", func() {
			app := filepath.Join(GinkgoT().TempDir(), "app")
			Expect(os.WriteFile(app, []byte("#!/bin/sh\necho hello\n"), 0o755)).To(Succeed())

			ev := &captureEvents{}
			tool := &fisk2.FiskCommandTool{Path: []string{"do"}, AppPath: app, Model: &fisk.CmdModel{}}
			r := &runner{stats: &util.RunStats{}, events: ev, tools: map[string]toolkit.Tool{"do": tool}}

			block, remote := r.executeTool(context.Background(), llm.ToolUseBlock{ID: "t1", Name: "do", Input: json.RawMessage(`{}`)})
			Expect(remote).To(BeFalse())
			Expect(block.ToolUseID).To(Equal("t1"))
			Expect(block.IsError).To(BeFalse())

			Expect(ev.calls).To(HaveLen(1))
			Expect(ev.calls[0].Kind).To(Equal(ToolLocal))
			Expect(ev.calls[0].Display).NotTo(BeEmpty())
			Expect(ev.results).To(HaveLen(1))
			Expect(ev.results[0].Kind).To(Equal(ToolLocal))
		})

		It("dispatches a remote tool: flags it remote, counts it, and traces the agent", func() {
			ev := &captureEvents{}
			desc := a2a.ToolDescriptor{Name: "info", Description: "reports info", InputSchema: json.RawMessage(`{"type":"object"}`)}
			rt, err := a2a.NewRemoteTool("nats_info", "nats", desc, stubInvoker{reply: a2a.NewToolReply("ok", false)})
			Expect(err).NotTo(HaveOccurred())

			r := &runner{stats: &util.RunStats{}, events: ev, tools: map[string]toolkit.Tool{"nats_info": rt}}

			block, remote := r.executeTool(context.Background(), llm.ToolUseBlock{ID: "t1", Name: "nats_info"})
			Expect(remote).To(BeTrue())
			Expect(r.stats.RemoteToolCalls).To(Equal(int64(1)))
			Expect(block.ToolUseID).To(Equal("t1"))
			Expect(block.IsError).To(BeFalse())

			Expect(ev.calls).To(HaveLen(1))
			Expect(ev.calls[0].Kind).To(Equal(ToolRemote))
			Expect(ev.calls[0].Agent).To(Equal("nats"))
			Expect(ev.results).To(HaveLen(1))
			Expect(ev.results[0].Kind).To(Equal(ToolRemote))
		})

		It("gates a confirm-tagged local tool and denies it without running when no operator can approve", func() {
			ev := &captureEvents{}
			tool := &fisk2.FiskCommandTool{
				Path:    []string{"stream", "rm"},
				AppPath: filepath.Join(GinkgoT().TempDir(), "never-run"),
				Model:   &fisk.CmdModel{Tags: []string{"ai:confirm"}},
			}
			r := &runner{
				stats:  &util.RunStats{},
				events: ev,
				tools:  map[string]toolkit.Tool{"stream_rm": tool},
				gate:   util.NewConfirmGate(toolkit.DefaultDenyPrompter()),
			}

			// With no operator reachable (the deny prompter reports it cannot prompt)
			// there is no one to approve, so the gate denies. The gated tool is never run:
			// no call or result line is emitted, and the denial is an authoritative
			// non-error result to the model.
			block, remote := r.executeTool(context.Background(), llm.ToolUseBlock{ID: "t1", Name: "stream_rm"})
			Expect(remote).To(BeFalse())
			Expect(block.ToolUseID).To(Equal("t1"))
			Expect(block.IsError).To(BeFalse())
			Expect(ev.calls).To(BeEmpty())
			Expect(ev.results).To(BeEmpty())
		})
	})

	Describe("completePending (partial-turn resume)", func() {
		It("runs only the unanswered tools, reuses the rest, and commits the turn", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			runID := ksuid.New().String()

			// Journal a run whose assistant turn called two (unknown) tools but
			// only the first was answered before the "crash": a partial batch.
			assistant := llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentBlock{
					{ToolUse: &llm.ToolUseBlock{ID: "toolu_a", Name: "missing_a", Input: json.RawMessage(`{}`)}},
					{ToolUse: &llm.ToolUseBlock{ID: "toolu_b", Name: "missing_b", Input: json.RawMessage(`{}`)}},
				},
			}

			j, err := store.Create(runID, runstate.MetaRecord{Version: runstate.Version, RunID: runID, Prompt: "go"})
			Expect(err).NotTo(HaveOccurred())
			Expect(j.Append(2, runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: &runstate.AssistantRecord{Iteration: 0, Message: assistant}})).To(Succeed())
			Expect(j.Append(3, runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: &runstate.ToolResultRecord{ToolUseID: "toolu_a", Result: llm.ToolResultBlock{ToolUseID: "toolu_a", Content: "already done"}}})).To(Succeed())
			Expect(j.Close()).To(Succeed())

			rs, err := store.Load(runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Pending).NotTo(BeNil())

			resumeJ, err := store.Open(runID)
			Expect(err).NotTo(HaveOccurred())

			r := &runner{
				stats:    &util.RunStats{},
				events:   nopEvents{},
				tools:    map[string]toolkit.Tool{},
				messages: rs.Messages,
				journal:  resumeJ,
				seq:      resumeJ.LastSeq(),
				pending:  rs.Pending,
			}

			before := len(r.messages)
			Expect(r.completePending(context.Background())).To(Succeed())

			// The assistant turn plus a single user results message are committed.
			Expect(r.messages).To(HaveLen(before + 2))
			// Only the unanswered tool executed.
			Expect(r.stats.ToolCalls).To(Equal(int64(1)))
			Expect(resumeJ.Close()).To(Succeed())

			// Re-folding shows the turn fully answered: no pending remains, and
			// both tool results are recorded.
			done, err := store.Load(runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(done.Pending).To(BeNil())
			Expect(done.Counters.ToolCalls).To(Equal(int64(2)))
		})
	})

	Describe("interactive continuation", func() {
		newCfg := func() *config.Config {
			cfg := &config.Config{}
			cfg.LLM.Model = "test-model"
			cfg.LLM.Budget.CallTimeoutParsed = time.Second
			cfg.LLM.Budget.MaxIterations = 10
			return cfg
		}
		emptyTools := func(r *runner) {
			r.tools = map[string]toolkit.Tool{}
		}
		finalMsg := func(text string) string {
			return `{"id":"x","type":"message","role":"assistant","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"` + text + `"}],"usage":{"input_tokens":1,"output_tokens":1}}`
		}
		toolMsg := `{"id":"m1","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"missing","input":{}}],"usage":{"input_tokens":10,"output_tokens":5}}`

		It("re-enters the loop with a follow-up and ends on a false continuation", func() {
			var calls, prompts int
			answers := []string{finalMsg("first"), finalMsg("second")}

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []llm.Message{userMsg("go")},
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					m := answers[calls]
					calls++
					return mustResponse(m), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "tell me more", Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			Expect(calls).To(Equal(2))
			Expect(prompts).To(Equal(2))
			Expect(r.stats.LlmCalls).To(Equal(int64(2)))
			// The follow-up became a user turn between the two assistant answers.
			Expect(r.messages).To(HaveLen(4))
			Expect(r.messages[2].Role).To(Equal(llm.RoleUser))
			// The iteration index stayed monotonic across the two turns.
			Expect(r.iter).To(Equal(int64(2)))
		})

		It("clears the conversation on a reset and runs the fresh prompt against the empty context", func() {
			var calls, prompts int
			var seenLens []int
			answers := []string{finalMsg("first"), finalMsg("second")}

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []llm.Message{userMsg("go")},
				provider: providerFunc(func(_ context.Context, p llm.Request) (*llm.Response, error) {
					seenLens = append(seenLens, len(p.Messages))
					m := answers[calls]
					calls++
					return mustResponse(m), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "fresh", Reset: true, Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			Expect(calls).To(Equal(2))
			// The first turn saw the original single-message conversation; the reset
			// dropped it, so the second turn ran against an empty context (one user
			// message) rather than the three it would have accumulated without the clear.
			Expect(seenLens).To(Equal([]int{1, 1}))
			Expect(r.messages).To(HaveLen(2))
			Expect(r.messages[0].Role).To(Equal(llm.RoleUser))
		})

		It("reopens the input on a bare reset without running an extra turn", func() {
			var calls, prompts int
			answers := []string{finalMsg("first"), finalMsg("second")}

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []llm.Message{userMsg("go")},
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					m := answers[calls]
					calls++
					return mustResponse(m), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					switch prompts {
					case 1:
						return Continuation{Reset: true, Continue: true}
					case 2:
						return Continuation{Text: "now", Continue: true}
					default:
						return Continuation{}
					}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			// The bare reset ran no turn: only the initial answer and the post-reset
			// "now" prompt reached the model.
			Expect(calls).To(Equal(2))
			Expect(prompts).To(Equal(3))
			// The cleared context leaves only the post-reset turn behind.
			Expect(r.messages).To(HaveLen(2))
		})

		It("rotates to a fresh checkpoint session on a reset, keeping the previous one resumable", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			oldID := ksuid.New().String()
			jA, err := store.Create(oldID, runstate.MetaRecord{Version: runstate.Version, RunID: oldID, Prompt: "go", Interactive: true})
			Expect(err).NotTo(HaveOccurred())

			var newID string
			newSession := func(prompt string) (runstate.Journal, string, error) {
				newID = ksuid.New().String()
				meta := runstate.MetaRecord{Version: runstate.Version, RunID: newID, Prompt: prompt, Interactive: true}
				j, e := store.Create(newID, meta)
				if e != nil {
					return nil, "", e
				}
				return j, newID, nil
			}

			var calls, prompts int
			rec := &rotateRecorder{}
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: rec,
				messages:   []llm.Message{userMsg("go")},
				journal:    jA,
				seq:        1,
				sessionID:  oldID,
				newSession: newSession,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					calls++
					return mustResponse(finalMsg("answer")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "fresh", Reset: true, Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonSuspended))
			Expect(calls).To(Equal(2))

			// The reset rotated onto a brand-new session and reported the previous one.
			Expect(newID).NotTo(BeEmpty())
			Expect(r.sessionID).To(Equal(newID))
			Expect(rec.prevIDs).To(Equal([]string{oldID}))

			// The previous session is finalized as suspended (never completed), so it stays
			// resumable, with just its single pre-reset turn recorded.
			old, err := store.Load(oldID)
			Expect(err).NotTo(HaveOccurred())
			Expect(old.Completed()).To(BeFalse())
			Expect(old.Counters.LlmCalls).To(Equal(int64(1)))

			// The fresh session holds only the post-reset conversation: its "fresh" prompt (the
			// new Meta.Prompt) and the one answer, not the pre-reset "go" turn.
			fresh, err := store.Load(newID)
			Expect(err).NotTo(HaveOccurred())
			Expect(fresh.Counters.LlmCalls).To(Equal(int64(1)))
			Expect(fresh.Messages).To(HaveLen(2))
			Expect(fresh.Messages[0].Role).To(Equal(llm.RoleUser))
		})

		It("defers a bare reset until the next prompt before rotating (checkpointed)", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			oldID := ksuid.New().String()
			jA, err := store.Create(oldID, runstate.MetaRecord{Version: runstate.Version, RunID: oldID, Prompt: "go", Interactive: true})
			Expect(err).NotTo(HaveOccurred())

			var newID string
			var rotateCalls int
			newSession := func(prompt string) (runstate.Journal, string, error) {
				rotateCalls++
				newID = ksuid.New().String()
				j, e := store.Create(newID, runstate.MetaRecord{Version: runstate.Version, RunID: newID, Prompt: prompt, Interactive: true})
				if e != nil {
					return nil, "", e
				}
				return j, newID, nil
			}

			var calls, prompts int
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages:   []llm.Message{userMsg("go")},
				journal:    jA,
				seq:        1,
				sessionID:  oldID,
				newSession: newSession,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					calls++
					return mustResponse(finalMsg("answer")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					switch prompts {
					case 1:
						return Continuation{Reset: true, Continue: true}
					case 2:
						return Continuation{Text: "typed", Continue: true}
					default:
						return Continuation{}
					}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonSuspended))

			// The bare reset ran no turn and did not rotate; rotation happened once, on the next
			// prompt, so only the "go" and "typed" turns reached the model.
			Expect(calls).To(Equal(2))
			Expect(rotateCalls).To(Equal(1))
			Expect(r.sessionID).To(Equal(newID))

			// The previous session kept just its pre-reset turn and stays resumable.
			old, err := store.Load(oldID)
			Expect(err).NotTo(HaveOccurred())
			Expect(old.Completed()).To(BeFalse())
			Expect(old.Counters.LlmCalls).To(Equal(int64(1)))
		})

		It("runs on in the current session and warns when rotation fails", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())

			oldID := ksuid.New().String()
			jA, err := store.Create(oldID, runstate.MetaRecord{Version: runstate.Version, RunID: oldID, Prompt: "go", Interactive: true})
			Expect(err).NotTo(HaveOccurred())

			newSession := func(string) (runstate.Journal, string, error) {
				return nil, "", errors.New("store unavailable")
			}

			var calls, prompts int
			we := &warnRecorder{}
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: we,
				messages:   []llm.Message{userMsg("go")},
				journal:    jA,
				seq:        1,
				sessionID:  oldID,
				newSession: newSession,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					calls++
					return mustResponse(finalMsg("answer")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "fresh", Reset: true, Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonSuspended))
			Expect(calls).To(Equal(2))

			// Rotation failed, so the reset was abandoned: the session id is unchanged and a
			// warning was raised.
			Expect(r.sessionID).To(Equal(oldID))
			Expect(we.has(WarnSessionRotate)).To(BeTrue())

			// The turn ran on in the original session, which keeps both turns and stays consistent.
			old, err := store.Load(oldID)
			Expect(err).NotTo(HaveOccurred())
			Expect(old.Completed()).To(BeFalse())
			Expect(old.Counters.LlmCalls).To(Equal(int64(2)))
		})

		It("recovers from a max-iterations turn, warning and re-prompting", func() {
			we := &warnRecorder{}
			var prompts int

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 1, events: we,
				messages: []llm.Message{userMsg("go")},
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					return mustResponse(toolMsg), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			Expect(prompts).To(Equal(1))
			Expect(we.has(WarnMaxIterInteractive)).To(BeTrue())
		})

		It("returns to the input bar after a turn error, warns, and folds the retry into the dangling turn", func() {
			we := &warnRecorder{}
			var calls, prompts int

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: we,
				messages: []llm.Message{userMsg("go")},
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					calls++
					if calls == 1 {
						return nil, errors.New("llm call: context deadline exceeded")
					}
					return mustResponse(finalMsg("recovered")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "try again", Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			Expect(calls).To(Equal(2))
			Expect(prompts).To(Equal(2))
			Expect(we.has(WarnTurnErrorInteractive)).To(BeTrue())
			// The failed turn left "go" dangling with no assistant reply, so the retry
			// folded into it rather than adding a second user message in a row: one user
			// turn carrying both texts, then the recovered assistant answer.
			Expect(r.messages).To(HaveLen(2))
			Expect(r.messages[0].Role).To(Equal(llm.RoleUser))
			Expect(r.messages[0].Content).To(HaveLen(2))
			Expect(r.messages[1].Role).To(Equal(llm.RoleAssistant))
		})

		It("journals interactive follow-ups and suspends a checkpointed chat on a clean end", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			id := ksuid.New().String()
			j, err := store.Create(id, runstate.MetaRecord{Version: runstate.Version, RunID: id, Prompt: "go", Interactive: true})
			Expect(err).NotTo(HaveOccurred())

			var calls, prompts int
			answers := []string{finalMsg("first"), finalMsg("second")}
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []llm.Message{userMsg("go")},
				journal:  j, seq: 1,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					m := answers[calls]
					calls++
					return mustResponse(m), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "more", Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			// A clean end on a checkpointed chat suspends (resumable), never completes.
			Expect(reason).To(Equal(runstate.ReasonSuspended))
			Expect(j.Close()).To(Succeed())

			rs, err := store.Load(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Completed()).To(BeFalse())
			Expect(rs.Interactive).To(BeTrue())
			// prompt, assistant(first), user(follow-up), assistant(second)
			Expect(rs.Messages).To(HaveLen(4))
			Expect(rs.Messages[2].Role).To(Equal(llm.RoleUser))
			Expect(rs.NextIteration).To(Equal(int64(2)))
		})

		It("resumes a chat at the input boundary without a spurious LLM call", func() {
			var calls, prompts int
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 12, events: nopEvents{},
				startIter:             2,
				resumeAtInputBoundary: true,
				// The restored conversation rests on an assistant turn awaiting a follow-up.
				messages: []llm.Message{
					userMsg("go"),
					assistantTextMsg("answer"),
				},
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					calls++
					return mustResponse(finalMsg("follow-up answer")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					if prompts == 1 {
						return Continuation{Text: "next", Continue: true}
					}
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			// Exactly one LLM call, the follow-up turn: the initial loop was skipped so no
			// spurious call was made against the already-complete conversation.
			Expect(calls).To(Equal(1))
			Expect(prompts).To(Equal(2))
			// Iteration numbering continued from the resumed position, not restarted.
			Expect(r.iter).To(Equal(int64(3)))
		})

		It("ends the session, warns, and does not loop when journaling a follow-up fails", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			id := ksuid.New().String()
			j, err := store.Create(id, runstate.MetaRecord{Version: runstate.Version, RunID: id, Prompt: "go", Interactive: true})
			Expect(err).NotTo(HaveOccurred())

			we := &warnRecorder{}
			var prompts int
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: we,
				messages: []llm.Message{userMsg("go")},
				journal:  failOnUserJournal{Journal: j}, seq: 1,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					return mustResponse(finalMsg("answer")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					prompts++
					return Continuation{Text: "a follow-up", Continue: true}
				},
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(reason).To(Equal(runstate.ReasonError))
			Expect(err).To(MatchError(ContainSubstring("disk full")))
			Expect(we.has(WarnJournalUser)).To(BeTrue())
			// The failed emit must break the continuation loop, not re-offer the bar (which
			// would fail again forever); the operator was prompted exactly once.
			Expect(prompts).To(Equal(1))
			Expect(j.Close()).To(Succeed())
		})

		It("surfaces an abort at the input boundary as an error, not a clean end", func() {
			ctx, cancel := context.WithCancel(context.Background())

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []llm.Message{userMsg("go")},
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					return mustResponse(finalMsg("done")), nil
				}),
				nextPrompt: func(context.Context) Continuation {
					// The operator aborted (Ctrl-C) while the field was up: ctx is
					// canceled and the prompt reports no continuation.
					cancel()
					return Continuation{}
				},
			}
			emptyTools(r)

			reason, err := r.run(ctx)
			Expect(reason).To(Equal(runstate.ReasonError))
			Expect(err).To(MatchError(context.Canceled))
		})
	})

	Describe("cache accounting", func() {
		emptyTools := func(r *runner) {
			r.tools = map[string]toolkit.Tool{}
		}

		It("flows the cache split into stats, the journal, folded counters and the budget", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			id := ksuid.New().String()

			cfg := &config.Config{}
			cfg.LLM.Model = "test-model"
			cfg.LLM.Budget.CallTimeoutParsed = time.Second
			// A budget just above the first turn's uncached input+output but below the full
			// throughput (which the cache tiers push over) proves the check counts all four.
			cfg.LLM.Budget.MaxTokens = 50

			// A tool turn whose usage carries a cache read and a cache write, so the split is
			// non-zero on a turn that continues (the budget check runs before the next turn).
			cachedTurn := `{"id":"m1","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"missing","input":{}}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40}}`

			j, err := store.Create(id, runstate.MetaRecord{Version: runstate.Version, RunID: id, Prompt: "go"})
			Expect(err).NotTo(HaveOccurred())

			r := &runner{
				cfg: cfg, stats: &util.RunStats{}, maxIter: 10, maxTokens: cfg.LLM.Budget.MaxTokens, events: nopEvents{},
				messages: []llm.Message{userMsg("go")},
				journal:  j,
				seq:      1,
				provider: providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
					return mustResponse(cachedTurn), nil
				}),
			}
			emptyTools(r)

			reason, err := r.run(context.Background())
			Expect(err).To(HaveOccurred())
			// input(10)+output(5)+read(100)+write(40) = 155 >= 50, so the budget stops it
			// before a second turn; uncached-only (15) would not have.
			Expect(reason).To(Equal(runstate.ReasonBudget))
			Expect(j.Close()).To(Succeed())

			// RunStats carries the split as first-class fields.
			Expect(r.stats.InTokens).To(Equal(int64(10)))
			Expect(r.stats.CacheReadTokens).To(Equal(int64(100)))
			Expect(r.stats.CacheCreateTokens).To(Equal(int64(40)))

			// The journaled assistant record carries it, and Fold sums it into the counters
			// that seed a resumed run, so all four stay consistent across a suspend.
			rs, err := store.Load(id)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Counters.InTokens).To(Equal(int64(10)))
			Expect(rs.Counters.OutTokens).To(Equal(int64(5)))
			Expect(rs.Counters.CacheReadTokens).To(Equal(int64(100)))
			Expect(rs.Counters.CacheCreateTokens).To(Equal(int64(40)))
		})
	})
})

var _ = Describe("Run tool availability guard", func() {
	// The guard must count every tool source the model can address (application,
	// built-in and remote), not just the filtered application tools; otherwise a run
	// whose only tools are native (for example knowledge_search) is wrongly aborted.

	// emptyAppCfg points at a fake fisk application that introspects to zero
	// commands, so LoadTools succeeds with an empty tool set and the run reaches the
	// availability guard rather than failing earlier in introspection.
	emptyAppCfg := func() *config.Config {
		dir := GinkgoT().TempDir()
		app := filepath.Join(dir, "fakeapp")
		Expect(os.WriteFile(app, []byte("#!/bin/sh\necho '{}'\n"), 0o755)).To(Succeed())

		cfg := &config.Config{ApplicationPath: app}
		cfg.LLM.Model = "test-model"
		cfg.LLM.Budget.MaxIterations = 1
		return cfg
	}

	It("aborts when no application, built-in, or remote tool is available", func() {
		cfg := emptyAppCfg()

		_, err := Run(context.Background(), Options{Config: cfg, ConfigFile: "agent.yaml"}, nopEvents{}, nil)
		Expect(err).To(MatchError(ContainSubstring("no tools available after filtering")))
	})

	It("aborts with an application-less message when no application_path and no tools are set", func() {
		cfg := &config.Config{}
		cfg.LLM.Model = "test-model"
		cfg.LLM.Budget.MaxIterations = 1

		_, err := Run(context.Background(), Options{Config: cfg, ConfigFile: "agent.yaml"}, nopEvents{}, nil)
		Expect(err).To(MatchError(ContainSubstring("this agent wraps no application")))
	})

	It("proceeds past the guard when only a native tool (knowledge_search) is enabled", func() {
		cfg := emptyAppCfg()
		cfg.Harness.RAG = &config.RAGConfig{Enabled: true, Directory: GinkgoT().TempDir()}

		// The guard now passes on the knowledge_search built-in, so the run continues
		// to the model call, which fails fast against an unreachable local endpoint.
		// The point is only that it is not the "no tools" abort.
		opts := Options{
			Config:     cfg,
			ConfigFile: "agent.yaml",
			APIKey:     "test",
			BaseURL:    "http://127.0.0.1:1",
		}
		_, err := Run(context.Background(), opts, nopEvents{}, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).ToNot(ContainSubstring("no tools available after filtering"))
	})
})

var _ = Describe("Run with an injected Options.Provider", func() {
	// ragAppCfg points at a fake fisk application that introspects to zero commands
	// and enables knowledge, so the run gets past the tool-availability guard on the
	// knowledge_search built-in and reaches the model call with the injected provider.
	ragAppCfg := func() *config.Config {
		dir := GinkgoT().TempDir()
		app := filepath.Join(dir, "fakeapp")
		Expect(os.WriteFile(app, []byte("#!/bin/sh\necho '{}'\n"), 0o755)).To(Succeed())

		cfg := &config.Config{ApplicationPath: app}
		cfg.LLM.Model = "test-model"
		cfg.LLM.Budget.MaxIterations = 1
		cfg.Harness.RAG = &config.RAGConfig{Enabled: true, Directory: GinkgoT().TempDir()}
		return cfg
	}

	It("uses the injected provider and never consults the registry", func() {
		cfg := ragAppCfg()
		// An unregistered provider name proves the registry is bypassed: the nil-provider
		// path would fail NewProvider with "unknown llm provider" before any model call.
		cfg.LLM.Provider = "definitely-not-a-registered-backend"

		var called atomic.Bool
		provider := providerFunc(func(context.Context, llm.Request) (*llm.Response, error) {
			called.Store(true)
			return mustResponse(`{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}`), nil
		})

		opts := Options{Config: cfg, ConfigFile: "agent.yaml", Provider: provider}
		res, err := Run(context.Background(), opts, nopEvents{}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(called.Load()).To(BeTrue())
		Expect(res.Reason).To(Equal(runstate.ReasonCompleted))
	})
})

// failCloseJournal is a runstate.Journal whose Close fails, to exercise the
// journal-close warning routing. Only Close is called by closeJournal.
type failCloseJournal struct {
	runstate.Journal
}

func (failCloseJournal) Close() error { return errors.New("disk gone") }

var _ = Describe("closeJournal", func() {
	It("routes a journal close failure through events.Warn rather than raw stderr", func() {
		ev := &warnRecorder{}
		closeJournal(failCloseJournal{}, ev)
		Expect(ev.has(WarnJournalClose)).To(BeTrue())
	})
})

var _ = Describe("validateCallerDir", func() {
	It("accepts an empty value as inherit-today's-behavior", func() {
		Expect(validateCallerDir("tool_work_dir", "")).To(Succeed())
	})

	It("rejects a relative path, naming the option", func() {
		err := validateCallerDir("tool_work_dir", "rel/dir")
		Expect(err).To(MatchError(ContainSubstring("tool_work_dir must be an absolute path")))
	})

	It("rejects a missing directory", func() {
		err := validateCallerDir("tool_work_dir", filepath.Join(GinkgoT().TempDir(), "absent"))
		Expect(err).To(MatchError(ContainSubstring("does not exist")))
	})

	It("rejects a path that is not a directory", func() {
		f := filepath.Join(GinkgoT().TempDir(), "afile")
		Expect(os.WriteFile(f, []byte("x"), 0o600)).To(Succeed())
		err := validateCallerDir("tool_work_dir", f)
		Expect(err).To(MatchError(ContainSubstring("is not a directory")))
	})

	It("accepts an existing absolute directory", func() {
		Expect(validateCallerDir("tool_work_dir", GinkgoT().TempDir())).To(Succeed())
	})
})

var _ = Describe("resolveMaxOutputTokens", func() {
	It("Should use the default when max_output_tokens is unset and thinking is off", func() {
		cfg := &config.Config{}
		Expect(resolveMaxOutputTokens(cfg, false)).To(Equal(int64(defaultMaxOutputTokens)))
	})

	It("Should raise the default for thinking when max_output_tokens is unset", func() {
		cfg := &config.Config{}
		Expect(resolveMaxOutputTokens(cfg, true)).To(Equal(int64(thinkingMaxOutputTokens)))
	})

	It("Should let an explicit max_output_tokens win over the default", func() {
		cfg := &config.Config{}
		cfg.LLM.Budget.MaxOutputTokens = 4096
		Expect(resolveMaxOutputTokens(cfg, false)).To(Equal(int64(4096)))
	})

	It("Should let an explicit max_output_tokens win over the thinking increase", func() {
		cfg := &config.Config{}
		cfg.LLM.Budget.MaxOutputTokens = 4096
		Expect(resolveMaxOutputTokens(cfg, true)).To(Equal(int64(4096)))
	})
})

var _ = Describe("toolSearchDegradation", func() {
	supports := llm.Caps{SupportsToolSearch: true}
	noSupport := llm.Caps{SupportsToolSearch: false}

	It("Should not warn when tool search is allowed", func() {
		Expect(toolSearchDegradation(util.ToolSearchThreshold, supports, true)).To(BeNil())
	})

	It("Should not warn when the set is below the threshold", func() {
		Expect(toolSearchDegradation(util.ToolSearchThreshold-1, supports, false)).To(BeNil())
	})

	It("Should report the operator-disabled cause when the provider supports tool search", func() {
		w := toolSearchDegradation(util.ToolSearchThreshold, supports, false)
		Expect(w).NotTo(BeNil())
		Expect(w.Kind).To(Equal(WarnToolSearchDisabled))
		Expect(w.Count).To(Equal(util.ToolSearchThreshold))
	})

	It("Should report the provider-unsupported cause when the provider cannot do tool search", func() {
		w := toolSearchDegradation(util.ToolSearchThreshold, noSupport, false)
		Expect(w).NotTo(BeNil())
		Expect(w.Kind).To(Equal(WarnToolSearchUnsupported))
	})
})
