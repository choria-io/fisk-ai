//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// These tests exercise the Options.CustomTools injection seam through the exported
// agent.Run API, alongside the living-doc examples in example_external_test.go. They
// assert the registration guards, the no-tools gate, the deferral threshold and the
// resume fingerprint, which are wiring properties rather than usage documentation.
package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/choria-io/fisk"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/agenttest"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/functool"
)

// noopCustomHandler is a handler that does nothing, for tools whose registration is
// rejected before they ever run or whose call is not the point of the test.
func noopCustomHandler(context.Context, json.RawMessage, *functool.CallContext) (string, error) {
	return "", nil
}

// staticTool is a minimal hand-rolled toolkit.Tool for the registration-guard cases a
// functool.New tool cannot express: an empty Name, or a Name that disagrees with its
// Definition. name is what Name() returns; defName is what Definition() advertises.
type staticTool struct {
	name    string
	defName string
}

func (s staticTool) Name() string                { return s.name }
func (s staticTool) Description() string         { return "a static tool" }
func (s staticTool) InputSchema() map[string]any { return map[string]any{"type": "object"} }
func (s staticTool) Definition(bool) llm.ToolDef { return llm.ToolDef{Name: s.defName} }
func (s staticTool) ExecuteUse(context.Context, llm.ToolUseBlock, toolkit.ExecDeps) llm.ToolResultBlock {
	return llm.ToolResultBlock{}
}

// emptyFiskApp is a fisk application with no commands, so LoadTools yields no
// application tools and a run's only tools are the ones the test injects.
func emptyFiskApp() *fisk.Application { return fisk.New("app", "an app") }

// fiskAppWithN is a fisk application with n bare commands, for reaching the tool-search
// threshold with a known number of application tools.
func fiskAppWithN(n int) *fisk.Application {
	app := fisk.New("app", "an app")
	for i := 0; i < n; i++ {
		app.Command(fmt.Sprintf("cmd%d", i), "a command")
	}
	return app
}

func anyDeferred(defs []llm.ToolDef) bool {
	for _, d := range defs {
		if d.DeferLoading {
			return true
		}
	}
	return false
}

// TestCustomTools_RegistrationRejections covers every way registering a custom tool is
// refused: a nil entry, an empty or mismatched name, a duplicate within the slice, a
// collision with an application or built-in tool, and a tool that claims remote
// presentation. Each aborts the run rather than silently shadowing or mis-accounting a
// tool. (A collision with a remote tool takes the same path but needs a broker, so it is
// covered by an Integration test rather than here.)
func TestCustomTools_RegistrationRejections(t *testing.T) {
	g := NewWithT(t)

	mkTool := func(name string) toolkit.Tool {
		tool, err := functool.New(functool.Spec{Name: name, Description: "a custom tool", Schema: map[string]any{"type": "object"}, Handler: noopCustomHandler})
		g.Expect(err).NotTo(HaveOccurred())
		return tool
	}

	remoteTool, err := functool.New(functool.Spec{
		Name:        "remote_thing",
		Description: "served by a peer",
		Schema:      map[string]any{"type": "object"},
		Handler:     noopCustomHandler,
		Remote:      &functool.RemoteSpec{Agent: "peer"},
	})
	g.Expect(err).NotTo(HaveOccurred())

	cases := []struct {
		name       string
		app        *fisk.Application
		withMemory bool
		custom     []toolkit.Tool
		wantErr    string
	}{
		{"nil tool", exampleApp(), false, []toolkit.Tool{nil}, "custom tool at index 0 is nil"},
		{"empty name", exampleApp(), false, []toolkit.Tool{staticTool{}}, "has an empty name"},
		{"name mismatch", exampleApp(), false, []toolkit.Tool{staticTool{name: "left", defName: "right"}}, "Name() and Definition().Name must match"},
		{"duplicate within slice", exampleApp(), false, []toolkit.Tool{mkTool("dup"), mkTool("dup")}, "duplicates an earlier custom tool"},
		{"collides with application tool", exampleApp(), false, []toolkit.Tool{mkTool("do")}, "existing application tool"},
		{"collides with built-in tool", exampleApp(), true, []toolkit.Tool{mkTool("memory_write")}, "existing built-in tool"},
		{"presents as remote", exampleApp(), false, []toolkit.Tool{remoteTool}, "presents as remote"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewWithT(t)

			app := agenttest.NewFakeApp(t, tc.app)
			var cfg *config.Config
			if tc.withMemory {
				cfg = agenttest.Config(t, app, agenttest.WithMemory())
			} else {
				cfg = agenttest.Config(t, app)
			}

			opts := agent.Options{
				Config:      cfg,
				ConfigFile:  "agent.yaml",
				Prompt:      []string{"go"},
				Provider:    agenttest.NewScriptedProvider(t),
				StoreDir:    t.TempDir(),
				CustomTools: tc.custom,
			}

			_, err := agent.Run(context.Background(), opts, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
			g.Expect(err).To(MatchError(ContainSubstring(tc.wantErr)))
		})
	}
}

