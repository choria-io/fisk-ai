//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/choria-io/fisk"
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
		savedStdinIsTerminal = stdinIsTerminal
		stdinIsTerminal = func() bool { return true }
		prompter = &fakePrompter{}
	})
	AfterEach(func() {
		stdinIsTerminal = savedStdinIsTerminal
	})

	// newGate builds a gate wired to the spec's fakePrompter.
	newGate := func() *ConfirmGate {
		return NewConfirmGate(prompter)
	}

	Describe("NeedsConfirm", func() {
		It("Should report the always-on ai:confirm tag regardless of configured tags", func() {
			confirm := &Tool{Path: []string{"stream", "rm"}, Model: &fisk.CmdModel{Tags: []string{confirmTag}}}
			plain := &Tool{Path: []string{"stream", "info"}, Model: &fisk.CmdModel{}}

			Expect(confirm.NeedsConfirm(nil)).To(BeTrue())
			Expect(plain.NeedsConfirm(nil)).To(BeFalse())
		})

		It("Should report a tool carrying a configured extra confirm tag", func() {
			tool := &Tool{Path: []string{"stream", "rm"}, Model: &fisk.CmdModel{Tags: []string{"impact:rw"}}}

			Expect(tool.NeedsConfirm([]string{"impact:rw"})).To(BeTrue())
			Expect(tool.NeedsConfirm([]string{"impact:ro"})).To(BeFalse())
			Expect(tool.NeedsConfirm(nil)).To(BeFalse())
		})
	})

	Describe("ConfirmTrigger", func() {
		It("Should prefer the always-on ai:confirm tag when several match", func() {
			tool := &Tool{Path: []string{"stream", "rm"}, Model: &fisk.CmdModel{Tags: []string{"impact:rw", confirmTag}}}
			Expect(tool.ConfirmTrigger([]string{"impact:rw"})).To(Equal(confirmTag))
		})

		It("Should name the first matching configured tag in command tag order", func() {
			tool := &Tool{Path: []string{"stream", "rm"}, Model: &fisk.CmdModel{Tags: []string{"impact:rw", "admin"}}}
			Expect(tool.ConfirmTrigger([]string{"admin", "impact:rw"})).To(Equal("impact:rw"))
		})

		It("Should be empty for an ungated command", func() {
			tool := &Tool{Path: []string{"stream", "info"}, Model: &fisk.CmdModel{Tags: []string{"impact:ro"}}}
			Expect(tool.ConfirmTrigger([]string{"impact:rw"})).To(BeEmpty())
		})
	})

	Describe("Approve", func() {
		It("Should allow once without remembering the tool", func() {
			calls := 0
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) {
				calls++
				return ConfirmOnce, nil
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
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) {
				calls++
				return ConfirmAlways, nil
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
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) { return ConfirmOnce, nil }
			gate := newGate()

			allowed, _ := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "impact:rw")
			Expect(allowed).To(BeTrue())
			Expect(prompter.lastGateReq).To(Equal(GateRequest{Command: "stream rm", Display: "stream rm ORDERS", Tag: "impact:rw"}))
		})

		It("Should decline when the operator says no, and re-prompt next time", func() {
			calls := 0
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) {
				calls++
				return ConfirmNo, nil
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
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) { return ConfirmNo, errors.New("interrupt") }
			gate := newGate()

			allowed, reason := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeFalse())
			Expect(reason).To(ContainSubstring("interrupt"))
		})

		It("Should deny with the no-terminal reason when no terminal is attached", func() {
			stdinIsTerminal = func() bool { return false }
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) {
				Fail("must not prompt without a terminal")
				return ConfirmNo, nil
			}
			gate := newGate()

			allowed, reason := gate.Approve(context.Background(), "stream_rm", "stream rm", "stream rm ORDERS", "ai:confirm")
			Expect(allowed).To(BeFalse())
			Expect(reason).To(Equal(noTerminalReason))
		})

		It("Should deny without prompting when the run was already canceled", func() {
			prompter.approveFn = func(GateRequest) (ConfirmChoice, error) {
				Fail("must not prompt once the run is canceled")
				return ConfirmNo, nil
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
			Expect(block.OfToolResult).NotTo(BeNil())
			Expect(block.OfToolResult.IsError.Value).To(BeFalse())

			var outcome confirmDeniedOutcome
			Expect(json.Unmarshal([]byte(block.OfToolResult.Content[0].OfText.Text), &outcome)).To(Succeed())
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
