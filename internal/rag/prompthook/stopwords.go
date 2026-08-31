//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strings"
)

// bomPrefix is the byte order mark an editor writes at the head of a UTF-8 file.
// strings.TrimSpace leaves it in place, so ReadStopwords drops it by name. It is
// spelled as its code point, since the character itself is invisible in a source file.
const bomPrefix = string(rune(0xfeff))

// stopwordsHeader opens what WriteStopwords writes. Every line of it is a comment
// ReadStopwords drops, so a dump reads back as the list it was written from.
const stopwordsHeader = `# Stopwords the knowledge lookup drops from a message before it queries the index.
# Words typed inside a <` + TagName + ` ...> tag are exempt, since their author chose them.
#
# This list replaces the built-in one rather than adding to it: delete a line to keep
# that word as a query term, add a line to drop one.
#
# --stopwords reads this file from the directory holding the agent configuration, so a
# relative path resolves beside that file rather than beside the shell you run in.
#
# One word per line. Everything from a # to the end of a line is a comment, a blank
# line is skipped, and each word is compared lowercased.
`

// WriteStopwords writes words to w in the format ReadStopwords takes: a header of
// comment lines, then one word per line in sorted order. The caller's slice keeps the
// order it arrived in.
func WriteStopwords(w io.Writer, words []string) error {
	sorted := slices.Clone(words)
	slices.Sort(sorted)

	var b strings.Builder

	b.WriteString(stopwordsHeader)
	for _, word := range sorted {
		b.WriteString(word)
		b.WriteString("\n")
	}

	_, err := io.WriteString(w, b.String())

	return err
}

// ReadStopwords reads a stopword list from r, one word per line, and returns it
// lowercased in the order the file spelled it. Everything from a # to the end of a
// line is a comment, so a file carries an operator's notes beside the words along with
// the header WriteStopwords writes. Parse splits a message on every rune that is
// neither a letter nor a digit, so no term ever holds a #, and a note after a word
// costs the file nothing. A line left blank is skipped.
//
// A byte order mark opening the file is dropped. An editor that re-saves a dump with
// one would otherwise turn the first header line into a word of its own.
//
// A file holding no words returns an empty non-nil slice, which Options.Stopwords
// reads as dropping nothing. Returning the default there instead would ignore the file
// the operator wrote.
func ReadStopwords(r io.Reader) ([]string, error) {
	words := []string{}
	first := true

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		if first {
			line = strings.TrimPrefix(line, bomPrefix)
			first = false
		}

		comment := strings.IndexByte(line, '#')
		if comment >= 0 {
			line = line[:comment]
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		words = append(words, strings.ToLower(line))
	}

	err := scanner.Err()
	if err != nil {
		return nil, fmt.Errorf("reading the stopword list: %w", err)
	}

	return words, nil
}
