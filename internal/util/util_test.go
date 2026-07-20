//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// captureStdoutStderr swaps os.Stdout and os.Stderr for pipes while fn runs and
// returns what each received. The pipes are not terminals, so markdown rendering
// stays raw and the captured output is deterministic. The drains run before fn so
// large writes cannot deadlock on a full pipe buffer.
func captureStdoutStderr(fn func()) (string, string) {
	GinkgoHelper()

	origOut, origErr := os.Stdout, os.Stderr

	outR, outW, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())
	errR, errW, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())

	os.Stdout, os.Stderr = outW, errW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, outR)
		outCh <- b.String()
	}()
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, errR)
		errCh <- b.String()
	}()

	fn()

	Expect(outW.Close()).To(Succeed())
	Expect(errW.Close()).To(Succeed())
	os.Stdout, os.Stderr = origOut, origErr

	return <-outCh, <-errCh
}

func newResponse(blocks ...llm.ContentBlock) llm.Response {
	return llm.Response{Content: blocks}
}

func textBlock(text string) llm.ContentBlock {
	return llm.ContentBlock{Text: &llm.TextBlock{Text: text}}
}

func thinkingBlock(thinking string) llm.ContentBlock {
	return llm.ContentBlock{Thinking: &llm.ThinkingBlock{Text: thinking, Signature: []byte("sig")}}
}

func toolUseBlock(name string) llm.ContentBlock {
	return llm.ContentBlock{ToolUse: &llm.ToolUseBlock{ID: "tool-1", Name: name, Input: json.RawMessage("{}")}}
}

var _ = Describe("PrintText", func() {
	It("Should render the final answer as markdown on stdout", func() {
		msg := newResponse(textBlock("# Title\n\nhello"))

		stdout, stderr := captureStdoutStderr(func() {
			PrintText(msg, true, true)
		})

		Expect(stdout).To(ContainSubstring("# Title"))
		Expect(stdout).To(ContainSubstring("hello"))
		Expect(stderr).NotTo(ContainSubstring("💭"))
	})

	It("Should keep intermediate prose on stderr so a piped result carries only the answer", func() {
		msg := newResponse(textBlock("mid-conversation update"))

		stdout, stderr := captureStdoutStderr(func() {
			PrintText(msg, false, true)
		})

		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(ContainSubstring("mid-conversation update"))
		Expect(stderr).NotTo(ContainSubstring("💭"))
	})

	It("Should mark thinking with a bubble on stderr while the answer stays on stdout", func() {
		msg := newResponse(
			thinkingBlock("weighing the options"),
			textBlock("the answer"),
		)

		stdout, stderr := captureStdoutStderr(func() {
			PrintText(msg, true, true)
		})

		Expect(stderr).To(ContainSubstring("💭 weighing the options"))
		Expect(stdout).To(ContainSubstring("the answer"))
		Expect(stdout).NotTo(ContainSubstring("💭"))
	})

	It("Should strip terminal escapes the model emits so a style cannot bleed past the answer", func() {
		msg := newResponse(textBlock("safe \x1b[31mred-injection\x1b[0m tail"))

		stdout, _ := captureStdoutStderr(func() {
			PrintText(msg, true, true)
		})

		Expect(stdout).To(ContainSubstring("safe red-injection tail"))
		Expect(stdout).NotTo(ContainSubstring("\x1b["))
	})

	It("Should strip terminal escapes from thinking so an injected style cannot bleed on stderr", func() {
		msg := newResponse(
			thinkingBlock("mulling \x1b[32mgreen\x1b[0m over it"),
			textBlock("answer"),
		)

		_, stderr := captureStdoutStderr(func() {
			PrintText(msg, true, true)
		})

		Expect(stderr).To(ContainSubstring("💭 mulling green over it"))
		Expect(stderr).NotTo(ContainSubstring("\x1b["))
	})

	It("Should skip empty thinking blocks so no stray bubble is shown", func() {
		msg := newResponse(
			thinkingBlock(""),
			textBlock("answer"),
		)

		stdout, stderr := captureStdoutStderr(func() {
			PrintText(msg, true, true)
		})

		Expect(stderr).NotTo(ContainSubstring("💭"))
		Expect(stdout).To(ContainSubstring("answer"))
	})

	It("Should concatenate text blocks so markdown spanning blocks is not split", func() {
		msg := newResponse(
			textBlock("| a | b |\n"),
			textBlock("|---|---|\n"),
			textBlock("| 1 | 2 |\n"),
		)

		stdout, _ := captureStdoutStderr(func() {
			PrintText(msg, true, true)
		})

		Expect(stdout).To(ContainSubstring("| a | b |"))
		Expect(stdout).To(ContainSubstring("| 1 | 2 |"))
	})

	It("Should emit nothing for a turn carrying neither text nor thinking", func() {
		msg := newResponse(toolUseBlock("foo"))

		stdout, stderr := captureStdoutStderr(func() {
			PrintText(msg, false, true)
		})

		Expect(stdout).To(BeEmpty())
		Expect(stderr).To(BeEmpty())
	})
})

var _ = Describe("TruncateString", func() {
	It("Should return a short string unchanged", func() {
		Expect(TruncateString("hello", 10)).To(Equal("hello"))
	})

	It("Should keep a string of exactly max runes unchanged", func() {
		Expect(TruncateString("hello", 5)).To(Equal("hello"))
	})

	It("Should cut and append an ellipsis when longer than max", func() {
		Expect(TruncateString("hello world", 5)).To(Equal("hello..."))
	})

	It("Should count runes so multibyte text is never split mid-character", func() {
		out := TruncateString("héllo wörld", 5)
		Expect(out).To(Equal("héllo..."))
		Expect(utf8.ValidString(out)).To(BeTrue())
	})
})

var _ = Describe("TruncateLine", func() {
	It("Should collapse runs of whitespace to single spaces", func() {
		Expect(TruncateLine("a\n\tb   c", 20)).To(Equal("a b c"))
	})

	It("Should collapse first, then truncate on the collapsed length", func() {
		Expect(TruncateLine("one  two  three  four", 7)).To(Equal("one two..."))
	})

	It("Should trim leading and trailing whitespace", func() {
		Expect(TruncateLine("   spaced   ", 20)).To(Equal("spaced"))
	})
})
