//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/choria-io/fisk"
	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/mcpserver"
	"github.com/choria-io/fisk-ai/internal/util"
)

// defaultMCPPort is the TCP port the MCP server listens on when neither the
// --port flag nor expose.agent.mcp.port in the config sets one.
const defaultMCPPort = 8080

func registerMcpAction(cmd *fisk.Application) {
	mcpCmd := cmd.Command("mcp", "Serves the tools over the Model Context Protocol").Action(mcpAction)
	mcpCmd.Flag("config", "Path to the agent configuration file").Default("agent.yaml").ExistingFileVar(&configFile)
	mcpCmd.Flag("port", "TCP port to listen on; overrides expose.agent.mcp.port").Envar("FISK_AI_MCP_PORT").IntVar(&mcpPort)
}

// mcpAction serves the configured tools over MCP instead of running the agent.
// It is opt-in: the config must carry an expose.agent.mcp block or the command
// refuses to start. It needs only the application and tool filters; the prompt
// and model are not used. The served set is the agent's tools narrowed by
// expose.agent.tools when set. All progress goes to stderr; the MCP protocol owns
// the HTTP response bodies.
func mcpAction(_ *fisk.ParseContext) error {
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeMCP)
	if err != nil {
		return err
	}

	if !cfg.MCPEnabled() {
		return fmt.Errorf("fisk-ai mcp requires an expose.agent.mcp block in %q; add expose.agent.mcp (optionally with a port) to serve tools over MCP", configFile)
	}

	if cfg.ApplicationPath == "" {
		fmt.Fprintln(os.Stderr, "note: no wrapped application configured (application_path unset); serving built-in tools only")
	}

	if cfg.HumanInTheLoopEnabled() {
		fmt.Fprintln(os.Stderr, "warning: human_in_the_loop has no effect over MCP; built-in human-in-the-loop tools are never exposed to MCP clients")
	}

	if cfg.MemoryEnabled() {
		fmt.Fprintln(os.Stderr, "warning: memory has no effect over MCP; the built-in memory tools are only available in an agent run")
	}

	tools, err := util.ServedTools(cfg)
	if err != nil {
		return err
	}

	ragBuiltins, ragStore, err := util.MCPKnowledgeBuiltins(ctx, cfg, os.Stderr)
	if err != nil {
		return err
	}
	// Close after Serve returns (Serve below is the final call, so this deferred
	// close runs only once graceful shutdown has drained in-flight tool calls),
	// never concurrently with a live query.
	if ragStore != nil {
		defer ragStore.Close()
	}

	if len(tools)+len(ragBuiltins) == 0 {
		return fmt.Errorf("no tools available after filtering; check include/exclude in %q", configFile)
	}

	port := mcpPort
	if port == 0 {
		port = cfg.MCPPort()
	}
	if port == 0 {
		port = defaultMCPPort
	}

	return mcpserver.Serve(ctx, tools, mcpserver.Options{
		Name:         cfg.Identity,
		Version:      util.Version(),
		Addr:         fmt.Sprintf(":%d", port),
		Instructions: cfg.MCPInstructions(),
		ConfirmTags:  cfg.ConfirmTags(),
		ConfirmMode:  mcpserver.ConfirmMode(cfg.ConfirmOverMCPMode()),
		Builtins:     ragBuiltins,
	})
}
