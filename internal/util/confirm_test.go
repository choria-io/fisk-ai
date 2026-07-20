//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ConfirmGate", func() {
	// The terminal check is stubbed per spec, since the test runner has no terminal;
	// the original is restored afterwards. The approval prompt is driven through a
	// fakePrompter installed per spec.
	var savedStdinIsTerminal func() bool
	var prompter *fakePrompter
	BeforeEach(func() {
		savedStdinIsTerminal = StdinIsTerminal
		StdinIsTerminal = func() bool { return true }
		prompter = &fakePrompter{}
	})
	AfterEach(func() {
		StdinIsTerminal = savedStdinIsTerminal
	})

	// newGate builds a gate wired to the spec's fakePrompter.
	newGate := func() *ConfirmGate {
		return NewConfirmGate(prompter)
	}

	Describe("Approve", func() {
		It("Should allow once without remembering the tool", func() {
			calls := 0
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
				calls++
				return toolkit.ConfirmOnce, nil
			}
			gate := newGate()

			allowed, reason := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeTrue())
			Expect(reason).To(BeEmpty())

			// A second call prompts again because "once" is not remembered.
			allowed, _ = gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm BILLING", "ai:confirm")
			Expect(allowed).To(BeTrue())
			Expect(calls).To(Equal(2))
		})

		It("Should remember an always answer by tool name for any arguments", func() {
			calls := 0
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
				calls++
				return toolkit.ConfirmAlways, nil
			}
			gate := newGate()

			allowed, _ := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeTrue())

			// A later call with different arguments is allowed without re-prompting; the
			// gate emits no trace of its own, the caller renders the command's line.
			allowed, _ = gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm EVERYTHING", "ai:confirm")
			Expect(allowed).To(BeTrue())
			Expect(calls).To(Equal(1))

			// A different tool is still asked about.
			allowed, _ = gate.Approve(context.Background(), "server_run", "server run", "server run", "ai:confirm")
			Expect(allowed).To(BeTrue())
			Expect(calls).To(Equal(2))
		})

		It("Should pass the triggering tag and command line to the prompter", func() {
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) { return toolkit.ConfirmOnce, nil }
			gate := newGate()

			allowed, _ := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "impact:rw")
			Expect(allowed).To(BeTrue())
			Expect(prompter.lastGateReq).To(Equal(toolkit.GateRequest{Command: "stream rm", Display: "stream rm ORDERS", Tag: "impact:rw"}))
		})

		It("Should decline when the operator says no, and re-prompt next time", func() {
			calls := 0
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
				calls++
				return toolkit.ConfirmNo, nil
			}
			gate := newGate()

			allowed, reason := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeFalse())
			Expect(reason).To(ContainSubstring("declined"))
			Expect(reason).To(ContainSubstring("do not retry"))

			// No is not sticky: the same command is asked about again.
			gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(calls).To(Equal(2))
		})

		It("Should deny by default when the prompt errors (interrupt, EOF)", func() {
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
				return toolkit.ConfirmNo, errors.New("interrupt")
			}
			gate := newGate()

			allowed, reason := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeFalse())
			Expect(reason).To(ContainSubstring("interrupt"))
		})

		It("Should deny with the no-terminal reason when no terminal is attached", func() {
			StdinIsTerminal = func() bool { return false }
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
				Fail("must not prompt without a terminal")
				return toolkit.ConfirmNo, nil
			}
			gate := newGate()

			allowed, reason := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeFalse())
			Expect(reason).To(Equal(NoTerminalReason))
		})

		It("Should deny without prompting when the run was already canceled", func() {
			prompter.approveFn = func(toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
				Fail("must not prompt once the run is canceled")
				return toolkit.ConfirmNo, nil
			}
			gate := newGate()

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			allowed, reason := gate.Approve(ctx, "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeFalse())
			Expect(reason).To(ContainSubstring("before the operator could approve"))
			Expect(reason).To(ContainSubstring("do not retry"))
		})
	})

	Describe("ConfirmDeniedResult", func() {
		It("Should be a non-error tool_result carrying the reason", func() {
			block := ConfirmDeniedResult("tool-1", "the operator declined")
			Expect(block.ToolUseID).To(Equal("tool-1"))
			Expect(block.IsError).To(BeFalse())

			var outcome confirmDeniedOutcome
			Expect(json.Unmarshal([]byte(block.Content), &outcome)).To(Succeed())
			Expect(outcome.Allowed).To(BeFalse())
			Expect(outcome.Reason).To(Equal("the operator declined"))
		})
	})

	Describe("SanitizeCommandLine", func() {
		It("Should strip terminal escape sequences from model-supplied argument values", func() {
			Expect(SanitizeCommandLine("stream rm \x1b[31mORDERS\x1b[0m")).To(Equal("stream rm ORDERS"))
		})
	})
})
