//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/choria-io/fisk"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/runstate"
	"github.com/choria-io/fisk-ai/internal/tui"
	"github.com/choria-io/fisk-ai/internal/util"
)

func registerRunCommand(cmd *fisk.Application) {
	run := cmd.Command("run", "Runs the agent").Action(runAction)
	run.Arg("q", "Interactive prompt").StringsVar(&q)
	run.Flag("config", "Path to the agent configuration file").Default("agent.yaml").ExistingFileVar(&configFile)
	run.Flag("api-key", "Anthropic API key to use").Required().Envar("ANTHROPIC_API_KEY").StringVar(&apiKey)
	run.Flag("base-url", "Anthropic API base URL to use").Envar("ANTHROPIC_BASE_URL").StringVar(&baseURL)
	run.Flag("http-debug", "Dump Anthropic API request and response bodies to "+httpDebugFilename).Envar("HTTP_DEBUG").UnNegatableBoolVar(&httpDebug)
	run.Flag("no-color", "Disable markdown rendering of the final answer, emitting raw text").Envar("NO_COLOR").UnNegatableBoolVar(&noColor)
	run.Flag("verbose", "Shows more verbose output").Envar("VERBOSE").UnNegatableBoolVar(&verbose)
	run.Flag("tool-output", "Show tool output during the run (expanded in the full-screen UI)").Envar("TOOL_OUTPUT").UnNegatableBoolVar(&showToolOutput)
	run.Flag("no-tui", "Disable the full-screen terminal UI and use the line-by-line output").Envar("NO_TUI").UnNegatableBoolVar(&noTUI)
	run.Flag("chat", "Keep the full-screen UI open for interactive follow-ups after each turn (requires the TUI)").UnNegatableBoolVar(&chatMode)
	run.Flag("trace", "Write a JSON-lines trace of every LLM request and response to a file").PlaceHolder("FILE").StringVar(&traceFile)
	run.Flag("checkpoint", "Journal the run so it can be suspended and resumed, using a generated session id").UnNegatableBoolVar(&checkpoint)
	run.Flag("name", "Override the generated session id when checkpointing").StringVar(&runName)
	run.Flag("resume", "Resume a checkpointed session by id instead of starting a new run").PlaceHolder("ID").StringVar(&resumeID)
	run.Flag("force", "Resume even if the configuration no longer matches the saved session").UnNegatableBoolVar(&forceResume)
	run.Flag("state-dir", "Directory for checkpointed sessions (default: XDG state dir)").StringVar(&stateDirFlag)
}

