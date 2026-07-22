//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"errors"
	"fmt"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/choria-io/fisk-ai/internal/util"
)

// promptPage is the Pages name the prompt overlays occupy. onKey lets the focused
// prompt widget own its keys while this page is front.
const promptPage = "prompt"

// errPromptCanceled is returned when the operator dismisses a prompt without
// answering (Esc). The caller treats it, like any prompt error, as a denial.
var errPromptCanceled = errors.New("the operator dismissed the prompt")

// tcellPrompter implements util.Prompter with native tview widgets, driven from the
// run goroutine. Each method marshals its widget onto the tview loop and blocks
// until the operator answers or ctx is canceled; the caller (the confirm gate and
// the human-in-the-loop builtins) treats any error or a cancel as an authoritative
// denial, so a Ctrl-C or a torn-down loop resolves to deny. It never owns the deny
// default itself.
type tcellPrompter struct {
	live *Live
}

func newTcellPrompter(live *Live) *tcellPrompter {
	return &tcellPrompter{live: live}
}

var _ toolkit.Prompter = (*tcellPrompter)(nil)

// CanPrompt reports true: the tcell prompter only exists when the full-screen UI owns
// an interactive terminal, so an operator is always reachable through its modals.
func (p *tcellPrompter) CanPrompt() bool { return true }

// ApproveCommand shows the confirm-gate modal (No default-focused; Enter on it or
// Esc declines) and returns the three-way choice.
func (p *tcellPrompter) ApproveCommand(ctx context.Context, req toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
	p.live.setBlocked()
	defer p.live.setRunning()

	result := make(chan toolkit.ConfirmChoice, 1)
	// The modal draws its text through tview's tag-aware printer, so the whole body
	// is escaped to keep a literal "[" in the model-supplied command from opening a
	// color tag; the fixed wording carries no brackets, so escaping it is harmless.
	body := tview.Escape(fmt.Sprintf("Run this command?\n\n%s\n\n%q carries tag %q",
		util.SanitizeForDisplay(req.Display), req.Command, req.Tag))

	p.modal(body, []string{"No", "Once", "Always"}, func(idx int) {
		switch idx {
		case 1:
			result <- toolkit.ConfirmOnce
		case 2:
			result <- toolkit.ConfirmAlways
		default:
			result <- toolkit.ConfirmNo
		}
	})

	select {
	case <-ctx.Done():
		p.dismiss()
		return toolkit.ConfirmNo, ctx.Err()
	case r := <-result:
		return r, nil
	}
}

// Confirm shows a yes/no modal (No default-focused).
func (p *tcellPrompter) Confirm(ctx context.Context, question string) (bool, error) {
	p.live.setBlocked()
	defer p.live.setRunning()

	result := make(chan bool, 1)

	p.modal(tview.Escape(util.SanitizeForDisplay(question)), []string{"No", "Yes"}, func(idx int) {
		result <- idx == 1
	})

	select {
	case <-ctx.Done():
		p.dismiss()
		return false, ctx.Err()
	case r := <-result:
		return r, nil
	}
}

// Select shows the options in a list; Enter chooses, Esc cancels.
func (p *tcellPrompter) Select(ctx context.Context, question string, options []string) (int, error) {
	p.live.setBlocked()
	defer p.live.setRunning()

	type res struct {
		idx int
		err error
	}
	result := make(chan res, 1)

	p.present(func(finish func()) (tview.Primitive, tview.Primitive) {
		list := tview.NewList().ShowSecondaryText(false)
		list.SetBorder(true).SetTitle(promptTitle(question))
		for i, opt := range options {
			idx := i
			// List escapes item text itself, so it is only sanitized here.
			list.AddItem(util.SanitizeForDisplay(opt), "", 0, func() {
				finish()
				result <- res{idx: idx}
			})
		}
		list.SetDoneFunc(func() {
			finish()
			result <- res{idx: -1, err: errPromptCanceled}
		})
		return overlay(list, 60, clamp(len(options)+2, 3, 20)), list
	})

	select {
	case <-ctx.Done():
		p.dismiss()
		return -1, ctx.Err()
	case r := <-result:
		return r.idx, r.err
	}
}

// Input shows a single free-text field pre-filled with def; Enter submits, Esc
// cancels.
func (p *tcellPrompter) Input(ctx context.Context, question, def string) (string, error) {
	p.live.setBlocked()
	defer p.live.setRunning()

	type res struct {
		text string
		err  error
	}
	result := make(chan res, 1)

	p.present(func(finish func()) (tview.Primitive, tview.Primitive) {
		input := tview.NewInputField().SetText(util.SanitizeForDisplay(def))
		input.SetBorder(true).SetTitle(promptTitle(question))
		input.SetDoneFunc(func(key tcell.Key) {
			finish()
			if key == tcell.KeyEsc {
				result <- res{err: errPromptCanceled}
				return
			}
			result <- res{text: input.GetText()}
		})
		return overlay(input, 60, 3), input
	})

	select {
	case <-ctx.Done():
		p.dismiss()
		return "", ctx.Err()
	case r := <-result:
		return r.text, r.err
	}
}

// modal shows a button modal. decide receives the chosen button index, or -1 when
// Esc dismisses it (which maps to the safe default, since the safe option is button
// zero and every decide treats a non-affirmative index as the decline).
func (p *tcellPrompter) modal(body string, buttons []string, decide func(idx int)) {
	p.present(func(finish func()) (tview.Primitive, tview.Primitive) {
		m := tview.NewModal().SetText(body).AddButtons(buttons)
		m.SetDoneFunc(func(idx int, _ string) {
			finish()
			decide(idx)
		})
		m.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
			if ev.Key() == tcell.KeyEsc {
				finish()
				decide(-1)
				return nil
			}
			return ev
		})
		return m, m
	})
}

// present adds a prompt overlay on the tview loop and focuses its widget. build
// returns the page primitive and the widget to focus, and receives a finish
// function (run on the loop) that removes the overlay and restores focus.
func (p *tcellPrompter) present(build func(finish func()) (page, focus tview.Primitive)) {
	p.live.v.app.QueueUpdateDraw(func() {
		finish := func() {
			p.live.v.pages.RemovePage(promptPage)
			p.live.v.app.SetFocus(p.live.v.view)
		}
		page, focus := build(finish)
		p.live.v.pages.AddPage(promptPage, page, true, true)
		p.live.v.app.SetFocus(focus)
	})
}

// dismiss removes the current prompt overlay from the run goroutine, used when ctx
// is canceled while a prompt is up.
func (p *tcellPrompter) dismiss() {
	p.live.v.app.QueueUpdateDraw(func() {
		p.live.v.pages.RemovePage(promptPage)
		p.live.v.app.SetFocus(p.live.v.view)
	})
}

// promptTitle builds a border title from a model-supplied question, escaped since a
// Box title is drawn through tview's tag-aware printer.
func promptTitle(q string) string {
	return " " + tview.Escape(util.SanitizeForDisplay(q)) + " "
}

// clamp bounds v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
