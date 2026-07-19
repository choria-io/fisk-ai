//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"

	"github.com/choria-io/fisk"
	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	_ "github.com/choria-io/fisk-ai/internal/a2a/nats"
	"github.com/choria-io/fisk-ai/internal/conns"
	fisktool "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	"github.com/choria-io/fisk-ai/internal/util"
	"github.com/choria-io/ui/columns"
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
	ctx, cancel := interruptContext()
	defer cancel()

	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeServer)
	if err != nil {
		return err
	}

	if !cfg.A2AEnabled() {
		return fmt.Errorf("fisk-ai a2a requires expose.agent.agent_to_agent: true in %q; this agent is not configured to serve its tools to other agents", configFile)
	}

	tools, err := fisktool.ServedTools(cfg)
	if err != nil {
		return err
	}
	if len(tools) == 0 {
		return fmt.Errorf("no tools available after filtering; check include/exclude in %q", configFile)
	}

	provider, err := conns.Connect(cfg.NatsContext, cfg.Identity)
	if err != nil {
		return err
	}
	defer provider.Close()

	transport, err := a2a.NewTransport(cfg.A2ATransport(), provider, a2a.TransportConfig{Identity: cfg.Identity})
	if err != nil {
		return err
	}

	srv, err := a2a.NewServer(transport, tools, a2a.ServerOptions{
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

	c := columns.New()
	c.Headingf("Serving {bold}%d{/bold} tools over a2a as {bold}%s{/bold}/{bold}%s{/bold} on NATS context {bold}%s{/bold}", len(exposed), cfg.Identity, util.Version(), cfg.NatsContext)
	for _, line := range srv.Describe() {
		c.Item(line.Label, line.Value)
	}
	c.Values("Exposed", exposed)

	fmt.Fprintln(os.Stderr, c.String())

	<-ctx.Done()
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "a2a server stopped")

	return nil
}
