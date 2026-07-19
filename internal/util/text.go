//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

// elide bounds how much of a value ElideMiddle keeps before eliding its middle:
// the head and tail are kept and the middle is replaced by an ellipsis, so similar
// values (which often share a prefix, like two subjects or stream names) stay
// distinguishable while the result stays short.
const (
	elideHeadRunes = 10
	elideTailRunes = 6
	elideEllipsis  = "..."
)

// ElideMiddle shortens s to its head and tail joined by an ellipsis when it is
// longer than the head, tail and ellipsis together, counting runes so a multibyte
// character is never split. A short value is returned unchanged. It is a generic
// display truncation: the short trace line uses it for a tool call's argument
// values, and the read-only transcript view uses it for raw JSON.
func ElideMiddle(s string) string {
	r := []rune(s)
	if len(r) <= elideHeadRunes+len(elideEllipsis)+elideTailRunes {
		return s
	}

	return string(r[:elideHeadRunes]) + elideEllipsis + string(r[len(r)-elideTailRunes:])
}
