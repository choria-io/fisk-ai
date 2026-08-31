//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/choria-io/fisk-ai/internal/rag"
	"github.com/choria-io/fisk-ai/internal/util"
)

const (
	// blockOpen and blockClose fence the injected text, so a model reading the
	// conversation can tell it from what the person typed.
	blockOpen  = "<knowledge-index>"
	blockClose = "</knowledge-index>"

	blockIntro    = "Sections of the operator's fisk knowledge base that rank against this message."
	blockTitles   = "These are titles, not content. Read one with:"
	blockStale    = "Indexed documents can lag the code; prefer the files in front of you when they disagree."
	blockTagWords = "The words inside the <" + TagName + " ...> tag chose these sections; the question is the rest of the message."

	// defaultBinary names the command when the caller supplies no path to it.
	defaultBinary = "fisk"

	// configPlaceholder stands in the fetch instruction when the caller supplies no
	// configuration path. It is angle-bracketed like the citation beside it, so the
	// line reads as a command with a hole in it rather than one to run as it stands.
	configPlaceholder = "<path to agent.yaml>"

	// maxHeadingRunes caps what one line takes from a heading, which holds whatever its
	// author wrote and costs the model attention once it wraps. A citation carries no
	// such budget, since rag agent show is handed the token whole.
	maxHeadingRunes = 120

	// maxCitationPad is the widest citation the heading column lines up behind. One
	// deeply nested path would otherwise push every heading in the block across the
	// screen, so a citation wider than this takes two spaces and moves its own heading
	// alone.
	maxCitationPad = 48
)

// BlockOptions are what the caller supplies to render a block. The zero value applies
// no per-document cap, names the binary "fisk" and passes no configuration file.
type BlockOptions struct {
	// BinaryPath is the absolute path to the fisk binary the fetch instruction names.
	// The model runs that command from a directory this package does not know, so a
	// bare name or a relative path can resolve somewhere else. An empty value renders
	// the name "fisk", which reaches the binary through PATH.
	BinaryPath string

	// ConfigPath is the absolute path to the agent configuration the fetch instruction
	// passes as --config, so the command reads the index the lookup ran against. It is
	// required: rag agent show runs in the directory holding the configuration file,
	// which is how it reaches an index that a relative knowledge.directory names, and
	// it refuses to run without the flag. An empty value renders a placeholder, leaving
	// the model a command to complete rather than one to run.
	ConfigPath string

	// PerDoc is the most sections one document contributes to the block. Eight sections
	// of one _index.md say the same thing eight times, where two sections from four
	// documents map the corpus. Zero or less applies no cap.
	PerDoc int

	// Mode is the Decision.Mode the lookup ran under. ModeTagWords adds a line saying
	// the words inside the tag were the lookup, since the model reads a message whose
	// author typed the tag and gets the tag as typed.
	Mode Mode
}

