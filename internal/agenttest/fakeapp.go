//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Package agenttest is a test harness for driving agent.Run from outside the agent
// package: it consolidates the idioms each internal package had grown its own copy
// of (a fake fisk application, a scripted llm.Provider, a recording agent.Events, a
// scripted toolkit.Prompter, and config builders) so a caller can stand up a run
// without reaching into internals.
//
// Every constructor takes a testing.TB. The package is a normal, linkable package
// rather than a _test.go file, so the job system can reuse the harness from its own
// package later; the testing.TB argument keeps it useless outside a test and
// documents that intent at every signature.
package agenttest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/choria-io/fisk"
)

// FakeApp is a runnable stand-in fisk application. Its --fisk-introspect output is
// the genuine introspection of a real fisk.Application, so the tool schemas the
// agent loads are real rather than hand-written JSON; every other invocation echoes
// its arguments one per line, so a tool call produces a deterministic result a test
// can assert against. Path is the value an agent config's application_path points at.
type FakeApp struct {
	Path string
}

// NewFakeApp introspects app in-process to capture its genuine command model, then
// writes an executable that replays that model on --fisk-introspect and echoes its
// arguments otherwise. It sets a no-op Terminate on app, since --fisk-introspect
// would otherwise call os.Exit through fisk's default terminate.
func NewFakeApp(tb testing.TB, app *fisk.Application) *FakeApp {
	tb.Helper()

	model := introspectJSON(tb, app)

	dir := tb.TempDir()
	jsonPath := filepath.Join(dir, "introspect.json")
	err := os.WriteFile(jsonPath, model, 0o600)
	if err != nil {
		tb.Fatalf("agenttest: writing introspect model: %v", err)
	}

	// On --fisk-introspect the binary replays the captured model; otherwise it reports
	// its working directory (so a test can observe the per-run ToolWorkDir) and echoes
	// each argument on its own line, matching the long-standing fake-application idiom so
	// a tool call's output is predictable.
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"--fisk-introspect\" ]; then\n  cat %q\n  exit 0\nfi\nprintf 'PWD=%%s\\n' \"$PWD\"\nfor a in \"$@\"; do printf '%%s\\n' \"$a\"; done\n", jsonPath)

	appPath := filepath.Join(dir, "app")
	err = os.WriteFile(appPath, []byte(script), 0o700)
	if err != nil {
		tb.Fatalf("agenttest: writing fake application: %v", err)
	}

	return &FakeApp{Path: appPath}
}

// introspectJSON drives app's real --fisk-introspect handler in-process and returns
// the JSON it writes, the same document the agent would read over the process
// boundary, so the captured schemas are precomputed exactly as production sees them.
// It redirects os.Stdout for the duration of the parse, so it must run serially.
func introspectJSON(tb testing.TB, app *fisk.Application) []byte {
	tb.Helper()

	// --fisk-introspect terminates the process; make that a no-op so the parse returns.
	app.Terminate(func(int) {})

	r, w, err := os.Pipe()
	if err != nil {
		tb.Fatalf("agenttest: creating introspect pipe: %v", err)
	}

	saved := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = saved }()

	// Read concurrently so a large model cannot fill the pipe and block the write.
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	_, err = app.Parse([]string{"--fisk-introspect"})
	if err != nil {
		tb.Fatalf("agenttest: introspecting fake application: %v", err)
	}
	err = w.Close()
	if err != nil {
		tb.Fatalf("agenttest: closing introspect pipe: %v", err)
	}

	return <-captured
}
