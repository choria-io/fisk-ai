//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package tui is the opt-in full-screen terminal UI, an alternative to the
// default line-oriented CLI. This first piece is a read-only transcript viewer:
// it renders a saved session's conversation in a scrolling viewport with a
// statusbar, driving no live run and reaching no operator, so it carries none of
// the run-lifecycle, signal, or prompt concerns the live UI will. The live loop
// and its native prompts land in later phases.
package tui

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/choria-io/fisk-ai/internal/util"
)

// noticeTTL is how long a transient statusbar notice stays up before it clears
// itself, if no key dismisses it first.
const noticeTTL = 2 * time.Second

// foldThreshold is the estimated wrapped-row height at or above which a thinking
// block becomes foldable. Smaller thinking blocks stay inline, since folding them
// to a one-line placeholder saves nothing and only adds churn. Tool output does not
// use it: raw tool results are collapsed regardless of size, since even a short one
// is rarely worth reading inline.
const foldThreshold = 6

// ErrNoTTY reports that the controlling terminal could not be opened, so the
// caller should fall back to the line UI rather than treat it as a hard failure.
var ErrNoTTY = errors.New("no controlling terminal available")

// LineKind selects how a transcript line is styled and prefixed, mirroring the
// glyph vocabulary of the line UI so the two read the same.
type LineKind int

const (
	// LineNarration is the model's prose; shown unprefixed.
	LineNarration LineKind = iota
	// LinePrompt is the operator's original prompt; prefixed "> ".
	LinePrompt
	// LineThinking is the model's reasoning; dimmed.
	LineThinking
	// LineToolCall is a tool invocation; prefixed "-> ".
	LineToolCall
	// LineToolResult is a tool's returned output; marked with "<-" on its own line
	// so the output below keeps its original column alignment.
	LineToolResult
	// LineToolError is a failed tool's output; marked with "<-" on its own line and
	// colored so a
	// failure stands out, and, when folded, kept visible through a distinct
	// placeholder rather than collapsing to the same neutral stand-in as success.
	LineToolError
	// LineMeta is a structural marker such as a section fence; dimmed.
	LineMeta
	// LineWarning is an advisory; prefixed "warning: " and colored.
	LineWarning
)

// Line is one renderable transcript entry. Text is the raw, untrusted content;
// the viewer sanitizes it and neutralizes any widget markup before display, so a
// caller passes model output straight through. Short, when set on a LineToolCall,
// is a pre-elided form of Text used only when the full Text would overflow the row;
// an empty Short means the line is never elided.
type Line struct {
	Kind  LineKind
	Text  string
	Short string
}

// Meta describes the session being viewed, shown in the status and header bars.
type Meta struct {
	// Title identifies the session, e.g. its run id.
	Title string
	// Model is the LLM the session used.
	Model string
	// Version is the build version, shown in the header bar. Empty hides it.
	Version string
	// Query is the prompt the agent was run with, previewed (truncated) in the header
	// bar so the operator can see which run they are looking at. Empty hides it.
	Query string
	// Dir is the working directory the run was started in, shown on the live view's
	// startup card. Empty hides the line. It is only consulted by the live run's splash;
	// the static transcript viewer never shows it.
	Dir string
	// Interactive marks a chat run, shown as a badge in the header bar so the operator
	// knows the input row will open when a turn finishes.
	Interactive bool
	// Resume marks a resumed session, whose restored transcript draws straight away. The
	// live view skips the startup card in that case: it would only flash up and vanish as
	// the first response is already in hand.
	Resume bool
	// InTokens and OutTokens are the session's accumulated token usage, shown on the
	// static statusbar the same way the live bar shows its running counter. Both zero
	// (a view with no usage to report) hides the count rather than showing "0/0".
	InTokens  int64
	OutTokens int64
}

// idempotentScreen wraps a tcell.Screen so Fini runs its restore exactly once, no
// matter how many owners call it. tview calls Fini on a normal Stop and again from
// its own panic recovery, and the viewer defers a Fini of its own for the early
// error and panic paths; the sync.Once keeps those from double-restoring the
// terminal while still guaranteeing it is always restored.
type idempotentScreen struct {
	tcell.Screen
	once sync.Once
}

func (s *idempotentScreen) Fini() { s.once.Do(s.Screen.Fini) }

// ShowTranscript renders a session's transcript full-screen and blocks until the
// operator quits. It binds the screen to /dev/tty so os.Stdout stays free, and
// returns ErrNoTTY (wrapped) when no controlling terminal is available so the
// caller can fall back to the line UI. foldThinking and foldTools set the initial
// fold state so the viewer can open collapsed on the conversation. The terminal is
// always restored on return, including on a panic in the tview loop.
func ShowTranscript(meta Meta, lines []Line, noColor, foldThinking, foldTools bool) error {
	tty, err := tcell.NewDevTty()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoTTY, err)
	}

	base, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoTTY, err)
	}

	return runViewer(&idempotentScreen{Screen: base}, meta, lines, noColor, foldThinking, foldTools)
}

// runViewer builds the widgets on the given screen and runs the tview loop. It is
// separate from ShowTranscript so tests can drive it with a SimulationScreen.
func runViewer(screen tcell.Screen, meta Meta, lines []Line, noColor, foldThinking, foldTools bool) error {
	// The deferred Fini restores the terminal on every path, including a panic that
	// unwinds through here; it is idempotent so it composes with tview's own Fini
	// on a normal quit or its panic recovery.
	defer screen.Fini()

	v := newViewer(meta, lines, noColor, false)
	v.foldThinking = foldThinking
	v.foldToolOutput = foldTools
	v.app.SetScreen(screen)

	return v.app.SetRoot(v.pages, true).SetFocus(v.view).Run()
}

