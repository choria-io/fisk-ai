//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package functool

import (
	"context"
	"encoding/json"
	"errors"
	"math"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// objectSchema builds a minimal object schema with the named required properties,
// using the []any form a schema decoded from JSON carries.
func objectSchema(required ...string) map[string]any {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":  map[string]any{"type": "string"},
			"force": map[string]any{"type": "boolean"},
		},
	}
	if len(required) > 0 {
		req := make([]any, len(required))
		for i, r := range required {
			req[i] = r
		}
		schema["required"] = req
	}

	return schema
}

// okHandler is a handler that echoes a fixed result.
func okHandler(context.Context, json.RawMessage, *CallContext) (string, error) {
	return "ok", nil
}

// mustNew builds a tool from spec and fails the spec if construction errors.
func mustNew(spec Spec) *Tool {
	GinkgoHelper()
	tool, err := New(spec)
	Expect(err).ToNot(HaveOccurred())

	return tool
}

var _ = Describe("New", func() {
	base := func() Spec {
		return Spec{Name: "do_thing", Description: "does a thing", Schema: objectSchema(), Handler: okHandler}
	}

	It("Should build a tool from a complete spec", func() {
		tool := mustNew(base())
		Expect(tool.Name()).To(Equal("do_thing"))
		Expect(tool.Description()).To(Equal("does a thing"))
		Expect(tool.InputSchema()).To(HaveKeyWithValue("type", "object"))
	})

	It("Should reject a spec missing a mandatory Spec field (Name, Description, Schema or Handler)", func() {
		spec := base()
		spec.Name = ""
		_, err := New(spec)
		Expect(err).To(MatchError(ContainSubstring("tool name is required")))

		spec = base()
		spec.Description = ""
		_, err = New(spec)
		Expect(err).To(MatchError(ContainSubstring("description is required")))

		spec = base()
		spec.Schema = nil
		_, err = New(spec)
		Expect(err).To(MatchError(ContainSubstring("schema is required")))

		spec = base()
		spec.Handler = nil
		_, err = New(spec)
		Expect(err).To(MatchError(ContainSubstring("handler is required")))
	})

	It("Should accept a tool whose parameters are all optional, and one with no parameters", func() {
		spec := base()
		spec.Schema = objectSchema() // properties present, no "required" list
		_, err := New(spec)
		Expect(err).ToNot(HaveOccurred())

		spec = base()
		spec.Schema = map[string]any{"type": "object", "properties": map[string]any{}}
		_, err = New(spec)
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should reject ValidateRequired when the schema declares no required parameters", func() {
		spec := base()
		spec.ValidateRequired = true
		_, err := New(spec)
		Expect(err).To(MatchError(ContainSubstring("no required parameters")))
	})

	It("Should accept ValidateRequired when the schema declares required parameters", func() {
		spec := base()
		spec.Schema = objectSchema("path")
		spec.ValidateRequired = true
		_, err := New(spec)
		Expect(err).ToNot(HaveOccurred())
	})

	It("Should reject a remote tool that is also confirm-gated", func() {
		spec := base()
		spec.Remote = &RemoteSpec{Agent: "billing"}
		spec.Confirm = &ConfirmSpec{}
		_, err := New(spec)
		Expect(err).To(MatchError(ContainSubstring("remote and cannot be confirm-gated")))
	})
})

var _ = Describe("Definition", func() {
	It("Should honor deferral when asked and no opt-out is set", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler})
		Expect(tool.Definition(false).DeferLoading).To(BeFalse())
		Expect(tool.Definition(true).DeferLoading).To(BeTrue())
	})

	It("Should never defer a NoDefer tool even when asked", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, NoDefer: true})
		Expect(tool.Definition(true).DeferLoading).To(BeFalse())
	})

	It("Should carry the name, description and schema", func() {
		schema := objectSchema()
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: schema, Handler: okHandler})
		def := tool.Definition(false)
		Expect(def.Name).To(Equal("n"))
		Expect(def.Description).To(Equal("d"))
		Expect(def.InputSchema).To(Equal(schema))
	})
})

