//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/choria-io/fisk-ai/internal/util"
)

// spinnerFrames animates a working indicator in the statusbar. ASCII, so it needs no
// unicode. spinnerInterval is how often it advances while the run is working.
var spinnerFrames = []string{"|", "/", "-", "\\"}

const spinnerInterval = 100 * time.Millisecond

// runState is the phase the live statusbar reflects. Running is the default; a
// prompt moves it to blocked and back; the run's end fixes it at one of the
// terminal states. It is read and written only on the tview loop.
type runState int

const (
	stateRunning runState = iota
	stateBlocked
	stateSuspending
	stateSuspended
	stateComplete
	stateAborted
	stateError
	// stateAwaitingInput is the interactive turn boundary: the run has yielded and the
	// input row is open for a follow-up. It is a calm, non-amber state so it does not
	// read as the blocking "awaiting approval" bar.
	stateAwaitingInput
)

// Live is a full-screen view of a live agent run: a viewport the run feeds through
// Append while the tview loop draws it, plus a live statusbar. It owns the tcell
// screen and restores the terminal on exit. The run goroutine and the tview loop
// are separate; Append is the seam between them and marshals onto the loop.
type Live struct {
	v        *viewer
	screen   tcell.Screen
	prompter *tcellPrompter

	// suspend requests a graceful checkpoint suspend at the run's next boundary; it is
	// nil for a non-checkpointing run, in which case a leave key aborts as before.
	suspend func()
	// suspended reports, after the run ends, whether it stopped at a graceful suspend
	// (as opposed to completing or being aborted). Set by the caller before Run so the
	// terminal state and the resume hint agree with the run's real outcome rather than
	// with the operator's intent.
	suspended func() bool
	// resumeHint returns the resume command to show on-screen when a run suspends, so a
	// suspended session's id is visible before the alt-screen is torn down. Empty when
	// there is nothing to resume. Set by the caller before Run, read after the run ends.
	resumeHint func() string

	// The status fields are mutated only on the tview loop (from Message, the prompter,
	// the ticker and teardown) so nothing races the draw. ended is read by the leave-key
	// handler to decide suspend vs abort.
	ended     bool
	inTokens  int64
	outTokens int64
	// cacheReadTokens is the input served from the prompt cache, shown as cached=X so a
	// live run's cache hit-rate is visible; a stuck-at-zero value against a climbing
	// inTokens is the tell of a silent cache miss. cacheCreateTokens (cache writes) is
	// tracked so a resumed run's counters stay whole but is not shown on the compact bar.
	cacheReadTokens   int64
	cacheCreateTokens int64
	state             runState

	// spinnerFrame indexes the spinner animation, advanced by the ticker while the run
	// is working. spinning is the ticker's read of "the run is working" (state ==
	// running), stored from the loop so the ticker (a separate goroutine) can gate its
	// repaints without touching loop-owned state; it keeps the bar still while idle.
	spinnerFrame int
	spinning     atomic.Bool

	// leaveRequested records that the operator ended the session from the input bar (/exit,
	// /quit or Ctrl-D). Such a run tears the view down at once rather than parking on a
	// "press q to quit" line, since the operator already asked to leave. Set on the run
	// goroutine before it returns and read by the teardown goroutine after, so an atomic
	// carries it across without a lock.
	leaveRequested atomic.Bool

	// bell rings the terminal bell when the run blocks on an operator decision; it is
	// off unless the caller enables it via SetBell. beep performs the ring (the
	// screen's Beep by default, overridable in tests). Both are read on the tview loop.
	bell bool
	beep func()
}

// NewLive binds a full-screen live view to the controlling terminal. It returns
// ErrNoTTY (wrapped) when no terminal can be opened so the caller falls back to the
// line UI. The view follows the tail as lines arrive until the operator scrolls up.
// suspend is the graceful-suspend request for a checkpointing run, or nil when the
// run is not checkpointing (a leave key then aborts rather than suspends).
func NewLive(meta Meta, noColor bool, suspend func()) (*Live, error) {
	tty, err := tcell.NewDevTty()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoTTY, err)
	}

	base, err := tcell.NewTerminfoScreenFromTty(tty)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoTTY, err)
	}

	return newLive(&idempotentScreen{Screen: base}, meta, noColor, suspend), nil
}

