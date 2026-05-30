//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"time"

	"github.com/gdamore/tcell/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/util"
)

var _ = Describe("tcellPrompter", func() {
	var (
		l    *Live
		sim  tcell.SimulationScreen
		done chan error
	)

	BeforeEach(func() {
		sim = tcell.NewSimulationScreen("")
		l = newLive(sim, Meta{Title: "t"}, true, nil)
		done = make(chan error, 1)
		go func() { done <- l.v.app.Run() }()
		l.v.app.QueueUpdateDraw(func() {}) // ensure the loop is running
	})

	AfterEach(func() {
		l.v.app.Stop()
		Eventually(done, time.Second).Should(Receive())
	})

	// front reads the front page name on the loop goroutine, so it never races the
	// draw. inject posts a key the loop then handles.
	front := func() string {
		var name string
		l.v.app.QueueUpdate(func() { name, _ = l.v.pages.GetFrontPage() })
		return name
	}
	inject := func(key tcell.Key, r rune) {
		sim.InjectKey(key, r, tcell.ModNone)
	}
	awaitPrompt := func() {
		GinkgoHelper()
		Eventually(front, time.Second).Should(Equal(promptPage))
	}

	Describe("ApproveCommand", func() {
		call := func(ctx context.Context) chan util.ConfirmChoice {
			out := make(chan util.ConfirmChoice, 1)
			go func() {
				c, _ := l.prompter.ApproveCommand(ctx, util.GateRequest{Command: "stream rm", Display: "stream rm ORDERS", Tag: "ai:confirm"})
				out <- c
			}()
			return out
		}

		It("Should decline when the operator presses Esc", func() {
			out := call(context.Background())
			awaitPrompt()

			inject(tcell.KeyEsc, 0)
			Eventually(out, time.Second).Should(Receive(Equal(util.ConfirmNo)))
		})

		It("Should return the operator's Always choice", func() {
			out := call(context.Background())
			awaitPrompt()

			// Buttons are No, Once, Always with No focused; move right twice and select.
			inject(tcell.KeyRight, 0)
			inject(tcell.KeyRight, 0)
			inject(tcell.KeyEnter, 0)
			Eventually(out, time.Second).Should(Receive(Equal(util.ConfirmAlways)))
		})

		It("Should deny when the run is canceled while the prompt is up", func() {
			ctx, cancel := context.WithCancel(context.Background())
			out := call(ctx)
			awaitPrompt()

			cancel()
			Eventually(out, time.Second).Should(Receive(Equal(util.ConfirmNo)))
			Eventually(front, time.Second).Should(Equal("main"))
		})

		It("Should show an injected color tag in the command literally", func() {
			go l.prompter.ApproveCommand(context.Background(), util.GateRequest{Command: "run", Display: "run [red]x", Tag: "ai:confirm"})
			awaitPrompt()

			var text string
			l.v.app.QueueUpdate(func() { text = screenText(sim) })
			Expect(text).To(ContainSubstring("[red]x"))
		})
	})

	Describe("Confirm", func() {
		It("Should return false on the default No", func() {
			out := make(chan bool, 1)
			go func() { r, _ := l.prompter.Confirm(context.Background(), "proceed?"); out <- r }()
			awaitPrompt()

			inject(tcell.KeyEnter, 0)
			Eventually(out, time.Second).Should(Receive(BeFalse()))
		})

		It("Should return true when the operator moves to Yes", func() {
			out := make(chan bool, 1)
			go func() { r, _ := l.prompter.Confirm(context.Background(), "proceed?"); out <- r }()
			awaitPrompt()

			inject(tcell.KeyRight, 0)
			inject(tcell.KeyEnter, 0)
			Eventually(out, time.Second).Should(Receive(BeTrue()))
		})
	})

	Describe("Select", func() {
		call := func() chan int {
			out := make(chan int, 1)
			go func() {
				i, _ := l.prompter.Select(context.Background(), "pick", []string{"alpha", "beta", "gamma"})
				out <- i
			}()
			return out
		}

		It("Should return the focused option on Enter", func() {
			out := call()
			awaitPrompt()

			inject(tcell.KeyEnter, 0)
			Eventually(out, time.Second).Should(Receive(Equal(0)))
		})

		It("Should return the next option after moving down", func() {
			out := call()
			awaitPrompt()

			inject(tcell.KeyDown, 0)
			inject(tcell.KeyEnter, 0)
			Eventually(out, time.Second).Should(Receive(Equal(1)))
		})

		It("Should report a negative index when canceled with Esc", func() {
			out := call()
			awaitPrompt()

			inject(tcell.KeyEsc, 0)
			Eventually(out, time.Second).Should(Receive(Equal(-1)))
		})
	})

	Describe("Input", func() {
		It("Should return the typed value on Enter", func() {
			out := make(chan string, 1)
			go func() { s, _ := l.prompter.Input(context.Background(), "name?", ""); out <- s }()
			awaitPrompt()

			for _, r := range "orders" {
				inject(tcell.KeyRune, r)
			}
			inject(tcell.KeyEnter, 0)
			Eventually(out, time.Second).Should(Receive(Equal("orders")))
		})
	})
})
