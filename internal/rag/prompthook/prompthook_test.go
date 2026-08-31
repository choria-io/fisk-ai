//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook_test

import (
	"fmt"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

var _ = Describe("Parse", func() {
	DescribeTable("Should read the tag forms",
		func(prompt string, opts prompthook.Options, mode prompthook.Mode, terms []string) {
			d := prompthook.Parse(prompt, opts)

			Expect(d.Skip).To(Equal(prompthook.SkipNone))
			Expect(d.Mode).To(Equal(mode))
			Expect(d.Terms).To(Equal(terms))
			Expect(d.Query).To(Equal(strings.Join(terms, " ")))
		},
		Entry("a bare tag looks up the rest of the message",
			"<rag> how does elicitation work", prompthook.Options{},
			prompthook.ModeTag, []string{"elicitation", "work"}),

		Entry("a bare tag takes the text on both sides of it",
			"what about approvals <rag> in the harness", prompthook.Options{},
			prompthook.ModeTag, []string{"approvals", "harness"}),

		Entry("a tag with words looks up exactly those words",
			"<rag elicitation approvals a2a>", prompthook.Options{},
			prompthook.ModeTagWords, []string{"elicitation", "approvals", "a2a"}),

		Entry("a tag with words ignores the text around it",
			"before the tag <rag elicitation approvals> after the tag", prompthook.Options{},
			prompthook.ModeTagWords, []string{"elicitation", "approvals"}),

		Entry("words typed inside a tag keep their stopwords",
			"<rag how it works>", prompthook.Options{},
			prompthook.ModeTagWords, []string{"how", "it", "works"}),

		Entry("a tag is matched whatever its case",
			"<RAG> tell me about approvals", prompthook.Options{},
			prompthook.ModeTag, []string{"approvals"}),

		Entry("the first tag of a form wins",
			"<rag alpha> and <rag beta>", prompthook.Options{},
			prompthook.ModeTagWords, []string{"alpha"}),

		Entry("the form carrying words is tested for first",
			"<rag> and <rag elicitation>", prompthook.Options{},
			prompthook.ModeTagWords, []string{"elicitation"}),

		// The rule is the first tag of a form, not the first tag: a bare tag opening the
		// message does not stop a words tag later in it from deciding the query.
		Entry("a words tag beats a bare tag typed before it",
			"<rag> how does elicitation work <rag approvals>", prompthook.Options{},
			prompthook.ModeTagWords, []string{"approvals"}),

		Entry("an untagged message is looked up in auto mode",
			"how do approvals reach the user", prompthook.Options{Auto: true},
			prompthook.ModeAuto, []string{"approvals", "reach", "user"}),

		Entry("punctuation separates terms rather than reaching the query",
			`<rag> what about "elicitation", and a2a-approvals?`, prompthook.Options{},
			prompthook.ModeTag, []string{"elicitation", "a2a", "approvals"}),

		// The first > ends the tag, so the rest of the body is text around it.
		Entry("a closing angle inside a tag body ends the tag",
			"<rag what is a > b comparison> tail", prompthook.Options{},
			prompthook.ModeTagWords, []string{"what", "is"}),
	)

	DescribeTable("Should keep every tag out of the query",
		func(prompt string, opts prompthook.Options, mode prompthook.Mode, terms []string) {
			d := prompthook.Parse(prompt, opts)

			Expect(d.Mode).To(Equal(mode))
			Expect(d.Terms).To(Equal(terms))
			Expect(d.Terms).ToNot(ContainElement(prompthook.TagName), "the tag name is not a term")
		},
		Entry("a second bare tag",
			"<rag> alpha <rag> beta", prompthook.Options{},
			prompthook.ModeTag, []string{"alpha", "beta"}),

		Entry("an unclosed tag after a bare one",
			"<rag> what about <rag unclosed", prompthook.Options{},
			prompthook.ModeTag, []string{"unclosed"}),

		Entry("an unclosed tag inside a tag body",
			"<rag <rag real>", prompthook.Options{},
			prompthook.ModeTagWords, []string{"real"}),

		Entry("an unclosed tag in an auto lookup",
			"<rag elicitation approvals", prompthook.Options{Auto: true},
			prompthook.ModeAuto, []string{"elicitation", "approvals"}),

		// A tag with no space around it separates the words it sits between.
		Entry("a tag written against its neighbors",
			"read the docs<rag>and tell me", prompthook.Options{},
			prompthook.ModeTag, []string{"read", "docs"}),

		// The word boundary after the tag name keeps a bracketed word opening with it out
		// of the replace, so the word reaches the query whole rather than as its tail.
		Entry("a bracketed word that opens like a tag",
			"<rag> the <ragged> edge", prompthook.Options{},
			prompthook.ModeTag, []string{"ragged", "edge"}),

		Entry("a longer bracketed word that opens like a tag",
			"<rag> some <ragtime> history", prompthook.Options{},
			prompthook.ModeTag, []string{"ragtime", "history"}),
	)

	It("Should not read a word that opens like the tag as a tag", func() {
		for _, prompt := range []string{"<ragged> the edge", "<ragtime> the piano", "<ragged approvals>", "<ragtime approvals>"} {
			d := prompthook.Parse(prompt, prompthook.Options{})

			Expect(d.Skip).To(Equal(prompthook.SkipNoTag), prompt)
			Expect(d.Mode).To(Equal(prompthook.ModeNone), prompt)
		}

		// Auto mode looks the message up, and the word arrives as the word it is.
		auto := prompthook.Parse("<ragged> the edge", prompthook.Options{Auto: true})
		Expect(auto.Mode).To(Equal(prompthook.ModeAuto))
		Expect(auto.Terms).To(Equal([]string{"ragged", "edge"}))
	})

	It("Should drop the stopwords a prose question carries", func() {
		measured := prompthook.Parse("<rag> lets see if you get anything about agent 2 agent communications", prompthook.Options{})

		Expect(measured.Skip).To(Equal(prompthook.SkipNone))
		Expect(measured.Terms).To(Equal([]string{"agent", "agent", "communications"}))
	})

	It("Should drop terms the index is too short to hold", func() {
		d := prompthook.Parse("<rag a b elicitation>", prompthook.Options{})

		Expect(d.Terms).To(Equal([]string{"elicitation"}))
	})

	It("Should cap the terms at forty", func() {
		var words []string
		for i := 0; i < 60; i++ {
			words = append(words, fmt.Sprintf("term%02d", i))
		}

		d := prompthook.Parse("<rag> "+strings.Join(words, " "), prompthook.Options{})

		Expect(d.Terms).To(HaveLen(40))
		Expect(d.Terms).To(Equal(words[:40]))
	})

	It("Should replace the stopword list when the caller supplies one", func() {
		d := prompthook.Parse("approvals and elicitation", prompthook.Options{Auto: true, Stopwords: []string{"approvals"}})

		Expect(d.Terms).To(Equal([]string{"and", "elicitation"}))

		// An empty list is a list, so it drops nothing.
		none := prompthook.Parse("the approvals", prompthook.Options{Auto: true, Stopwords: []string{}})
		Expect(none.Terms).To(Equal([]string{"the", "approvals"}))
	})

	It("Should hand out a copy of the default stopwords", func() {
		first := prompthook.DefaultStopwords()
		Expect(first).To(ContainElement("about"))

		first[0] = "elicitation"
		first = append(first, "approvals")

		second := prompthook.DefaultStopwords()
		Expect(second).To(HaveLen(len(first) - 1))
		Expect(second[0]).To(Equal("a"))
		Expect(second).ToNot(ContainElement("elicitation"))
		Expect(second).ToNot(ContainElement("approvals"))
	})

	Describe("Gates", func() {
		It("Should skip an empty message", func() {
			for _, prompt := range []string{"", "   \t\n "} {
				d := prompthook.Parse(prompt, prompthook.Options{Auto: true})

				Expect(d.Skip).To(Equal(prompthook.SkipEmptyPrompt))
				Expect(d.Mode).To(Equal(prompthook.ModeNone))
				Expect(d.Terms).To(BeEmpty())
				Expect(d.Query).To(BeEmpty())
			}
		})

		It("Should skip an untagged message while auto is off", func() {
			d := prompthook.Parse("how does elicitation work", prompthook.Options{})

			Expect(d.Skip).To(Equal(prompthook.SkipNoTag))
			Expect(d.Mode).To(Equal(prompthook.ModeNone))
			Expect(d.Terms).To(BeEmpty())
			Expect(d.Query).To(BeEmpty())
		})

		It("Should skip a slash command in auto mode", func() {
			d := prompthook.Parse("/clear the elicitation history", prompthook.Options{Auto: true})

			Expect(d.Skip).To(Equal(prompthook.SkipSlashCommand))
			Expect(d.Mode).To(Equal(prompthook.ModeAuto))
			Expect(d.Query).To(BeEmpty())

			// A tagged message asked for the lookup, so it reaches no such gate.
			tagged := prompthook.Parse("/clear <rag elicitation>", prompthook.Options{Auto: true})
			Expect(tagged.Skip).To(Equal(prompthook.SkipNone))
			Expect(tagged.Terms).To(Equal([]string{"elicitation"}))
		})

		It("Should skip a message nothing survives the filter of", func() {
			bare := prompthook.Parse("<rag>", prompthook.Options{})
			Expect(bare.Skip).To(Equal(prompthook.SkipNoTerms))
			Expect(bare.Mode).To(Equal(prompthook.ModeTag))
			Expect(bare.Query).To(BeEmpty())

			short := prompthook.Parse("<rag> a e i", prompthook.Options{})
			Expect(short.Skip).To(Equal(prompthook.SkipNoTerms))

			stopped := prompthook.Parse("lets see if you know", prompthook.Options{Auto: true})
			Expect(stopped.Skip).To(Equal(prompthook.SkipNoTerms))
			Expect(stopped.Query).To(BeEmpty())
		})

		It("Should skip an untagged message left with fewer terms than MinWords", func() {
			d := prompthook.Parse("approvals now", prompthook.Options{Auto: true, MinWords: 3})

			Expect(d.Skip).To(Equal(prompthook.SkipTooFewWords))
			Expect(d.Mode).To(Equal(prompthook.ModeAuto))
			Expect(d.Terms).To(Equal([]string{"approvals"}), "the gate counts what survived the filter")
			Expect(d.Query).To(BeEmpty(), "a skipped decision runs no lookup")

			// Zero enforces no minimum.
			none := prompthook.Parse("approvals now", prompthook.Options{Auto: true})
			Expect(none.Skip).To(Equal(prompthook.SkipNone))
			Expect(none.Query).To(Equal("approvals"))
		})

		It("Should exempt a tagged message from MinWords", func() {
			opts := prompthook.Options{Auto: true, MinWords: 5}

			words := prompthook.Parse("<rag approvals>", opts)
			Expect(words.Skip).To(Equal(prompthook.SkipNone))
			Expect(words.Terms).To(Equal([]string{"approvals"}))

			bare := prompthook.Parse("<rag> approvals", opts)
			Expect(bare.Skip).To(Equal(prompthook.SkipNone))
			Expect(bare.Terms).To(Equal([]string{"approvals"}))
		})
	})
})