// viewer holds the widgets and the search state of a running transcript view.
type viewer struct {
	app         *tview.Application
	pages       *tview.Pages
	view        *tview.TextView
	status      *tview.TextView
	searchInput *tview.InputField

	// screen is the tcell screen the view draws on, captured on the first draw so a
	// key handler can reach it (e.g. to post to the system clipboard). tview.Application
	// exposes no getter, so it is stashed here.
	screen tcell.Screen

	// meta identifies the session, shown on the left of the statusbar; kept so the
	// live view can rebuild the bar as elapsed, tokens and run state change.
	meta Meta

	// notice is a transient statusbar message (e.g. a copy confirmation), shown until
	// the next keypress or until noticeTimer fires. It and the timer/generation below
	// are only ever touched on the tview loop.
	notice string
	// noticeTimer auto-clears the notice after noticeTTL; noticeGen invalidates a
	// timer whose notice was already replaced or cleared, so a late fire cannot wipe a
	// fresh notice. noticeTTL is a field so tests can shorten it.
	noticeTimer *time.Timer
	noticeGen   int
	noticeTTL   time.Duration
	// repaintStatus rebuilds the statusbar text. The default rebuilds the static bar;
	// the live view overrides it so a notice repaint keeps the elapsed/token/state
	// content instead of flattening it. It is the single owner of the status text.
	repaintStatus func()

	// lines is the transcript so far, kept so narration can be re-rendered to
	// markdown at the current width on resize and so live events can append.
	lines   []Line
	noColor bool
	// follow starts the viewport at the tail and keeps it there as new lines arrive,
	// until the operator scrolls up; used for a live run. A static transcript leaves
	// it false so the view opens at the top.
	follow bool
	// scrollProbe records that a downward scroll key was just dispatched to the
	// viewport so the after-draw hook can tell whether it landed pinned at the bottom
	// (its clamped offset did not advance) and, if so, resume tail-follow like tail -f.
	// tview only re-arms follow on mouse-scroll-to-bottom, End and G, not on
	// Down/PgDn, so we detect the latter ourselves. scrollProbeOffset is the offset
	// captured before the key. Both are touched only on the tview loop.
	scrollProbe       bool
	scrollProbeOffset int
	// foldThinking and foldToolOutput collapse every large thinking / tool-result
	// block to a one-line placeholder, toggled by z / Z. They are a global view mode,
	// not a per-block state (a flat TextView has no cursor), and are read only when
	// building line markup, so nothing else desyncs when they flip.
	foldThinking   bool
	foldToolOutput bool
	// plain holds the un-styled text of each line, indexed the same as the regions
	// embedded in the view, so a search can match content and scroll to it.
	plain []string
	// rendered holds each line's viewport markup at the current width, so an append
	// re-renders only the new line while a resize re-renders all of them.
	rendered []string
	// width is the width the viewport was last rendered at, 0 before the first draw.
	width int
	// lastTerm is the most recent search term, repeated by the "n" key.
	lastTerm string
	// match is the index into plain of the current search hit, -1 before any match.
	match int
	// onQuit is invoked for a quit key. It stops the loop for a static view; a live
	// run overrides it to cancel the run and let teardown stop the loop in order.
	onQuit func()

	// body is the main vertical flex (header, viewport, statusbar). It is kept so the
	// interactive input row can be inserted above the statusbar after construction.
	body *tview.Flex
	// headerRows is 1 when the viewer draws a header band above the viewport, 0 otherwise.
	// It is a fixed row the input row cannot grow into, so the auto-grow cap subtracts it
	// when working out how tall the input may get without starving the transcript.
	headerRows int

	// The interactive follow-up input row, present only when EnableInteractive was
	// called. promptInput is the "> " field; promptTop and promptBottom are the divider
	// rules that frame it. awaiting is true while a turn boundary is offering the field;
	// deliver hands the operator's follow-up (or a clean end) back to the run goroutine.
	// promptRows is the input row's currently applied height, so a resize only churns the
	// Flex when the wrapped line count actually changes. All are touched only on the tview
	// loop.
	promptInput  *tview.TextArea
	promptTop    *tview.TextView
	promptBottom *tview.TextView
	promptRows   int
	awaiting     bool
	deliver      func(text string, reset, cont bool)

	// splashCard is the live view's startup card, a single opaque TextView kept so the
	// spinner can repaint it; nil for the static viewer, which shows no card. splashBody
	// is its right-column content (version/model/dir), kept so a spinner repaint recomposes
	// the card without rebuilding it. splashDismissed latches once the card is removed so it
	// never re-shows. All are touched only on the tview loop.
	splashCard      *tview.TextView
	splashBody      []string
	splashDismissed bool

	// history is the in-memory list of submitted follow-ups, recalled with Up/Down like a
	// shell. histIdx is the cursor into it: len(history) is the newest position, where the
	// operator's in-progress draft (stashed in histDraft the first time they navigate up)
	// lives. It does not persist across runs.
	history   []string
	histIdx   int
	histDraft string
}

// newViewer assembles the viewport, statusbar, help and search overlays. The
// viewport text is rendered lazily on the first draw, once the screen width is
// known, and re-rendered when the width changes. follow makes it track the tail for
// a live run.
func newViewer(meta Meta, lines []Line, noColor, follow bool) *viewer {
	v := &viewer{
		app:       tview.NewApplication(),
		pages:     tview.NewPages(),
		meta:      meta,
		lines:     lines,
		noColor:   noColor,
		follow:    follow,
		match:     -1,
		noticeTTL: noticeTTL,
	}

	v.view = tview.NewTextView().
		SetDynamicColors(true).
		SetRegions(true).
		SetScrollable(true).
		SetWrap(true)

	v.plain = make([]string, len(lines))
	for i, l := range lines {
		v.plain[i] = util.SanitizeForDisplay(l.Text)
	}

	// The statusbar is a filled bar so it reads as a distinct band separate from the
	// viewport rather than blending into the transcript; the background color fills
	// the whole row regardless of the text length. A live run rebuilds its text and
	// colors as it progresses; a static view sets it once here.
	v.status = tview.NewTextView().SetDynamicColors(true)
	v.status.SetTextColor(tcell.ColorWhite)
	v.status.SetBackgroundColor(tcell.ColorBlue)
	v.repaintStatus = func() { v.status.SetText(v.withNotice(statusText(v.meta))) }
	v.repaintStatus()

	main := tview.NewFlex().SetDirection(tview.FlexRow)
	// A header band above the viewport shows the build version and, in a chat run, a
	// [chat] marker, framing the transcript against the statusbar below. It is omitted
	// when there is nothing to show so a bare viewer keeps its original two-band layout.
	if header := headerText(meta); header != "" {
		bar := tview.NewTextView().SetDynamicColors(true)
		bar.SetTextColor(tcell.ColorWhite)
		bar.SetBackgroundColor(tcell.ColorBlue)
		bar.SetText(header)
		main.AddItem(bar, 1, 0, false)
		v.headerRows = 1
	}
	main.
		AddItem(v.view, 0, 1, true).
		AddItem(v.status, 1, 0, false)
	v.body = main

	v.pages.
		AddPage("main", main, true, true).
		AddPage("help", helpOverlay(false, false), true, false).
		AddPage("search", v.searchOverlay(), true, false)

	v.onQuit = v.app.Stop
	v.app.SetInputCapture(v.onKey)
	// Bracketed paste is left on so a multi-line paste lands in the input row's TextArea
	// verbatim (via its PasteHandler) instead of arriving as raw Enter keys, which the
	// key capture would otherwise read as a submit at the first embedded newline.
	v.app.EnablePaste(true)
	// Render at the real width before the first paint and again whenever it changes;
	// this runs on the tview loop, so mutating the viewport text here is safe.
	v.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		// Stash the screen so a key handler can reach it; the first draw always runs
		// before the first key event, so it is set for every entry path.
		v.screen = screen
		if w, _ := screen.Size(); w != v.width {
			first := v.width == 0
			v.renderAll(w)
			v.updateDividers(w)
			if first && v.follow {
				// Turn on tview's own tail-tracking once; a manual scroll up will
				// switch it off, and scrolling back to the bottom will restore it.
				v.view.ScrollToEnd()
			}
		}
		// Size the input row every frame, not just on a resize: an edit resizes the row
		// through its change callback, but the TextArea re-scrolls to the cursor against its
		// old height as part of the same key event, so the top-anchor has to be re-applied
		// here, after all event handling and right before the draw, to stick.
		v.resizeInput()
		return false
	})
	// After each draw, resume tail-follow if a downward scroll key left the viewport
	// pinned at the bottom (tview does not re-arm follow for Down/PgDn on its own).
	v.app.SetAfterDrawFunc(v.afterScroll)

	return v
}

// afterScroll resumes tail-follow when a downward scroll key left the viewport pinned
// at the bottom, i.e. its clamped offset did not advance past the pre-key offset. tview
// re-arms follow itself for mouse-scroll-to-bottom, End and G but not for Down/PgDn, so
// this closes that gap so reaching the bottom by any means behaves like tail -f. Any
// other draw is a no-op. It runs on the tview loop, after the offset has been clamped.
func (v *viewer) afterScroll(tcell.Screen) {
	if !v.scrollProbe {
		return
	}
	v.scrollProbe = false

	row, _ := v.view.GetScrollOffset()
	if row == v.scrollProbeOffset {
		v.view.ScrollToEnd()
	}
}