// runAction maps the run flags into an agent.Options, wires the signal contract,
// and runs the agent, rendering its events and final Result. All orchestration
// lives in the agent package; this holds only flag handling and rendering.
func runAction(_ *fisk.ParseContext) error {
	err := validateRunFlags()
	if err != nil {
		return err
	}

	// Checkpointing changes the interrupt contract: an interrupt requests a graceful
	// suspend at the next boundary, a second aborts. The suspend flag is shared; under
	// the TUI the live view drives it from a leave key and owns signals itself, so the
	// signal-based handler below is installed only for the line UI (and the TUI
	// fallback), never fighting the TUI's own handler.
	checkpointing := checkpoint || resumeID != ""
	var suspend atomic.Bool
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	cfg, err := config.ParseConfigFile(configFile)
	if err != nil {
		return err
	}

	// The session store is not configured in the file yet; synthesize it from
	// --state-dir (and its default) so the construction path is the one a future
	// YAML block will use. The flag is applied here, last, so it always wins.
	cfg.Harness.Sessions = config.SessionConfigFromStateDir(stateDirFlag)

	// --http-debug dumps the API bodies to a file rather than stderr, so it coexists
	// with the full-screen UI whose alt-screen stderr would otherwise be corrupted.
	// The CLI owns the file's lifecycle.
	httpDebugOut, err := resolveHTTPDebugOut()
	if err != nil {
		return err
	}
	if closer, ok := httpDebugOut.(io.Closer); ok {
		defer closer.Close()
	}

	opts := agent.Options{
		Config:       cfg,
		ConfigFile:   configFile,
		Prompt:       q,
		APIKey:       apiKey,
		BaseURL:      baseURL,
		HTTPDebugOut: httpDebugOut,
		TraceFile:    traceFile,
		Verbose:      verbose,
		Checkpoint: agent.Checkpoint{
			Enabled:  checkpoint,
			Name:     runName,
			ResumeID: resumeID,
			Force:    forceResume,
		},
	}
	if checkpointing {
		opts.SuspendRequested = suspend.Load
	}

	// A resumed session already records whether it was a chat session, so its input
	// bar reopens without the operator re-passing --chat. Peeking the stored flag (a
	// cheap unlocked read; the resume itself takes the lock) lets the TUI wire the
	// input bar and the agent wire NextPrompt before the run starts.
	interactive := chatMode
	if resumeID != "" {
		resumed, err := agent.SessionInteractive(cfg, resumeID)
		if err != nil {
			return err
		}
		interactive = resumed
	}

	// The full-screen UI is the default on an interactive terminal. It is turned off
	// by --no-tui (or NO_TUI) or the agent config's no_tui; without a real terminal it
	// cannot run at all. When it runs it owns the whole run, its own signals, and the
	// suspend flag, and we return once it exits; otherwise control falls through to the
	// line UI, which installs its own signal handler below.
	// A chat run (fresh --chat or a resumed chat session) is full-screen-only: there is
	// no line-UI equivalent, so refuse it loudly rather than silently ignoring it when
	// the TUI will not run.
	if interactive && !runUsesTUI(cfg) {
		if resumeID != "" {
			return fmt.Errorf("resuming an interactive chat session requires the full-screen TUI: it needs an interactive terminal and is unavailable with --no-tui, NO_TUI, or no_tui in the config")
		}
		return fmt.Errorf("--chat requires the full-screen TUI: it needs an interactive terminal and is unavailable with --no-tui, NO_TUI, or no_tui in the config")
	}

	if runUsesTUI(cfg) {
		var requestSuspend func()
		if checkpointing {
			requestSuspend = func() { suspend.Store(true) }
		}
		err := runWithTUI(runCtx, opts, cfg, interactive, requestSuspend)
		if !errors.Is(err, tui.ErrNoTTY) {
			return err
		}
		// The terminal probe failed after the isatty checks passed; fall through to
		// the line UI silently, since the TUI was not explicitly requested.
	}

	// Line UI (default, or TUI fallback): install the signal-based suspend/abort
	// contract now, so it was never active during a live TUI run.
	if checkpointing {
		installSuspendHandler(&suspend, runCancel)
	} else {
		var stopSignals context.CancelFunc
		runCtx, stopSignals = signal.NotifyContext(context.Background(), os.Interrupt)
		defer stopSignals()
	}

	res, err := agent.Run(runCtx, opts, &cliEvents{verbose: verbose, noColor: noColor, showToolOutput: showToolOutput}, util.NewSurveyPrompter())

	if res != nil {
		if res.Reason == runstate.ReasonSuspended {
			fmt.Fprintf(os.Stderr, "\nsuspended; resume with: fisk-ai run --resume %s\n", res.SessionID)
		}
		if res.Stats != nil {
			res.Stats.Print(verbose)
		}
		if res.Reason == runstate.ReasonSuspended {
			return nil
		}
	}

	return err
}

// httpDebugFilename is where --http-debug writes the API body dumps. It is a file
// (not stderr) so debugging coexists with the full-screen UI, whose alt-screen would
// otherwise be corrupted by inline dumps.
const httpDebugFilename = "http-debug.log"

// resolveHTTPDebugOut opens the http-debug dump file when --http-debug is set,
// truncating any previous run's dump, and returns nil when it is not set. The caller
// owns closing the returned writer.
func resolveHTTPDebugOut() (io.Writer, error) {
	if !httpDebug {
		return nil, nil
	}

	f, err := os.Create(httpDebugFilename)
	if err != nil {
		return nil, fmt.Errorf("opening http-debug file %q: %w", httpDebugFilename, err)
	}
	fmt.Fprintf(os.Stderr, "Dumping Anthropic API request and response bodies to %s\n", httpDebugFilename)

	return f, nil
}

// runUsesTUI reports whether a run should render in the full-screen UI. The UI is
// the default on an interactive terminal; it is turned off by --no-tui (or NO_TUI)
// or the agent config's no_tui, and it cannot run without a real terminal on both
// stdin and stdout.
func runUsesTUI(cfg *config.Config) bool {
	return !noTUI && !cfg.TUIDisabled() && util.StdinIsTerminal() && util.StdoutIsTerminal()
}

