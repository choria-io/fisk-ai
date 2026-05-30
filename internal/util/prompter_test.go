//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"bytes"
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakePrompter is a scripted Prompter for tests: each interactive method delegates
// to a closure the test installs, so a spec drives the confirm gate and the
// human-in-the-loop handlers without a terminal. A method whose closure is unset
// fails the spec, since reaching it means the caller prompted when it should have
// resolved the outcome itself (for example when no terminal is attached).
type fakePrompter struct {
	approveFn   func(GateRequest) (ConfirmChoice, error)
	confirmFn   func(string) (bool, error)
	selectFn    func(string, []string) (int, error)
	inputFn     func(string, string) (string, error)
	lastGateReq GateRequest
}

func (f *fakePrompter) ApproveCommand(_ context.Context, req GateRequest) (ConfirmChoice, error) {
	f.lastGateReq = req
	if f.approveFn == nil {
		Fail("ApproveCommand called but no approveFn was set")
	}
	return f.approveFn(req)
}

func (f *fakePrompter) Confirm(_ context.Context, question string) (bool, error) {
	if f.confirmFn == nil {
		Fail("Confirm called but no confirmFn was set")
	}
	return f.confirmFn(question)
}

func (f *fakePrompter) Select(_ context.Context, question string, options []string) (int, error) {
	if f.selectFn == nil {
		Fail("Select called but no selectFn was set")
	}
	return f.selectFn(question, options)
}

func (f *fakePrompter) Input(_ context.Context, question, def string) (string, error) {
	if f.inputFn == nil {
		Fail("Input called but no inputFn was set")
	}
	return f.inputFn(question, def)
}

var _ = Describe("surveyPrompter", func() {
	// The survey-backed methods need a real terminal, so only the rendering helpers
	// (which write to an io.Writer and never call survey) are exercised here; the
	// gate and handler policy is covered against the fakePrompter.
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
