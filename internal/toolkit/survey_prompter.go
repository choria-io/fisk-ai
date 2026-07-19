//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/AlecAivazis/survey/v2"
)

// surveyPrompter is the line-oriented Prompter used by the default CLI. It wraps
// AlecAivazis/survey, rendering prompts and traces on stderr so stdout stays clean
// for a piped final answer. survey turns an interrupt (Ctrl-C) or a closed input
// into an error, which the caller treats as a denial; it cannot be canceled
// mid-prompt through ctx, so the caller performs the authoritative context and
// no-terminal deny checks before a prompt is ever shown, and a full-screen
// Prompter (which can select on ctx) is used when one owns the screen.
type surveyPrompter struct {
	// out is where prompt headers and command traces are written: os.Stderr in a
	// real run, redirected in tests to keep their output quiet. The interactive
	// survey widgets themselves render on os.Stderr, since survey needs a real
	// terminal file for its cursor control.
	out io.Writer
}

// NewSurveyPrompter returns the CLI Prompter, writing its prompt headers and
// command traces to stderr.
func NewSurveyPrompter() Prompter {
	return &surveyPrompter{out: os.Stderr}
}

// ApproveCommand renders the confirm-gate header and command trace, then asks the
// operator to allow the command. The safe option (No) is listed first so survey
// highlights it and a reflexive Enter declines; an interrupt or closed input
// returns an error the caller treats as a denial.
func (p *surveyPrompter) ApproveCommand(_ context.Context, req GateRequest) (ConfirmChoice, error) {
	printGateHeader(p.out, req)

	options := []string{
		"No, do not run it",
		"Yes, run it once",
		fmt.Sprintf("Yes, and allow %q (any arguments) for the rest of this session", req.Command),
	}

	idx := 0
	err := survey.AskOne(
		&survey.Select{Message: "Run this command?", Options: options},
		&idx,
		survey.WithStdio(os.Stdin, os.Stderr, os.Stderr),
	)
	if err != nil {
		return ConfirmNo, err
	}

	switch idx {
	case 1:
		return ConfirmOnce, nil
	case 2:
		printAlwaysNote(p.out, req.Command)
		return ConfirmAlways, nil
	default:
		return ConfirmNo, nil
	}
}

// Confirm prompts for a yes/no answer. The bound value starts false and the prompt
// defaults to No, so an operator who simply presses Enter declines; survey returns
// an error on Ctrl-C or a closed input, which the caller treats as a denial.
func (p *surveyPrompter) Confirm(_ context.Context, question string) (bool, error) {
	printPromptSeparator(p.out)

	confirmed := false
	err := survey.AskOne(
		&survey.Confirm{Message: question, Default: false},
		&confirmed,
		survey.WithStdio(os.Stdin, os.Stderr, os.Stderr),
	)
	if err != nil {
		return false, err
	}

	return confirmed, nil
}

// Select prompts the operator to choose one of options and returns its index. The
// index starts at -1, so a Ctrl-C or closed input (survey returns an error) leaves
// no choice rather than defaulting to the first option.
func (p *surveyPrompter) Select(_ context.Context, question string, options []string) (int, error) {
	printPromptSeparator(p.out)

	idx := -1
	err := survey.AskOne(
		&survey.Select{Message: question, Options: options},
		&idx,
		survey.WithStdio(os.Stdin, os.Stderr, os.Stderr),
	)
	if err != nil {
		return -1, err
	}

	return idx, nil
}

// Input prompts the operator for a free-text value, pre-filled with def (which may
// be empty). survey returns an error on Ctrl-C or a closed input.
func (p *surveyPrompter) Input(_ context.Context, question, def string) (string, error) {
	printPromptSeparator(p.out)

	answer := ""
	err := survey.AskOne(
		&survey.Input{Message: question, Default: def},
		&answer,
		survey.WithStdio(os.Stdin, os.Stderr, os.Stderr),
	)
	if err != nil {
		return "", err
	}

	return answer, nil
}

// printGateHeader writes the confirm-gate approval header and command trace: a
// separator to set the question apart from the model's preceding narration, a line
// naming the command and the tag that gated it, and the sanitized command line.
func printGateHeader(out io.Writer, req GateRequest) {
	printPromptSeparator(out)
	fmt.Fprintf(out, "confirmation required: %q carries tag %q\n", req.Command, req.Tag)
	fmt.Fprintf(out, "-> %s\n", req.Display)
}

// printAlwaysNote confirms to the operator that a session-wide allow was recorded,
// so they know the tool will not be asked about again this session.
func printAlwaysNote(out io.Writer, commandPath string) {
	fmt.Fprintf(out, "confirmation: will not ask again for %q this session\n", commandPath)
}

// printPromptSeparator writes a blank line to w before an interactive prompt, so a
// question put to the operator is visually set apart from the model's preceding
// narration or a tool's output rather than running straight on from it.
func printPromptSeparator(w io.Writer) {
	fmt.Fprintln(w)
}
