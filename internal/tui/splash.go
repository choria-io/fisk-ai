//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/choria-io/fisk-ai/internal/util"
)

// splashName is the Pages name the startup card occupies while a live run waits for
// its first response.
const splashName = "splash"

// splashLogo is the FISK wordmark shown on the popup cards. It is trusted, static text;
// the block glyphs are the same family of unicode the dividers already use.
var splashLogo = []string{
	"███████ ██ ███████ ██   ██",
	"██      ██ ██      ██  ██",
	"█████   ██ ███████ █████",
	"██      ██      ██ ██  ██",
	"██      ██ ███████ ██   ██",
}

// splashLogoWidth is the widest logo row in cells, used to pad the logo column so the
// body sits beside it rather than under it.
var splashLogoWidth = func() int {
	w := 0
	for _, l := range splashLogo {
		if n := utf8.RuneCountInString(l); n > w {
			w = n
		}
	}
	return w
}()

const (
	// splashWidth sizes the centered startup card. On a terminal narrower than the card
	// tview clips it; the card cannot be resized from the before-draw hook (that runs under
	// the application lock, and hiding a page re-focuses through it, which would deadlock),
	// so a fixed size that fits a normal terminal is used.
	splashWidth = 70
	// splashAccent tints the border, logo and spinner; the labels stay dim and the values
	// keep the terminal default so the card is colored only subtly.
	splashAccent = "blue"
	// splashValueMax bounds a value column entry so a long model id or path does not
	// overflow the card; the directory is left-elided to keep its tail.
	splashValueMax = 28

	// cardPadLeft is the space between the border and the content, so the logo is not
	// jammed against the frame. cardGap separates the logo column from the body beside it.
	cardPadLeft = 1
	cardGap     = 2
)

// composeCard lays out a popup card as a single opaque TextView's markup: the FISK logo
// on the left, the body lines to its right (each block vertically centered against the
// other), and an optional footer beneath. logoCaption, when set, is placed centered on the
// line directly below the logo, within the logo column (used for the project URL under the
// help logo). Every line carries a left pad and the content is framed with a blank top and
// bottom row, so it sits off the border on all sides. A single TextView is used rather than
// nested Flexes because a Flex leaves its background unfilled (tview sets dontClear on it),
// which lets the transcript bleed through the gaps between widgets; a TextView clears its
// background and is fully opaque. It returns the markup and its line count, so the caller
// can size the overlay to fit.
func composeCard(logoCaption string, body []string, footer string) (string, int) {
	blankLogo := strings.Repeat(" ", splashLogoWidth)

	// The left column is the logo, with the optional caption centered directly beneath it.
	left := make([]string, 0, len(splashLogo)+1)
	for _, l := range splashLogo {
		l += strings.Repeat(" ", splashLogoWidth-utf8.RuneCountInString(l))
		left = append(left, "["+splashAccent+"]"+l+"[-]")
	}
	if logoCaption != "" {
		left = append(left, "["+splashAccent+"]"+centerText(logoCaption, splashLogoWidth)+"[-]")
	}

	rows := len(left)
	if len(body) > rows {
		rows = len(body)
	}
	leftTop := (rows - len(left)) / 2
	bodyTop := (rows - len(body)) / 2

	pad := strings.Repeat(" ", cardPadLeft)
	gap := strings.Repeat(" ", cardGap)

	lines := make([]string, 0, rows+4)
	lines = append(lines, "")
	for r := 0; r < rows; r++ {
		leftCell := blankLogo
		if r >= leftTop && r < leftTop+len(left) {
			leftCell = left[r-leftTop]
		}
		bodyCell := ""
		if r >= bodyTop && r < bodyTop+len(body) {
			bodyCell = body[r-bodyTop]
		}
		lines = append(lines, pad+leftCell+gap+bodyCell)
	}
	if footer != "" {
		lines = append(lines, "", pad+footer)
	}
	lines = append(lines, "")

	return strings.Join(lines, "\n"), len(lines)
}

