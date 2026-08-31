//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package prompthook

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseCitation reads a citation token Block wrote and returns the document path with
// the first and last ordinal it covers. A single ordinal comes back as first and last
// alike, so a caller reads every citation as one range.
//
// Block folds consecutive sections into docs/a2a.md#4-6. The block tells the model to
// read a section with rag agent show, a command still to be built, and that command
// takes back whatever the block cited, so it resolves both forms through here. The
// ordinals come from the last #, since a document path can hold one itself.
func ParseCitation(citation string) (string, int, int, error) {
	malformed := func() error {
		return fmt.Errorf("citation %q is malformed; expected <relpath>#<ordinal> or <relpath>#<first>-<last>, e.g. docs/design.md#3 or docs/design.md#3-5", citation)
	}

	idx := strings.LastIndex(citation, "#")
	if idx < 0 {
		return "", 0, 0, fmt.Errorf("citation %q is missing the '#<ordinal>' suffix; expected <relpath>#<ordinal> or <relpath>#<first>-<last>", citation)
	}

	path := citation[:idx]
	if path == "" {
		return "", 0, 0, malformed()
	}

	firstText, lastText, ranged := strings.Cut(citation[idx+1:], "-")
	if !ranged {
		lastText = firstText
	}

	first, err := strconv.Atoi(firstText)
	if err != nil || first < 0 {
		return "", 0, 0, malformed()
	}

	last, err := strconv.Atoi(lastText)
	if err != nil || last < 0 {
		return "", 0, 0, malformed()
	}

	if last < first {
		return "", 0, 0, fmt.Errorf("citation %q covers a range that ends before it starts", citation)
	}

	return path, first, last, nil
}