// newLive builds the live view on the given screen. It is separate from NewLive so
// tests can drive it with a SimulationScreen.
func newLive(screen tcell.Screen, meta Meta, noColor bool, suspend func()) *Live {
	l := &Live{
		v:       newViewer(meta, nil, noColor, true),
		screen:  screen,
		suspend: suspend,
		state:   stateRunning,
		beep:    func() { _ = screen.Beep() },
	}
	// Tool output is shown but starts collapsed: a large result folds to a one-line
	// placeholder the operator opens with Z, so a chatty command does not bury the
	// narration. Thinking stays expanded, matching the operator's request.
	l.v.foldToolOutput = true
	l.prompter = newTcellPrompter(l)
	// Route notice repaints through the live bar builder so a copy confirmation keeps
	// the elapsed/token/state content instead of flattening it to the static text.
	l.v.repaintStatus = l.refreshStatus
	// A checkpointing run's leave keys suspend before they abort; reword the help.
	if suspend != nil {
		l.v.pages.RemovePage("help")
		l.v.pages.AddPage("help", helpOverlay(true, false), true, false)
	}
	// Added last so it sits on top of the main, help and search pages: the startup card
	// covers the view until the first response is ready to draw. Only the live run builds
	// it, so the static transcript viewer never shows a card. A resumed session skips it
	// too: its restored transcript is drawn at once, so the card would only flash and go.
	if !meta.Resume {
		// Seed the body with the prompt the run is acting on, styled like a chat follow-up,
		// so once the startup card lifts the operator's request stays as the first line above
		// the response, matching how the resumed run and static viewer replay message zero. A
		// resumed session already carries this line in its restored transcript, so it is only
		// added on a fresh run. Pre-draw appendLine just buffers the line; the first draw
		// renders it at the real width.
		if q := strings.TrimSpace(meta.Query); q != "" {
			l.v.appendLine(Line{Kind: LinePrompt, Text: q})
		}
		l.v.enableSplash(meta)
	}
	l.v.app.SetScreen(screen)
	l.v.app.SetRoot(l.v.pages, true).SetFocus(l.v.view)

	return l
}

// ExpandToolOutput shows tool output expanded rather than folded to a placeholder.
// Call once, before Run, like SetBell; Z still toggles folding at runtime.
func (l *Live) ExpandToolOutput() {
	l.v.foldToolOutput = false
}

// Prompter returns the native prompter that puts a run's interactive decisions to
// the operator through tview widgets, for injection into agent.Run.
func (l *Live) Prompter() util.Prompter {
	return l.prompter
}

// promptResult is the outcome of an interactive turn boundary: the follow-up text,
// whether to clear the conversation context first, and whether the session continues
// (false ends it cleanly).
type promptResult struct {
	text  string
	reset bool
	cont  bool
}

// EnableInteractive turns on interactive follow-up mode: it builds the input row, folds
// thinking by default (a chat run leans on the answers, with reasoning a keystroke away),
// and widens the help overlay to cover the follow-up keys. Call once, before Run, like
// SetBell; NextPromptFunc then yields the continuation to inject into agent.Run.
func (l *Live) EnableInteractive() {
	l.v.enableInput()
	l.v.foldThinking = true
	l.v.pages.RemovePage("help")
	l.v.pages.AddPage("help", helpOverlay(l.suspend != nil, true), true, false)
}

// NextPromptFunc returns the interactive continuation to inject as agent Options
// NextPrompt. At each turn boundary it opens the input row and blocks the run goroutine
// until the operator submits a follow-up (continue) or ends the session.
func (l *Live) NextPromptFunc() func(context.Context) (text string, reset, cont bool) {
	return l.nextPrompt
}

