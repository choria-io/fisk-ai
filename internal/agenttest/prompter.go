//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agenttest

import (
	"context"
	"testing"

	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// ScriptedPrompter is a toolkit.Prompter whose four interactive methods each defer
// to a closure the spec installs, so a test drives the confirm gate and the
// human-in-the-loop handlers without a terminal. A method reached with no closure
// set fails the test, since reaching it means the run prompted where it should have
// resolved the outcome itself (for example on the no-operator path). It replaces the
// per-package fakePrompter copies the tree had grown.
type ScriptedPrompter struct {
	tb testing.TB

	// canPrompt is what CanPrompt reports; true by default, since a scripted operator
	// is present. NoOperator flips it to model the no-operator path.
	canPrompt bool

	// ApproveFn answers the confirm-gate approval; ConfirmFn, SelectFn and InputFn
	// answer the ask_human_confirm, ask_human_select and ask_human_input builtins.
	// Install only the ones a spec expects to reach.
	ApproveFn func(toolkit.GateRequest) (toolkit.ConfirmChoice, error)
	ConfirmFn func(question string) (bool, error)
	SelectFn  func(question string, options []string) (int, error)
	InputFn   func(question, def string) (string, error)

	// LastGateRequest is the last request ApproveCommand received, for assertion.
	LastGateRequest toolkit.GateRequest
}

// NewScriptedPrompter returns a prompter with no closures installed and CanPrompt
// reporting true; set the fields for the interactions the spec drives.
func NewScriptedPrompter(tb testing.TB) *ScriptedPrompter {
	tb.Helper()
	return &ScriptedPrompter{tb: tb, canPrompt: true}
}

// CanPrompt reports whether an operator is reachable; true unless NoOperator was set.
func (p *ScriptedPrompter) CanPrompt() bool { return p.canPrompt }

// NoOperator makes CanPrompt report false, modeling a run with no operator (the
// default-deny path the confirm gate and human-in-the-loop tools take).
func (p *ScriptedPrompter) NoOperator() *ScriptedPrompter {
	p.canPrompt = false
	return p
}

func (p *ScriptedPrompter) ApproveCommand(_ context.Context, req toolkit.GateRequest) (toolkit.ConfirmChoice, error) {
	p.LastGateRequest = req
	if p.ApproveFn == nil {
		p.tb.Fatalf("agenttest: ApproveCommand called but no ApproveFn was set")
	}
	return p.ApproveFn(req)
}

func (p *ScriptedPrompter) Confirm(_ context.Context, question string) (bool, error) {
	if p.ConfirmFn == nil {
		p.tb.Fatalf("agenttest: Confirm called but no ConfirmFn was set")
	}
	return p.ConfirmFn(question)
}

func (p *ScriptedPrompter) Select(_ context.Context, question string, options []string) (int, error) {
	if p.SelectFn == nil {
		p.tb.Fatalf("agenttest: Select called but no SelectFn was set")
	}
	return p.SelectFn(question, options)
}

func (p *ScriptedPrompter) Input(_ context.Context, question, def string) (string, error) {
	if p.InputFn == nil {
		p.tb.Fatalf("agenttest: Input called but no InputFn was set")
	}
	return p.InputFn(question, def)
}
