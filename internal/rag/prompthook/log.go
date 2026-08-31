//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook

import (
	"fmt"
	"strings"
	"time"

	"github.com/choria-io/fisk-ai/internal/util"
)

// logLabel pads every label to one width, so the values start in one column and a
// person scanning a file of entries reads down it.
const logLabel = "%-8s%s\n"

// logNone stands for a value the run does not have, so the line is there to read
// rather than missing from an entry that has every other line.
const logNone = "(none)"

// LogEntry is one run of the pipeline as a logfile records it. A hook adds nothing a
// person can see. An operator reads a file of these to tell a hook that is working
// from one that has never run.
//
// The caller fills it from what it passed to Run and what Run returned, and String
// renders the text the logfile holds.
type LogEntry struct {
	// Time is when the run started, rendered in UTC.
	Time time.Time

	// Verb names the command that ran, as in hook or preview. Both write to one file,
	// so the line opening an entry says which of them wrote it.
	Verb string

	// ConfigPath is the agent configuration the run read, as an absolute path. A run
	// that failed before it had one leaves it empty and the entry leaves the line out.
	ConfigPath string

	// StorePath is the index file the lookup ran against, as an absolute path. An
	// index nobody has built yet is named here too, since the file that is missing is
	// most of what a first entry has to explain. A run that failed before it could
	// resolve the path leaves it empty and the entry leaves the line out.
	StorePath string

	// Prompt is the message the person typed, which is also what the model receives:
	// a prompt hook cannot edit the message. Its words are kept as they were typed and
	// its escape sequences are not, since the file is read on a terminal. A message of
	// several lines is recorded as several lines, indented under the label.
	Prompt string

	// Result is what Run made of the message.
	Result Result

	// PerDoc is the per-document cap the run applied, which the search line counts the
	// surviving sections against. Zero or less applied no cap and the line then counts
	// the hits alone.
	//
	// Pass the RunOptions.Block.PerDoc the run used. The entry re-applies the cap to
	// render the sections it cites, so another value records lines that disagree with
	// the block the run produced.
	PerDoc int

	// VectorEnabled is rag.Store.VectorEnabled for the index the lookup ran against,
	// which names the tier the search line reports.
	VectorEnabled bool

	// Elapsed is what the caller timed, whatever the caller chose to time. A hook
	// starts its clock as the command starts and stops it as the entry is rendered, so
	// the wait for a payload on stdin, opening the logfile, reading the configuration,
	// opening the index and the search are all inside it: a message that sat on stdin
	// for two seconds records two seconds.
	Elapsed time.Duration

	// Err is the error that ended the run, and is nil for a run that finished. An
	// entry carrying one records what the run reached and then the fault.
	Err error
}

// String renders the entry: a line naming the time and the verb, a labeled line for
// each field, then the citation lines the block offered the model. It ends with a
// blank line, so entries appended to one file read apart.
//
// A run that failed before it reached the pipeline records the fields it got as far
// as. The configuration and the index are named where the run resolved them, the mode
// and the terms where a message was read, and the fault always. An operator whose
// index will not open reads an entry naming the file it could not read, and so sees
// that the hook ran.
//
// Nothing the message carries reaches column 0, so no message can forge an entry or
// break one in half. The whole entry is one string, which a caller writes in one call
// so two hooks running at once interleave entries rather than lines.
func (e LogEntry) String() string {
	var b strings.Builder

	var entries []entry
	if e.Result.Outcome() == OutcomeBlock {
		entries = entriesOf(e.Result.Search.Hits, e.PerDoc)
	}

	fmt.Fprintf(&b, "=== %s  %s\n", e.Time.UTC().Format(time.RFC3339), e.Verb)

	logPath(&b, "config", e.ConfigPath)
	logPath(&b, "store", e.StorePath)
	logPrompt(&b, e.Prompt)

	// Parse names a mode or a gate for every message it reads, so a decision carrying
	// neither belongs to a run that failed before the message reached it.
	if e.Result.Decision.Mode != ModeNone || e.Result.Decision.Skip != SkipNone {
		logField(&b, "mode", logModeName(e.Result.Decision.Mode))
		logField(&b, "terms", logTerms(e.Result.Decision.Terms))
	}

	switch {
	case e.Err != nil:
		logField(&b, "fault", e.Err.Error())

	case e.Result.Decision.Skip != SkipNone:
		logField(&b, "gate", string(e.Result.Decision.Skip))

	case e.Result.Search != nil:
		logField(&b, "search", e.searchLine(entries))

		if e.Result.Search.Degraded {
			logField(&b, "degrade", fmt.Sprintf("%s: %s", e.Result.Search.DegradeKind, e.Result.Search.DegradeReason))
		}
	}

	logField(&b, "took", fmt.Sprintf("%dms", e.Elapsed.Milliseconds()))

	if len(entries) > 0 {
		b.WriteString("block\n")

		for _, line := range citationLines(entries) {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")

	return b.String()
}

// searchLine says what the index answered: the status, the tier that ran, the hits it
// ranked and how many sections the per-document cap left of them. It counts sections
// rather than lines, and names the unit, since a run of consecutive ordinals folds
// into one line citing a range and three sections then print as one.
//
// A degraded query ran on the lexical tier, and the line below it names the failure
// that put it there.
func (e LogEntry) searchLine(entries []entry) string {
	sr := e.Result.Search

	tier := "lexical"
	if e.VectorEnabled && !sr.Degraded {
		tier = "hybrid"
	}

	line := fmt.Sprintf("%s, %s, %d hits", sr.Status, tier, len(sr.Hits))
	if e.PerDoc > 0 && len(entries) > 0 {
		line = fmt.Sprintf("%s, %d sections after the per-document cap", line, sectionCount(entries))
	}

	return line
}

// logField writes one labeled line.
func logField(b *strings.Builder, label string, value string) {
	fmt.Fprintf(b, logLabel, label, value)
}

// logPath writes one labeled line for a file the run resolved, and writes nothing for
// one it never got as far as. A label over an empty column says a file has no name,
// where the missing line leaves the fault to say why the run has no file.
func logPath(b *strings.Builder, label string, path string) {
	if path == "" {
		return
	}

	logField(b, label, path)
}

// logPrompt writes the message under the prompt label, indenting every line after the
// first to the width of the label column. A person types blank lines and pastes text,
// and an entry opens with "=== " and ends with a blank line, so a message reaching
// column 0 would append an entry of its own to the file or cut this one in half. An
// indented blank line is not blank and an indented "=== " opens nothing.
//
// util.SanitizeForDisplay takes the escape sequences out and leaves the newlines in.
// The file is read with tail, so a color sequence or a window title somebody pasted
// would otherwise run on the terminal reading the file.
//
// A message that is empty, or that is nothing but the escapes it carried, writes no
// line at all: stdin carrying no message is a fault of its own, and the entry says so
// on the fault line.
func logPrompt(b *strings.Builder, prompt string) {
	prompt = strings.TrimRight(util.SanitizeForDisplay(prompt), "\n")
	if strings.TrimSpace(prompt) == "" {
		return
	}

	for i, line := range strings.Split(prompt, "\n") {
		label := "prompt"
		if i > 0 {
			label = ""
		}

		logField(b, label, line)
	}
}

// logModeName spells the mode, naming the zero value a message that asked for nothing
// carries.
func logModeName(m Mode) string {
	if m == ModeNone {
		return "none"
	}

	return string(m)
}

// logTerms spells the query terms as the lookup asked them.
func logTerms(terms []string) string {
	if len(terms) == 0 {
		return logNone
	}

	return strings.Join(terms, " ")
}
