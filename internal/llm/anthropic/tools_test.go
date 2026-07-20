//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
)

var _ = Describe("ToolDefToAnthropic", func() {
	// marshal renders a rendered tool to the JSON the SDK would send, so assertions
	// check what the model actually receives; the Opt fields are only observable after
	// marshaling.
	marshal := func(td llm.ToolDef) map[string]any {
		GinkgoHelper()
		data, err := json.Marshal(ToolDefToAnthropic(td))
		Expect(err).NotTo(HaveOccurred())
		out := map[string]any{}
		Expect(json.Unmarshal(data, &out)).To(Succeed())
		return out
	}

	It("renders a custom tool carrying name, description and defer_loading", func() {
		got := marshal(llm.ToolDef{Name: "deploy", Description: "deploy things", DeferLoading: true})
		Expect(got["type"]).To(Equal("custom"))
		Expect(got["name"]).To(Equal("deploy"))
		Expect(got["description"]).To(Equal("deploy things"))
		Expect(got["defer_loading"]).To(BeTrue())
		// Strict mode is not used: its grammar compilation caps total optional
		// parameters across all tools, which a broad command tree exceeds.
		Expect(got).NotTo(HaveKey("strict"))
	})

	It("emits defer_loading as a present field even when false", func() {
		// The field is rendered unconditionally so the wire form stays a pure function
		// of the neutral value. A present false and an absent field request the same
		// thing, since false is the API default; this pins which one is sent.
		got := marshal(llm.ToolDef{Name: "deploy", DeferLoading: false})
		Expect(got).To(HaveKey("defer_loading"))
		Expect(got["defer_loading"]).To(BeFalse())
	})

	It("annotates optional parameters in the rendered schema", func() {
		got := marshal(llm.ToolDef{
			Name: "deploy",
			InputSchema: map[string]any{
				"type":     "object",
				"required": []any{"target"},
				"properties": map[string]any{
					"target": map[string]any{"type": "string", "description": "where to deploy"},
					"force":  map[string]any{"type": "string", "description": "force the deploy"},
				},
			},
		})

		schema, ok := got["input_schema"].(map[string]any)
		Expect(ok).To(BeTrue())
		props, ok := schema["properties"].(map[string]any)
		Expect(ok).To(BeTrue())

		target, ok := props["target"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(target["description"]).To(Equal("where to deploy"))

		// The optional parameter (absent from required) is annotated so the model does
		// not mistake it for mandatory; the required one is left as written.
		force, ok := props["force"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(force["description"]).To(Equal("force the deploy (optional)"))
	})
})
