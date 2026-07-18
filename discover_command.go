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
	"github.com/choria-io/ui/columns"
	"github.com/jedib0t/go-pretty/v6/table"
)

// maxDiscoverDescriptionLen is the width at which tool descriptions are truncated
// in the discover table.
const maxDiscoverDescriptionLen = 60

var (
	discoverAgent   string
	natsContextFlag string
)

func registerDiscoverAction(cmd *fisk.Application) {
	discover := cmd.Command("discover", "Discovers a remote agent over NATS and prints its tools").Action(discoverAction)
	discover.Arg("agent", "Identity of the agent to discover").Required().StringVar(&discoverAgent)
	discover.Flag("config", "Path to the agent configuration file to read nats_context from").Default("agent.yaml").StringVar(&configFile)
	discover.Flag("context", "NATS context name to use, instead of reading it from the config").StringVar(&natsContextFlag)
}

// discoverAction sends a discovery request to a named agent and prints its agent
// card: a quick way to confirm an agent is reachable and see the tools it exposes
// before wiring it into remote_tools. The NATS context comes from --context when
// given, otherwise from nats_context in the config file.
func discoverAction(_ *fisk.ParseContext) error {
	ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	contextName, sender, err := discoverContext()
	if err != nil {
		return err
	}

	client, err := a2anats.Connect(contextName, sender, 0)
	if err != nil {
		return err
	}
	defer client.Close()

	card, err := client.Discover(ctx, discoverAgent)
	if err != nil {
		return err
	}

	c := columns.New()
	c.Headingf("Agent Card for {bold}%s{/bold}", discoverAgent)
	c.Item("Agent", card.Name)
	c.Item("Version", card.Version)
	c.ItemUnlessZero("Description", card.Description)
	c.ItemUnlessZero("Protocols", card.Protocols)
	fmt.Println(c.String())

	if len(card.Tools) == 0 {
		fmt.Println("The agent exposes no tools.")
		return nil
	}

	tbl := util.NewTable(os.Stdout)
	tbl.AppendHeader(table.Row{"Tool", "Description"})
	for _, t := range card.Tools {
		tbl.AppendRow(table.Row{t.Name, util.TruncateString(t.Description, maxDiscoverDescriptionLen)})
	}
	tbl.Render()

	return nil
}

// discoverContext resolves the NATS context name and the sender identity to use.
// A --context flag takes precedence and needs no config file; otherwise the
// config file is read for nats_context and the agent's identity is used as the
// sender. The sender defaults to "fisk-ai" when no config identity is available.
func discoverContext() (contextName string, sender string, err error) {
	if natsContextFlag != "" {
		return natsContextFlag, "fisk-ai", nil
	}

	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeMCP)
	if err != nil {
		return "", "", fmt.Errorf("reading %q for nats_context (or pass --context): %w", configFile, err)
	}
	if cfg.NatsContext == "" {
		return "", "", fmt.Errorf("no nats_context in %q; set it or pass --context", configFile)
	}

	sender = cfg.Identity
	if sender == "" {
		sender = "fisk-ai"
	}

	return cfg.NatsContext, sender, nil
}