// armScrollProbe records the viewport offset before a downward scroll key so the next
// draw can tell whether the key landed at the bottom. It runs on the tview loop.
func (v *viewer) armScrollProbe() {
	v.scrollProbeOffset, _ = v.view.GetScrollOffset()
	v.scrollProbe = true
}

// isScrollDownKey reports whether ev scrolls the viewport toward the bottom, so reaching
// the bottom with it should resume tail-follow.
func isScrollDownKey(ev *tcell.EventKey) bool {
	switch ev.Key() {
	case tcell.KeyDown, tcell.KeyPgDn, tcell.KeyCtrlF:
		return true
	case tcell.KeyRune:
		return ev.Rune() == 'j'
	}

	return false
}

// renderAll rebuilds every line's markup at the given width and sets the viewport
// text. It is used on the first draw and on a resize, when word wrapping changes.
func (v *viewer) renderAll(width int) {
	v.width = width
	v.rendered = make([]string, len(v.lines))
	for i := range v.lines {
		v.rendered[i] = v.lineMarkup(i, width)
	}
	v.view.SetText(strings.Join(v.rendered, "\n"))
}

// appendLine adds a line to the transcript and, once the width is known, renders
// just that line and appends it, leaving the rest untouched. It must run on the
// tview loop (inside a QueueUpdateDraw closure) since it mutates viewer state the
// draw reads. Tail-tracking, if on, keeps the new line in view.
func (v *viewer) appendLine(l Line) {
	v.lines = append(v.lines, l)
	v.plain = append(v.plain, util.SanitizeForDisplay(l.Text))

	if v.width == 0 {
		// Not drawn yet; the first BeforeDraw will render everything at the real width.
		return
	}

	i := len(v.lines) - 1
	v.rendered = append(v.rendered, v.lineMarkup(i, v.width))
	v.view.SetText(strings.Join(v.rendered, "\n"))
}

// lineMarkup renders line i to its viewport markup, wrapped in a search region so a
// match can scroll straight to it. A folded line renders as a placeholder instead of
// its content; the region wrapper stays so a search match still lands on it and the
// rendered/lines/plain slices stay index-aligned 1:1. It is a pure function of i, the
// width and the two fold flags, so an append never leaves a half-rendered folded line.
func (v *viewer) lineMarkup(i, width int) string {
	body := v.markup(v.lines[i], width)
	if v.isFolded(i, width) {
		body = foldPlaceholder(v.lines[i].Kind, foldRows(v.plain[i], width))
	}

	marked := fmt.Sprintf(`["l%d"]%s[""]`, i, body)
	if v.breakBefore(i) {
		// A blank line, outside the search region, separates a turn's prose from the
		// tool activity it would otherwise butt against, above and below.
		marked = "\n" + marked
	}

	return marked
}

// lineGroup buckets a line kind for spacing. Prose (a turn's thinking and narration)
// and tools (a tool call and its output) are the two groups a blank line separates;
// everything else (the prompt, meta fences, warnings) is its own bucket that never
// triggers a break.
type lineGroup int

const (
	groupOther lineGroup = iota
	groupProse
	groupTools
)

func groupOf(kind LineKind) lineGroup {
	switch kind {
	case LineThinking, LineNarration:
		return groupProse
	case LineToolCall, LineToolResult, LineToolError:
		return groupTools
	default:
		return groupOther
	}
}

// breakBefore reports whether a blank line should precede line i. It does at a
// transition between a turn's prose and its tool activity, in either direction, so
// the narration is set off from the tool calls below it and from the previous turn's
// tool output above it. Lines within a group (a call then its result, thinking then
// narration) stay tight, and the prompt, fences and warnings never trigger a break.
func (v *viewer) breakBefore(i int) bool {
	if i == 0 {
		return false
	}

	prev, cur := groupOf(v.lines[i-1].Kind), groupOf(v.lines[i].Kind)
	if prev == groupOther || cur == groupOther {
		return false
	}

	return prev != cur
}

// isFolded reports whether line i is currently collapsed. Tool output collapses
// whenever tool folding is on, at any size, since its raw text is rarely worth
// reading inline; a thinking block collapses only once its estimated height reaches
// the threshold, so a short thought still shows inline rather than as a same-height
// placeholder.
func (v *viewer) isFolded(i, width int) bool {
	switch v.lines[i].Kind {
	case LineThinking:
		// Thinking folds only when it is large; a short block saves nothing folded.
		return v.foldThinking && foldRows(v.plain[i], width) >= foldThreshold
	case LineToolResult, LineToolError:
		// Tool output always folds when folding is on, even a single line: the raw
		// result is rarely useful to read inline, so it collapses to a placeholder
		// the operator opens on demand.
		return v.foldToolOutput
	default:
		return false
	}
}

// foldRows estimates how many wrapped rows s occupies at the given width, so a single
// very long line (a JSON or base64 tool dump with no newlines) counts as the many rows
// it actually paints rather than one. It is an approximation: it counts runes, not
// display cells, and wraps on the width boundary rather than on words like tview, which
// is close enough to decide foldability and to size the placeholder.
func foldRows(s string, width int) int {
	rows := 0
	for _, line := range strings.Split(s, "\n") {
		n := utf8.RuneCountInString(line)
		if width > 0 && n > width {
			rows += (n + width - 1) / width
		} else {
			rows++
		}
	}

	return rows
}

// foldPlaceholder is the dimmed one-line stand-in for a collapsed block. It is trusted
// markup: only an int and fixed words are interpolated, no model text, so it needs no
// escaping. The key hint is the only in-context affordance for expanding, since the
// alt-screen has no other cue that content is hidden.
func foldPlaceholder(kind LineKind, rows int) string {
	noun, key, color := "thinking", "z", "gray"
	switch kind {
	case LineToolResult:
		noun, key = "output", "Z"
	case LineToolError:
		noun, key, color = "error output", "Z", "red"
	}
	unit := "lines"
	if rows == 1 {
		unit = "line"
	}

	return fmt.Sprintf("[%s]+ %d %s of %s (%s to expand)[-]", color, rows, unit, noun, key)
}

// toggleFold flips the fold flag for a kind and re-renders. It runs on the tview loop
// (from onKey), so it is serialized against the run goroutine's appends. Scroll is left
// to tview: an active search match is kept in view, a tail-following live view stays at
// the bottom, and otherwise the row offset is preserved (exact when the folded content
// is below the viewport, drifting only when content above it collapses). It is a no-op
// before the first draw, where the width is unknown and the first draw renders anew.
func (v *viewer) toggleFold(kind LineKind) {
	if v.width == 0 {
		return
	}

	var folded bool
	switch kind {
	case LineThinking:
		v.foldThinking = !v.foldThinking
		folded = v.foldThinking
	case LineToolResult:
		v.foldToolOutput = !v.foldToolOutput
		folded = v.foldToolOutput
	default:
		return
	}

	v.renderAll(v.width)
	if v.match >= 0 {
		v.view.ScrollToHighlight()
	}

	v.notice = foldNotice(kind, folded)
	v.armNoticeExpiry()
	v.repaintStatus()
}

