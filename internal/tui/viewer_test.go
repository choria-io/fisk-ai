//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTui(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/TUI")
}

// finiCounter is a stand-in tcell.Screen that only records Fini calls, so the
// idempotent wrapper can be tested without a real terminal.
type finiCounter struct {
	tcell.Screen
	calls int
}

func (f *finiCounter) Fini() { f.calls++ }

// drawViewer runs a viewer on a SimulationScreen, forces a draw, and returns the
// on-screen text. It always stops the loop and restores the screen.
func drawViewer(meta Meta, lines []Line) string {
	GinkgoHelper()

	sim := tcell.NewSimulationScreen("")
	v := newViewer(meta, lines, true, false)
	v.app.SetScreen(sim)
	v.app.SetRoot(v.pages, true).SetFocus(v.view)

	done := make(chan error, 1)
	go func() { done <- v.app.Run() }()

	// QueueUpdateDraw blocks until the update has run and the screen redrawn, so the
	// contents are stable when it returns even though the loop runs concurrently.
	v.app.QueueUpdateDraw(func() {})
	text := screenText(sim)

	v.app.Stop()
	Eventually(done, time.Second).Should(Receive(BeNil()))

	return text
}

// screenText flattens a SimulationScreen's cells into rows of text.
func screenText(sim tcell.SimulationScreen) string {
	cells, w, h := sim.GetContents()

	var b strings.Builder
	for row := 0; row < h; row++ {
		for col := 0; col < w; col++ {
			c := cells[row*w+col]
			if len(c.Runes) == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteString(string(c.Runes))
		}
		b.WriteByte('\n')
	}

	return b.String()
}

