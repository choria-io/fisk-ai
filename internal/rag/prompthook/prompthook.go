//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package prompthook turns the message a person typed into the terms a knowledge
// lookup runs, and names the gate that stopped the ones it refuses.
//
// It sits between a chat harness and rag.Search. Search takes free text and reduces
// it to terms itself: it splits on every rune that is neither a letter nor a digit,
// drops the terms below rag.MinTermRunes, quotes what is left and ORs them. This
// package tokenizes the same way for what happens between the two steps. The stopword
// drop needs terms to strip, and counting terms rather than words is what lets the
// MinWords gate measure what the index will be queried for. The terms it returns are
// the terms that ran, so a caller can show them.
//
// Parse returns a Decision. The caller runs the lookup and renders what it wants of
// the answer.
package prompthook

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/choria-io/fisk-ai/internal/rag"
)

// maxTerms caps one query, so a pasted stack trace makes a query diffuse rather than
// enormous. rag.Search clamps its match expression at the same number.
const maxTerms = 40

// TagName is the word a tag is written with, as in <rag> and <rag elicitation a2a>.
// Parse builds the patterns it matches with from this constant, and a caller naming
// the tag in its own help reads it here rather than spelling it again.
const TagName = "rag"

// Mode names how the lookup was asked for.
type Mode string

const (
	// ModeNone is the zero value and the mode of a message that asked for nothing.
	ModeNone Mode = ""

	// ModeTag is a message carrying a bare <rag> tag. The rest of the message, with the
	// tag removed, is the question.
	ModeTag Mode = "tag"

	// ModeTagWords is a message carrying a tag with its own words, as in
	// <rag elicitation approvals a2a>. Those words are the query and the text around the
	// tag is ignored. The first > ends the tag, so <rag a > b> asks for a alone.
	ModeTagWords Mode = "tag_words"

	// ModeAuto is a message with no tag, looked up because Options.Auto is set.
	ModeAuto Mode = "auto"
)

// Skip names the gate that stopped a lookup. A person reads one of these in a logfile
// and a caller reports it, so each gate states its own reason rather than leaving
// every reader to infer one from an empty result.
type Skip string

const (
	// SkipNone is the zero value, carried by a decision that runs a lookup.
	SkipNone Skip = ""

	// SkipEmptyPrompt is a message that is empty or all whitespace.
	SkipEmptyPrompt Skip = "empty_prompt"

	// SkipNoTag is a message with no tag while Options.Auto is off.
	SkipNoTag Skip = "no_tag"

	// SkipSlashCommand is a message that opens with a slash. It instructs the harness
	// rather than asking about the corpus. A tagged message reaches no such gate,
	// because the tag asked for the lookup.
	SkipSlashCommand Skip = "slash_command"

	// SkipNoTerms is a message whose words were all too short or all stopwords.
	SkipNoTerms Skip = "no_terms"

	// SkipTooFewWords is an untagged message left with fewer terms than
	// Options.MinWords. The gate counts terms rather than words, so "lets try it yes"
	// counts as none.
	SkipTooFewWords Skip = "too_few_words"
)

// Decision is what Parse made of one message.
type Decision struct {
	// Mode names how the lookup was asked for.
	Mode Mode

	// Terms are the query terms in the order the message spelled them, at most forty
	// of them. A skipped decision carries the terms the gate that stopped it had built,
	// so a caller can report what it declined to ask; the gates that run before the
	// filter leave it empty.
	Terms []string

	// Query is Terms joined by a space, which is what rag.Search takes. A skipped
	// decision leaves it empty, so a non-empty Query is a lookup to run.
	Query string

	// Skip names the gate that stopped the lookup, and is SkipNone when one runs.
	Skip Skip
}

// Options are what the caller tunes. The zero value looks up tagged messages only,
// applies DefaultStopwords and enforces no minimum.
type Options struct {
	// Auto looks up messages carrying no tag. It is off by default, so a person who
	// has not asked for a lookup does not get one.
	Auto bool

	// MinWords is the fewest terms an untagged message must be left with after the
	// filter for its lookup to run. Zero enforces no minimum. A tagged message is
	// exempt: a tag is a request, and answering one with silence for being short is
	// the guess the tag exists to replace.
	MinWords int

	// Stopwords replaces DefaultStopwords. A nil slice uses the default; an empty
	// non-nil slice drops no word. Entries are compared lowercased.
	Stopwords []string
}

var (
	// tagWordsPattern matches a tag carrying its own words. It requires a non-space
	// character after the tag name, so <rag   > is the bare form rather than a tag whose
	// words are all whitespace.
	tagWordsPattern = regexp.MustCompile(`(?i)<` + TagName + `\s+([^>\s][^>]*)>`)

	// tagPattern matches a bare tag.
	tagPattern = regexp.MustCompile(`(?i)<` + TagName + `\s*>`)

	// tagOpening matches the start of a tag and the whitespace behind it. Every
	// occurrence is replaced by a space before tokenizing, so a second tag, an unclosed
	// one and the tag that chose the mode all leave the text rather than reaching the
	// query as the term rag. The space keeps "docs<rag>and" two words. The word boundary
	// after the name requires a non-word rune next, so <ragged> and <ragtime> are words
	// the query keeps whole.
	tagOpening = regexp.MustCompile(`(?i)<` + TagName + `\b\s*`)
)

