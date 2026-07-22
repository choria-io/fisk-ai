//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

// frontmatterDelimiter opens and closes the YAML frontmatter block that carries
// a memory's description ahead of its body.
const frontmatterDelimiter = "---"

// frontmatter is the typed shape of the YAML header stored ahead of a memory's
// body. Only the description lives there today.
type frontmatter struct {
	Description string `yaml:"description"`
}

// Serialize renders a memory value: a YAML frontmatter block holding the
// description, then the body verbatim. The description is encoded by a real YAML
// marshaller so a value containing a colon, a quote or a leading dash cannot
// corrupt the header. The store guarantees the description is a single line, so
// the header is always a single key. It lives in the memory package rather than a
// single backend so every backend writes one on-disk format and a value migrates
// between backends unchanged.
func Serialize(description, content string) ([]byte, error) {
	header, err := yaml.Marshal(frontmatter{Description: description})
	if err != nil {
		return nil, fmt.Errorf("encoding memory frontmatter: %w", err)
	}

	var b strings.Builder
	b.WriteString(frontmatterDelimiter)
	b.WriteString("\n")
	b.Write(header)
	b.WriteString(frontmatterDelimiter)
	b.WriteString("\n\n")
	b.WriteString(content)

	return []byte(b.String()), nil
}

// Parse splits a stored value into its description and body. It is lenient: a
// value that does not open with a frontmatter block is treated as a bodyless-header
// file whose whole content is the body and whose description is empty, so a
// hand-written value still reads. Only the first closing delimiter after the header
// is honored, so a body that itself contains a "---" line is preserved intact.
func Parse(data []byte) (description, content string) {
	s := string(data)

	opening := frontmatterDelimiter + "\n"
	if !strings.HasPrefix(s, opening) {
		return "", s
	}

	rest := s[len(opening):]

	closing := "\n" + frontmatterDelimiter + "\n"
	header := ""
	switch idx := strings.Index(rest, closing); {
	case idx >= 0:
		header = rest[:idx+1]
		content = rest[idx+len(closing):]
	case strings.HasSuffix(rest, "\n"+frontmatterDelimiter):
		header = rest[:len(rest)-len(frontmatterDelimiter)]
	default:
		// No closing delimiter: not a frontmatter document after all.
		return "", s
	}

	content = strings.TrimPrefix(content, "\n")

	var fm frontmatter
	if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
		return "", s
	}

	return fm.Description, content
}
