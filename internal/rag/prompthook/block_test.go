//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/rag/prompthook"
)

// hit builds the fields Block reads, citing the chunk the way rag.Search does.
func hit(path string, ordinal int, heading string, docTitle string) rag.Hit {
	return rag.Hit{
		Citation:    rag.Citation(path, ordinal),
		DocPath:     path,
		Ordinal:     ordinal,
		HeadingPath: heading,
		DocTitle:    docTitle,
	}
}

// bodyLines returns the cited sections, dropping the preamble ahead of the blank line
// and the closing fence behind them.
func bodyLines(block string) []string {
	var out []string
	body := false

	for _, line := range strings.Split(block, "\n") {
		if line == "</knowledge-index>" {
			break
		}

		if body {
			out = append(out, line)
			continue
		}

		if line == "" {
			body = true
		}
	}

	return out
}

// packed collapses the alignment padding, for the specs that pin which sections are
// cited rather than where their headings start.
func packed(block string) []string {
	var out []string

	for _, line := range bodyLines(block) {
		out = append(out, strings.Join(strings.Fields(line), " "))
	}

	return out
}

var _ = Describe("Block", func() {
	It("Should render nothing when it is handed no hits", func() {
		Expect(prompthook.Block(nil, prompthook.BlockOptions{})).To(BeEmpty())
		Expect(prompthook.Block([]rag.Hit{}, prompthook.BlockOptions{PerDoc: 2})).To(BeEmpty())
	})

	Describe("The per document cap", func() {
		// Rank order runs a.md, a.md, b.md, a.md, so a cap that drops the surplus of
		// a.md must leave b.md where it ranked.
		ranked := []rag.Hit{
			hit("docs/a.md", 0, "A > One", "A"),
			hit("docs/a.md", 2, "A > Two", "A"),
			hit("docs/b.md", 1, "B > One", "B"),
			hit("docs/a.md", 4, "A > Three", "A"),
		}

		DescribeTable("Should keep the highest ranked sections of a document",
			func(perDoc int, expected []string) {
				Expect(packed(prompthook.Block(ranked, prompthook.BlockOptions{PerDoc: perDoc}))).To(Equal(expected))
			},
			Entry("zero applies no cap", 0, []string{
				"docs/a.md#0 A > One",
				"docs/a.md#2 A > Two",
				"docs/b.md#1 B > One",
				"docs/a.md#4 A > Three",
			}),
			Entry("a negative cap applies none either", -1, []string{
				"docs/a.md#0 A > One",
				"docs/a.md#2 A > Two",
				"docs/b.md#1 B > One",
				"docs/a.md#4 A > Three",
			}),
			Entry("one section per document", 1, []string{
				"docs/a.md#0 A > One",
				"docs/b.md#1 B > One",
			}),
			Entry("a cap the document exceeds drops its lowest ranked section", 2, []string{
				"docs/a.md#0 A > One",
				"docs/a.md#2 A > Two",
				"docs/b.md#1 B > One",
			}),
		)

		It("Should keep every section of a document that exactly meets the cap", func() {
			// a.md brings three sections and b.md four, so a cap of three is the
			// boundary for one document while the other loses its lowest ranked hit.
			hits := []rag.Hit{
				hit("docs/a.md", 0, "A > One", "A"),
				hit("docs/b.md", 1, "B > One", "B"),
				hit("docs/a.md", 2, "A > Two", "A"),
				hit("docs/b.md", 3, "B > Two", "B"),
				hit("docs/a.md", 4, "A > Three", "A"),
				hit("docs/b.md", 5, "B > Three", "B"),
				hit("docs/b.md", 7, "B > Four", "B"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{PerDoc: 3}))).To(Equal([]string{
				"docs/a.md#0 A > One",
				"docs/b.md#1 B > One",
				"docs/a.md#2 A > Two",
				"docs/b.md#3 B > Two",
				"docs/a.md#4 A > Three",
				"docs/b.md#5 B > Three",
			}))
		})

		It("Should charge the cap for distinct sections rather than repeated ones", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{PerDoc: 2}))).To(Equal([]string{
				"docs/a2a.md#4-5 Elicitation > Approvals",
			}))
		})
	})

	Describe("Collapsing a run", func() {
		It("Should cite consecutive ordinals under one heading as a range", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 6, "Elicitation > Approvals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#4-6 Elicitation > Approvals",
			}))
		})

		It("Should stop a run at a gap in the ordinals", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 6, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 7, "Elicitation > Approvals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#4 Elicitation > Approvals",
				"docs/a2a.md#6-7 Elicitation > Approvals",
			}))
		})

		It("Should cite a lone section without a range while a run beside it folds", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/tasks.md", 1, "Tasks > Waking a peer", "Tasks"),
				hit("docs/tasks.md", 2, "Tasks > Waking a peer", "Tasks"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#4 Elicitation > Approvals",
				"docs/tasks.md#1-2 Tasks > Waking a peer",
			}))
		})

		It("Should fold under one heading and stop at the next", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 6, "Elicitation > Refusals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#4-5 Elicitation > Approvals",
				"docs/a2a.md#6 Elicitation > Refusals",
			}))
		})

		It("Should fold headings the sanitizer makes identical", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 5, "Elicitation  >\tApprovals", "A2A"),
				hit("docs/a2a.md", 6, "Elicitation > Approvals\x1b[0m", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#4-6 Elicitation > Approvals",
			}))
		})

		It("Should collapse only what the cap left", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 6, "Elicitation > Approvals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{PerDoc: 2}))).To(Equal([]string{
				"docs/a2a.md#4-5 Elicitation > Approvals",
			}))
		})

		It("Should stop a run at a gap the cap opened in the middle of it", func() {
			// Ordinal 5 ranks below the cap, so the run it sat in the middle of is two
			// sections the model can fetch rather than one range covering a third it
			// was never offered.
			hits := []rag.Hit{
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 6, "Elicitation > Approvals", "A2A"),
				hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{PerDoc: 2}))).To(Equal([]string{
				"docs/a2a.md#4 Elicitation > Approvals",
				"docs/a2a.md#6 Elicitation > Approvals",
			}))
		})

		It("Should rank a run where its best placed section ranked", func() {
			hits := []rag.Hit{
				hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
				hit("docs/tasks.md", 1, "Tasks > Waking a peer", "Tasks"),
				hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
			}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#4-5 Elicitation > Approvals",
				"docs/tasks.md#1 Tasks > Waking a peer",
			}))
		})
	})

	Describe("Naming a section", func() {
		It("Should name a chunk with no heading path by its document title", func() {
			hits := []rag.Hit{hit("docs/a2a.md", 0, "", "Agent to Agent")}

			Expect(packed(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"docs/a2a.md#0 Agent to Agent",
			}))
		})

		It("Should cite a document holding no heading by path alone", func() {
			hits := []rag.Hit{hit("docs/notes.txt", 0, "", "")}

			Expect(bodyLines(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"  docs/notes.txt#0",
			}))
		})

		It("Should sanitize a heading the corpus supplied", func() {
			hits := []rag.Hit{hit("docs/a2a.md", 1, "Elicitation\x1b[31m >\tApprovals", "A2A")}

			Expect(bodyLines(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"  docs/a2a.md#1  Elicitation > Approvals",
			}))
		})
	})

	Describe("Citing a section", func() {
		It("Should sanitize a document path the corpus supplied", func() {
			hits := []rag.Hit{hit("docs/a2a\x1b[31m.md", 4, "Elicitation > Approvals", "A2A")}

			lines := bodyLines(prompthook.Block(hits, prompthook.BlockOptions{}))
			Expect(lines).To(Equal([]string{"  docs/a2a.md#4  Elicitation > Approvals"}))

			path, first, last, err := prompthook.ParseCitation(strings.Fields(lines[0])[0])
			Expect(err).ToNot(HaveOccurred())
			Expect(path).To(Equal("docs/a2a.md"))
			Expect(first).To(Equal(4))
			Expect(last).To(Equal(4))
		})

		It("Should cite a long path without cutting it short", func() {
			// The model hands the token back to a command that opens the file, so a
			// path past any display budget has to survive whole.
			long := "docs/" + strings.Repeat("deep/", 42) + "notes.md"
			Expect(len(long)).To(BeNumerically(">", 200))

			hits := []rag.Hit{hit(long, 4, "Elicitation > Approvals", "A2A")}

			lines := bodyLines(prompthook.Block(hits, prompthook.BlockOptions{}))
			Expect(lines).To(HaveLen(1))

			path, first, _, err := prompthook.ParseCitation(strings.Fields(lines[0])[0])
			Expect(err).ToNot(HaveOccurred())
			Expect(path).To(Equal(long))
			Expect(first).To(Equal(4))
		})
	})

	Describe("Aligning the headings", func() {
		It("Should pad the citations to one column", func() {
			hits := []rag.Hit{
				hit("docs/content/a2a/_index.md", 4, "Elicitation > Approvals", "A2A"),
				hit("docs/content/a2a/_index.md", 5, "Elicitation > Approvals", "A2A"),
				hit("docs/content/a2a/_index.md", 6, "Elicitation > Approvals", "A2A"),
				hit("docs/content/a2a/tasks.md", 11, "Tasks > Waking a peer", "Tasks"),
			}

			Expect(bodyLines(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"  docs/content/a2a/_index.md#4-6  Elicitation > Approvals",
				"  docs/content/a2a/tasks.md#11    Tasks > Waking a peer",
			}))
		})

		It("Should leave a citation past the column width to itself", func() {
			long := "docs/content/a2a/a-very-deeply-nested-directory/tasks.md"
			hits := []rag.Hit{
				hit(long, 1, "Tasks > Waking a peer", "Tasks"),
				hit("docs/a2a.md", 2, "Elicitation > Approvals", "A2A"),
			}

			Expect(bodyLines(prompthook.Block(hits, prompthook.BlockOptions{}))).To(Equal([]string{
				"  " + long + "#1  Tasks > Waking a peer",
				"  docs/a2a.md#2  Elicitation > Approvals",
			}))
		})
	})

	Describe("The preamble", func() {
		hits := []rag.Hit{hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A")}

		It("Should fence the block and say what it holds", func() {
			block := prompthook.Block(hits, prompthook.BlockOptions{
				BinaryPath: "/opt/fisk/bin/fisk",
				ConfigPath: "/etc/fisk/agent.yaml",
			})

			Expect(block).To(HavePrefix("<knowledge-index>\n"))
			Expect(block).To(HaveSuffix("\n</knowledge-index>"))
			Expect(block).To(ContainSubstring("These are titles, not content."))
			Expect(block).To(ContainSubstring("Indexed documents can lag the code"))
		})

		It("Should name the binary and the configuration the caller supplied", func() {
			block := prompthook.Block(hits, prompthook.BlockOptions{
				BinaryPath: "/opt/fisk/bin/fisk",
				ConfigPath: "/etc/fisk/agent.yaml",
			})

			Expect(block).To(ContainSubstring("\n  /opt/fisk/bin/fisk rag agent show --config /etc/fisk/agent.yaml \"<citation>\"\n"))
		})

		// rag agent show refuses to run without --config, since the flag picks the
		// directory it reads the index from. A caller that supplied no path gets the
		// flag with a placeholder rather than a command that fails on the model's
		// first attempt to fetch a section.
		It("Should fall back to the name on PATH and a placeholder configuration", func() {
			block := prompthook.Block(hits, prompthook.BlockOptions{})

			Expect(block).To(ContainSubstring("\n  fisk rag agent show --config <path to agent.yaml> \"<citation>\"\n"))
		})

		It("Should always name the configuration flag", func() {
			Expect(prompthook.Block(hits, prompthook.BlockOptions{})).To(ContainSubstring("rag agent show --config "))
			Expect(prompthook.Block(hits, prompthook.BlockOptions{ConfigPath: "/etc/fisk/agent.yaml"})).To(ContainSubstring("rag agent show --config "))
		})

		DescribeTable("Should quote a path the shell would otherwise read as more than a word",
			func(binary string, config string, expected string) {
				block := prompthook.Block(hits, prompthook.BlockOptions{BinaryPath: binary, ConfigPath: config})

				Expect(block).To(ContainSubstring("\n  " + expected + " \"<citation>\"\n"))
			},
			// The path holding nothing a shell reads specially goes out bare in each of
			// these, so a spec fails on quoting that is blanket rather than needed.
			Entry("a space in the binary path",
				"/opt/my fisk/fisk", "/etc/agent.yaml",
				`'/opt/my fisk/fisk' rag agent show --config /etc/agent.yaml`),
			Entry("a dollar in the configuration path",
				"/opt/fisk", "/etc/$HOME/agent.yaml",
				`/opt/fisk rag agent show --config '/etc/$HOME/agent.yaml'`),
			Entry("a backtick in the configuration path",
				"/opt/fisk", "/etc/`id`/agent.yaml",
				"/opt/fisk rag agent show --config '/etc/`id`/agent.yaml'"),
			Entry("a single quote in the configuration path",
				"/opt/fisk", "/etc/rip's/agent.yaml",
				`/opt/fisk rag agent show --config '/etc/rip'\''s/agent.yaml'`),
			Entry("a double quote in the configuration path",
				"/opt/fisk", `/etc/"a"/agent.yaml`,
				`/opt/fisk rag agent show --config '/etc/"a"/agent.yaml'`),
		)

		DescribeTable("Should explain the tag only for the form carrying words",
			func(mode prompthook.Mode, explained bool) {
				block := prompthook.Block(hits, prompthook.BlockOptions{Mode: mode})

				Expect(strings.Contains(block, "The words inside the <rag ...> tag chose these sections")).To(Equal(explained))
			},
			Entry("a tag with words", prompthook.ModeTagWords, true),
			Entry("a bare tag", prompthook.ModeTag, false),
			Entry("an automatic lookup", prompthook.ModeAuto, false),
			Entry("no mode at all", prompthook.ModeNone, false),
		)
	})

	It("Should emit citations ParseCitation reads back", func() {
		hits := []rag.Hit{
			hit("docs/a2a.md", 4, "Elicitation > Approvals", "A2A"),
			hit("docs/a2a.md", 5, "Elicitation > Approvals", "A2A"),
			hit("docs/tasks.md", 11, "Tasks > Waking a peer", "Tasks"),
		}

		var cited [][3]any

		for _, line := range bodyLines(prompthook.Block(hits, prompthook.BlockOptions{})) {
			path, first, last, err := prompthook.ParseCitation(strings.Fields(line)[0])
			Expect(err).ToNot(HaveOccurred())

			cited = append(cited, [3]any{path, first, last})
		}

		Expect(cited).To(Equal([][3]any{
			{"docs/a2a.md", 4, 5},
			{"docs/tasks.md", 11, 11},
		}))
	})
})