// centerText centers s within width by padding both sides, so a short caption sits under
// the middle of the logo. It counts runes, not bytes, and returns s unchanged when it is
// already at least as wide as the column.
func centerText(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	leftPad := (width - n) / 2

	return strings.Repeat(" ", leftPad) + s + strings.Repeat(" ", width-n-leftPad)
}

// coloredCard wraps composed card markup in a bordered, accented TextView. The single
// TextView keeps the card opaque; the border and title carry the accent to match the bars.
func coloredCard(text, title string) *tview.TextView {
	card := tview.NewTextView().SetDynamicColors(true)
	card.SetText(text)
	card.SetBorder(true).SetTitle(title).SetBorderColor(tcell.ColorBlue).SetTitleColor(tcell.ColorBlue)

	return card
}

// enableSplash builds the startup card and adds it as a visible page on top. It is
// called only for a live run (from newLive), so the static transcript viewer never
// shows a card. The card is removed the moment the first response is ready to draw, the
// run ends, or the operator presses a key. Version, model and dir are model- and
// operator-adjacent text, so they are sanitized and escaped like every other bar.
func (v *viewer) enableSplash(meta Meta) {
	v.splashBody = splashInfoLines(meta)
	text, count := composeCard("", v.splashBody, splashCaptionText(spinnerFrames[0]))
	v.splashCard = coloredCard(text, " fisk-ai ")

	v.pages.AddPage(splashName, overlay(v.splashCard, splashWidth, count+2), true, true)
}

// splashInfoLines is the right column of the startup card: the version, model and working
// directory, label-aligned so the values line up. Values are sanitized and escaped; the
// model is truncated and the directory left-elided so a long one does not overflow.
func splashInfoLines(meta Meta) []string {
	lines := []string{
		fmt.Sprintf("[gray]version[-]  %s", escapeSplash(meta.Version)),
		fmt.Sprintf("[gray]model[-]    %s", escapeSplash(truncateRunes(meta.Model, splashValueMax))),
	}
	if meta.Dir != "" {
		lines = append(lines, fmt.Sprintf("[gray]dir[-]      %s", escapeSplash(elideLeft(meta.Dir, splashValueMax))))
	}

	return lines
}

// splashCaptionText is the animated waiting line: an accented spinner glyph then the
// waiting words. It carries the run's liveness cue while the card covers the statusbar's
// own spinner.
func splashCaptionText(spin string) string {
	return fmt.Sprintf("[%s]%s[-]  waiting for first response", splashAccent, spin)
}

// escapeSplash neutralizes a value for display in a dynamic-colors TextView: terminal
// escapes are stripped and any literal "[" is escaped so a path or model id containing
// one cannot open a color tag.
func escapeSplash(s string) string {
	return tview.Escape(util.SanitizeForDisplay(s))
}

// elideLeft shortens s to at most max runes by dropping from the front and prefixing
// "...", so the tail survives; for a path that keeps the leaf directories, which are the
// informative part. It counts runes, not bytes, so it never splits a multibyte glyph.
func elideLeft(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 3 {
		return string(r[len(r)-max:])
	}

	return "..." + string(r[len(r)-(max-3):])
}

// hideSplash removes the startup card for good. It is idempotent and loop-only: the live
// view calls it directly from its loop closures (teardown, a prompt, a turn boundary)
// and through HideSplash's QueueUpdateDraw from the run goroutine. It leaves focus
// untouched so it does not fight a just-focused input row.
func (v *viewer) hideSplash() {
	if v.splashCard == nil || v.splashDismissed {
		return
	}
	v.splashDismissed = true
	v.pages.HidePage(splashName)
}

// splashActive reports whether the card is still up, so the spinner repaint only runs
// while it is showing.
func (v *viewer) splashActive() bool {
	return v.splashCard != nil && !v.splashDismissed
}

// setSplashSpinner recomposes the card with the current spinner glyph in its caption.
// Runs on the loop from the live view's status refresh.
func (v *viewer) setSplashSpinner(spin string) {
	if v.splashCard == nil {
		return
	}
	text, _ := composeCard("", v.splashBody, splashCaptionText(spin))
	v.splashCard.SetText(text)
}