// nextPrompt opens the input row on the tview loop and blocks until the operator acts
// or ctx is canceled (an abort), then returns the follow-up and whether to continue. It
// delivers exactly once via a buffered channel guarded by sync.Once, so a rapid second
// key cannot double-deliver. It runs on the single run goroutine, never concurrently.
func (l *Live) nextPrompt(ctx context.Context) (text string, reset, cont bool) {
	result := make(chan promptResult, 1)
	var once sync.Once
	deliver := func(text string, reset, cont bool) {
		once.Do(func() { result <- promptResult{text: text, reset: reset, cont: cont} })
	}

	l.v.app.QueueUpdateDraw(func() {
		l.state = stateAwaitingInput
		// A chat whose first LLM call errors reaches the input row without ever firing
		// Message, so dismiss the card here so it does not sit stuck over the row. Direct
		// call: this closure already runs on the loop.
		l.v.hideSplash()
		l.v.activatePrompt(deliver)
		l.refreshStatus()
	})

	select {
	case <-ctx.Done():
		// Aborted while the field was up: reset the row so teardown draws a clean view.
		// The run goroutine is still alive here, so the loop applies this before Stop.
		l.v.app.QueueUpdateDraw(l.v.deactivatePrompt)
		return "", false, false
	case r := <-result:
		l.v.app.QueueUpdateDraw(func() {
			l.v.deactivatePrompt()
			if r.cont {
				l.state = stateRunning
				l.refreshStatus()
			}
		})
		if !r.cont {
			// A clean end from the input bar (/exit, /quit, Ctrl-D): the operator asked to
			// leave, so mark it for an immediate teardown rather than the read-the-transcript
			// park a self-ending run gets.
			l.leaveRequested.Store(true)
		}
		return r.text, r.reset, r.cont
	}
}

// SetSuspendedFunc records how to tell, once the run ends, whether it stopped at a
// graceful suspend. The caller sets it before Run so the terminal state is classified
// from the run's real outcome, not from whether the operator asked to suspend (the
// two differ when the run completes before reaching a suspend boundary).
func (l *Live) SetSuspendedFunc(f func() bool) { l.suspended = f }

// SetResumeHintFunc records how to render the resume command once the run ends, shown
// in the completion line when the run suspended. Set before Run, like SetSuspendedFunc.
func (l *Live) SetResumeHintFunc(f func() string) { l.resumeHint = f }

// HideSplash removes the startup card from the run goroutine, marshaling onto the loop.
// The caller invokes it when the first response is ready to draw, or when a resumed
// transcript is shown, so the card gives way to the live view. It is idempotent, so a
// later call once the card is already gone is a no-op.
func (l *Live) HideSplash() {
	l.v.app.QueueUpdateDraw(l.v.hideSplash)
}

