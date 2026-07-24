//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/choria-io/fisk"

	// Link the file session backend in so it registers itself. The run path links it
	// transitively through the agent package; importing it here as well keeps the
	// session subcommands, which construct the store directly, self-sufficient.
	_ "github.com/choria-io/fisk-ai/internal/runstate/file"
	"github.com/choria-io/fisk-ai/internal/util"
)

// version is the build version shown on the splash card and reported by the MCP and
// A2A identities. It defaults to "devel" and is overridden at release time: goreleaser's
// default build ldflags set -X main.version=<tag>, so no extra config is needed.
var version = "devel"

var (
	configFile string
	apiKey     string
	baseURL    string
	q          []string
	httpDebug  bool
	noColor    bool
	mcpPort    int
	mcpAddress string
	verbose    bool
	noTUI      bool
	chatMode   bool
	traceFile  string

	showToolOutput bool

	checkpoint        bool
	runName           string
	resumeID          string
	forceResume       bool
	stateDirFlag      string
	sessionConfigFile string
	sessionArgID      string
	sessionTranscript bool
)

// interruptContext returns a context canceled on the first Ctrl-C (SIGINT) or
// SIGTERM, the shared interrupt contract for the one-shot commands. SIGTERM is
// included so a server (mcp, a2a) shuts down cleanly under systemd or a
// container stop; a second signal falls through to the default disposition and
// terminates the process. The run command keeps its own signal handling because
// it layers a graceful-suspend contract on top.
func interruptContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func main() {
	util.SetVersion(version)

	cmd := fisk.New("fisk", "Fisk AI Toolkit")
	cmd.Version(version)

	registerRunCommand(cmd)
	registerSessionCommand(cmd)
	registerInfoAction(cmd)
	registerRAGCommand(cmd)
	registerMcpAction(cmd)
	registerA2AAction(cmd)
	registerDiscoverAction(cmd)

	cmd.MustParseWithUsage(os.Args[1:])
}
