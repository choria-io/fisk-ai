//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("annotateOptional", func() {
	It("Should annotate only optional properties, leaving required ones untouched", func() {
		props := map[string]any{
			"target": map[string]any{"type": "string", "description": "where to deploy"},
			"force":  map[string]any{"type": "boolean", "description": "force the deploy"},
		}

		got := annotateOptional(props, []string{"target"}).(map[string]any)
		Expect(got["target"].(map[string]any)["description"]).To(Equal("where to deploy"))
		Expect(got["force"].(map[string]any)["description"]).To(Equal("force the deploy (optional)"))
	})

	It("Should label an optional property that has no description", func() {
		props := map[string]any{"dir": map[string]any{"type": "string"}}

		got := annotateOptional(props, nil).(map[string]any)
		Expect(got["dir"].(map[string]any)["description"]).To(Equal("Optional."))
	})

	It("Should not mutate the input, so repeated calls stay idempotent", func() {
		original := map[string]any{"dir": map[string]any{"type": "string", "description": "Directory to test"}}

		first := annotateOptional(original, nil).(map[string]any)
		Expect(first["dir"].(map[string]any)["description"]).To(Equal("Directory to test (optional)"))

		// The shared input is unchanged, so a second pass yields the same result
		// rather than stacking another "(optional)".
		Expect(original["dir"].(map[string]any)["description"]).To(Equal("Directory to test"))
		second := annotateOptional(original, nil).(map[string]any)
		Expect(second["dir"].(map[string]any)["description"]).To(Equal("Directory to test (optional)"))
	})
})