// Run drives the run function under the full-screen view: it runs on its own
// goroutine while the tview loop runs on the caller's, and Append marshals between
// them. The view stays up after the run finishes so the operator can read the
// transcript; it tears down only when they quit (q / Esc / Ctrl-C), which also
// cancels a still-running run. On every path the terminal is restored and stderr,
// muzzled for the duration so the run's own writes cannot corrupt the alt-screen,
// is flushed to the restored terminal. It returns the run function's error (or a
// recovered panic).
func (l *Live) Run(parent context.Context, run func(context.Context) error) (runErr error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// Deferred so the terminal is restored and the muzzled stderr flushed on every
	// path, including a panic unwinding through the tview loop. They run in reverse
	// order: Fini restores the screen, then restore flushes stderr to the real
	// terminal. Fini is idempotent, composing with tview's own Fini on Stop.
	restore := muzzleStderr()
	defer restore()
	defer l.screen.Fini()

	var quitOnce sync.Once
	quitCh := make(chan struct{})
	// abort cancels the run and releases the teardown so the terminal is always
	// restored, no matter which path triggered the exit.
	abort := func() {
		cancel()
		quitOnce.Do(func() { close(quitCh) })
	}

	// A leave key (q / Esc / Ctrl-C) drives the checkpoint suspend/abort contract. On a
	// checkpointing run the first press requests a graceful suspend at the next boundary
	// and keeps the view up; a second press, a non-checkpointing run, a run already
	// ended, or a run blocked on a prompt (which cannot reach a suspend boundary) all
	// abort. Runs on the tview loop, so it mutates view state directly.
	//
	// At the interactive input bar the loop-boundary suspend flag is useless (the run is
	// parked in nextPrompt, not the loop, so it never polls the flag): the graceful leave
	// there is Ctrl-D, which delivers a clean end that the runner turns into a suspend for
	// a checkpointed chat. So Ctrl-C at the input bar (which also lands here) aborts
	// directly rather than starting a dance that could never complete.
	var suspendAsked bool
	l.v.onQuit = func() {
		if l.suspend != nil && !suspendAsked && !l.ended && l.state != stateBlocked && l.state != stateAwaitingInput {
			suspendAsked = true
			l.suspend()
			l.state = stateSuspending
			l.refreshStatus()
			l.v.appendLine(Line{Kind: LineMeta, Text: "suspend requested; finishing current step, press again to abort"})
			return
		}
		abort()
	}

	// The TUI owns the terminal in raw mode, so a keyboard Ctrl-C arrives as a key
	// event, not SIGINT. A real signal (SIGTERM, or SIGINT from elsewhere) must still
	// tear the view down cleanly rather than kill the process with the alt-screen held,
	// so it aborts through the same path that closes quitCh and restores the terminal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	sigStop := make(chan struct{})
	defer close(sigStop)
	go func() {
		select {
		case <-sigCh:
			abort()
		case <-sigStop:
		}
	}()

	// The ticker advances the working spinner while the run is active. It only repaints
	// when spinning (state == running), so an idle view (awaiting input or a terminal
	// state) is left still rather than redrawn many times a second. It is signaled to
	// stop on every exit (the defer covers a loop panic); the teardown goroutine also
	// waits for it to exit before Stop so no tick marshals onto a stopped loop.
	// Signal-only in the defer, never a wait, so it cannot hang if the loop is already
	// dead.
	tickStop := make(chan struct{})
	tickDone := make(chan struct{})
	var tickOnce sync.Once
	stopTicker := func() { tickOnce.Do(func() { close(tickStop) }) }
	defer stopTicker()

	go func() {
		defer close(tickDone)
		t := time.NewTicker(spinnerInterval)
		defer t.Stop()
		for {
			select {
			case <-tickStop:
				return
			case <-t.C:
				if l.spinning.Load() {
					l.v.app.QueueUpdateDraw(l.advanceSpinner)
				}
			}
		}
	}()

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("the run panicked: %v", r)
			}
		}()
		runErr = run(ctx)
	}()

	// One goroutine owns the teardown ordering so nothing marshals onto a stopped
	// loop: wait for the run to finish, stop the ticker (freezing elapsed and closing
	// the race window before Stop), mark the outcome in the view and bar while the
	// loop is still live, then wait for the operator to quit before stopping the loop.
	go func() {
		<-runDone
		stopTicker()
		<-tickDone
		l.markEnded(runErr)
		// A run the operator explicitly left tears down at once: they already asked to go, so
		// the "press q to quit" park would be a redundant keystroke. The answer, stats and any
		// resume hint are reprinted to the restored terminal, so nothing is lost by leaving
		// immediately. A run that ended on its own stays up so the transcript can be read.
		if !l.leaveRequested.Load() {
			l.appendCompletion(runErr)
			<-quitCh
		}
		l.v.app.Stop()
	}()

	// Paint the live bar before starting the loop so it does not sit on the static
	// text until the first tick. This runs on the goroutine that is about to become
	// the tview loop, so it is a direct call, not a QueueUpdateDraw (which would block
	// on a loop that has not started yet).
	l.refreshStatus()

	loopErr := l.v.app.Run()

	// The loop has stopped; the run goroutine finishes before Stop, so waiting on it
	// here just confirms the ordering before the deferred teardown runs.
	<-runDone

	if runErr == nil {
		runErr = loopErr
	}
	return runErr
}

// wasSuspended reports whether the run ended at a graceful suspend, using the run's
// real outcome. A suspend returns a nil error, so the intent alone is not enough: the
// run may have completed at the same boundary the operator asked to suspend.
func (l *Live) wasSuspended(runErr error) bool {
	return runErr == nil && l.suspended != nil && l.suspended()
}

// appendCompletion marks the end of the run in the view so the operator knows it is
// done and can quit. A cancellation (their own quit) is not shown as an error.
func (l *Live) appendCompletion(runErr error) {
	switch {
	case l.wasSuspended(runErr):
		l.Append(Line{Kind: LineMeta, Text: "--- run suspended, press q to quit ---"})
		if l.resumeHint != nil {
			if hint := l.resumeHint(); hint != "" {
				l.Append(Line{Kind: LineMeta, Text: hint})
			}
		}
	case runErr == nil:
		l.Append(Line{Kind: LineMeta, Text: "--- run complete, press q to quit ---"})
	case errors.Is(runErr, context.Canceled):
		l.Append(Line{Kind: LineMeta, Text: "--- run aborted, press q to quit ---"})
	default:
		l.Append(Line{Kind: LineWarning, Text: "run ended: " + runErr.Error()})
	}
}

