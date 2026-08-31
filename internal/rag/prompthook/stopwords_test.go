//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// bom is the byte order mark an editor writes at the head of a UTF-8 file, spelled as
// its code point because the character itself is invisible in a source file.
const bom = string(rune(0xfeff))

var _ = Describe("Stopwords", func() {
	It("Should read back the list it wrote", func() {
		var b strings.Builder

		Expect(prompthook.WriteStopwords(&b, prompthook.DefaultStopwords())).To(Succeed())

		words, err := prompthook.ReadStopwords(strings.NewReader(b.String()))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).To(Equal(prompthook.DefaultStopwords()))
	})

	It("Should write the words sorted and leave the caller's slice in the order it arrived", func() {
		words := []string{"the", "approvals", "and"}

		var b strings.Builder
		Expect(prompthook.WriteStopwords(&b, words)).To(Succeed())

		Expect(words).To(Equal([]string{"the", "approvals", "and"}))

		read, err := prompthook.ReadStopwords(strings.NewReader(b.String()))
		Expect(err).ToNot(HaveOccurred())
		Expect(read).To(Equal([]string{"and", "approvals", "the"}))
	})

	It("Should skip blank lines and lines opening with a comment", func() {
		file := strings.Join([]string{
			"# a header the dump writes",
			"",
			"approvals",
			"   ",
			"  # an indented note",
			"\telicitation\t",
			"",
		}, "\n")

		words, err := prompthook.ReadStopwords(strings.NewReader(file))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).To(Equal([]string{"approvals", "elicitation"}))
	})

	// Parse splits on every rune that is neither a letter nor a digit, so a term never
	// holds a #. A line carrying one would otherwise reach the list as a phrase that
	// matches nothing, and the word in front of it would go on being queried for.
	It("Should read everything from a # to the end of a line as a comment", func() {
		file := strings.Join([]string{
			"cache # the corpus discusses this one",
			"approvals#no space before it",
			"# a whole line",
			"elicitation",
		}, "\n")

		words, err := prompthook.ReadStopwords(strings.NewReader(file))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).To(Equal([]string{"cache", "approvals", "elicitation"}))
	})

	It("Should drop a byte order mark opening the file", func() {
		words, err := prompthook.ReadStopwords(strings.NewReader(bom + "cache\napprovals\n"))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).To(Equal([]string{"cache", "approvals"}))
	})

	// An editor that re-saves a dump with a mark would otherwise put the first header
	// line into the list as a word of its own, since the mark sits in front of its #.
	It("Should read back a dump an editor re-saved with a byte order mark", func() {
		var b strings.Builder
		Expect(prompthook.WriteStopwords(&b, prompthook.DefaultStopwords())).To(Succeed())

		words, err := prompthook.ReadStopwords(strings.NewReader(bom + b.String()))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).To(Equal(prompthook.DefaultStopwords()))
	})

	// A dump is read from beside the agent configuration while the shell redirects it
	// to wherever the operator stood, so the file says which directory reads it.
	It("Should say in the header where the file is read from", func() {
		var b strings.Builder
		Expect(prompthook.WriteStopwords(&b, prompthook.DefaultStopwords())).To(Succeed())

		Expect(b.String()).To(ContainSubstring("directory holding the agent configuration"))
	})

	It("Should lowercase every word it reads", func() {
		words, err := prompthook.ReadStopwords(strings.NewReader("Approvals\nA2A\nELICITATION\n"))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).To(Equal([]string{"approvals", "a2a", "elicitation"}))
	})

	// The list replaces the default rather than adding to it, so a file the operator
	// emptied means no word is dropped. Reading it back as the default would ignore
	// the file they wrote.
	It("Should read a file holding no words as an empty list rather than the default", func() {
		words, err := prompthook.ReadStopwords(strings.NewReader("# every word is commented out\n\n"))
		Expect(err).ToNot(HaveOccurred())
		Expect(words).ToNot(BeNil())
		Expect(words).To(BeEmpty())

		d := prompthook.Parse("the approvals", prompthook.Options{Auto: true, Stopwords: words})
		Expect(d.Terms).To(Equal([]string{"the", "approvals"}))
	})

	// The point of the file: a corpus that discusses a word the default drops keeps it
	// as a query term once the operator deletes the line.
	It("Should keep a term the default drops when the file omits it", func() {
		dropped := prompthook.Parse("show the elicitation", prompthook.Options{Auto: true})
		Expect(dropped.Terms).To(Equal([]string{"elicitation"}))

		var b strings.Builder
		kept := []string{}
		for _, w := range prompthook.DefaultStopwords() {
			if w == "show" {
				continue
			}

			kept = append(kept, w)
		}
		Expect(prompthook.WriteStopwords(&b, kept)).To(Succeed())

		words, err := prompthook.ReadStopwords(strings.NewReader(b.String()))
		Expect(err).ToNot(HaveOccurred())

		d := prompthook.Parse("show the elicitation", prompthook.Options{Auto: true, Stopwords: words})
		Expect(d.Terms).To(Equal([]string{"show", "elicitation"}))
	})
})