var _ = Describe("transcript viewer", func() {
	Describe("renderLine", func() {
		It("Should neutralize tview markup in model text so a literal tag is not interpreted", func() {
			out := renderLine(Line{Kind: LineNarration, Text: "see [red]this[white]"})
			// The escaped form keeps the tag text but breaks the opening bracket so
			// tview renders it literally rather than as a color directive.
			Expect(out).To(ContainSubstring("red"))
			Expect(out).NotTo(ContainSubstring("[red]"))
		})

		It("Should strip terminal escape sequences from model text", func() {
			out := renderLine(Line{Kind: LineNarration, Text: "a\x1b[31mb\x1b[0mc"})
			Expect(out).To(Equal("abc"))
		})

		It("Should carry the line-UI glyph prefixes for each kind", func() {
			Expect(renderLine(Line{Kind: LineToolCall, Text: "stream rm"})).To(ContainSubstring("-> stream rm"))
			Expect(renderLine(Line{Kind: LineToolResult, Text: "ok"})).To(ContainSubstring("<-\nok"))
			Expect(renderLine(Line{Kind: LineToolError, Text: "boom"})).To(ContainSubstring("<-\nboom"))
			Expect(renderLine(Line{Kind: LinePrompt, Text: "do it"})).To(ContainSubstring("> do it"))
			Expect(renderLine(Line{Kind: LineWarning, Text: "careful"})).To(ContainSubstring("warning: careful"))
		})
	})

	Describe("resolveCommand", func() {
		It("resolves known commands, aliases, arguments and escapes", func() {
			cmd, args, ok := resolveCommand("/clear")
			Expect(ok).To(BeTrue())
			Expect(cmd).NotTo(BeNil())
			Expect(cmd.name).To(Equal("clear"))
			Expect(args).To(Equal(""))

			cmd, args, ok = resolveCommand("/clear  do the thing")
			Expect(ok).To(BeTrue())
			Expect(cmd.name).To(Equal("clear"))
			Expect(args).To(Equal("do the thing"))

			// Arguments keep their internal newlines so a multi-line prompt survives.
			cmd, args, _ = resolveCommand("/clear line one\nline two")
			Expect(cmd.name).To(Equal("clear"))
			Expect(args).To(Equal("line one\nline two"))

			// The command word is case-insensitive.
			cmd, _, ok = resolveCommand("/CLEAR")
			Expect(ok).To(BeTrue())
			Expect(cmd.name).To(Equal("clear"))

			// Aliases resolve to their primary command.
			for _, alias := range []string{"/redo", "/repeat"} {
				cmd, _, ok = resolveCommand(alias)
				Expect(ok).To(BeTrue())
				Expect(cmd.name).To(Equal("restart"))
			}
			cmd, _, ok = resolveCommand("/quit")
			Expect(ok).To(BeTrue())
			Expect(cmd.name).To(Equal("exit"))

			// A slash-shaped line naming nothing known is matched but has no command.
			cmd, _, ok = resolveCommand("/bogus now")
			Expect(ok).To(BeTrue())
			Expect(cmd).To(BeNil())

			// An ordinary prompt is not a command.
			_, _, ok = resolveCommand("hello there")
			Expect(ok).To(BeFalse())

			// A "//" escape is an ordinary prompt, not a command.
			_, _, ok = resolveCommand("//usr/bin")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("toolCallText", func() {
		full := "stream add ORDERS --subject=orders.events.created"
		short := "stream add ORDERS --subject=orders.eve...reated"

		It("Should keep the full command when it fits the row", func() {
			Expect(toolCallText(Line{Kind: LineToolCall, Text: full, Short: short}, 120)).To(Equal(full))
		})

		It("Should fall back to the short form when the full command would overflow the row", func() {
			Expect(toolCallText(Line{Kind: LineToolCall, Text: full, Short: short}, 30)).To(Equal(short))
		})

		It("Should re-pick between the two forms as the width changes, as a resize re-renders", func() {
			l := Line{Kind: LineToolCall, Text: full, Short: short}
			Expect(toolCallText(l, 120)).To(Equal(full))
			Expect(toolCallText(l, 30)).To(Equal(short))
			Expect(toolCallText(l, 120)).To(Equal(full))
		})

		It("Should never elide a line with no short form, whatever the width", func() {
			Expect(toolCallText(Line{Kind: LineToolCall, Text: full}, 10)).To(Equal(full))
		})

		It("Should account for the glyph prefix so a line that exactly fills the row still elides", func() {
			// The full command is 49 runes; at width 52 the content budget after the
			// "-> " prefix and the one-cell margin is 48, one short of fitting.
			Expect(toolCallText(Line{Kind: LineToolCall, Text: full, Short: short}, 52)).To(Equal(short))
			Expect(toolCallText(Line{Kind: LineToolCall, Text: full, Short: short}, 53)).To(Equal(full))
		})
	})

	Describe("breakBefore", func() {
		It("Should break at each prose/tools boundary but keep a group tight and ignore the prompt", func() {
			v := newViewer(Meta{}, []Line{
				{Kind: LinePrompt, Text: "go"},
				{Kind: LineNarration, Text: "first turn"},
				{Kind: LineToolCall, Text: "call"},
				{Kind: LineToolResult, Text: "res"},
				{Kind: LineNarration, Text: "second turn"},
				{Kind: LineToolCall, Text: "call2"},
				{Kind: LineToolError, Text: "boom"},
				{Kind: LineThinking, Text: "third turn thinking"},
			}, true, false)

			Expect(v.breakBefore(0)).To(BeFalse()) // nothing precedes the prompt
			Expect(v.breakBefore(1)).To(BeFalse()) // narration right after the prompt
			Expect(v.breakBefore(2)).To(BeTrue())  // first tool call after the narration
			Expect(v.breakBefore(3)).To(BeFalse()) // a result stays with its call
			Expect(v.breakBefore(4)).To(BeTrue())  // narration after a tool result
			Expect(v.breakBefore(5)).To(BeTrue())  // first tool call after the narration
			Expect(v.breakBefore(6)).To(BeFalse()) // an error stays with its call
			Expect(v.breakBefore(7)).To(BeTrue())  // thinking after a tool error
		})

		It("Should render the break as a leading blank line on the boundary line's markup", func() {
			v := newViewer(Meta{}, []Line{
				{Kind: LineNarration, Text: "some prose"},
				{Kind: LineToolCall, Text: "call"},
				{Kind: LineToolResult, Text: "res"},
				{Kind: LineNarration, Text: "next turn"},
			}, true, false)

			v.renderAll(80)
			Expect(v.rendered[0]).NotTo(HavePrefix("\n")) // first line
			Expect(v.rendered[1]).To(HavePrefix("\n"))    // call after prose
			Expect(v.rendered[2]).NotTo(HavePrefix("\n")) // result stays with its call
			Expect(v.rendered[3]).To(HavePrefix("\n"))    // narration after result
		})
	})

	Describe("statusText", func() {
		It("Should always show the quit and help hints so the operator is never stranded", func() {
			out := statusText(Meta{Title: "run-1", Model: "claude"})
			Expect(out).To(ContainSubstring("session=run-1"))
			Expect(out).To(ContainSubstring("model=claude"))
			Expect(out).To(ContainSubstring("q quit"))
			Expect(out).To(ContainSubstring("? help"))
			Expect(out).To(ContainSubstring("z/Z fold"))
		})

		It("Should omit the model when it is unknown", func() {
			Expect(statusText(Meta{Title: "run-1"})).NotTo(ContainSubstring("model="))
		})

		It("Should show the token count like the live bar when usage is set", func() {
			Expect(statusText(Meta{Title: "run-1", InTokens: 1200, OutTokens: 340})).To(ContainSubstring("tokens=1200/340"))
		})

		It("Should omit the token count when there is no usage rather than show 0/0", func() {
			Expect(statusText(Meta{Title: "run-1"})).NotTo(ContainSubstring("tokens="))
		})
	})

	Describe("help overlay", func() {
		It("Should show the logo, key bindings and project url when opened", func() {
			sim := tcell.NewSimulationScreen("")
			sim.SetSize(100, 40)
			v := newViewer(Meta{Title: "run-1", Model: "opus"}, []Line{{Kind: LineNarration, Text: "hi"}}, true, false)
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()

			sim.InjectKey(tcell.KeyRune, '?', tcell.ModNone)
			Eventually(func() string {
				var t string
				v.app.QueueUpdate(func() { t = screenText(sim) })
				return t
			}, time.Second).Should(And(
				ContainSubstring("Keys"),
				ContainSubstring("copy full transcript"),
				ContainSubstring("https://choria.io"),
			))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})
	})

	Describe("header", func() {
		It("Should show just the logo when there is no version or query", func() {
			h := headerText(Meta{Title: "run-1", Model: "opus"})
			Expect(h).To(ContainSubstring("o(((c"))
			Expect(h).NotTo(ContainSubstring("fisk-ai "))
			Expect(h).NotTo(ContainSubstring("query:"))
		})

		It("Should show the version and a query preview", func() {
			h := headerText(Meta{Version: "v1.2.3", Query: "delete the orders stream"})
			Expect(h).To(ContainSubstring("fisk-ai v1.2.3"))
			Expect(h).To(ContainSubstring("query: delete the orders stream"))
		})

		It("Should collapse a multi-line query onto one line", func() {
			Expect(headerText(Meta{Query: "line one\n\tline two"})).To(ContainSubstring("query: line one line two"))
		})

		It("Should truncate on rune boundaries, never splitting a character", func() {
			Expect(truncateRunes("hello", 10)).To(Equal("hello"))
			Expect(truncateRunes("hello world", 5)).To(Equal("hello..."))
			Expect(truncateRunes("héllo", 2)).To(Equal("hé..."))
		})

		It("Should draw the header band and render query markup literally", func() {
			text := drawViewer(Meta{Version: "v9", Query: "see [red]this"}, []Line{{Kind: LineNarration, Text: "hi"}})
			Expect(text).To(ContainSubstring("fisk-ai v9"))
			Expect(text).To(ContainSubstring("[red]this"))
		})
	})

	Describe("fold helpers", func() {
		It("Should count a single very long line as the many rows it wraps to", func() {
			// One source line, no newlines, far wider than the viewport: it must fold.
			long := strings.Repeat("x", 500)
			Expect(foldRows(long, 80)).To(BeNumerically(">=", foldThreshold))
			// Short source lines each count as one row.
			Expect(foldRows("a\nb\nc", 80)).To(Equal(3))
			// Width 0 (pre-draw) falls back to source-line counting without dividing.
			Expect(foldRows(long, 0)).To(Equal(1))
		})

		It("Should word the placeholder for the kind, with a singular unit at one row", func() {
			Expect(foldPlaceholder(LineThinking, 7)).To(ContainSubstring("7 lines of thinking (z to expand)"))
			Expect(foldPlaceholder(LineToolResult, 12)).To(ContainSubstring("12 lines of output (Z to expand)"))
			Expect(foldPlaceholder(LineThinking, 1)).To(ContainSubstring("1 line of thinking"))
			// A folded error keeps a distinct, red placeholder so a failure is not lost
			// behind the same neutral stand-in as a successful result.
			errPlaceholder := foldPlaceholder(LineToolError, 12)
			Expect(errPlaceholder).To(ContainSubstring("12 lines of error output (Z to expand)"))
			Expect(errPlaceholder).To(ContainSubstring("[red]"))
		})

		It("Should word the notice as the new state per kind", func() {
			Expect(foldNotice(LineThinking, true)).To(Equal("thinking folded"))
			Expect(foldNotice(LineThinking, false)).To(Equal("thinking expanded"))
			Expect(foldNotice(LineToolResult, true)).To(Equal("output folded"))
		})
	})

	Describe("idempotentScreen", func() {
		It("Should restore the terminal exactly once no matter how many owners call Fini", func() {
			fc := &finiCounter{}
			s := &idempotentScreen{Screen: fc}

			s.Fini()
			s.Fini()
			s.Fini()

			Expect(fc.calls).To(Equal(1))
		})
	})

	Describe("rendering to a screen", func() {
		lines := []Line{
			{Kind: LinePrompt, Text: "delete the orders stream"},
			{Kind: LineNarration, Text: "I will remove it now."},
			{Kind: LineToolCall, Text: "stream rm ORDERS"},
		}

		It("Should draw the transcript and the statusbar hint", func() {
			text := drawViewer(Meta{Title: "run-42", Model: "opus"}, lines)

			Expect(text).To(ContainSubstring("delete the orders stream"))
			Expect(text).To(ContainSubstring("stream rm ORDERS"))
			Expect(text).To(ContainSubstring("q quit"))
			Expect(text).To(ContainSubstring("session=run-42"))
		})

		It("Should render narration markdown without losing the content", func() {
			text := drawViewer(Meta{Title: "r"}, []Line{
				{Kind: LineNarration, Text: "Here is **bold** and a list:\n\n- one\n- two"},
			})

			Expect(text).To(ContainSubstring("bold"))
			Expect(text).To(ContainSubstring("one"))
			Expect(text).To(ContainSubstring("two"))
		})

		It("Should render an injected color tag literally rather than acting on it", func() {
			text := drawViewer(Meta{Title: "run-42"}, []Line{
				{Kind: LineNarration, Text: "payload [red]danger[white] end"},
			})

			// If the tag were interpreted the brackets would be consumed; seeing the
			// literal bracketed text on screen proves the escaping held.
			Expect(text).To(ContainSubstring("[red]danger"))
		})

		It("Should follow the tail as lines are appended to a live view", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "live"}, nil, true, true)
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()

			for i := 0; i < 60; i++ {
				n := i
				v.app.QueueUpdateDraw(func() {
					v.appendLine(Line{Kind: LineToolCall, Text: fmt.Sprintf("event-%d", n)})
				})
			}
			text := screenText(sim)

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))

			// The newest line is on screen and the oldest has scrolled off the top,
			// so the view tracked the tail rather than staying pinned at the start.
			Expect(text).To(ContainSubstring("event-59"))
			Expect(text).NotTo(ContainSubstring("event-0 "))
		})

		It("Should post the transcript to the clipboard and confirm when y is pressed", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "r"}, []Line{
				{Kind: LinePrompt, Text: "do a thing"},
				{Kind: LineToolCall, Text: "stream ls"},
			}, true, false)
			v.noticeTTL = 80 * time.Millisecond
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)

			readScreen := func() string {
				var t string
				v.app.QueueUpdate(func() { t = screenText(sim) })
				return t
			}
			Eventually(readScreen, time.Second).Should(ContainSubstring("sent 2 lines to clipboard"))

			var clip []byte
			v.app.QueueUpdate(func() { clip = sim.GetClipboardData() })
			Expect(string(clip)).To(Equal("do a thing\nstream ls"))

			// With no key pressed the notice clears itself once the TTL elapses.
			Eventually(readScreen, time.Second).ShouldNot(ContainSubstring("sent 2 lines to clipboard"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should repeat the previous search when the query is empty", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "s"}, []Line{
				{Kind: LineNarration, Text: "alpha one"},
				{Kind: LineNarration, Text: "beta two"},
				{Kind: LineNarration, Text: "alpha three"},
			}, true, false)
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			matchOf := func() int {
				var m int
				v.app.QueueUpdate(func() { m = v.match })
				return m
			}

			// "/alpha" finds the first alpha line.
			sim.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
			sim.InjectKeyBytes([]byte("alpha"))
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			Eventually(matchOf, time.Second).Should(Equal(0))

			// A bare "/" then Enter repeats it, advancing to the next alpha line.
			sim.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
			Eventually(matchOf, time.Second).Should(Equal(2))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should collapse a large thinking block to a placeholder and restore it on a second press", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "f"}, []Line{
				{Kind: LineNarration, Text: "narration stays"},
				{Kind: LineThinking, Text: "l1\nl2\nl3\nl4\nl5\nl6\nSECRETTHOUGHT"},
			}, true, false)
			v.noticeTTL = 80 * time.Millisecond
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			readScreen := func() string {
				var t string
				v.app.QueueUpdate(func() { t = screenText(sim) })
				return t
			}

			// Before folding the block content is on screen.
			Expect(readScreen()).To(ContainSubstring("SECRETTHOUGHT"))

			// z hides it behind a placeholder that names the count and the expand key.
			sim.InjectKey(tcell.KeyRune, 'z', tcell.ModNone)
			Eventually(readScreen, time.Second).Should(ContainSubstring("7 lines of thinking (z to expand)"))
			Expect(readScreen()).NotTo(ContainSubstring("SECRETTHOUGHT"))
			// Narration is never foldable, so it stays put.
			Expect(readScreen()).To(ContainSubstring("narration stays"))

			// A second z expands it again.
			sim.InjectKey(tcell.KeyRune, 'z', tcell.ModNone)
			Eventually(readScreen, time.Second).Should(ContainSubstring("SECRETTHOUGHT"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should leave a sub-threshold block inline when its kind is folded", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "f"}, []Line{
				{Kind: LineThinking, Text: "short one\nshort two"},
			}, true, false)
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			readScreen := func() string {
				var t string
				v.app.QueueUpdate(func() { t = screenText(sim) })
				return t
			}

			sim.InjectKey(tcell.KeyRune, 'z', tcell.ModNone)
			// The mode flips (the notice confirms it) but a two-line block is not worth
			// folding, so its content stays on screen rather than becoming a placeholder.
			Eventually(readScreen, time.Second).Should(ContainSubstring("thinking folded"))
			Expect(readScreen()).To(ContainSubstring("short one"))
			Expect(readScreen()).NotTo(ContainSubstring("to expand"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should fold even a one-line tool result, since raw output is rarely worth reading inline", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "f"}, []Line{
				{Kind: LineToolResult, Text: "MISSING: output/memory/x.md"},
			}, true, false)
			v.foldToolOutput = true
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			text := screenText(sim)
			// The single line collapses to a placeholder rather than showing its raw text.
			Expect(text).To(ContainSubstring("1 line of output (Z to expand)"))
			Expect(text).NotTo(ContainSubstring("MISSING"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should open with thinking and tool output folded when the initial fold state is set", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "f"}, []Line{
				{Kind: LineNarration, Text: "conversation here"},
				{Kind: LineThinking, Text: "th1\nth2\nth3\nth4\nth5\nth6\nTHINKSECRET"},
				{Kind: LineToolResult, Text: "to1\nto2\nto3\nto4\nto5\nto6\nTOOLSECRET"},
				{Kind: LineToolError, Text: "er1\ner2\ner3\ner4\ner5\ner6\nERRORSECRET"},
			}, true, false)
			// Mirror what runViewer does for a --transcript viewer: open collapsed.
			v.foldThinking = true
			v.foldToolOutput = true
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			text := screenText(sim)
			// The conversation is visible; the heavy kinds open as placeholders, and a
			// failed tool folds too but keeps its own error wording so it stays visible.
			Expect(text).To(ContainSubstring("conversation here"))
			Expect(text).To(ContainSubstring("lines of thinking (z to expand)"))
			Expect(text).To(ContainSubstring("lines of output (Z to expand)"))
			Expect(text).To(ContainSubstring("lines of error output (Z to expand)"))
			Expect(text).NotTo(ContainSubstring("THINKSECRET"))
			Expect(text).NotTo(ContainSubstring("TOOLSECRET"))
			Expect(text).NotTo(ContainSubstring("ERRORSECRET"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should reveal a folded block when a search matches inside it", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "f"}, []Line{
				{Kind: LineThinking, Text: "a1\na2\na3\na4\na5\na6\nNEEDLEHERE"},
			}, true, false)
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			readScreen := func() string {
				var t string
				v.app.QueueUpdate(func() { t = screenText(sim) })
				return t
			}

			// Fold it away, then search for text that only lives inside the fold.
			sim.InjectKey(tcell.KeyRune, 'z', tcell.ModNone)
			Eventually(readScreen, time.Second).ShouldNot(ContainSubstring("NEEDLEHERE"))

			sim.InjectKey(tcell.KeyRune, '/', tcell.ModNone)
			sim.InjectKeyBytes([]byte("NEEDLE"))
			sim.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

			// Search is authoritative over fold: the hit is revealed, not skipped.
			Eventually(readScreen, time.Second).Should(ContainSubstring("NEEDLEHERE"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should copy the full transcript even while a block is folded", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "f"}, []Line{
				{Kind: LinePrompt, Text: "do a thing"},
				{Kind: LineThinking, Text: "t1\nt2\nt3\nt4\nt5\nt6\nHIDDENLINE"},
			}, true, false)
			v.noticeTTL = 80 * time.Millisecond
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()
			v.app.QueueUpdateDraw(func() {})

			sim.InjectKey(tcell.KeyRune, 'z', tcell.ModNone)
			readScreen := func() string {
				var t string
				v.app.QueueUpdate(func() { t = screenText(sim) })
				return t
			}
			Eventually(readScreen, time.Second).Should(ContainSubstring("to expand"))

			sim.InjectKey(tcell.KeyRune, 'y', tcell.ModNone)
			Eventually(readScreen, time.Second).Should(ContainSubstring("sent"))

			var clip []byte
			v.app.QueueUpdate(func() { clip = sim.GetClipboardData() })
			// Copy is a view-independent full-buffer operation, so the folded content is
			// still present in what lands on the clipboard.
			Expect(string(clip)).To(ContainSubstring("HIDDENLINE"))

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))
		})

		It("Should paint the statusbar as a filled contrasting bar", func() {
			sim := tcell.NewSimulationScreen("")
			v := newViewer(Meta{Title: "run-1"}, []Line{{Kind: LineNarration, Text: "hi"}}, true, false)
			v.app.SetScreen(sim)
			v.app.SetRoot(v.pages, true).SetFocus(v.view)

			done := make(chan error, 1)
			go func() { done <- v.app.Run() }()

			v.app.QueueUpdateDraw(func() {})
			cells, w, h := sim.GetContents()
			_, bg, _ := cells[(h-1)*w].Style.Decompose()

			v.app.Stop()
			Eventually(done, time.Second).Should(Receive(BeNil()))

			// The bottom row is the statusbar; its background is filled so it stands
			// apart from the default-background viewport above it.
			Expect(bg).To(Equal(tcell.ColorBlue))
		})
	})

	Describe("inputRowsFor", func() {
		DescribeTable("counts the wrapped display rows of a draft",
			func(text string, width, expected int) {
				Expect(inputRowsFor(text, width)).To(Equal(expected))
			},
			Entry("empty is one row", "", 40, 1),
			Entry("a short line is one row", "hello there", 40, 1),
			Entry("explicit newlines each add a row", "one\ntwo\nthree", 40, 3),
			Entry("a trailing newline leaves an empty final row", "one\n", 40, 2),
			Entry("word wrap splits a long line", "aaaa bbbb cccc dddd", 10, 2),
			Entry("a word longer than the width still breaks", strings.Repeat("x", 25), 10, 3),
			Entry("tabs expand before measuring", "\t\t\tabc", 10, 2),
			Entry("a zero width is treated as one column", "ab", 0, 2),
		)
	})
})