// markEnded fixes the statusbar at the run's terminal state and stops the spinner. It
// runs on the loop after the ticker has stopped, mirroring appendCompletion's outcomes
// so the bar and the completion line agree.
func (l *Live) markEnded(runErr error) {
	l.v.app.QueueUpdateDraw(func() {
		l.ended = true
		// A run that ends before any response (an immediate error or abort) never fired
		// the first-response dismiss, so clear the card here too before the completion
		// line is drawn. Direct call: this closure already runs on the loop.
		l.v.hideSplash()
		switch {
		case l.wasSuspended(runErr):
			l.state = stateSuspended
		case runErr == nil:
			l.state = stateComplete
		case errors.Is(runErr, context.Canceled):
			l.state = stateAborted
		default:
			l.state = stateError
		}
		l.refreshStatus()
	})
}

// SeedUsage sets the live token counter to a starting total, marshaling onto the loop.
// A resumed run seeds it from the restored counters (which the resumed RunStats also
// starts from) so the live number and the end-of-run summary still agree once this
// session's own usage accumulates on top.
func (l *Live) SeedUsage(in, out, cacheRead, cacheCreate int64) {
	l.v.app.QueueUpdateDraw(func() {
		l.inTokens = in
		l.outTokens = out
		l.cacheReadTokens = cacheRead
		l.cacheCreateTokens = cacheCreate
		l.refreshStatus()
	})
}

// AddUsage accumulates a message's token usage into the live counter from the run
// goroutine, marshaling onto the loop. Summing every message's usage matches the
// end-of-run RunStats total, so the running number and the summary agree.
func (l *Live) AddUsage(in, out, cacheRead, cacheCreate int64) {
	l.v.app.QueueUpdateDraw(func() {
		l.inTokens += in
		l.outTokens += out
		l.cacheReadTokens += cacheRead
		l.cacheCreateTokens += cacheCreate
		l.refreshStatus()
	})
}

// SetBell enables ringing the terminal bell when the run blocks on an operator
// decision. It is off by default; the caller wires it from the agent config. Set
// before Run, on the setup goroutine, like SetSuspendedFunc.
func (l *Live) SetBell(enabled bool) {
	l.bell = enabled
}

// setBlocked marks the run blocked on an operator decision, recoloring the bar so an
// operator who looked away notices the run is waiting on them, and ringing the bell
// when enabled. Called from the run goroutine, cleared by setRunning when the prompt
// resolves.
func (l *Live) setBlocked() {
	l.v.app.QueueUpdateDraw(func() {
		l.state = stateBlocked
		// A prompt before the first response must not be masked by the card, so clear it
		// here too. Direct call: this closure already runs on the loop.
		l.v.hideSplash()
		// Drop any lingering copy notice (and its timer) so it cannot mask the
		// awaiting-approval bar or fire during the prompt.
		l.v.clearNotice()
		if l.bell {
			l.beep()
		}
		l.refreshStatus()
	})
}

// setRunning clears the blocked state once a prompt resolves. It only reverts from
// blocked, so a terminal state set by teardown after a ctx-canceled prompt is not
// overwritten by the prompt's own deferred clear.
func (l *Live) setRunning() {
	l.v.app.QueueUpdateDraw(func() {
		if l.state == stateBlocked {
			l.state = stateRunning
			l.refreshStatus()
		}
	})
}

// advanceSpinner steps the working-spinner frame and repaints. It runs on the loop from
// the ticker, only while spinning, so the animation advances during an LLM call.
func (l *Live) advanceSpinner() {
	l.spinnerFrame++
	l.refreshStatus()
}

// refreshStatus rebuilds the statusbar text and colors from the current live state.
// It must run on the tview loop since it reads state the draw reads and writes the
// widget the draw paints. It also republishes whether the run is working so the ticker
// knows whether to keep the spinner moving.
func (l *Live) refreshStatus() {
	l.spinning.Store(l.state == stateRunning)

	switch {
	case l.state == stateBlocked || l.state == stateSuspending:
		l.v.status.SetTextColor(tcell.ColorBlack)
		l.v.status.SetBackgroundColor(tcell.ColorYellow)
	case l.state == stateAwaitingInput:
		// A calm green bar, distinct from the amber block, signals "your turn" rather
		// than "the agent needs a decision from you".
		l.v.status.SetTextColor(tcell.ColorBlack)
		l.v.status.SetBackgroundColor(tcell.ColorGreen)
	default:
		l.v.status.SetTextColor(tcell.ColorWhite)
		l.v.status.SetBackgroundColor(tcell.ColorBlue)
	}
	l.v.status.SetText(l.v.withNotice(l.liveStatusText()))

	// While the startup card covers the statusbar, keep its waiting caption's spinner in
	// step with the same frame the bar would show, so the card reads as alive.
	if l.v.splashActive() {
		l.v.setSplashSpinner(spinnerFrames[l.spinnerFrame%len(spinnerFrames)])
	}
}

