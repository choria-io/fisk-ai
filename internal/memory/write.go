//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValidateWrite checks a write against the store's shared rules and returns the
// description a backend should persist. It validates the key, requires a
// non-empty description after normalization, and caps the content size. Every
// backend calls it before storing so the rules cannot drift between backends;
// the normalized description flattens to a single trimmed line so it is a clean
// single-line index entry and header.
func ValidateWrite(key, description, content string) (normalizedDescription string, err error) {
	if err := ValidateKey(key); err != nil {
		return "", err
	}

	description = normalizeDescription(description)
	if description == "" {
		return "", fmt.Errorf("a memory description must not be empty")
	}

	if len(content) > maxContentBytes {
		return "", fmt.Errorf("memory content is too large: %d bytes, limit is %d", len(content), maxContentBytes)
	}

	return description, nil
}

// CheckCapacity reports whether a store already holding count entries may accept
// a new key. A backend calls it from its create path (overwrite off) with its
// current entry count so the shared cap and its message stay in one place.
func CheckCapacity(count int) error {
	if count >= maxEntries {
		return fmt.Errorf("memory is full: %d entries, limit is %d; delete an entry before creating another", count, maxEntries)
	}

	return nil
}

// normalizeDescription flattens a description to a single trimmed line and caps
// its length: control characters (including newlines and tabs) become spaces,
// runs of whitespace collapse to one, so the value is a clean single-key
// frontmatter header and a clean single line in the index.
func normalizeDescription(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")

	if utf8.RuneCountInString(s) > maxDescriptionRunes {
		s = string([]rune(s)[:maxDescriptionRunes])
	}

	return s
}