// foldNotice is the transient statusbar confirmation for a toggle, worded as the new
// state rather than the action so a toggle that hides nothing on screen still shows the
// mode changed.
func foldNotice(kind LineKind, folded bool) string {
	noun := "thinking"
	if kind == LineToolResult {
		noun = "output"
	}
	state := "expanded"
	if folded {
		state = "folded"
	}

	return noun + " " + state
}

// revealForMatch unfolds the kind of line i when a search lands on folded content, so a
// match is never hidden behind a placeholder. Search stays authoritative over fold: the
// buffer search walks is the same one copy emits, so a hit is always shown, not skipped.
func (v *viewer) revealForMatch(i int) {
	if v.width == 0 || !v.isFolded(i, v.width) {
		return
	}

	switch v.lines[i].Kind {
	case LineThinking:
		v.foldThinking = false
	case LineToolResult, LineToolError:
		v.foldToolOutput = false
	}

	v.renderAll(v.width)
}

// markup renders one line to viewport markup. Narration goes through glamour at
// the current width; the resulting ANSI is escaped (so a literal "[" in the model
// text cannot open a tag) and then translated into tview's own color tags. Other
// kinds keep the fixed plain styling.
func (v *viewer) markup(l Line, width int) string {
	switch l.Kind {
	case LineNarration:
		md := util.RenderMarkdownWidth(util.SanitizeForDisplay(l.Text), width, v.noColor)
		return tview.TranslateANSI(tview.Escape(md))
	case LineToolCall:
		// A tool-call line keeps its full command when it fits the row and falls back to
		// the elided form only when it would wrap; the choice is made here, at render
		// time with the width in hand, so a resize re-picks it through renderAll.
		l.Text = toolCallText(l, width)
		return renderLine(l)
	default:
		return renderLine(l)
	}
}

// toolCallPrefixWidth is the visible width of the "-> " glyph styleLine prefixes a
// tool-call line with; toolCallFitMargin keeps one cell in hand so a line that lands
// on the last column does not wrap past a scrollbar or cursor cell.
const (
	toolCallPrefixWidth = 3
	toolCallFitMargin   = 1
)

// toolCallText picks the tool-call line's displayed command: the full Text when it
// fits the content width, otherwise the pre-elided Short. A line with no Short (a
// built-in or remote trace, already short) is never elided.
func toolCallText(l Line, width int) string {
	return ToolCallText(l.Text, l.Short, width)
}

// ToolCallText picks the displayed command for a tool call: the full display when it
// fits width, otherwise the pre-elided short. An empty short is never elided. Width is
// the total column budget of the surface (the "-> " prefix and a one-cell edge margin
// are subtracted here), measured in runes on the sanitized single-line form, the same
// approximation foldRows uses, so a run of wide runes can still wrap; the fit test
// biases to eliding at the edge. The line UI shares this with the TUI viewport so both
// surfaces elide identically.
func ToolCallText(display, short string, width int) string {
	if short == "" {
		return display
	}

	avail := width - toolCallPrefixWidth - toolCallFitMargin
	if avail > 0 && utf8.RuneCountInString(util.SanitizeForDisplay(display)) <= avail {
		return display
	}

	return short
}

// onKey routes global keys. Scroll keys fall through to the focused TextView; the
// overlays swallow input while they are up so typing a search term or dismissing
// help never quits the view.
func (v *viewer) onKey(ev *tcell.EventKey) *tcell.EventKey {
	front, _ := v.pages.GetFrontPage()

	switch front {
	case splashName:
		// The startup card is up, waiting for the first response. Any key dismisses it;
		// a leave key then aborts against the revealed view, and everything else is
		// swallowed so a stray key cannot act on the transcript hidden beneath it.
		v.hideSplash()
		if ev.Key() == tcell.KeyCtrlC || ev.Key() == tcell.KeyEsc || ev.Rune() == 'q' {
			v.onQuit()
		}
		return nil
	case "search":
		// The search input owns all keys; its done handler closes the overlay.
		return ev
	case "help":
		// Any key dismisses help.
		v.closeOverlay("help")
		return nil
	case promptPage:
		// The prompt widget owns its keys so the operator can answer; only Ctrl-C
		// still reaches the run, so an abort is always possible while a prompt is up.
		if ev.Key() == tcell.KeyCtrlC {
			v.onQuit()
			return nil
		}
		return ev
	}

	// Interactive follow-up: while a turn boundary is offering the input row, the field
	// owns typing so the operator can enter any text (including words with 'q'), with a
	// small fixed set of control keys handled here. In nav mode (focus handed to the
	// transcript) it falls through so scroll/search/fold/copy work as usual.
	if v.awaiting {
		typing := v.app.GetFocus() == v.promptInput
		switch {
		case ev.Key() == tcell.KeyCtrlC:
			// Always available: abort the session and tear the view down.
			v.onQuit()
			return nil
		case ev.Key() == tcell.KeyCtrlD:
			// Clean end from either mode: finish the session without aborting.
			v.submitInput(false)
			return nil
		case typing && ev.Key() == tcell.KeyEnter && ev.Modifiers()&tcell.ModAlt != 0:
			// Alt-Enter (Option-Enter on macOS) inserts a newline instead of sending, so a
			// multi-line follow-up can be composed. Ctrl-J is the universal fallback for
			// terminals that do not pass the Alt modifier through.
			v.insertNewline()
			return nil
		case typing && ev.Key() == tcell.KeyCtrlJ:
			v.insertNewline()
			return nil
		case typing && ev.Key() == tcell.KeyEnter:
			v.submitInput(true)
			return nil
		case typing && (ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab || ev.Key() == tcell.KeyEsc):
			// Hand focus to the transcript for navigation, keeping the draft intact.
			v.focusTranscript()
			return nil
		case typing && ev.Key() == tcell.KeyUp:
			// History recall lives off the top edge of the draft: when the cursor is on the
			// first display row, Up recalls an older follow-up; otherwise it moves the cursor
			// up a line within a multi-line draft.
			if row, _, _, _ := v.promptInput.GetCursor(); row <= 0 {
				v.historyPrev()
				return nil
			}
			v.forwardToInput(ev)
			return nil
		case typing && ev.Key() == tcell.KeyDown:
			// Symmetric to Up: forward the key first, and if the cursor did not move it was
			// already on the last display row, so recall a newer follow-up (or the draft).
			before, _, _, _ := v.promptInput.GetCursor()
			v.forwardToInput(ev)
			if after, _, _, _ := v.promptInput.GetCursor(); after == before {
				v.historyNext()
			}
			return nil
		case typing && (ev.Key() == tcell.KeyPgUp || ev.Key() == tcell.KeyPgDn):
			// Scroll the transcript without leaving the field, so the operator can
			// glance up while composing. Up/Down are the history keys, so paging is the
			// scroll gesture here. Paging down to the bottom resumes tail-follow too.
			if ev.Key() == tcell.KeyPgDn {
				v.armScrollProbe()
			}
			v.forwardToView(ev)
			return nil
		}
		if typing {
			return ev
		}
		// Nav mode (transcript focused while the row is open): Tab or 'i' tabs into the
		// field to reply; everything else falls through to the normal viewer keys.
		if ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab || ev.Rune() == 'i' {
			v.focusInput()
			return nil
		}
	} else if v.promptInput != nil {
		// The row exists but no turn boundary is open (the agent is working): swallow Tab
		// so focus can never land on the field and light it up. It is reachable only
		// while awaiting, so it stays quiet and unfocusable in between.
		if ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab {
			return nil
		}
	}

	// Any key on the main view dismisses a lingering notice (the copy confirmation),
	// except "y" itself so a repeated copy re-posts a fresh notice rather than blanking
	// it first. This is the single clear point so scroll keys that fall through to the
	// TextView still dismiss it.
	if v.notice != "" && ev.Rune() != 'y' {
		v.clearNotice()
		v.repaintStatus()
	}

	switch {
	case ev.Key() == tcell.KeyCtrlC, ev.Key() == tcell.KeyEsc, ev.Rune() == 'q':
		v.onQuit()
		return nil
	case ev.Rune() == '?':
		v.pages.ShowPage("help")
		return nil
	case ev.Rune() == '/':
		v.openSearch()
		return nil
	case ev.Rune() == 'n':
		v.search(v.lastTerm)
		return nil
	case ev.Rune() == 'y':
		v.copyTranscript()
		return nil
	case ev.Rune() == 'z':
		v.toggleFold(LineThinking)
		return nil
	case ev.Rune() == 'Z':
		v.toggleFold(LineToolResult)
		return nil
	}

	// A downward scroll key falls through to the focused viewport; arm the probe so the
	// after-draw hook can resume tail-follow if it lands at the bottom.
	if isScrollDownKey(ev) {
		v.armScrollProbe()
	}

	return ev
}

