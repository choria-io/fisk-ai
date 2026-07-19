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
	"github.com/segmentio/ksuid"

	"github.com/choria-io/fisk-ai/config"
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

func (nopEvents) Warn(Warning)                                               {}
func (nopEvents) Starting(RunInfo)                                           {}
func (nopEvents) RemoteHostNotes([]remotetools.HostImport)                   {}
func (nopEvents) ResumeTranscript(*runstate.RunState, map[string]*util.Tool) {}
func (nopEvents) LLMRequest(string)                                          {}
func (nopEvents) ToolCall(ToolTrace)                                         {}
func (nopEvents) ToolResult(ToolResultTrace)                                 {}
func (nopEvents) Message(*anthropic.Message, bool)                           {}
func (nopEvents) SessionRotated(string)                                      {}

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

func mustMessage(j string) *anthropic.Message {
	GinkgoHelper()
	var m anthropic.Message
	Expect(json.Unmarshal([]byte(j), &m)).To(Succeed())
	return &m
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
	Describe("toolUseFromParam", func() {
		It("rebuilds id, name and input from a stored tool_use block", func() {
			p := &anthropic.ToolUseBlockParam{ID: "toolu_1", Name: "shell", Input: map[string]any{"cmd": "ls"}}
			use, err := toolUseFromParam(p)
			Expect(err).NotTo(HaveOccurred())
			Expect(use.ID).To(Equal("toolu_1"))
			Expect(use.Name).To(Equal("shell"))

			var in map[string]any
			Expect(json.Unmarshal(use.Input, &in)).To(Succeed())
			Expect(in).To(HaveKeyWithValue("cmd", "ls"))
		})
	})

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
				r.byName = map[string]*util.Tool{}
				r.builtinByName = map[string]*util.BuiltinTool{}
				r.remoteByName = map[string]*util.RemoteTool{}
			}

			// Runner A: one tool-using turn, then a suspend request lands, so the
			// loop stops at the next boundary before calling the LLM again.
			jA, err := store.Create(id, runstate.MetaRecord{Version: runstate.Version, RunID: id, Prompt: "go"})
			Expect(err).NotTo(HaveOccurred())

			var suspendNow atomic.Bool
			rA := &runner{
				cfg: cfg, stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages:         []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:          jA,
				seq:              1,
				suspendRequested: suspendNow.Load,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					suspendNow.Store(true)
					return mustMessage(toolMsg), nil
				},
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
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					return mustMessage(finalMsg), nil
				},
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
				stats:         &util.RunStats{},
				events:        ev,
				byName:        map[string]*util.Tool{},
				builtinByName: map[string]*util.BuiltinTool{},
				remoteByName:  map[string]*util.RemoteTool{},
			}

			block, remote := r.executeTool(context.Background(), anthropic.ToolUseBlock{ID: "t1", Name: "nope"})
			Expect(remote).To(BeFalse())
			Expect(block.OfToolResult).NotTo(BeNil())
			Expect(block.OfToolResult.IsError.Or(false)).To(BeTrue())

			// An unknown tool is reported as a warning only; it never ran, so it leaves
			// no call or result line in the transcript.
			Expect(ev.calls).To(BeEmpty())
			Expect(ev.results).To(BeEmpty())
		})

		It("rejects a local tool call missing a required parameter without running it", func() {
			ev := &captureEvents{}
			tool := &util.Tool{
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
				stats:         &util.RunStats{},
				events:        ev,
				byName:        map[string]*util.Tool{"do": tool},
				builtinByName: map[string]*util.BuiltinTool{},
				remoteByName:  map[string]*util.RemoteTool{},
			}

			block, remote := r.executeTool(context.Background(), anthropic.ToolUseBlock{ID: "t1", Name: "do", Input: json.RawMessage(`{"level":"info"}`)})
			Expect(remote).To(BeFalse())
			Expect(block.OfToolResult).NotTo(BeNil())
			Expect(block.OfToolResult.IsError.Or(false)).To(BeTrue())

			// Like an unknown tool, a rejected call never ran, so it emits only a
			// warning naming the missing parameters and no call or result line.
			Expect(ev.calls).To(BeEmpty())
			Expect(ev.results).To(BeEmpty())
			Expect(ev.warns).To(HaveLen(1))
			Expect(ev.warns[0].Kind).To(Equal(WarnMissingRequired))
			Expect(ev.warns[0].Name).To(Equal("do"))
			Expect(ev.warns[0].Params).To(Equal([]string{"subject"}))
		})
	})

	Describe("completePending (partial-turn resume)", func() {
		It("runs only the unanswered tools, reuses the rest, and commits the turn", func() {
			store, err := runstatefile.NewFileStore(GinkgoT().TempDir())
			Expect(err).NotTo(HaveOccurred())
			runID := ksuid.New().String()

			// Journal a run whose assistant turn called two (unknown) tools but
			// only the first was answered before the "crash": a partial batch.
			assistant := anthropic.MessageParam{
				Role: anthropic.MessageParamRoleAssistant,
				Content: []anthropic.ContentBlockParamUnion{
					anthropic.NewToolUseBlock("toolu_a", map[string]any{}, "missing_a"),
					anthropic.NewToolUseBlock("toolu_b", map[string]any{}, "missing_b"),
				},
			}

			j, err := store.Create(runID, runstate.MetaRecord{Version: runstate.Version, RunID: runID, Prompt: "go"})
			Expect(err).NotTo(HaveOccurred())
			Expect(j.Append(2, runstate.Record{Protocol: runstate.AssistantProtocol, Assistant: &runstate.AssistantRecord{Iteration: 0, Message: assistant}})).To(Succeed())
			Expect(j.Append(3, runstate.Record{Protocol: runstate.ToolResultProtocol, ToolResult: &runstate.ToolResultRecord{ToolUseID: "toolu_a", Result: anthropic.NewToolResultBlock("toolu_a", "already done", false)}})).To(Succeed())
			Expect(j.Close()).To(Succeed())

			rs, err := store.Load(runID)
			Expect(err).NotTo(HaveOccurred())
			Expect(rs.Pending).NotTo(BeNil())

			resumeJ, err := store.Open(runID)
			Expect(err).NotTo(HaveOccurred())

			r := &runner{
				stats:         &util.RunStats{},
				events:        nopEvents{},
				byName:        map[string]*util.Tool{},
				builtinByName: map[string]*util.BuiltinTool{},
				remoteByName:  map[string]*util.RemoteTool{},
				messages:      rs.Messages,
				journal:       resumeJ,
				seq:           resumeJ.LastSeq(),
				pending:       rs.Pending,
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
			r.byName = map[string]*util.Tool{}
			r.builtinByName = map[string]*util.BuiltinTool{}
			r.remoteByName = map[string]*util.RemoteTool{}
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					m := answers[calls]
					calls++
					return mustMessage(m), nil
				},
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
			Expect(r.messages[2].Role).To(Equal(anthropic.MessageParamRoleUser))
			// The iteration index stayed monotonic across the two turns.
			Expect(r.iter).To(Equal(int64(2)))
		})

		It("clears the conversation on a reset and runs the fresh prompt against the empty context", func() {
			var calls, prompts int
			var seenLens []int
			answers := []string{finalMsg("first"), finalMsg("second")}

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				callLLM: func(_ context.Context, _ anthropic.Client, p anthropic.MessageNewParams, _ time.Duration) (*anthropic.Message, error) {
					seenLens = append(seenLens, len(p.Messages))
					m := answers[calls]
					calls++
					return mustMessage(m), nil
				},
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
			Expect(r.messages[0].Role).To(Equal(anthropic.MessageParamRoleUser))
		})

		It("reopens the input on a bare reset without running an extra turn", func() {
			var calls, prompts int
			answers := []string{finalMsg("first"), finalMsg("second")}

			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					m := answers[calls]
					calls++
					return mustMessage(m), nil
				},
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
				messages:   []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:    jA,
				seq:        1,
				sessionID:  oldID,
				newSession: newSession,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					calls++
					return mustMessage(finalMsg("answer")), nil
				},
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
			Expect(fresh.Messages[0].Role).To(Equal(anthropic.MessageParamRoleUser))
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
				messages:   []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:    jA,
				seq:        1,
				sessionID:  oldID,
				newSession: newSession,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					calls++
					return mustMessage(finalMsg("answer")), nil
				},
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
				messages:   []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:    jA,
				seq:        1,
				sessionID:  oldID,
				newSession: newSession,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					calls++
					return mustMessage(finalMsg("answer")), nil
				},
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					return mustMessage(toolMsg), nil
				},
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					calls++
					if calls == 1 {
						return nil, errors.New("llm call: context deadline exceeded")
					}
					return mustMessage(finalMsg("recovered")), nil
				},
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
			Expect(r.messages[0].Role).To(Equal(anthropic.MessageParamRoleUser))
			Expect(r.messages[0].Content).To(HaveLen(2))
			Expect(r.messages[1].Role).To(Equal(anthropic.MessageParamRoleAssistant))
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:  j, seq: 1,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					m := answers[calls]
					calls++
					return mustMessage(m), nil
				},
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
			Expect(rs.Messages[2].Role).To(Equal(anthropic.MessageParamRoleUser))
			Expect(rs.NextIteration).To(Equal(int64(2)))
		})

		It("resumes a chat at the input boundary without a spurious LLM call", func() {
			var calls, prompts int
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 12, events: nopEvents{},
				startIter:             2,
				resumeAtInputBoundary: true,
				// The restored conversation rests on an assistant turn awaiting a follow-up.
				messages: []anthropic.MessageParam{
					anthropic.NewUserMessage(anthropic.NewTextBlock("go")),
					anthropic.NewAssistantMessage(anthropic.NewTextBlock("answer")),
				},
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					calls++
					return mustMessage(finalMsg("follow-up answer")), nil
				},
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:  failOnUserJournal{Journal: j}, seq: 1,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					return mustMessage(finalMsg("answer")), nil
				},
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					return mustMessage(finalMsg("done")), nil
				},
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

	Describe("prompt caching", func() {
		newCfg := func() *config.Config {
			cfg := &config.Config{}
			cfg.LLM.Model = "test-model"
			cfg.LLM.Budget.CallTimeoutParsed = time.Second
			return cfg
		}
		emptyTools := func(r *runner) {
			r.byName = map[string]*util.Tool{}
			r.builtinByName = map[string]*util.BuiltinTool{}
			r.remoteByName = map[string]*util.RemoteTool{}
		}
		// A tool turn then a final answer gives two LLM calls, so the request assertions
		// cover a resend of the growing conversation, not just the first turn.
		toolMsg := `{"id":"m1","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","content":[{"type":"tool_use","id":"toolu_1","name":"missing","input":{}}],"usage":{"input_tokens":10,"output_tokens":5}}`
		finalMsg := `{"id":"m2","type":"message","role":"assistant","model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":3,"output_tokens":2}}`
		newSystem := func() []anthropic.TextBlockParam {
			return []anthropic.TextBlockParam{{Text: "sys one"}, {Text: "sys two"}}
		}
		var zeroCC anthropic.CacheControlEphemeralParam

		captureRun := func(r *runner) []anthropic.MessageNewParams {
			GinkgoHelper()
			var captured []anthropic.MessageNewParams
			answers := []string{toolMsg, finalMsg}
			var calls int
			r.callLLM = func(_ context.Context, _ anthropic.Client, p anthropic.MessageNewParams, _ time.Duration) (*anthropic.Message, error) {
				captured = append(captured, p)
				m := answers[calls]
				calls++
				return mustMessage(m), nil
			}
			emptyTools(r)
			reason, err := r.run(context.Background())
			Expect(err).NotTo(HaveOccurred())
			Expect(reason).To(Equal(runstate.ReasonCompleted))
			Expect(captured).To(HaveLen(2))
			return captured
		}

		It("places both breakpoints and leaves persistent state marker-free across iterations", func() {
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				system:      newSystem(),
				promptCache: true,
				messages:    []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
			}
			captured := captureRun(r)

			for _, p := range captured {
				last := p.System[len(p.System)-1]
				Expect(last.CacheControl).NotTo(Equal(zeroCC), "tools+system breakpoint set")
				Expect(p.CacheControl).NotTo(Equal(zeroCC), "conversation-tail breakpoint set")
			}

			// The value copy must not write the marker back through to r.system, or the
			// fingerprint would be poisoned; the runner's own slice stays clean.
			Expect(r.system[len(r.system)-1].CacheControl).To(Equal(zeroCC))

			// The tail breakpoint is a request-level field, so no marker is ever written
			// into the journaled conversation, however many turns run.
			raw, err := json.Marshal(r.messages)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(raw)).NotTo(ContainSubstring("cache_control"))
		})

		It("sets no breakpoints when caching is disabled", func() {
			r := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				system:      newSystem(),
				promptCache: false,
				messages:    []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
			}
			captured := captureRun(r)

			for _, p := range captured {
				Expect(p.System[len(p.System)-1].CacheControl).To(Equal(zeroCC))
				Expect(p.CacheControl).To(Equal(zeroCC))
			}
		})

		It("selects a 1h TTL for an interactive run and the 5m default for an autonomous one", func() {
			interactive := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				system: newSystem(), promptCache: true, interactive: true,
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
			}
			for _, p := range captureRun(interactive) {
				Expect(p.CacheControl.TTL).To(Equal(anthropic.CacheControlEphemeralTTLTTL1h))
				Expect(p.System[len(p.System)-1].CacheControl.TTL).To(Equal(anthropic.CacheControlEphemeralTTLTTL1h))
			}

			autonomous := &runner{
				cfg: newCfg(), stats: &util.RunStats{}, maxIter: 10, events: nopEvents{},
				system: newSystem(), promptCache: true, interactive: false,
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
			}
			for _, p := range captureRun(autonomous) {
				// The 5m default leaves TTL at its zero value (the SDK omits it).
				Expect(p.CacheControl.TTL).To(BeEmpty())
				Expect(p.System[len(p.System)-1].CacheControl.TTL).To(BeEmpty())
			}
		})
	})

	Describe("cache accounting", func() {
		emptyTools := func(r *runner) {
			r.byName = map[string]*util.Tool{}
			r.builtinByName = map[string]*util.BuiltinTool{}
			r.remoteByName = map[string]*util.RemoteTool{}
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
				messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("go"))},
				journal:  j,
				seq:      1,
				callLLM: func(context.Context, anthropic.Client, anthropic.MessageNewParams, time.Duration) (*anthropic.Message, error) {
					return mustMessage(cachedTurn), nil
				},
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
