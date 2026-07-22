//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/util"
)

var _ = Describe("Built-in tools", func() {
	// Whether an operator is reachable is now the prompter's own report, so a spec
	// drives it through the fakePrompter (interactive by default) rather than stubbing a
	// process-global terminal check.
	var prompter *fakePrompter
	BeforeEach(func() {
		prompter = &fakePrompter{canPrompt: true}
	})

	// confirmResult runs the ask_human_confirm handler through the spec's prompter
	// and returns the decoded outcome and any handler error.
	confirmResult := func(input string) (confirmOutcome, error) {
		GinkgoHelper()

		out, err := askHumanConfirm(context.Background(), json.RawMessage(input), prompter)
		if err != nil {
			return confirmOutcome{}, err
		}

		var outcome confirmOutcome
		Expect(json.Unmarshal([]byte(out), &outcome)).To(Succeed())
		return outcome, nil
	}

	Describe("HITLTools", func() {
		It("Should offer the ask_human tools only when enabled", func() {
			off := &config.Config{}
			Expect(HITLTools(off)).To(BeEmpty())

			on := &config.Config{Harness: config.HarnessConfig{HumanInTheLoop: &config.HumanInTheLoopConfig{Enabled: true}}}
			var names []string
			for _, t := range HITLTools(on) {
				names = append(names, t.Name())
			}
			Expect(names).To(ConsistOf("ask_human_confirm", "ask_human_select", "ask_human_input"))
		})

		It("Should never defer the built-in tools", func() {
			for _, tool := range []*BuiltinTool{askHumanConfirmTool(), askHumanSelectTool(), askHumanInputTool()} {
				Expect(tool.Definition(false).DeferLoading).To(BeFalse())
			}
		})
	})

	Describe("HITLSystemNote", func() {
		It("Should be empty when there are no built-in tools", func() {
			Expect(HITLSystemNote(nil)).To(BeEmpty())
		})

		It("Should explain the tools are the only way to reach the operator and name them", func() {
			on := &config.Config{Harness: config.HarnessConfig{HumanInTheLoop: &config.HumanInTheLoopConfig{Enabled: true}}}
			note := HITLSystemNote(HITLTools(on))

			Expect(note).To(ContainSubstring("non-interactive"))
			Expect(note).To(ContainSubstring("only way"))
			Expect(note).To(ContainSubstring("ask_human_confirm"))
			Expect(note).To(ContainSubstring("ask_human_select"))
			Expect(note).To(ContainSubstring("ask_human_input"))
		})
	})

	Describe("ask_human_confirm handler", func() {
		It("Should confirm only when the operator answers yes", func() {
			prompter.confirmFn = func(string) (bool, error) { return true, nil }

			outcome, err := confirmResult(`{"question":"Proceed?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Confirmed).To(BeTrue())
			Expect(outcome.Reason).To(BeEmpty())
		})

		It("Should report a plain no as confirmed false without a reason", func() {
			prompter.confirmFn = func(string) (bool, error) { return false, nil }

			outcome, err := confirmResult(`{"question":"Proceed?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Confirmed).To(BeFalse())
			Expect(outcome.Reason).To(BeEmpty())
		})

		It("Should deny by default when the prompt errors (interrupt, EOF)", func() {
			prompter.confirmFn = func(string) (bool, error) { return false, errors.New("interrupt") }

			outcome, err := confirmResult(`{"question":"Proceed?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Confirmed).To(BeFalse())
			Expect(outcome.Reason).To(ContainSubstring("did not confirm"))
		})

		It("Should deny without asking when no interactive terminal is attached", func() {
			prompter.canPrompt = false
			prompter.confirmFn = func(string) (bool, error) {
				Fail("the prompter must not be called without a terminal")
				return false, nil
			}

			outcome, err := confirmResult(`{"question":"Proceed?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Confirmed).To(BeFalse())
			Expect(outcome.Reason).To(ContainSubstring("no interactive terminal"))
		})

		It("Should deny without asking when the run was already canceled", func() {
			prompter.confirmFn = func(string) (bool, error) {
				Fail("the prompter must not be called once the run is canceled")
				return false, nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			out, err := askHumanConfirm(ctx, json.RawMessage(`{"question":"Proceed?"}`), prompter)
			Expect(err).NotTo(HaveOccurred())
			var outcome confirmOutcome
			Expect(json.Unmarshal([]byte(out), &outcome)).To(Succeed())
			Expect(outcome.Confirmed).To(BeFalse())
			Expect(outcome.Reason).To(ContainSubstring("before the operator could answer"))
		})

		It("Should error on a missing or empty question", func() {
			_, err := askHumanConfirm(context.Background(), json.RawMessage(`{"question":"   "}`), prompter)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("non-empty question"))

			_, err = askHumanConfirm(context.Background(), json.RawMessage(`{}`), prompter)
			Expect(err).To(HaveOccurred())
		})

		It("Should error on malformed input", func() {
			_, err := askHumanConfirm(context.Background(), json.RawMessage(`{"question":`), prompter)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid ask_human_confirm input"))
		})
	})

	Describe("ask_human_select handler", func() {
		selectResult := func(input string) (selectOutcome, error) {
			GinkgoHelper()
			out, err := askHumanSelect(context.Background(), json.RawMessage(input), prompter)
			if err != nil {
				return selectOutcome{}, err
			}
			var outcome selectOutcome
			Expect(json.Unmarshal([]byte(out), &outcome)).To(Succeed())
			return outcome, nil
		}

		It("Should return the chosen option", func() {
			prompter.selectFn = func(string, []string) (int, error) { return 1, nil }

			outcome, err := selectResult(`{"question":"Pick","options":["a","b","c"]}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Selected).NotTo(BeNil())
			Expect(*outcome.Selected).To(Equal("b"))
		})

		It("Should make no choice when the operator cancels", func() {
			prompter.selectFn = func(string, []string) (int, error) { return -1, errors.New("interrupt") }

			outcome, err := selectResult(`{"question":"Pick","options":["a","b"]}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Selected).To(BeNil())
			Expect(outcome.Reason).To(ContainSubstring("did not choose"))
		})

		It("Should never auto-pick when no terminal is attached", func() {
			prompter.canPrompt = false
			prompter.selectFn = func(string, []string) (int, error) {
				Fail("the prompter must not be called without a terminal")
				return 0, nil
			}

			outcome, err := selectResult(`{"question":"Pick","options":["a","b"]}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Selected).To(BeNil())
			Expect(outcome.Reason).To(ContainSubstring("no interactive terminal"))
		})

		It("Should sanitize the option labels shown to the operator", func() {
			var got []string
			prompter.selectFn = func(_ string, options []string) (int, error) {
				got = options
				return 0, nil
			}

			outcome, err := selectResult(`{"question":"Pick","options":["\u001b[31mred\u001b[0m","b"]}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal([]string{"red", "b"}))
			Expect(*outcome.Selected).To(Equal("red"))
		})

		It("Should require at least one option", func() {
			_, err := askHumanSelect(context.Background(), json.RawMessage(`{"question":"Pick","options":[]}`), prompter)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at least one option"))
		})

		It("Should reject more options than the cap", func() {
			opts := make([]string, maxSelectOptions+1)
			for i := range opts {
				opts[i] = "x"
			}
			payload, _ := json.Marshal(map[string]any{"question": "Pick", "options": opts})

			_, err := askHumanSelect(context.Background(), payload, prompter)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("at most"))
		})
	})

	Describe("ask_human_input handler", func() {
		inputResult := func(input string) (inputOutcome, error) {
			GinkgoHelper()
			out, err := askHumanInput(context.Background(), json.RawMessage(input), prompter)
			if err != nil {
				return inputOutcome{}, err
			}
			var outcome inputOutcome
			Expect(json.Unmarshal([]byte(out), &outcome)).To(Succeed())
			return outcome, nil
		}

		It("Should return the entered value", func() {
			prompter.inputFn = func(string, string) (string, error) { return "stream-1", nil }

			outcome, err := inputResult(`{"question":"Name?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Value).NotTo(BeNil())
			Expect(*outcome.Value).To(Equal("stream-1"))
		})

		It("Should treat an empty answer as a value, not a cancellation", func() {
			prompter.inputFn = func(string, string) (string, error) { return "", nil }

			outcome, err := inputResult(`{"question":"Name?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Value).NotTo(BeNil())
			Expect(*outcome.Value).To(BeEmpty())
		})

		It("Should return no value when the operator cancels", func() {
			prompter.inputFn = func(string, string) (string, error) { return "", errors.New("interrupt") }

			outcome, err := inputResult(`{"question":"Name?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Value).To(BeNil())
			Expect(outcome.Reason).To(ContainSubstring("did not answer"))
		})

		It("Should pass a sanitized default through for the operator to edit", func() {
			var gotDefault string
			prompter.inputFn = func(_, def string) (string, error) {
				gotDefault = def
				return def, nil
			}

			outcome, err := inputResult(`{"question":"Name?","default":"\u001b[1mdraft\u001b[0m"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(gotDefault).To(Equal("draft"))
			Expect(*outcome.Value).To(Equal("draft"))
		})

		It("Should deny without asking when no terminal is attached", func() {
			prompter.canPrompt = false

			outcome, err := inputResult(`{"question":"Name?"}`)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome.Value).To(BeNil())
			Expect(outcome.Reason).To(ContainSubstring("no interactive terminal"))
		})
	})

	Describe("ExecuteBuiltinUse", func() {
		resultBlock := func(block llm.ToolResultBlock) (text string, isError bool) {
			return block.Content, block.IsError
		}

		It("Should return a declined confirmation as a normal result, not an error", func() {
			prompter.confirmFn = func(string) (bool, error) { return false, nil }
			use := llm.ToolUseBlock{ID: "tu_1", Name: "ask_human_confirm", Input: json.RawMessage(`{"question":"Proceed?"}`)}

			text, isError := resultBlock(askHumanConfirmTool().ExecuteUse(context.Background(), use, toolkit.ExecDeps{Prompter: prompter}))
			Expect(isError).To(BeFalse())
			Expect(text).To(ContainSubstring(`"confirmed":false`))
		})

		It("Should return malformed input as an error result", func() {
			use := llm.ToolUseBlock{ID: "tu_2", Name: "ask_human_confirm", Input: json.RawMessage(`{"question":`)}

			text, isError := resultBlock(askHumanConfirmTool().ExecuteUse(context.Background(), use, toolkit.ExecDeps{Prompter: prompter}))
			Expect(isError).To(BeTrue())
			Expect(text).To(ContainSubstring("invalid ask_human_confirm input"))
		})
	})

	Describe("sanitizePrompt", func() {
		It("Should strip ANSI escape sequences", func() {
			Expect(sanitizePrompt("\x1b[31mDelete?\x1b[0m")).To(Equal("Delete?"))
		})

		It("Should neutralize cursor moves and screen clears used to spoof the operator", func() {
			Expect(sanitizePrompt("\x1b[2J\x1b[1;1HFake prompt")).To(Equal("Fake prompt"))
		})

		It("Should strip OSC sequences", func() {
			Expect(sanitizePrompt("\x1b]0;title\x07Real question?")).To(Equal("Real question?"))
		})

		It("Should collapse control characters and whitespace to single spaces on one line", func() {
			Expect(sanitizePrompt("line one\nline\ttwo")).To(Equal("line one line two"))
		})

		It("Should keep plain text and UTF-8 intact", func() {
			Expect(sanitizePrompt("Delete café data?")).To(Equal("Delete café data?"))
		})

		It("Should cap an over-long question", func() {
			out := sanitizePrompt(strings.Repeat("a", maxPromptRunes+50))
			Expect([]rune(out)).To(HaveLen(maxPromptRunes + 1)) // capped runes plus the ellipsis
			Expect(out).To(HaveSuffix("…"))
		})
	})

	Describe("SanitizeForDisplay", func() {
		It("Should strip escape sequences and other control characters", func() {
			Expect(util.SanitizeForDisplay("a\x1b[31mb\x1b[0mc")).To(Equal("abc"))
			Expect(util.SanitizeForDisplay("a\x07b")).To(Equal("a b"))
		})

		It("Should preserve newlines and tabs so multi-line content keeps its structure", func() {
			Expect(util.SanitizeForDisplay("line one\nline\ttwo")).To(Equal("line one\nline\ttwo"))
		})

		It("Should neither collapse whitespace nor cap the length", func() {
			long := strings.Repeat("word ", maxPromptRunes)
			Expect(util.SanitizeForDisplay(long)).To(Equal(long))
		})

		It("Should keep plain text and UTF-8 intact", func() {
			Expect(util.SanitizeForDisplay("Delete café data?")).To(Equal("Delete café data?"))
		})
	})
})
