//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// screenTextOf reads the SimulationScreen on the loop goroutine so it never races
// the draw.
func screenTextOf(l *Live, sim tcell.SimulationScreen) func() string {
	return func() string {
		var t string
		l.v.app.QueueUpdate(func() { t = screenText(sim) })
		return t
	}
}

// dismissSplash removes the startup card and waits for it to be gone, so a test can
// drive the post-first-response view (scroll, copy, help) the way real usage reaches
// it, rather than acting on keys the card would otherwise intercept.
func dismissSplash(l *Live, sim tcell.SimulationScreen) {
	l.HideSplash()
	Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("waiting for first response"))
}

var _ = Describe("Live", func() {
	It("Should append event lines into the viewport", func() {
		sim := tcell.NewSimulationScreen("")
		l := newLive(sim, Meta{Title: "run"}, true, nil)

		done := make(chan error, 1)
		go func() { done <- l.v.app.Run() }()

		l.Append(Line{Kind: LineToolCall, Text: "stream ls"})
		l.Append(Line{Kind: LineNarration, Text: "all done"})
		l.v.app.QueueUpdateDraw(func() {})
		text := screenText(sim)

		l.v.app.Stop()
		Eventually(done, time.Second).Should(Receive(BeNil()))

		Expect(text).To(ContainSubstring("stream ls"))
		Expect(text).To(ContainSubstring("all done"))
	})

	Describe("Run", func() {
		It("Should show the run output, stay up until quit, and restore stderr", func() {
			orig := os.Stderr
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Title: "t"}, true, nil)

			run := func(context.Context) error {
				l.Append(Line{Kind: LineNarration, Text: "run output here"})
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("run complete"))
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("run output here"))

			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
			Expect(os.Stderr).To(Equal(orig))
		})

		It("Should surface a run error in the view and return it", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Title: "t"}, true, nil)

			run := func(context.Context) error { return context.DeadlineExceeded }

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("run ended"))

			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(MatchError(context.DeadlineExceeded)))
		})

		It("Should show the terminal state and stop the spinner on completion", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), func(context.Context) error { return nil }) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("[complete]"))
			// A completed run is idle, so the spinner is stopped (blank slot, not a frame).
			Expect(l.spinning.Load()).To(BeFalse())

			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should abort a still-running run when the operator quits with Ctrl-C", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Title: "t"}, true, nil)

			started := make(chan struct{})
			run := func(ctx context.Context) error {
				l.Append(Line{Kind: LineNarration, Text: "working"})
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			<-started
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("working"))

			sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(MatchError(context.Canceled)))
		})
	})

	Describe("tail-follow", func() {
		It("Should freeze on scroll-up and resume follow when scrolled back to the bottom", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Title: "t"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()
			dismissSplash(l, sim)

			// Fill well past a screen so there is room to scroll.
			for i := 0; i < 60; i++ {
				l.Append(Line{Kind: LineMeta, Text: fmt.Sprintf("line-%d", i)})
			}
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("line-59"))

			// Scroll up: follow freezes, so the tail leaves the screen and a newly
			// appended line does not appear.
			sim.InjectKey(tcell.KeyPgUp, 0, tcell.ModNone)
			Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("line-59"))
			l.Append(Line{Kind: LineMeta, Text: "frozen-line"})
			Consistently(screenTextOf(l, sim), 200*time.Millisecond).ShouldNot(ContainSubstring("frozen-line"))

			// Scroll back to the bottom with Down: follow resumes, so the next line
			// appends into view like tail -f.
			for i := 0; i < 80; i++ {
				sim.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
			}
			l.Append(Line{Kind: LineMeta, Text: "tailed-line"})
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("tailed-line"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})
	})

	Describe("chat (interactive follow-up)", func() {
		typeRunes := func(sim tcell.SimulationScreen, s string) {
			for _, r := range s {
				sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
			}
		}

		// promptRows reads the input row's applied height on the loop goroutine, so a test
		// can assert the row grew or collapsed without racing the draw.
		promptRows := func(l *Live) func() int {
			return func() int {
				var n int
				l.v.app.QueueUpdate(func() { n = l.v.promptRows })
				return n
			}
		}

		It("Should open the input row, accept a follow-up, continue, and end on Ctrl-D", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()

			run := func(ctx context.Context) error {
				next := l.NextPromptFunc()
				l.Append(Line{Kind: LineNarration, Text: "first answer"})
				text, _, cont := next(ctx)
				if !cont {
					return nil
				}
				l.Append(Line{Kind: LineNarration, Text: "you said " + text})
				next(ctx)
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			// The header badge advertises chat mode, and the row opens focused after the
			// first turn: the statusbar reads ready and the grey hint shows in the field.
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("[chat]"))
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("ready for input"),
				ContainSubstring("Ready for a follow-up"),
			))
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("first answer"))

			// The field is already focused, so typing lands in it. The 'q' proves the
			// field owns keys rather than quitting.
			typeRunes(sim, "explain q")
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

			// The follow-up is echoed and the next turn runs, then the row reopens.
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("> explain q"),
				ContainSubstring("you said explain q"),
				ContainSubstring("ready for input"),
			))

			// Ctrl-D ends the session cleanly; the operator asked to leave, so the view tears
			// down at once with no extra keypress.
			sim.InjectKey(tcell.KeyCtrlD, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should recall submitted follow-ups with Up/Down and stash the draft", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()
			v := l.v

			v.pushHistory("first")
			v.pushHistory("second")
			v.histIdx = len(v.history)
			v.promptInput.SetText("draft", true)

			v.historyPrev()
			Expect(v.promptInput.GetText()).To(Equal("second"))
			v.historyPrev()
			Expect(v.promptInput.GetText()).To(Equal("first"))
			v.historyPrev() // already at the oldest entry: no-op
			Expect(v.promptInput.GetText()).To(Equal("first"))

			v.historyNext()
			Expect(v.promptInput.GetText()).To(Equal("second"))
			v.historyNext() // back past the newest entry: the stashed draft returns
			Expect(v.promptInput.GetText()).To(Equal("draft"))
			v.historyNext() // already at the draft: no-op
			Expect(v.promptInput.GetText()).To(Equal("draft"))
		})

		It("Should grow the input row for a multi-line draft and collapse it after sending", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()

			delivered := make(chan string, 1)
			run := func(ctx context.Context) error {
				next := l.NextPromptFunc()
				l.Append(Line{Kind: LineNarration, Text: "answer"})
				text, _, _ := next(ctx)
				delivered <- text
				next(ctx)
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			// The row opens at one line on the first turn boundary.
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("Ready for a follow-up"))
			Eventually(promptRows(l), time.Second).Should(Equal(1))

			// A line, an Alt-Enter newline, then another line: the row grows to two and both
			// lines stay visible (the first must not scroll off as the box grows).
			typeRunes(sim, "one")
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModAlt)
			typeRunes(sim, "two")
			Eventually(promptRows(l), time.Second).Should(Equal(2))
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("> one"),
				ContainSubstring("two"),
			))

			// Plain Enter sends the whole multi-line draft, newlines intact, and the row
			// reopens empty at a single line on the next turn boundary.
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			Eventually(delivered, time.Second).Should(Receive(Equal("one\ntwo")))
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("> one"),
				ContainSubstring("Ready for a follow-up"),
			))
			Eventually(promptRows(l), time.Second).Should(Equal(1))

			sim.InjectKey(tcell.KeyCtrlD, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should deliver a reset for /clear and mark the cleared context", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()

			type turn struct {
				text  string
				reset bool
				cont  bool
			}
			delivered := make(chan turn, 1)
			run := func(ctx context.Context) error {
				next := l.NextPromptFunc()
				l.Append(Line{Kind: LineNarration, Text: "answer"})
				text, reset, cont := next(ctx)
				delivered <- turn{text, reset, cont}
				next(ctx)
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("Ready for a follow-up"))

			typeRunes(sim, "/clear hello")
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

			Eventually(delivered, time.Second).Should(Receive(Equal(turn{text: "hello", reset: true, cont: true})))
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("context cleared"),
				ContainSubstring("> hello"),
			))

			sim.InjectKey(tcell.KeyCtrlD, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should report an unknown command and keep the input open", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()

			run := func(ctx context.Context) error {
				next := l.NextPromptFunc()
				l.Append(Line{Kind: LineNarration, Text: "answer"})
				next(ctx)
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("Ready for a follow-up"))

			typeRunes(sim, "/bogus")
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

			// The command is rejected with a notice and the input row stays open rather than
			// delivering anything to the run goroutine.
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("unknown command: /bogus"))
			Consistently(out, 200*time.Millisecond).ShouldNot(Receive())

			sim.InjectKey(tcell.KeyCtrlD, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should skip a consecutive duplicate in history", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()
			v := l.v

			v.pushHistory("same")
			v.pushHistory("same")
			v.pushHistory("other")
			Expect(v.history).To(Equal([]string{"same", "other"}))
		})

		It("Should fold thinking by default", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()
			Expect(l.v.foldThinking).To(BeTrue())
		})

		It("Should abort the session with Ctrl-C while the input row is open", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, nil)
			l.EnableInteractive()

			run := func(ctx context.Context) error {
				next := l.NextPromptFunc()
				l.Append(Line{Kind: LineNarration, Text: "answer"})
				_, _, cont := next(ctx)
				if !cont {
					return ctx.Err()
				}
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("ready for input"))
			sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(MatchError(context.Canceled)))
		})

		It("Should word the input-bar leave key as suspend for a checkpointed chat, and end otherwise", func() {
			checkpointed := newLive(tcell.NewSimulationScreen(""), Meta{Model: "m", Interactive: true}, true, func() {})
			checkpointed.EnableInteractive()
			checkpointed.state = stateAwaitingInput
			Expect(checkpointed.liveStatusText()).To(ContainSubstring("ctrl-d suspend"))

			plain := newLive(tcell.NewSimulationScreen(""), Meta{Model: "m", Interactive: true}, true, nil)
			plain.EnableInteractive()
			plain.state = stateAwaitingInput
			Expect(plain.liveStatusText()).To(ContainSubstring("ctrl-d end"))
		})

		It("Should abort a checkpointed chat with Ctrl-C at the input bar rather than starting a doomed suspend", func() {
			sim := tcell.NewSimulationScreen("")
			var suspendCalled atomic.Bool
			l := newLive(sim, Meta{Model: "m", Interactive: true}, true, func() { suspendCalled.Store(true) })
			l.EnableInteractive()

			run := func(ctx context.Context) error {
				next := l.NextPromptFunc()
				l.Append(Line{Kind: LineNarration, Text: "answer"})
				_, _, cont := next(ctx)
				if !cont {
					return ctx.Err()
				}
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("ready for input"))
			// The run is parked in the input bar, not the loop, so the loop-boundary suspend
			// flag can never be polled: Ctrl-C must abort and tear down, not start a suspend
			// dance that would hang. The suspend request is never made from here.
			sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(MatchError(context.Canceled)))
			Expect(suspendCalled.Load()).To(BeFalse())
		})
	})

	Describe("startup splash", func() {
		It("Should draw the startup card until the first response, then remove it", func() {
			sim := tcell.NewSimulationScreen("")
			sim.SetSize(100, 30)
			l := newLive(sim, Meta{Model: "opus", Version: "1.2.3", Dir: "~/work"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			// A first real draw with a full-width screen used to deadlock under the
			// application lock; reaching this text proves the loop draws and the card is up.
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("waiting for first response"),
				ContainSubstring("version"),
				ContainSubstring("1.2.3"),
				ContainSubstring("~/work"),
			))

			// The first response dismisses the card and reveals the transcript.
			l.HideSplash()
			l.Append(Line{Kind: LineNarration, Text: "first answer"})
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("first answer"))
			Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("waiting for first response"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should let a leave key abort while the card is still up", func() {
			sim := tcell.NewSimulationScreen("")
			sim.SetSize(100, 30)
			l := newLive(sim, Meta{Model: "opus"}, true, nil)

			started := make(chan struct{})
			run := func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			<-started
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("waiting for first response"))

			// The statusbar hint is hidden behind the card, but the leave key still aborts.
			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(MatchError(context.Canceled)))
		})

		It("Should not draw the startup card for a resumed session", func() {
			sim := tcell.NewSimulationScreen("")
			sim.SetSize(100, 30)
			l := newLive(sim, Meta{Model: "opus", Version: "1.2.3", Dir: "~/work", Resume: true}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			// A resumed run has its first response in hand, so the card never appears; its
			// restored transcript draws straight away with no waiting caption to flash.
			l.Append(Line{Kind: LineNarration, Text: "restored answer"})
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("restored answer"))
			Consistently(screenTextOf(l, sim), 200*time.Millisecond).ShouldNot(ContainSubstring("waiting for first response"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})
	})

	Describe("statusbar", func() {
		It("Should accumulate token usage in the bar", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			l.AddUsage(100, 40, 0, 0)
			l.AddUsage(50, 10, 0, 0)
			l.v.app.QueueUpdateDraw(l.refreshStatus)
			text := screenTextOf(l, sim)()

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))

			Expect(text).To(ContainSubstring("tokens=150/50"))
		})

		It("Should show the cache read count only once the cache is hit", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			l.AddUsage(20, 5, 0, 0)
			l.v.app.QueueUpdateDraw(l.refreshStatus)
			Expect(screenTextOf(l, sim)()).NotTo(ContainSubstring("cached="))

			l.AddUsage(10, 5, 4096, 0)
			l.v.app.QueueUpdateDraw(l.refreshStatus)
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("cached=4096"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should spin while the run works and stop the spinner when it is idle or done", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			release := make(chan struct{})
			run := func(context.Context) error {
				l.Append(Line{Kind: LineNarration, Text: "working"})
				<-release
				return nil
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("working"))
			// While the run works the spinner animates: its frame advances over time.
			Eventually(l.spinning.Load, time.Second).Should(BeTrue())
			frame := func() int {
				var f int
				l.v.app.QueueUpdate(func() { f = l.spinnerFrame })
				return f
			}
			first := frame()
			Eventually(frame, time.Second).Should(BeNumerically(">", first))

			// Once the run finishes it is idle, so the spinner stops.
			close(release)
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("[complete]"))
			Expect(l.spinning.Load()).To(BeFalse())

			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should mark the bar awaiting approval while a prompt is up and clear it once answered", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			ans := make(chan bool, 1)
			go func() {
				r, _ := l.prompter.Confirm(context.Background(), "proceed")
				ans <- r
			}()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("proceed"))
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("awaiting approval"))

			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			Eventually(ans, time.Second).Should(Receive(BeFalse()))
			Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("awaiting approval"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should ring the bell when a prompt blocks the run and the bell is enabled", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)
			beeps := make(chan struct{}, 8)
			l.beep = func() { beeps <- struct{}{} }
			l.SetBell(true)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			ans := make(chan bool, 1)
			go func() {
				r, _ := l.prompter.Confirm(context.Background(), "proceed")
				ans <- r
			}()

			Eventually(beeps, time.Second).Should(Receive())

			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			Eventually(ans, time.Second).Should(Receive(BeFalse()))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should not ring the bell when it is disabled", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)
			beeps := make(chan struct{}, 8)
			l.beep = func() { beeps <- struct{}{} }

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			ans := make(chan bool, 1)
			go func() {
				r, _ := l.prompter.Confirm(context.Background(), "proceed")
				ans <- r
			}()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("proceed"))
			Consistently(beeps, 200*time.Millisecond).ShouldNot(Receive())

			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			Eventually(ans, time.Second).Should(Receive(BeFalse()))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should copy the transcript, survive a tick, and clear on the next key", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()
			dismissSplash(l, sim)

			l.Append(Line{Kind: LineNarration, Text: "hello world"})
			l.v.app.QueueUpdateDraw(func() {})

			sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("sent 1 line to clipboard"))

			var clip []byte
			l.v.app.QueueUpdate(func() { clip = sim.GetClipboardData() })
			Expect(string(clip)).To(Equal("hello world"))

			// A statusbar repaint (as the ticker would do) must not wipe the notice.
			l.v.app.QueueUpdateDraw(l.refreshStatus)
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("sent 1 line to clipboard"))

			// The next key dismisses it.
			sim.InjectKey(tcell.KeyRune, 'j', tcell.ModNone)
			Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("sent 1 line"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should auto-clear the copy notice after the TTL with no key pressed", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)
			l.v.noticeTTL = 80 * time.Millisecond

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()
			dismissSplash(l, sim)

			l.Append(Line{Kind: LineNarration, Text: "hello world"})
			l.v.app.QueueUpdateDraw(func() {})

			sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("sent 1 line to clipboard"))
			Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("sent 1 line to clipboard"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should ignore a copy request on an empty transcript", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()
			dismissSplash(l, sim)
			l.v.app.QueueUpdateDraw(func() {})

			sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
			Consistently(screenTextOf(l, sim), 200*time.Millisecond, 50*time.Millisecond).ShouldNot(ContainSubstring("sent"))

			var clip []byte
			l.v.app.QueueUpdate(func() { clip = sim.GetClipboardData() })
			Expect(clip).To(BeNil())

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should clear the awaiting-approval state when a prompt is canceled", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			ctx, cancel := context.WithCancel(context.Background())
			ans := make(chan error, 1)
			go func() {
				_, e := l.prompter.Confirm(ctx, "proceed")
				ans <- e
			}()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("awaiting approval"))

			cancel()
			Eventually(ans, time.Second).Should(Receive(MatchError(context.Canceled)))
			Eventually(screenTextOf(l, sim), time.Second).ShouldNot(ContainSubstring("awaiting approval"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})
	})

	Describe("suspend", func() {
		It("Should request a graceful suspend on the first leave key and end suspended", func() {
			sim := tcell.NewSimulationScreen("")
			var suspendFlag atomic.Bool
			l := newLive(sim, Meta{Model: "m"}, true, func() { suspendFlag.Store(true) })
			l.SetSuspendedFunc(func() bool { return suspendFlag.Load() })

			release := make(chan struct{})
			run := func(ctx context.Context) error {
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()

			// First press requests the suspend; the run keeps going.
			sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("suspend requested"))
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("suspending"))
			Consistently(out, 100*time.Millisecond).ShouldNot(Receive())

			// Reaching the boundary ends the run as suspended, and the view stays up.
			close(release)
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("run suspended"))
			Expect(screenTextOf(l, sim)()).To(ContainSubstring("[suspended]"))

			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should abort on the second leave key after a suspend request", func() {
			sim := tcell.NewSimulationScreen("")
			var suspendFlag atomic.Bool
			l := newLive(sim, Meta{Model: "m"}, true, func() { suspendFlag.Store(true) })
			l.SetSuspendedFunc(func() bool { return suspendFlag.Load() })

			started := make(chan struct{})
			run := func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), run) }()
			<-started

			sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("suspending"))

			// Second press aborts and tears down.
			sim.InjectKey(tcell.KeyCtrlC, 0, tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(MatchError(context.Canceled)))
		})

		It("Should not mislabel a checkpointing run that completes as suspended", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, func() {})
			// The run completed, not suspended: res.Reason is not ReasonSuspended.
			l.SetSuspendedFunc(func() bool { return false })

			out := make(chan error, 1)
			go func() { out <- l.Run(context.Background(), func(context.Context) error { return nil }) }()

			Eventually(screenTextOf(l, sim), time.Second).Should(ContainSubstring("[complete]"))
			Expect(screenTextOf(l, sim)()).NotTo(ContainSubstring("suspended"))

			sim.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
			Eventually(out, time.Second).Should(Receive(BeNil()))
		})

		It("Should seed the token counter so a resumed run agrees with the summary", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, nil)

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()

			l.SeedUsage(500, 200, 0, 0)
			l.AddUsage(30, 10, 0, 0)
			l.v.app.QueueUpdateDraw(l.refreshStatus)

			Expect(screenTextOf(l, sim)()).To(ContainSubstring("tokens=530/210"))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should reword the help for a checkpointing run", func() {
			sim := tcell.NewSimulationScreen("")
			l := newLive(sim, Meta{Model: "m"}, true, func() {})

			done := make(chan error, 1)
			go func() { done <- l.v.app.Run() }()
			dismissSplash(l, sim)

			l.v.app.QueueUpdateDraw(func() { l.v.pages.ShowPage("help") })
			Eventually(screenTextOf(l, sim), time.Second).Should(And(
				ContainSubstring("suspend (again: abort)"),
				ContainSubstring("https://choria.io"),
			))

			l.v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})
	})
})