// inputReadyHint is the grey placeholder shown when a turn boundary opens and focuses the
// input row, until the operator types. While the agent works the placeholder is cleared so
// the reserved row is blank.
const inputReadyHint = "Ready for a follow-up. Enter sends, Alt-Enter for a newline, /help for commands"

// enableInput builds the interactive follow-up row, a "> " field framed by divider
// rules, and inserts it above the statusbar. It is called once, only for a live view
// started interactive, so a non-interactive view keeps its original two-band layout.
func (v *viewer) enableInput() {
	v.promptTop = dividerBar()
	v.promptBottom = dividerBar()

	// A TextArea rather than an InputField so the row can hold and display more than one
	// line; it word-wraps and self-scrolls to keep the cursor in view, which the auto-grow
	// below builds on. The text and box backgrounds default to tview's muted contrast
	// colors; override both to the terminal default so the row never paints a colored bar,
	// and carry the grey hint on that same default background.
	v.promptInput = tview.NewTextArea().SetLabel("> ")
	v.promptInput.SetTextStyle(tcell.StyleDefault)
	v.promptInput.SetBackgroundColor(tcell.ColorDefault)
	v.promptInput.SetPlaceholderStyle(tcell.StyleDefault.Foreground(tcell.ColorGray))
	// Every edit (typing, paste, history recall, clear) recomputes the wrapped line count
	// and grows or shrinks the row to fit, capped at inputMaxRows.
	v.promptInput.SetChangedFunc(v.resizeInput)
	v.promptRows = 1

	// The statusbar is the last item; remove it and re-append it after the new rows so
	// it stays pinned to the bottom with the input row directly above it.
	v.body.RemoveItem(v.status)
	v.body.AddItem(v.promptTop, 1, 0, false)
	v.body.AddItem(v.promptInput, 1, 0, false)
	v.body.AddItem(v.promptBottom, 1, 0, false)
	v.body.AddItem(v.status, 1, 0, false)

	v.updateDividers(v.width)
}

// inputLabelWidth is the screen width of the "> " prompt label, subtracted from the row
// width when measuring how the draft wraps; the label indents every wrapped row.
const inputLabelWidth = 2

// inputMaxRows is the hard cap on how tall the input row grows; past this the TextArea
// scrolls internally. On short terminals the effective cap is lowered so the transcript
// keeps at least inputMinTranscriptRows rows.
const inputMaxRows = 5

// inputMinTranscriptRows is the number of transcript rows the input row must leave intact
// when it grows, so composing a long follow-up never squeezes the conversation to nothing.
const inputMinTranscriptRows = 3

// inputRowsFor reports how many wrapped display rows text occupies in the given width,
// counting explicit newlines and word-wrap the same way the TextArea lays it out. It is a
// pure helper so the geometry is unit-testable without a screen; the result is at least 1.
func inputRowsFor(text string, width int) int {
	if width < 1 {
		width = 1
	}
	total := 0
	for _, seg := range strings.Split(text, "\n") {
		// Tabs render as several columns in the TextArea but count as one to WordWrap, so
		// expand them first to keep the row count aligned with what is drawn.
		seg = strings.ReplaceAll(seg, "\t", "    ")
		n := len(tview.WordWrap(seg, width))
		if n < 1 {
			n = 1
		}
		total += n
	}
	if total < 1 {
		total = 1
	}
	return total
}

// inputMaxRowsNow reports the effective cap for the input row at the current screen height:
// the fixed inputMaxRows, lowered so the transcript never drops below inputMinTranscriptRows
// once the header, dividers and statusbar are accounted for.
func (v *viewer) inputMaxRowsNow() int {
	maxRows := inputMaxRows
	if v.screen != nil {
		_, h := v.screen.Size()
		// Fixed rows around the input: header (0 or 1), the two divider rules, the
		// statusbar, plus the transcript floor.
		avail := h - v.headerRows - 3 - inputMinTranscriptRows
		if avail < maxRows {
			maxRows = avail
		}
	}
	if maxRows < 1 {
		maxRows = 1
	}
	return maxRows
}

// resizeInput grows or shrinks the input row to fit the current draft, capped at
// inputMaxRowsNow, and refreshes the overflow marker on the bottom divider. It is a no-op
// before the first draw sets the width, and only touches the Flex when the row count
// actually changes so a keystroke that does not cross a wrap boundary causes no relayout.
// Runs on the tview loop.
func (v *viewer) resizeInput() {
	if v.promptInput == nil || v.width <= 0 {
		return
	}

	total := inputRowsFor(v.promptInput.GetText(), v.width-inputLabelWidth)
	rows := total
	if maxRows := v.inputMaxRowsNow(); rows > maxRows {
		rows = maxRows
	}

	if rows != v.promptRows {
		v.promptRows = rows
		v.body.ResizeItem(v.promptInput, rows, 0)
		// Growing the row steals height from the transcript with no scroll key pressed, so
		// re-pin the tail when following to keep the newest line above the input.
		if v.follow {
			v.view.ScrollToEnd()
		}
	}

	// Inserting a newline scrolls the TextArea to keep the cursor in view against its old,
	// shorter height, and growing the row afterwards does not undo that. So whenever the
	// whole draft fits the grown row, anchor it back to the top; only when it overflows the
	// cap is a non-zero offset wanted, and the TextArea keeps the cursor visible itself.
	if total <= rows {
		v.promptInput.SetOffset(0, 0)
	}

	v.updateBottomDivider(total)
}

// dividerBar is a horizontal rule row; its content is filled to the viewport width by
// updateDividers once the width is known.
func dividerBar() *tview.TextView {
	return tview.NewTextView().SetDynamicColors(true)
}

// updateDividers fills the input-row rules to the current width. It is a no-op before
// the row exists or before the first draw sets the width.
func (v *viewer) updateDividers(width int) {
	if v.promptTop == nil || width <= 0 {
		return
	}
	v.promptTop.SetText("[gray]" + strings.Repeat("─", width) + "[-]")
	v.updateBottomDivider(inputRowsFor(v.promptInput.GetText(), width-inputLabelWidth))
}