// Block renders ranked hits as the text injected into the model's context. A line
// carries the heading path of a section rather than its body: an irrelevant title is
// one line the model skips, where an irrelevant body competes with the file the model
// is reading. A section with no heading path is named by its document title, and one
// whose document holds no heading at all is cited by path alone.
//
// Hits arrive in rank order and hold it. opts.PerDoc drops the lowest ranked surplus
// of a document rather than reordering anything, and consecutive ordinals under one
// heading path in one document then collapse to a single line citing a range, as in
// docs/a2a.md#4-6. A gap ends a run, because the cap or the ranking removed the
// section the gap stands for and a range spanning it would cite text the model was
// never offered.
//
// No hits render the empty string.
//
// The citation and the heading both come out of the corpus and pass through
// util.SanitizeForTerminal before anything groups or prints them, so a heading that
// differs from another only in the whitespace the sanitizer collapses folds with it
// rather than printing the same line twice.
func Block(hits []rag.Hit, opts BlockOptions) string {
	lines := citationLines(entriesOf(hits, opts.PerDoc))
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder

	b.WriteString(blockOpen)
	b.WriteString("\n")
	b.WriteString(blockIntro)
	b.WriteString("\n")
	b.WriteString(blockTitles)
	b.WriteString("\n")
	b.WriteString(fetchInstruction(opts))
	b.WriteString("\n")
	b.WriteString(blockStale)
	b.WriteString("\n")

	if opts.Mode == ModeTagWords {
		b.WriteString(blockTagWords)
		b.WriteString("\n")
	}

	b.WriteString("\n")

	for _, line := range lines {
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString(blockClose)

	return b.String()
}

// citationLines renders one line per entry: the citation, then the heading behind it,
// with the headings lined up behind the widest citation the column takes. The block
// prints these under its preamble and a logfile prints them alone, so an operator
// reading the logfile reads the lines the model was offered.
func citationLines(entries []entry) []string {
	cites := make([]string, len(entries))
	titles := make([]string, len(entries))
	width := 0

	for i, e := range entries {
		cites[i] = citationRange(e.docPath, e.first, e.last)
		titles[i] = e.title()

		n := utf8.RuneCountInString(cites[i])
		if n > width && n <= maxCitationPad {
			width = n
		}
	}

	lines := make([]string, len(entries))

	for i := range entries {
		var b strings.Builder

		b.WriteString("  ")
		b.WriteString(cites[i])

		if titles[i] != "" {
			pad := width - utf8.RuneCountInString(cites[i])
			if pad < 0 {
				pad = 0
			}

			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString("  ")
			b.WriteString(titles[i])
		}

		lines[i] = b.String()
	}

	return lines
}

// sectionCount counts the sections the entries cite, which is the number of hits the
// per-document cap left. A line citing a range stands for every ordinal in the range,
// so the lines are fewer than the sections wherever a run folded.
func sectionCount(entries []entry) int {
	n := 0

	for _, e := range entries {
		n += e.last - e.first + 1
	}

	return n
}

// entry is one line of the block: a run of consecutive ordinals in one document under
// one heading path, holding the rank of its best placed hit. Its strings are sanitized
// already, since the block groups on the text it prints.
type entry struct {
	docPath  string
	heading  string
	docTitle string
	first    int
	last     int
	rank     int
}

// title names the section, falling back to the document's title for a chunk that
// opens a document and carries no heading path of its own.
func (e entry) title() string {
	if e.heading != "" {
		return e.heading
	}

	return e.docTitle
}

// section gathers the hits sharing one document and one heading path, with the rank
// each ordinal arrived at.
type section struct {
	docPath  string
	heading  string
	docTitle string
	ordinals []int
	ranks    map[int]int
}

// entriesOf sanitizes each hit, applies the per-document cap, then folds the survivors
// into runs of consecutive ordinals and returns those runs in rank order.
func entriesOf(hits []rag.Hit, perDoc int) []entry {
	kept := map[string]int{}
	sections := map[string]*section{}
	var order []string

	for i, h := range hits {
		docPath := citationToken(h.DocPath)
		heading := util.SanitizeForTerminal(h.HeadingPath, maxHeadingRunes)
		key := docPath + "\x00" + heading

		// A section cited twice takes the rank it first reached, and it is dropped
		// before the cap so a repeat cannot spend a slot a distinct section would fill.
		s := sections[key]
		if s != nil {
			_, repeat := s.ranks[h.Ordinal]
			if repeat {
				continue
			}
		}

		if perDoc > 0 {
			if kept[docPath] >= perDoc {
				continue
			}

			kept[docPath]++
		}

		if s == nil {
			s = &section{docPath: docPath, heading: heading, docTitle: util.SanitizeForTerminal(h.DocTitle, maxHeadingRunes), ranks: map[int]int{}}
			sections[key] = s
			order = append(order, key)
		}

		s.ranks[h.Ordinal] = i
		s.ordinals = append(s.ordinals, h.Ordinal)
	}

	var out []entry

	for _, key := range order {
		s := sections[key]
		slices.Sort(s.ordinals)

		start := 0
		for i := 1; i <= len(s.ordinals); i++ {
			if i < len(s.ordinals) && s.ordinals[i] == s.ordinals[i-1]+1 {
				continue
			}

			out = append(out, s.run(start, i-1))
			start = i
		}
	}

	slices.SortFunc(out, func(a, b entry) int { return a.rank - b.rank })

	return out
}

// run builds the entry for the ordinals from through to, ranked where the best placed
// of them ranked, so a fold never moves a line ahead of a hit that outranked it.
func (s *section) run(from int, to int) entry {
	e := entry{
		docPath:  s.docPath,
		heading:  s.heading,
		docTitle: s.docTitle,
		first:    s.ordinals[from],
		last:     s.ordinals[to],
		rank:     s.ranks[s.ordinals[from]],
	}

	for _, o := range s.ordinals[from : to+1] {
		if s.ranks[o] < e.rank {
			e.rank = s.ranks[o]
		}
	}

	return e
}

// citationToken sanitizes a document path without cutting it short. The model hands
// the citation back to rag agent show, which resolves a path it can open rather than
// reading the token, and a truncated one names no document.
func citationToken(s string) string {
	return util.SanitizeForTerminal(s, utf8.RuneCountInString(s))
}

// citationRange renders the token a line cites, which is the canonical citation for a
// single section and the range form for a run of them.
func citationRange(docPath string, first int, last int) string {
	if first == last {
		return rag.Citation(docPath, first)
	}

	return fmt.Sprintf("%s#%d-%d", docPath, first, last)
}

// shellPlain matches a value every POSIX shell reads as the characters it spells, so
// it goes out bare and the line reads as the command it is.
var shellPlain = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellWord renders one argument of a command line a POSIX shell will read. A value
// holding a space arrives as two arguments and one holding a dollar or a backtick
// arrives as whatever the shell substituted, so a value outside the plain set is
// wrapped in single quotes, which make every character literal and leave only the
// single quote itself to escape. The empty string renders as one empty argument.
//
// The fetch instruction Block writes is built with it, and so is the hook command line
// the fisk setup verb writes into a Claude Code settings file. A caller rendering a
// path into a command line a model or a settings file hands to a shell wants this.
func ShellWord(s string) string {
	if shellPlain.MatchString(s) {
		return s
	}

	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// fetchInstruction renders the command that reads one section, indented under the line
// that introduces it. The flag is always there: rag agent show takes the directory it
// runs in from --config and refuses the command without it, so a line that left the flag
// off would name a command the model cannot run.
func fetchInstruction(opts BlockOptions) string {
	binary := opts.BinaryPath
	if binary == "" {
		binary = defaultBinary
	}

	configPath := configPlaceholder
	if opts.ConfigPath != "" {
		configPath = ShellWord(opts.ConfigPath)
	}

	return fmt.Sprintf("  %s rag agent show --config %s \"<citation>\"", ShellWord(binary), configPath)
}
