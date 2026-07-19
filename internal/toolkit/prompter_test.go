//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	"bytes"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("surveyPrompter", func() {
	// The survey-backed methods need a real terminal, so only the rendering helpers
	// (which write to an io.Writer and never call survey) are exercised here; the
	// confirm gate and handler policy are covered against a scripted Prompter in
	// their own packages' tests.
	Describe("printGateHeader", func() {
		It("Should name the command and the triggering tag and show the command line", func() {
			var buf bytes.Buffer
			printGateHeader(&buf, GateRequest{Command: "stream rm", Display: "stream rm ORDERS", Tag: "impact:rw"})

			out := buf.String()
			Expect(out).To(ContainSubstring(`confirmation required: "stream rm" carries tag "impact:rw"`))
			Expect(out).To(ContainSubstring("-> stream rm ORDERS"))
			// A blank separator line precedes the header so it stands apart from the
			// model's narration.
			Expect(out).To(HavePrefix("\n"))
		})
	})

	Describe("printAlwaysNote", func() {
		It("Should confirm the tool will not be asked about again this session", func() {
			var buf bytes.Buffer
			printAlwaysNote(&buf, "stream rm")
			Expect(buf.String()).To(Equal("confirmation: will not ask again for \"stream rm\" this session\n"))
		})
	})
})
