//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/choria-io/fisk"
	"github.com/choria-io/ui/columns"
	"github.com/choria-io/ui/table"

	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/tui"
	"github.com/choria-io/fisk-ai/internal/util"
)

func registerSessionCommand(cmd *fisk.Application) {
	session := cmd.Command("session", "Manage checkpointed agent runs")
	session.Flag("state-dir", "Directory holding checkpointed sessions (default: XDG state dir)").StringVar(&stateDirFlag)

	session.Command("ls", "Lists checkpointed sessions").Alias("list").Action(sessionLsAction)

	show := session.Command("show", "Shows a checkpointed session in detail").Alias("view").Action(sessionShowAction)
	show.Arg("id", "Session id").Required().StringVar(&sessionArgID)
	show.Flag("transcript", "Shows the full conversation transcript, opening the interactive viewer on a terminal").UnNegatableBoolVar(&sessionTranscript)
	show.Flag("no-tui", "Disable the full-screen viewer and print the transcript as line output without tool result output").Envar("NO_TUI").UnNegatableBoolVar(&noTUI)

	rm := session.Command("rm", "Removes a checkpointed session").Alias("delete").Action(sessionRmAction)
	rm.Arg("id", "Session id").Required().StringVar(&sessionArgID)
}

func sessionStatus(reason runstate.TerminalReason) string {
	if reason == "" {
		return "open"
	}

	return string(reason)
}

func sessionLsAction(_ *fisk.ParseContext) error {
	store, err := runstate.OpenStore(stateDirFlag)
	if err != nil {
		return err
	}

	infos, err := store.List()
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Println("No checkpointed sessions")
		return nil
	}

	sort.Slice(infos, func(i, j int) bool {
		return infos[i].Updated.After(infos[j].Updated)
	})

	tbl := table.NewTableWriter("")
	defer tbl.WriteTo(os.Stdout)

	tbl.AddHeaders("ID", "Model", "Status", "Updated", "Prompt")
	for _, info := range infos {
		tbl.AddRow(info.RunID, info.Model, sessionStatus(info.Terminal), info.Updated, util.TruncateString(info.Prompt, 50))
	}

	return nil
}

func sessionShowAction(_ *fisk.ParseContext) error {
	store, err := runstate.OpenStore(stateDirFlag)
	if err != nil {
		return err
	}

	rs, err := store.Load(sessionArgID)
	if err != nil {
		return err
	}

	// Without --transcript, show only the session's counters and prompt.
	if !sessionTranscript {
		c := columns.New()
		printSessionMeta(c, rs)
		fmt.Println(c.String())

		return nil
	}

	// --transcript opens the full-screen viewer, the default rendering of the
	// transcript on a real terminal; it is the whole output, so return once it exits.
	// When it cannot run (piped, redirected, or no controlling terminal) it falls back
	// to the meta block plus a line transcript below.
	shown, err := showTranscriptTUI(rs)
	if err != nil {
		return err
	}
	if shown {
		return nil
	}

	// --no-tui asks for a plain line transcript, where the verbose tool result output
	// is more noise than help, so it is omitted; a fallback taken only because there is
	// no terminal (piped or redirected, --no-tui unset) still includes it.

	c := columns.New()
	printSessionMeta(c, rs)
	fmt.Println(c.String())

	fmt.Printf("\n--- transcript ---\n\n")
	dumpTranscript(os.Stdout, rs, noColor, !noTUI)

	return nil
}

// printSessionMeta writes the session's counters and prompt to stdout.
func printSessionMeta(c *columns.Document, rs *runstate.RunState) {
	c.Headingf("Session {bold}%s{/bold}", rs.RunID)

	c.Item("Status", sessionStatus(terminalReason(rs)))
	c.Item("Model", rs.Fingerprint.Model)
	c.Item("Next iter", rs.NextIteration)
	c.Item("LLM calls", rs.Counters.LlmCalls)
	c.Item("Tool calls", fmt.Sprintf("%d (remote %d)", rs.Counters.ToolCalls, rs.Counters.RemoteToolCalls))
	c.Item("Tokens", fmt.Sprintf("%d in / %d out", rs.Counters.InTokens, rs.Counters.OutTokens))

	if rs.Pending != nil {
		c.ItemUnlessZero("Pending", "an in-flight tool batch will resume first")
	}
	c.Blank()
	c.Section("Prompt", func(c *columns.Document) {
		c.Print(util.TruncateString(rs.Prompt, 200))
	})
}

// showTranscriptTUI renders the session in the full-screen viewer. It reports
// whether the viewer actually ran: false when it is turned off (--no-tui or NO_TUI)
// or cannot take over (stdin or stdout is not an interactive terminal, or no
// controlling terminal could be opened), so the caller falls back to the line view.
// The fallback is silent since the viewer is implicit (--transcript, not an explicit
// flag), and a line transcript is a fine result. Thinking and tool output are always
// included and start folded, so the viewer opens on the conversation and either can
// be expanded.
func showTranscriptTUI(rs *runstate.RunState) (bool, error) {
	if noTUI || !util.StdinIsTerminal() || !util.StdoutIsTerminal() {
		return false, nil
	}

	meta := tui.Meta{Title: rs.RunID, Model: rs.Fingerprint.Model, Version: util.Version(), Query: rs.Prompt, InTokens: rs.Counters.InTokens, OutTokens: rs.Counters.OutTokens}
	err := tui.ShowTranscript(meta, transcriptLines(rs, true), noColor, true, true)
	if errors.Is(err, tui.ErrNoTTY) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

func sessionRmAction(_ *fisk.ParseContext) error {
	store, err := runstate.OpenStore(stateDirFlag)
	if err != nil {
		return err
	}

	err = store.Delete(sessionArgID)
	if err != nil {
		return err
	}

	fmt.Printf("Removed session %s\n", sessionArgID)

	return nil
}

func terminalReason(rs *runstate.RunState) runstate.TerminalReason {
	if rs.Terminal == nil {
		return ""
	}

	return rs.Terminal.Reason
}