// TestCustomTools_OnlyCustomToolStartsRun proves the no-tools availability gate counts
// injected tools: a run wrapping an application with no commands and no built-in or
// remote tools, but with one custom tool, starts and completes rather than aborting as
// having no tools. The custom tool is advertised to the model.
func TestCustomTools_OnlyCustomToolStartsRun(t *testing.T) {
	g := NewWithT(t)

	tool, err := functool.New(functool.Spec{Name: "only_tool", Description: "the sole tool", Schema: map[string]any{"type": "object"}, Handler: noopCustomHandler})
	g.Expect(err).NotTo(HaveOccurred())

	app := agenttest.NewFakeApp(t, emptyFiskApp())
	provider := agenttest.NewScriptedProvider(t, agenttest.TextResponse("done"))

	opts := agent.Options{
		Config:      agenttest.Config(t, app),
		ConfigFile:  "agent.yaml",
		Prompt:      []string{"go"},
		Provider:    provider,
		CustomTools: []toolkit.Tool{tool},
	}

	res, err := agent.Run(context.Background(), opts, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res.Reason).To(Equal(runstate.ReasonCompleted))

	advertised := false
	for _, td := range provider.Requests()[0].Tools {
		if td.Name == "only_tool" {
			advertised = true
		}
	}
	g.Expect(advertised).To(BeTrue())
}

// TestCustomTools_CountTowardDeferralThreshold pins the shared deferral decision: nine
// application tools sit just under the tool-search threshold, so nothing defers; adding
// one custom tool reaches the threshold and the whole set, the custom tool included, is
// offered through tool search. It guards against a custom tool being excluded from the
// count that decides deferral.
func TestCustomTools_CountTowardDeferralThreshold(t *testing.T) {
	g := NewWithT(t)

	app := agenttest.NewFakeApp(t, fiskAppWithN(9))

	provA := agenttest.NewScriptedProvider(t, agenttest.TextResponse("done"))
	_, err := agent.Run(context.Background(), agent.Options{
		Config:     agenttest.Config(t, app),
		ConfigFile: "agent.yaml",
		Prompt:     []string{"go"},
		Provider:   provA,
	}, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(anyDeferred(provA.Requests()[0].Tools)).To(BeFalse())

	tool, err := functool.New(functool.Spec{Name: "tenth_tool", Description: "the tool that tips the set over", Schema: map[string]any{"type": "object"}, Handler: noopCustomHandler})
	g.Expect(err).NotTo(HaveOccurred())

	provB := agenttest.NewScriptedProvider(t, agenttest.TextResponse("done"))
	_, err = agent.Run(context.Background(), agent.Options{
		Config:      agenttest.Config(t, app),
		ConfigFile:  "agent.yaml",
		Prompt:      []string{"go"},
		Provider:    provB,
		CustomTools: []toolkit.Tool{tool},
	}, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())

	g.Expect(anyDeferred(provB.Requests()[0].Tools)).To(BeTrue())
	for _, td := range provB.Requests()[0].Tools {
		if td.Name == "tenth_tool" {
			g.Expect(td.DeferLoading).To(BeTrue())
		}
	}
}

// TestCustomTools_ResumeRefusesWhenChanged proves a custom tool is part of the run
// fingerprint: a checkpointed run started with one custom tool refuses to resume when a
// different custom tool is injected, because the tool set changed. This is the contract
// the Options.CustomTools doc rests on (a custom tool's Definition must be deterministic
// across restarts).
func TestCustomTools_ResumeRefusesWhenChanged(t *testing.T) {
	g := NewWithT(t)
	ctx := context.Background()

	storeDir := t.TempDir()
	app := agenttest.NewFakeApp(t, exampleApp())

	toolA, err := functool.New(functool.Spec{Name: "alpha", Description: "the first tool", Schema: map[string]any{"type": "object"}, Handler: noopCustomHandler})
	g.Expect(err).NotTo(HaveOccurred())
	toolB, err := functool.New(functool.Spec{Name: "beta", Description: "a different tool", Schema: map[string]any{"type": "object"}, Handler: noopCustomHandler})
	g.Expect(err).NotTo(HaveOccurred())

	// Run 1: one application tool call, then suspend at the next boundary, with toolA
	// injected.
	suspendPolls := 0
	opts1 := agent.Options{
		Config:           agenttest.Config(t, app),
		ConfigFile:       "agent.yaml",
		Prompt:           []string{"start"},
		Provider:         agenttest.NewScriptedProvider(t, agenttest.ToolUseResponse("c1", "do", json.RawMessage(`{"subject":"x"}`))),
		StoreDir:         storeDir,
		Checkpoint:       agent.Checkpoint{Enabled: true},
		SuspendRequested: func() bool { suspendPolls++; return suspendPolls > 1 },
		CustomTools:      []toolkit.Tool{toolA},
	}
	res1, err := agent.Run(ctx, opts1, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(res1.Reason).To(Equal(runstate.ReasonSuspended))

	// Run 2: resume the saved session with a different custom tool set, so the tool-set
	// fingerprint no longer matches and the resume is refused.
	opts2 := agent.Options{
		Config:      agenttest.Config(t, app),
		ConfigFile:  "agent.yaml",
		Provider:    agenttest.NewScriptedProvider(t, agenttest.TextResponse("finished")),
		StoreDir:    storeDir,
		Checkpoint:  agent.Checkpoint{ResumeID: res1.SessionID},
		CustomTools: []toolkit.Tool{toolB},
	}
	_, err = agent.Run(ctx, opts2, agenttest.NewRecordingEvents(), agenttest.NewScriptedPrompter(t))
	g.Expect(err).To(MatchError(ContainSubstring("the configuration changed")))
	g.Expect(err).To(MatchError(ContainSubstring("tool set: changed")))
}
