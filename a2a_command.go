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
	"github.com/choria-io/fisk-ai/internal/a2anats"
	"github.com/choria-io/fisk-ai/internal/util"
)

func registerA2AAction(cmd *fisk.Application) {
	a2aCmd := cmd.Command("a2a", "Serves the tools to other agents over NATS (a2a)").Action(a2aAction)
	a2aCmd.Flag("config", "Path to the agent configuration file").Default("agent.yaml").ExistingFileVar(&configFile)
}

// a2aAction serves the configured tools to other fisk-ai agents over NATS, the
// producer side of remote tool import. It is opt-in: the config must set
// expose.agent.agent_to_agent: true or the command refuses to start. Like the
// MCP server it needs only the application, its identity, and the tool filters;
// the prompt and model are not used. All progress goes to stderr. The served set
// is the agent's tools narrowed by expose.agent.tools when set. Tools gated behind
// operator confirmation (which a served agent has no operator to satisfy) are never
// exposed; use ai:deny to keep a command off entirely.
func a2aAction(_ *fisk.ParseContext) error {
	ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeServer)
	if err != nil {
		return err
	}

	if !cfg.A2AEnabled() {
		return fmt.Errorf("fisk-ai a2a requires expose.agent.agent_to_agent: true in %q; this agent is not configured to serve its tools to other agents", configFile)
	}

	tools, err := util.ServedTools(cfg)
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		return fmt.Errorf("no tools available after filtering; check include/exclude in %q", configFile)
	}

	srv, err := a2anats.Serve(cfg.NatsContext, tools, a2anats.ServerOptions{
		Identity:    cfg.Identity,
		Version:     util.Version(),
		ConfirmTags: cfg.ConfirmTags(),
	})
	if err != nil {
		return err
	}
	defer srv.Stop()

	exposed := srv.ExposedTools()
	if len(exposed) == 0 {
		return fmt.Errorf("no tools available to serve over a2a; all were filtered or confirmation-gated")
	}

	discovery, tool := srv.Subjects()
	fmt.Fprintf(os.Stderr, "Serving %d tools over a2a as %s/%s on NATS context %s\n", len(exposed), cfg.Identity, util.Version(), cfg.NatsContext)
	fmt.Fprintf(os.Stderr, "  discovery: %s\n", discovery)
	fmt.Fprintf(os.Stderr, "  tools:     %s\n", tool)
	for _, name := range exposed {
		fmt.Fprintf(os.Stderr, "  %s\n", name)
	}

	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "a2a server stopped")

	return nil
}