// updateBottomDivider draws the rule below the input row. When the draft is taller than the
// row can show (total exceeds the current cap) it embeds a right-aligned "N lines" marker in
// the rule so the scrolled-off text never reads as lost; otherwise it is a plain rule. A
// no-op before the row exists or the width is known.
func (v *viewer) updateBottomDivider(total int) {
	if v.promptBottom == nil || v.width <= 0 {
		return
	}

	if total <= v.inputMaxRowsNow() {
		v.promptBottom.SetText("[gray]" + strings.Repeat("─", v.width) + "[-]")
		return
	}

	marker := fmt.Sprintf(" %d lines ", total)
	if utf8.RuneCountInString(marker) >= v.width {
		v.promptBottom.SetText("[gray]" + strings.Repeat("─", v.width) + "[-]")
		return
	}
	rule := strings.Repeat("─", v.width-utf8.RuneCountInString(marker))
	v.promptBottom.SetText("[gray]" + rule + marker + "[-]")
}

// activatePrompt turns the input row on at a turn boundary: it wires the delivery
// callback, clears the field, shows the grey ready hint and focuses the field so the
// operator can type a follow-up straight away. Runs on the loop.
func (v *viewer) activatePrompt(deliver func(text string, reset, cont bool)) {
	if v.promptInput == nil {
		return
	}
	v.deliver = deliver
	v.awaiting = true
	v.promptInput.SetText("", false)
	v.promptInput.SetPlaceholder(inputReadyHint)
	// A fresh turn starts at the newest history position with an empty draft, so Up
	// recalls the most recent follow-up rather than resuming a stale scroll position.
	v.histIdx = len(v.history)
	v.histDraft = ""
	v.focusInput()
}

// deactivatePrompt turns the input row off, returning focus to the transcript and
// blanking the field so it sits quiet while the agent works. Runs on the loop;
// idempotent so it composes with submitInput having already cleared the awaiting state.
func (v *viewer) deactivatePrompt() {
	if v.promptInput == nil {
		return
	}
	v.awaiting = false
	v.deliver = nil
	v.promptInput.SetText("", false)
	v.promptInput.SetPlaceholder("")
	v.app.SetFocus(v.view)
}

func (v *viewer) focusInput()      { v.app.SetFocus(v.promptInput) }
func (v *viewer) focusTranscript() { v.app.SetFocus(v.view) }

// submitInput hands the operator's decision back to the run goroutine. With send=true
// an Enter carrying non-blank text echoes the prompt into the transcript and continues
// the session; a blank Enter is a no-op so a stray keystroke cannot end it. With
// send=false (Ctrl-D) it ends the session cleanly. The awaiting flag is cleared before
// delivery so a rapid second key cannot deliver twice. Runs on the loop.
func (v *viewer) submitInput(send bool) {
	if v.deliver == nil {
		return
	}

	if !send {
		v.finishInput("", false, false)
		return
	}

	text := strings.TrimSpace(v.promptInput.GetText())
	if text == "" {
		return
	}

	if cmd, args, ok := resolveCommand(text); ok {
		if cmd == nil {
			// A slash-shaped line that names nothing known: report it and keep the draft
			// so the operator can fix a typo rather than losing what they typed.
			v.setNotice("unknown command: " + firstWord(text))
			return
		}
		v.promptInput.SetText("", false)
		cmd.run(v, args)
		return
	}

	// A "//" prefix escapes a prompt that itself begins with a slash; send it on with the
	// leading slash removed so it is not mistaken for a command.
	if strings.HasPrefix(text, "//") {
		text = text[1:]
	}
	v.pushHistory(text)
	v.appendLine(Line{Kind: LinePrompt, Text: text})
	v.finishInput(text, false, true)
}

// pushHistory records a submitted follow-up for Up/Down recall, skipping a consecutive
// duplicate, and parks the cursor at the newest (draft) position. Runs on the loop.
func (v *viewer) pushHistory(text string) {
	if n := len(v.history); n == 0 || v.history[n-1] != text {
		v.history = append(v.history, text)
	}
	v.histIdx = len(v.history)
	v.histDraft = ""
}

// historyPrev recalls an older follow-up into the field, stashing the in-progress draft
// the first time so historyNext can restore it. A no-op with no history or at the oldest
// entry. Runs on the loop.
func (v *viewer) historyPrev() {
	if v.promptInput == nil || v.histIdx == 0 {
		return
	}
	if v.histIdx == len(v.history) {
		v.histDraft = v.promptInput.GetText()
	}
	v.histIdx--
	v.promptInput.SetText(v.history[v.histIdx], true)
}

// historyNext moves toward the newest entry, restoring the stashed draft once past the
// last recorded follow-up. A no-op already at the draft position. Runs on the loop.
func (v *viewer) historyNext() {
	if v.promptInput == nil || v.histIdx >= len(v.history) {
		return
	}
	v.histIdx++
	if v.histIdx == len(v.history) {
		v.promptInput.SetText(v.histDraft, true)
		return
	}
	v.promptInput.SetText(v.history[v.histIdx], true)
}

// finishInput delivers exactly once, clearing the awaiting state and the callback first.
func (v *viewer) finishInput(text string, reset, cont bool) {
	d := v.deliver
	v.deliver = nil
	v.awaiting = false
	d(text, reset, cont)
}

// slashCommand is one operator command typed at the chat input, resolved by submitInput.
// Adding a command is a single table entry: its canonical name, any aliases, the one-line
// summary shown in the help overlay, and the effect run when it is submitted. args is the
// text after the command word with surrounding whitespace trimmed but internal newlines
// kept, so a multi-line prompt after /clear survives intact. run is invoked on the tview
// loop, like every other input handler.
type slashCommand struct {
	name    string
	aliases []string
	// args is the argument hint shown after the command name in the help overlay, e.g.
	// "[prompt]"; empty for a command that takes none.
	args    string
	summary string
	run     func(v *viewer, args string)
}

// slashCommands is the chat command set. The help overlay's command list is generated from
// it, so a new command is documented by adding it here.
var slashCommands = []slashCommand{
	{
		name:    "clear",
		args:    "<prompt>",
		summary: "drop context, keep scrollback",
		run:     (*viewer).cmdClear,
	},
	{
		name:    "restart",
		aliases: []string{"redo", "repeat"},
		summary: "re-run the original prompt with a fresh context",
		run:     (*viewer).cmdRestart,
	},
	{
		name:    "exit",
		aliases: []string{"quit"},
		summary: "end the session (a checkpointed chat suspends)",
		run:     (*viewer).cmdExit,
	},
	{
		name:    "help",
		summary: "show commands and keys",
		run:     (*viewer).cmdHelp,
	},
}

// slashLookup maps every command name and alias to its command, built once from
// slashCommands.
var slashLookup = func() map[string]*slashCommand {
	m := make(map[string]*slashCommand, len(slashCommands))
	for i := range slashCommands {
		c := &slashCommands[i]
		m[c.name] = c
		for _, a := range c.aliases {
			m[a] = c
		}
	}
	return m
}()