var _ = Describe("ExecuteUse", func() {
	use := func(input string) llm.ToolUseBlock {
		return llm.ToolUseBlock{ID: "u1", Name: "n", Input: json.RawMessage(input)}
	}

	It("Should return a normal result carrying the handler output", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler})
		res := tool.ExecuteUse(context.Background(), use("{}"), toolkit.ExecDeps{})
		Expect(res.ToolUseID).To(Equal("u1"))
		Expect(res.Content).To(Equal("ok"))
		Expect(res.IsError).To(BeFalse())
	})

	It("Should return an error result carrying the handler error", func() {
		handler := func(context.Context, json.RawMessage, *CallContext) (string, error) {
			return "", errors.New("boom")
		}
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: handler})
		res := tool.ExecuteUse(context.Background(), use("{}"), toolkit.ExecDeps{})
		Expect(res.Content).To(Equal("boom"))
		Expect(res.IsError).To(BeTrue())
	})

	It("Should hand the handler the per-run working directory from ExecDeps", func() {
		handler := func(_ context.Context, _ json.RawMessage, tc *CallContext) (string, error) {
			return tc.WorkDir(), nil
		}
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: handler})
		res := tool.ExecuteUse(context.Background(), use("{}"), toolkit.ExecDeps{WorkDir: "/run/42"})
		Expect(res.Content).To(Equal("/run/42"))
	})

	It("Should give the handler a fail-closed prompter when none is wired", func() {
		handler := func(_ context.Context, _ json.RawMessage, tc *CallContext) (string, error) {
			if tc.Prompter().CanPrompt() {
				return "reachable", nil
			}

			return "no-operator", nil
		}
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: handler})
		res := tool.ExecuteUse(context.Background(), use("{}"), toolkit.ExecDeps{})
		Expect(res.Content).To(Equal("no-operator"))
	})
})

var _ = Describe("Call", func() {
	It("Should run the handler directly and return its string", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler})
		out, err := tool.Call(context.Background(), json.RawMessage("{}"), toolkit.DefaultDenyPrompter())
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal("ok"))
	})

	It("Should pass the caller's prompter through to the handler", func() {
		handler := func(_ context.Context, _ json.RawMessage, tc *CallContext) (string, error) {
			_, err := tc.Prompter().Confirm(context.Background(), "ok?")

			return "", err
		}
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: handler})
		_, err := tool.Call(context.Background(), json.RawMessage("{}"), toolkit.DefaultDenyPrompter())
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Describe", func() {
	input := json.RawMessage(`{"path":"x"}`)

	It("Should present a remote tool as remote, naming its agent, with no dependencies", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Remote: &RemoteSpec{Agent: "billing"}})
		info := tool.Describe(input)
		Expect(info.Present).To(Equal(toolkit.PresentRemote))
		Expect(info.Agent).To(Equal("billing"))
		Expect(info.NeedsPrompter).To(BeFalse())
		Expect(info.NeedsWorkDir).To(BeFalse())
		Expect(info.Display).To(Equal(""))
	})

	It("Should present a remote tool as remote even when its agent name is empty", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Remote: &RemoteSpec{}})
		info := tool.Describe(input)
		Expect(info.Present).To(Equal(toolkit.PresentRemote))
		Expect(info.Agent).To(Equal(""))
	})

	It("Should present a traced tool like a command and request the dependencies", func() {
		trace := func(json.RawMessage) string { return "wrote key foo" }
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Trace: trace})
		info := tool.Describe(input)
		Expect(info.Present).To(Equal(toolkit.PresentTraced))
		Expect(info.Display).To(Equal("wrote key foo"))
		Expect(info.NeedsPrompter).To(BeTrue())
		Expect(info.NeedsWorkDir).To(BeTrue())
	})

	It("Should present an untraced in-process tool as self-rendered with no display", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler})
		info := tool.Describe(input)
		Expect(info.Present).To(Equal(toolkit.PresentSelfRendered))
		Expect(info.Display).To(Equal(""))
		Expect(info.NeedsPrompter).To(BeTrue())
		Expect(info.NeedsWorkDir).To(BeTrue())
	})

	It("Should sanitize a handler-supplied trace before displaying it", func() {
		trace := func(json.RawMessage) string { return "\x1b[2Jdanger\nzone" }
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Trace: trace})
		Expect(tool.Describe(input).Display).To(Equal("danger zone"))
	})
})

var _ = Describe("Confirmable", func() {
	summary := func(json.RawMessage) string { return "delete /var/x" }

	It("Should never gate a tool with no ConfirmSpec", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler})
		Expect(tool.NeedsConfirm(nil)).To(BeFalse())
		Expect(tool.NeedsConfirm([]string{"impact:rw"})).To(BeFalse())
		Expect(tool.ConfirmTrigger([]string{"impact:rw"})).To(Equal(""))
	})

	It("Should always gate a confirm-gated tool by the always-on ai:confirm", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Confirm: &ConfirmSpec{}})
		Expect(tool.NeedsConfirm(nil)).To(BeTrue())
		Expect(tool.ConfirmTrigger(nil)).To(Equal(toolkit.ConfirmTag))
	})

	It("Should also gate on an operator's extra tag matching the spec's tags", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Confirm: &ConfirmSpec{Tags: []string{"impact:rw"}}})
		// ai:confirm always wins the trigger, but the extra tag is part of the set.
		Expect(tool.NeedsConfirm([]string{"impact:rw"})).To(BeTrue())
		Expect(tool.ConfirmTrigger([]string{"impact:rw"})).To(Equal(toolkit.ConfirmTag))
	})

	It("Should render the sanitized confirm summary as its trace line", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Confirm: &ConfirmSpec{Summary: summary}})
		Expect(tool.TraceLine(nil)).To(Equal("delete /var/x"))
	})

	It("Should fall back to no trace line when a confirm-gated tool has no summary", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Confirm: &ConfirmSpec{}})
		Expect(tool.TraceLine(nil)).To(Equal(""))
	})

	It("Should report its name as the command shown in the approval prompt", func() {
		tool := mustNew(Spec{Name: "delete_thing", Description: "d", Schema: objectSchema(), Handler: okHandler, Confirm: &ConfirmSpec{}})
		Expect(tool.Command()).To(Equal("delete_thing"))
	})
})