// Parse decides what a message asks the knowledge index for. Every outcome is a
// Decision, and a Decision names the gate that stopped it.
//
// A tag anywhere in the message asks for a lookup. <rag> looks up the rest of the
// message; <rag elicitation approvals a2a> looks up exactly those words. Detection is
// case insensitive and the first tag of a form wins, so a message carrying both forms
// is read as the words its author typed. A message with neither is looked up when
// opts.Auto is set.
func Parse(prompt string, opts Options) Decision {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return Decision{Skip: SkipEmptyPrompt}
	}

	mode, text := detectTag(trimmed)
	if mode == ModeNone {
		if !opts.Auto {
			return Decision{Skip: SkipNoTag}
		}

		mode = ModeAuto
		text = trimmed
	}

	if mode == ModeAuto && strings.HasPrefix(trimmed, "/") {
		return Decision{Mode: mode, Skip: SkipSlashCommand}
	}

	d := Decision{Mode: mode}

	// Words typed inside a tag were chosen deliberately, so filtering them would drop a
	// term their author meant.
	stops := stopwordSet(opts)
	if mode == ModeTagWords {
		stops = nil
	}

	d.Terms = termsOf(tagOpening.ReplaceAllString(text, " "), stops)

	if len(d.Terms) == 0 {
		d.Skip = SkipNoTerms
		return d
	}

	if mode == ModeAuto && opts.MinWords > 0 && len(d.Terms) < opts.MinWords {
		d.Skip = SkipTooFewWords
		return d
	}

	d.Query = strings.Join(d.Terms, " ")

	return d
}

// detectTag names the form the message used and returns the text its terms come from.
// The form carrying words is tested for first, because a message holding one can also
// hold a bare tag and the words its author typed are the more specific request. The
// bare form returns the whole message, since Parse strips the tag itself along with
// any other.
func detectTag(prompt string) (Mode, string) {
	words := tagWordsPattern.FindStringSubmatch(prompt)
	if words != nil {
		return ModeTagWords, words[1]
	}

	if tagPattern.MatchString(prompt) {
		return ModeTag, prompt
	}

	return ModeNone, ""
}

// termsOf rebuilds the text as query terms, splitting on every rune that is neither a
// letter nor a digit and dropping what the index will not hold.
func termsOf(text string, stops map[string]struct{}) []string {
	split := func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}

	var out []string

	for _, word := range strings.FieldsFunc(strings.ToLower(text), split) {
		// The index drops a one-character term, so a query carrying one asks for a word
		// no document was filed under.
		if utf8.RuneCountInString(word) < rag.MinTermRunes {
			continue
		}

		_, stop := stops[word]
		if stop {
			continue
		}

		out = append(out, word)
		if len(out) == maxTerms {
			break
		}
	}

	return out
}

// stopwordSet resolves the words to drop, using the default list when the caller
// named none.
func stopwordSet(opts Options) map[string]struct{} {
	if opts.Stopwords == nil {
		return defaultStopwordSet
	}

	set := make(map[string]struct{}, len(opts.Stopwords))
	for _, w := range opts.Stopwords {
		set[strings.ToLower(w)] = struct{}{}
	}

	return set
}

// DefaultStopwords is the list Parse drops from a message that was not typed inside a
// tag. Each call returns a fresh copy, so a caller can build its own list from it.
//
// The list is why a conversational question works at all. Asking about agent to agent
// communication returned four useful sections of eight on this corpus; the same
// question typed as "lets see if you get anything about agent 2 agent communications"
// returned one, because see, get, anything and about reach the index as terms like any
// other and are common enough to outrank the words carrying the question.
func DefaultStopwords() []string {
	return slices.Clone(defaultStopwords)
}

var defaultStopwords = []string{
	"a", "about", "above", "after", "again", "against", "all", "also", "am", "an",
	"and", "any", "anything", "are", "as", "ask", "at", "be", "because", "been",
	"before", "being", "below", "between", "both", "but", "by", "can", "cannot",
	"could", "did", "do", "does", "doing", "done", "down", "during", "each", "either",
	"else", "even", "ever", "every", "few", "for", "from", "further", "get", "gets",
	"getting", "give", "given", "go", "going", "got", "had", "has", "have", "having",
	"he", "her", "here", "hers", "him", "his", "how", "however", "i", "if", "im", "in",
	"into", "is", "it", "its", "itself", "ive", "just", "know", "known", "let", "lets",
	"like", "look", "looking", "made", "make", "making", "many", "may", "maybe", "me",
	"might", "mine", "more", "most", "much", "must", "my", "myself", "need", "needs",
	"new", "no", "nor", "not", "now", "of", "off", "on", "once", "one", "only", "or",
	"other", "others", "ought", "our", "ours", "out", "over", "own", "please", "put",
	"same", "say", "says", "see", "seeing", "seen", "shall", "she", "should", "show",
	"shows", "so", "some", "something", "still", "such", "sure", "take", "tell", "than",
	"that", "the", "their", "theirs", "them", "then", "there", "these", "they", "thing",
	"things", "think", "this", "those", "though", "through", "thus", "to", "too", "try",
	"trying", "two", "under", "until", "up", "upon", "us", "use", "used", "using",
	"very", "want", "wants", "was", "way", "we", "well", "were", "what", "when",
	"where", "whether", "which", "while", "who", "whom", "why", "will", "with",
	"within", "without", "would", "yes", "yet", "you", "your", "yours",
}

var defaultStopwordSet = func() map[string]struct{} {
	set := make(map[string]struct{}, len(defaultStopwords))
	for _, w := range defaultStopwords {
		set[w] = struct{}{}
	}

	return set
}()
