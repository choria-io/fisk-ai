//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"strings"
	"unicode/utf8"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RenderMarkdownWidth", func() {
	It("Should render markdown to styled ANSI when color is enabled", func() {
		out := RenderMarkdownWidth("# Heading\n\nsome **bold** prose", 80, false)
		Expect(out).To(ContainSubstring("Heading"))
		Expect(out).To(ContainSubstring("bold"))
		Expect(out).To(ContainSubstring("\x1b["), "expected ANSI styling escapes in the colored output")
	})

	It("Should emit no ANSI escapes when noColor is set", func() {
		out := RenderMarkdownWidth("# Heading\n\nsome **bold** prose", 80, true)
		Expect(out).To(ContainSubstring("Heading"))
		Expect(out).NotTo(ContainSubstring("\x1b"), "the notty style must not emit escapes")
	})

	It("Should wrap prose near the requested width", func() {
		para := strings.Repeat("word ", 80)
		out := RenderMarkdownWidth(para, 30, true)

		for _, line := range strings.Split(out, "\n") {
			Expect(utf8.RuneCountInString(line)).To(BeNumerically("<=", 30))
		}
	})

	It("Should not panic on an absurdly small width", func() {
		Expect(RenderMarkdownWidth("hello world", 1, true)).To(ContainSubstring("hello"))
	})
})