var _ = Describe("TraceLine", func() {
	It("Should equal the Describe display for a traced tool", func() {
		trace := func(json.RawMessage) string { return "read key foo" }
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler, Trace: trace})
		Expect(tool.TraceLine(nil)).To(Equal(tool.Describe(nil).Display))
	})

	It("Should be empty for a tool that is neither traced nor confirm-gated", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema(), Handler: okHandler})
		Expect(tool.TraceLine(nil)).To(Equal(""))
	})
})

var _ = Describe("ArgumentValidator", func() {
	newValidated := func() *Tool {
		return mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema("path"), Handler: okHandler, ValidateRequired: true})
	}

	It("Should report nothing when the tool does not validate", func() {
		tool := mustNew(Spec{Name: "n", Description: "d", Schema: objectSchema("path"), Handler: okHandler})
		Expect(tool.MissingRequired(json.RawMessage("{}"))).To(BeNil())
	})

	It("Should report every required parameter for an empty or null input", func() {
		tool := newValidated()
		Expect(tool.MissingRequired(json.RawMessage("{}"))).To(Equal([]string{"path"}))
		Expect(tool.MissingRequired(nil)).To(Equal([]string{"path"}))
		Expect(tool.MissingRequired(json.RawMessage("null"))).To(Equal([]string{"path"}))
	})

	It("Should treat a present key as supplied regardless of its value", func() {
		tool := newValidated()
		Expect(tool.MissingRequired(json.RawMessage(`{"path":null}`))).To(BeNil())
		Expect(tool.MissingRequired(json.RawMessage(`{"path":""}`))).To(BeNil())
	})

	It("Should report nothing for a non-object input, leaving it for the handler to reject", func() {
		tool := newValidated()
		Expect(tool.MissingRequired(json.RawMessage(`[1,2]`))).To(BeNil())
	})

	It("Should name the missing parameters and the roster in the message", func() {
		tool := newValidated()
		msg := tool.MissingRequiredMessage([]string{"path"})
		Expect(msg).To(ContainSubstring(`tool "n" was called without required parameter(s): path`))
		Expect(msg).To(ContainSubstring("required: path"))
		Expect(msg).To(ContainSubstring("optional: force"))
	})
})

var _ = Describe("Result", func() {
	It("Should marshal a value to its JSON string", func() {
		out, err := Result(map[string]bool{"ok": true})
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(Equal(`{"ok":true}`))
	})

	It("Should return an error for a value that cannot be marshaled", func() {
		_, err := Result(math.Inf(1))
		Expect(err).To(MatchError(ContainSubstring("marshaling tool result")))
	})
})

var _ = Describe("Prompter fail-closed", func() {
	// promptingTool consults the operator through the CallContext prompter and surfaces
	// whatever it returns, so a spec can observe the fail-closed path when no operator
	// is reachable. This is the guarantee a custom tool that elicits relies on: with no
	// live operator the call is denied, never left to prompt into the void or panic.
	promptingTool := func() *Tool {
		return mustNew(Spec{
			Name:        "escalate",
			Description: "asks the operator",
			Schema:      objectSchema(),
			Handler: func(ctx context.Context, _ json.RawMessage, tc *CallContext) (string, error) {
				ok, err := tc.Prompter().Confirm(ctx, "escalate?")
				if err != nil {
					return "", err
				}
				return Result(map[string]any{"escalated": ok})
			},
		})
	}

	It("Should deny, not panic, when dispatched with the default-deny prompter", func() {
		_, err := promptingTool().Call(context.Background(), json.RawMessage(`{}`), toolkit.DefaultDenyPrompter())
		Expect(err).To(MatchError(ContainSubstring("no operator is available")))
	})

	It("Should default a nil prompter to deny rather than dereferencing it", func() {
		_, err := promptingTool().Call(context.Background(), json.RawMessage(`{}`), nil)
		Expect(err).To(MatchError(ContainSubstring("no operator is available")))
	})
})
