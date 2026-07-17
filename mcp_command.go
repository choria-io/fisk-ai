//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/choria-io/fisk"
	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/mcpserver"
	"github.com/choria-io/fisk-ai/internal/rag"
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
	ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt)
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

	ragBuiltins, ragStore, err := mcpKnowledgeBuiltins(ctx, cfg)
	if err != nil {
		return err
	}
	// Close after Serve returns (Serve below is the final call, so this deferred
	// close runs only once graceful shutdown has drained in-flight tool calls),
	// never concurrently with a live query.
	if ragStore != nil {
		defer ragStore.Close()
	}

	if err := checkMCPBuiltinCollisions(tools, ragBuiltins); err != nil {
		return err
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

// mcpKnowledgeBuiltins opens the knowledge store read-only and returns the
// knowledge_search built-in (and the open store, for the caller to close after the
// server stops) when it is allowlisted in expose.agent.mcp.builtins. The store is
// opened only when allowlisted, so an agent-only knowledge config never opens the
// index over MCP; because the operator explicitly opted in, an index that cannot be
// opened cleanly (a stale rag_meta, a bad embeddings block) fails the command
// loudly rather than silently dropping the tool. It returns a nil store when
// knowledge_search is not exposed, printing a discoverability note if knowledge is
// enabled but simply not allowlisted.
func mcpKnowledgeBuiltins(ctx context.Context, cfg *config.Config) ([]*util.BuiltinTool, *rag.Store, error) {
	if !cfg.MCPExposesKnowledgeSearch() {
		if cfg.RAGEnabled() {
			fmt.Fprintln(os.Stderr, "note: knowledge is enabled but not exposed over MCP; add knowledge_search to expose.agent.mcp.builtins to let MCP clients search your knowledge base")
		}
		return nil, nil, nil
	}

	store, err := rag.Open(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot expose knowledge_search over MCP: %w", err)
	}

	line, err := store.TierLine(ctx)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	fmt.Fprintf(os.Stderr, "knowledge %s\n", line)
	if !store.Built() {
		fmt.Fprintln(os.Stderr, "note: the knowledge index is not built yet; knowledge_search will return index_not_built until you run: fisk-ai knowledge index")
	}

	return util.RAGTools(cfg, store), store, nil
}

// checkMCPBuiltinCollisions refuses to start when a wrapped command tool already
// exposes a name a built-in would use. The model addresses every tool by one flat
// name, so a collision would silently shadow one with the other; the allowlist is a
// deliberate security opt-in, so this is a hard error naming the fix rather than a
// skipped tool.
func checkMCPBuiltinCollisions(tools []*util.Tool, builtins []*util.BuiltinTool) error {
	if len(builtins) == 0 {
		return nil
	}

	names := make(map[string]bool, len(tools))
	for _, t := range tools {
		names[t.Name()] = true
	}
	for _, b := range builtins {
		if names[b.Name()] {
			return fmt.Errorf("cannot expose built-in %q over MCP: a wrapped command already exposes a tool with that name; exclude or rename it", b.Name())
		}
	}

	return nil
}