// resolveCommand splits a submitted line into a slash command and its arguments. It
// returns ok=false for an ordinary prompt: one without a leading slash, or a "//" escape
// for literal text that begins with a slash. A slash-shaped line returns ok=true, with cmd
// naming the command or nil when it names nothing known, so the caller can report it.
func resolveCommand(text string) (cmd *slashCommand, args string, ok bool) {
	if !strings.HasPrefix(text, "/") || strings.HasPrefix(text, "//") {
		return nil, "", false
	}

	name := text[1:]
	if i := strings.IndexFunc(name, unicode.IsSpace); i >= 0 {
		name, args = name[:i], strings.TrimSpace(name[i+1:])
	}

	return slashLookup[strings.ToLower(name)], args, true
}

// firstWord returns the first whitespace-delimited token of text, used to echo the offending
// word back in an unknown-command notice.
func firstWord(text string) string {
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		return text[:i]
	}
	return text
}

// setNotice shows a transient statusbar message and schedules it to clear, repainting the
// bar. It centralizes the notice/arm/repaint sequence.
func (v *viewer) setNotice(msg string) {
	v.notice = msg
	v.armNoticeExpiry()
	v.repaintStatus()
}

// cmdClear drops the conversation context, keeping the scrollback. With no argument it
// reopens the input for a fresh prompt; with one it runs that prompt against the cleared
// context. In a checkpointed chat the runner rotates to a fresh, resumable session behind
// this; a plain chat clears in memory.
func (v *viewer) cmdClear(args string) {
	v.appendLine(Line{Kind: LineMeta, Text: "--- context cleared ---"})
	if args != "" {
		v.pushHistory(args)
		v.appendLine(Line{Kind: LinePrompt, Text: args})
	}
	v.setNotice("context cleared")
	v.finishInput(args, true, true)
}

// cmdRestart re-runs the run's original prompt against a fresh context. It is a no-op with
// a notice when the run had no prompt to repeat.
func (v *viewer) cmdRestart(args string) {
	prompt := strings.TrimSpace(v.meta.Query)
	if prompt == "" {
		v.setNotice("no original prompt to restart")
		return
	}

	v.appendLine(Line{Kind: LineMeta, Text: "--- context cleared ---"})
	v.pushHistory(prompt)
	v.appendLine(Line{Kind: LinePrompt, Text: prompt})
	v.setNotice("restarting with the original prompt")
	v.finishInput(prompt, true, true)
}

// cmdExit ends the session cleanly, the same as Ctrl-D: a plain chat completes and a
// checkpointed one suspends and stays resumable.
func (v *viewer) cmdExit(args string) {
	v.finishInput("", false, false)
}

// cmdHelp opens the help overlay, the typed equivalent of "?" (which at the input row would
// type a literal character rather than open help).
func (v *viewer) cmdHelp(args string) {
	v.pages.ShowPage("help")
}

// forwardToView replays a scroll key into the transcript without moving focus, so the
// operator can scroll while the field keeps their draft and the cursor.
func (v *viewer) forwardToView(ev *tcell.EventKey) {
	if h := v.view.InputHandler(); h != nil {
		h(ev, func(tview.Primitive) {})
	}
}

// forwardToInput replays a key into the input field's own handler, used for cursor motion
// keys the outer switch intercepts (Up/Down) so they still move within a multi-line draft.
func (v *viewer) forwardToInput(ev *tcell.EventKey) {
	if h := v.promptInput.InputHandler(); h != nil {
		h(ev, func(tview.Primitive) {})
	}
}

// insertNewline inserts a literal newline at the cursor by handing the TextArea a synthetic
// Enter through its own input handler, rather than re-injecting the event through the app
// where the key capture would read it as a submit. The change handler grows the row to fit.
func (v *viewer) insertNewline() {
	v.forwardToInput(tcell.NewEventKey(tcell.KeyEnter, '\r', tcell.ModNone))
}

// copyTranscript posts the whole transcript as sanitized plain text to the system
// clipboard and shows a transient confirmation. The wording says what was sent, not
// that it landed: OSC-52 is fire-and-forget and many terminals disable or cap it, so
// success cannot be confirmed. It is a no-op on an empty transcript or before the
// first draw has captured the screen.
func (v *viewer) copyTranscript() {
	if v.screen == nil || len(v.plain) == 0 {
		return
	}

	joined := strings.Join(v.plain, "\n")
	v.screen.SetClipboard([]byte(joined))

	n := strings.Count(joined, "\n") + 1
	unit := "lines"
	if n == 1 {
		unit = "line"
	}
	v.notice = fmt.Sprintf("sent %d %s to clipboard", n, unit)
	v.armNoticeExpiry()
	v.repaintStatus()
}

// armNoticeExpiry schedules the current notice to clear itself after noticeTTL. The
// generation guard makes a late fire a no-op once the notice has been replaced or
// cleared, so a fresh notice is never wiped by an older timer. The timer only fires
// while the loop is live: every path that stops the loop first cancels it (a quit key
// goes through clearNotice; a live prompt clears it in setBlocked), so QueueUpdateDraw
// never runs against a stopped loop.
func (v *viewer) armNoticeExpiry() {
	v.stopNoticeTimer()
	v.noticeGen++
	gen := v.noticeGen
	v.noticeTimer = time.AfterFunc(v.noticeTTL, func() {
		v.app.QueueUpdateDraw(func() {
			if v.noticeGen == gen {
				v.notice = ""
				v.repaintStatus()
			}
		})
	})
}

// stopNoticeTimer cancels a pending auto-clear. Called on the loop.
func (v *viewer) stopNoticeTimer() {
	if v.noticeTimer != nil {
		v.noticeTimer.Stop()
		v.noticeTimer = nil
	}
}

// clearNotice drops the notice and cancels its timer, bumping the generation so an
// in-flight fire cannot re-clear a notice set afterwards. It does not repaint; the
// caller does, so it composes with a following status rebuild.
func (v *viewer) clearNotice() {
	if v.notice == "" && v.noticeTimer == nil {
		return
	}
	v.notice = ""
	v.noticeGen++
	v.stopNoticeTimer()
}

// withNotice prepends the transient notice, if any, to the statusbar text as a
// distinct colored segment. The notice is escaped since the bar has dynamic colors on
// and a bracketed word would otherwise be read as a color tag.
func (v *viewer) withNotice(base string) string {
	if v.notice == "" {
		return base
	}

	return fmt.Sprintf("[black:green] %s [-:-]%s", tview.Escape(v.notice), base)
}

// searchOverlay builds the "/" input field, wired to run the search on Enter and
// to cancel on Esc.
func (v *viewer) searchOverlay() tview.Primitive {
	v.searchInput = tview.NewInputField().SetLabel("/")
	v.searchInput.SetBorder(true).SetTitle(" search ")
	v.searchInput.SetDoneFunc(func(key tcell.Key) {
		term := v.searchInput.GetText()
		v.closeOverlay("search")
		if key == tcell.KeyEnter {
			// An empty query repeats the previous search, like "/<enter>" in vim/less.
			if term == "" {
				term = v.lastTerm
			}
			v.search(term)
		}
	})

	return overlay(v.searchInput, 40, 3)
}

// openSearch shows the search input focused, cleared of any prior term.
func (v *viewer) openSearch() {
	v.searchInput.SetText("")
	v.pages.ShowPage("search")
	v.app.SetFocus(v.searchInput)
}

// closeOverlay hides an overlay page and returns focus to the viewport.
func (v *viewer) closeOverlay(name string) {
	v.pages.HidePage(name)
	v.app.SetFocus(v.view)
}

