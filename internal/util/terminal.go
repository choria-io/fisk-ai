//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// StdinIsTerminal reports whether the agent's own stdin is an interactive
// terminal, the condition the confirm gate and the ask_human_* builtins need to
// reach a human. It is a variable so a test can exercise those paths without a
// real terminal.
var StdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// StdoutIsTerminal reports whether stdout is an interactive terminal. The
// full-screen UI takes over the screen only when both this and StdinIsTerminal
// hold, so a piped or redirected stdout falls back to the line UI and stays clean.
func StdoutIsTerminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// NoTerminalReason is the reason returned when a human-in-the-loop tool or the
// confirm gate is reached with no interactive terminal attached to ask an operator.
const NoTerminalReason = "no interactive terminal is attached, so no operator could be asked"

// ansiSequence matches terminal escape sequences (CSI and OSC) plus any other
// two-byte escape, so a model-supplied string cannot carry control sequences that
// rewrite or spoof what the operator sees on their terminal.
var ansiSequence = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b.")

// SanitizeForTerminal makes a model-influenced string safe to print to the
// operator's terminal: it removes terminal escape sequences and other control
// characters, collapses whitespace to single spaces on one line, and caps the
// length at maxRunes. The result is plain text the operator can trust reflects
// what the model produced. Escapes are stripped before truncation so a cut can
// never leave a dangling sequence behind.
func SanitizeForTerminal(s string, maxRunes int) string {
	s = ansiSequence.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")

	if utf8.RuneCountInString(s) > maxRunes {
		s = string([]rune(s)[:maxRunes]) + "…"
	}

	return s
}

// SanitizeForDisplay makes model-influenced text safe to show in the full-screen
// UI. It strips terminal escape sequences and other control characters that could
// spoof the display, but unlike SanitizeForTerminal it preserves newlines and tabs
// so multi-line content keeps its structure, and neither collapses whitespace nor
// caps the length, since the viewport wraps and scrolls. Any tview widget markup in
// the text is neutralized separately by the UI layer before display.
func SanitizeForDisplay(s string) string {
	s = ansiSequence.ReplaceAllString(s, "")

	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}