// liveStatusText is the live statusbar: a working spinner on the left where the eye
// lands (animated while the LLM works, blank when idle or done), then the token count,
// session identity and run state, then the fixed key hint. Elapsed is deliberately not
// shown: it kept climbing through idle input waits, which read as odd; the end-of-run
// summary still reports the real latency.
func (l *Live) liveStatusText() string {
	spin := " "
	if l.state == stateRunning {
		spin = spinnerFrames[l.spinnerFrame%len(spinnerFrames)]
	}

	parts := []string{
		fmt.Sprintf("tokens=%d/%d", l.inTokens, l.outTokens),
	}
	// cached=X appears only once the cache is hit, mirroring the end-of-run summary line,
	// so an uncached run's bar stays uncluttered while a caching run shows the read total
	// climbing as a live hit indicator.
	if l.cacheReadTokens > 0 {
		parts = append(parts, fmt.Sprintf("cached=%d", l.cacheReadTokens))
	}
	if l.v.meta.Model != "" {
		parts = append(parts, "model="+tview.Escape(l.v.meta.Model))
	}
	if l.v.meta.Title != "" {
		parts = append(parts, "session="+tview.Escape(l.v.meta.Title))
	}
	if word := stateWord(l.state); word != "" {
		// The bar has dynamic colors on for the bold tags, so a bracketed word like
		// "[complete]" would be parsed as a color tag and swallowed; escape it so the
		// brackets render literally.
		parts = append(parts, tview.Escape("["+word+"]"))
	}

	hint := "? help   q quit   / search   z/Z fold"
	if l.state == stateAwaitingInput {
		// Ctrl-D leaves the session: a checkpointed chat suspends (resumable), a plain
		// chat completes. Word it for what actually happens so the key does not read as
		// "end" when it in fact saves the conversation for later.
		leave := "ctrl-d end"
		if l.suspend != nil {
			leave = "ctrl-d suspend"
		}
		hint = fmt.Sprintf("enter send   %s   ctrl-c abort   tab transcript", leave)
	}

	return fmt.Sprintf(" [::b]%s  %s   |   %s[::-] ", spin, strings.Join(parts, "  "), hint)
}

// stateWord is the operator-facing label for a run state, kept even when the bar
// recolors so the information survives on a monochrome terminal. Running has no word:
// the animated spinner is its liveness cue.
func stateWord(s runState) string {
	switch s {
	case stateBlocked:
		return "awaiting approval"
	case stateSuspending:
		return "suspending"
	case stateSuspended:
		return "suspended"
	case stateComplete:
		return "complete"
	case stateAborted:
		return "aborted"
	case stateError:
		return "error"
	case stateAwaitingInput:
		return "ready for input"
	default:
		return ""
	}
}

// muzzleStderr redirects os.Stderr into a buffer for the duration of the run so the
// run's own deferred writes, the SDK, and any library logging cannot draw onto the
// alt-screen. The returned function restores os.Stderr and flushes the captured
// output to it, so nothing is lost, only deferred. It is best-effort: if a pipe
// cannot be made, stderr is left as-is.
func muzzleStderr() func() {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		return func() {}
	}
	os.Stderr = w

	var buf bytes.Buffer
	copied := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(copied)
	}()

	return func() {
		os.Stderr = old
		_ = w.Close()
		<-copied
		_ = r.Close()
		if buf.Len() > 0 {
			_, _ = io.Copy(old, &buf)
		}
	}
}

// Append adds lines to the viewport from the run goroutine, marshaling the mutation
// onto the tview loop so it never races the draw. It blocks until the loop has
// applied the update, which the teardown path keeps possible by draining the loop
// until the run goroutine returns.
func (l *Live) Append(lines ...Line) {
	if len(lines) == 0 {
		return
	}

	l.v.app.QueueUpdateDraw(func() {
		for _, ln := range lines {
			l.v.appendLine(ln)
		}
	})
}