// runWithTUI drives the run inside the full-screen UI: events render into the
// viewport and interactive decisions are put to the operator through native
// widgets. The view stays up after the run so the operator can read the transcript;
// on exit the terminal is restored and the advisories, final answer and stats are
// re-printed to the normal buffer so the result survives in scrollback and stays
// pipe-compatible. It returns ErrNoTTY (wrapped) when the screen cannot be opened,
// so the caller falls back to the line UI.
func runWithTUI(ctx context.Context, opts agent.Options, cfg *config.Config, interactive bool, requestSuspend func()) error {
	live, err := tui.NewLive(tui.Meta{
		Model:       cfg.LLM.Model,
		Version:     util.Version(),
		Query:       strings.Join(opts.Prompt, " "),
		Interactive: interactive,
		Resume:      opts.Checkpoint.ResumeID != "",
		Dir:         runDir(),
	}, noColor, requestSuspend)
	if err != nil {
		return err
	}
	live.SetBell(cfg.BellEnabled())

	// Tool output is always shown in the full-screen UI but starts folded to a
	// placeholder; --tool-output expands it by default so the raw results are visible
	// inline without pressing Z.
	if showToolOutput {
		live.ExpandToolOutput()
	}

	// In chat mode the view stays interactive: after each turn the input row opens for a
	// follow-up, and the run loop continues with the accumulated conversation. This
	// covers both a fresh --chat run and a resumed chat session (the caller resolved the
	// flag from the stored session).
	if interactive {
		live.EnableInteractive()

		next := live.NextPromptFunc()
		opts.NextPrompt = func(c context.Context) agent.Continuation {
			text, reset, cont := next(c)
			return agent.Continuation{Text: text, Reset: reset, Continue: cont}
		}
	}

	events := &tcellEvents{live: live, verbose: verbose}

	var res *agent.Result
	// The live view classifies the terminal state from the run's real outcome, so tell
	// it how to read a graceful suspend; res is set before the run goroutine returns
	// and the view only consults this after the run ends.
	live.SetSuspendedFunc(func() bool {
		return res != nil && res.Reason == runstate.ReasonSuspended
	})
	// Show the resume command on-screen, before the alt-screen is torn down, so a
	// suspended session's id is visible while the operator is still reading the view
	// (it is also re-printed to the restored terminal below for scrollback).
	live.SetResumeHintFunc(func() string {
		if res == nil || res.SessionID == "" {
			return ""
		}
		return "resume with: fisk-ai run --resume " + res.SessionID
	})
	runErr := live.Run(ctx, func(runCtx context.Context) error {
		var e error
		res, e = agent.Run(runCtx, opts, events, live.Prompter())
		return e
	})

	for _, w := range events.warnings {
		fmt.Fprintf(os.Stderr, "warning: %s\n", w)
	}
	// A context reset during the run leaves earlier sessions saved and resumable; re-print
	// their resume commands to native scrollback so their ids survive the alt-screen teardown.
	for _, id := range events.rotatedSessions {
		fmt.Fprintf(os.Stderr, "previous session saved; resume with: fisk-ai run --resume %s\n", id)
	}
	if events.answer != "" {
		fmt.Fprintln(os.Stdout, util.RenderAnswer(events.answer, noColor))
	}
	if res != nil && res.Reason == runstate.ReasonSuspended {
		fmt.Fprintf(os.Stderr, "\nsuspended; resume with: fisk-ai run --resume %s\n", res.SessionID)
	}
	if res != nil && res.Stats != nil {
		res.Stats.Print(verbose)
	}

	return runErr
}

// runDir is the working directory shown on the live view's startup card, with the home
// prefix collapsed to ~. It returns "" when the directory cannot be determined, which
// hides the line.
func runDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if dir == home {
		return "~"
	}
	if strings.HasPrefix(dir, home+string(os.PathSeparator)) {
		return "~" + dir[len(home):]
	}

	return dir
}

// validateRunFlags rejects incompatible combinations of the checkpoint and
// resume flags before any work is done.
func validateRunFlags() error {
	if resumeID != "" && checkpoint {
		return fmt.Errorf("--resume cannot be combined with --checkpoint")
	}
	if resumeID != "" && len(q) > 0 {
		return fmt.Errorf("--resume does not take a query; the prompt is restored from the session")
	}
	if runName != "" && !checkpoint {
		return fmt.Errorf("--name requires --checkpoint")
	}
	if forceResume && resumeID == "" {
		return fmt.Errorf("--force only applies when resuming")
	}

	return nil
}

// installSuspendHandler wires the graceful-suspend signal contract: the first
// interrupt or SIGTERM sets the suspend flag, polled by the loop at its next
// boundary; a second aborts the run.
func installSuspendHandler(suspend *atomic.Bool, cancelRun context.CancelFunc) {
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigs
		suspend.Store(true)
		fmt.Fprintln(os.Stderr, "\nsuspend requested; finishing current step, press ^C again to abort")
		<-sigs
		cancelRun()
	}()
}
