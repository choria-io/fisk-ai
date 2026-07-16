//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"

	"github.com/choria-io/fisk"

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
	sessionArgID      string
	sessionTranscript bool

	ctx    context.Context
	cancel context.CancelFunc
)

func main() {
	util.SetVersion(version)

	cmd := fisk.New("fisk-ai", "Fisk AI Agent builder")
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