// search scrolls to the next logical line containing term, case-insensitively,
// wrapping around from the current match. An empty term (a bare "n" with no prior
// search) does nothing.
func (v *viewer) search(term string) {
	if term == "" {
		return
	}
	v.lastTerm = term

	needle := strings.ToLower(term)
	n := len(v.plain)
	for off := 1; off <= n; off++ {
		i := (v.match + off) % n
		if strings.Contains(strings.ToLower(v.plain[i]), needle) {
			v.match = i
			v.revealForMatch(i)
			v.view.Highlight(fmt.Sprintf("l%d", i)).ScrollToHighlight()
			return
		}
	}
}

// renderLine turns a raw transcript line into the styled markup shown in the
// viewport. The two-step defense against markup injection lives here: the content
// is sanitized of terminal escapes, then tview.Escape neutralizes any literal "["
// so model text cannot open a color or region tag, and only then are the trusted
// per-kind tags wrapped around it.
func renderLine(l Line) string {
	return styleLine(l.Kind, tview.Escape(util.SanitizeForDisplay(l.Text)))
}

// styleLine wraps already-escaped content in the trusted color tags for its kind,
// carrying the line UI's prefixes so the two surfaces read the same.
func styleLine(kind LineKind, escaped string) string {
	switch kind {
	case LinePrompt:
		return "[::b]> " + escaped + "[-:-:-]"
	case LineThinking:
		return "[gray]" + escaped + "[-]"
	case LineToolCall:
		return "[teal]-> " + escaped + "[-]"
	case LineToolResult:
		return "[blue]<-\n" + escaped + "[-]"
	case LineToolError:
		return "[red]<-\n" + escaped + "[-]"
	case LineMeta:
		return "[gray]" + escaped + "[-]"
	case LineWarning:
		return "[yellow]warning: " + escaped + "[-]"
	default:
		return escaped
	}
}

// headerText is the top bar: the build version and, in a chat run, a [chat] marker. The
// prompt itself is no longer shown here; it leads the transcript body as a prompt line so
// it stays with the response rather than sitting truncated in the bar. It returns "" when
// nothing is set so the caller can drop the band.
func headerText(meta Meta) string {
	parts := []string{"o(((c"}
	if meta.Version != "" {
		parts = append(parts, "fisk-ai "+tview.Escape(meta.Version))
	}
	if meta.Interactive {
		// The header has dynamic colors on, so a bracketed word would be read as a tag;
		// escape it so the brackets render literally.
		parts = append(parts, tview.Escape("[chat]"))
	}
	if len(parts) == 0 {
		return ""
	}

	return fmt.Sprintf(" [::b]%s[::-] ", strings.Join(parts, " "))
}

// truncateRunes shortens s to at most n runes, appending an ellipsis when it cut. It
// counts runes, not bytes, so it never splits a multibyte character.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}

	return string(r[:n]) + "..."
}

// statusText is the single-line statusbar: what is being viewed on the left and
// the always-visible key hint so an operator is never stranded without a way out.
func statusText(meta Meta) string {
	var parts []string
	if meta.InTokens > 0 || meta.OutTokens > 0 {
		parts = append(parts, fmt.Sprintf("tokens=%d/%d", meta.InTokens, meta.OutTokens))
	}
	if meta.Title != "" {
		parts = append(parts, "session="+tview.Escape(meta.Title))
	}
	if meta.Model != "" {
		parts = append(parts, "model="+tview.Escape(meta.Model))
	}
	left := strings.Join(parts, "  ")

	// Bold on the filled bar; the surrounding spaces pad it off the edges. The bar's
	// own background color supplies the contrast, so no dim foreground is used.
	return fmt.Sprintf(" [::b]%s   |   ? help   q quit   / search   z/Z fold[::-] ", left)
}

// helpLines lists the key bindings shown in the help overlay, since an alt-screen has
// no native affordances to discover them. canSuspend rewords the leave line for a
// checkpointing run, where a leave key suspends before it aborts. interactive adds the
// follow-up section, describing how the input row is driven. Section headers carry the
// accent so they stand out against the plain binding rows.
func helpLines(canSuspend, interactive bool) []string {
	leave := "  q / Esc             quit"
	if canSuspend {
		leave = "  q / Esc / ^C        suspend (again: abort)"
	}

	lines := []string{
		"[" + splashAccent + "::b]Keys[-:-:-]",
		"",
		"  arrows, PgUp/PgDn   scroll",
		"  g / G               top / bottom",
		"  / , n               search, next match",
		"  z / Z               fold thinking / output",
		"  y                   copy full transcript",
		"  ?                   toggle this help",
		leave,
	}
	if interactive {
		// In a checkpointed chat Ctrl-D suspends (the session stays resumable) and Ctrl-C
		// aborts, but the journal keeps the conversation so an abort is resumable too; a
		// plain chat's Ctrl-D ends it for good. Word each for what actually happens.
		leave := []string{"  Ctrl-D / Ctrl-C     end / abort"}
		if canSuspend {
			leave = []string{
				"  Ctrl-D              suspend (resumable)",
				"  Ctrl-C              abort (still resumable)",
			}
		}

		lines = append(lines,
			"",
			"["+splashAccent+"::b]Follow-up[-:-:-]",
			"",
			"  Enter               send follow-up",
			"  Alt-Enter / Ctrl-J  newline",
			"  Up / Down           history / move line",
			"  Ctrl-W              delete word back",
			"  Ctrl-A / Ctrl-E     line start / end",
			"  Ctrl-U / Ctrl-K     kill to start / end",
			"  Alt-B / Alt-F       word left / right",
			"  Tab                 field <-> transcript",
		)
		lines = append(lines, leave...)
	}

	return lines
}

// commandHelpLines renders the slash-command reference as full-width rows for the help
// overlay. It sits below the two-column logo and key-binding block so the summaries have
// the whole card width rather than wrapping in the narrow bindings column beside the logo.
func commandHelpLines() []string {
	pad := strings.Repeat(" ", cardPadLeft)

	lines := []string{"", pad + "[" + splashAccent + "::b]Commands[-:-:-]", ""}
	for _, c := range slashCommands {
		name := "/" + c.name
		if c.args != "" {
			name += " " + c.args
		}
		lines = append(lines, fmt.Sprintf("%s  %-16s%s", pad, name, c.summary))
	}

	return lines
}

// helpCardWidth is the fixed width of the help card: the left pad, the logo column, a
// gap, and enough room for the widest binding row beside it, plus a right margin.
const helpCardWidth = 78

// helpOverlay builds the centered help card: the FISK logo beside the key bindings, with
// the project URL below, framed and accented to match the startup card. It sizes itself
// to the number of binding rows so the box hugs the content in both the compact viewer
// help and the taller interactive one.
func helpOverlay(canSuspend, interactive bool) tview.Primitive {
	// The URL rides directly under the logo, centered in the logo column; there is no
	// bottom footer, so the logo-plus-url block centers vertically against the key list.
	text, count := composeCard("https://choria.io", helpLines(canSuspend, interactive), "")
	if interactive {
		// The command reference goes full-width below the two-column block so its summaries
		// are not squeezed into the narrow key-binding column; recount the rows for sizing.
		text += strings.Join(commandHelpLines(), "\n") + "\n"
		count = strings.Count(text, "\n") + 1
	}
	card := coloredCard(text, " help ")

	return overlay(card, helpCardWidth, count+2)
}

// overlay centers a primitive in a box of the given size over the main view.
func overlay(p tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
}
