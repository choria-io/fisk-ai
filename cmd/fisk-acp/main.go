//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Command fisk-acp is a proof of concept that serves a remote Fisk agent over the
// Agent Client Protocol, so an ACP client such as Gold Band or Zed can drive it.
//
// It is deliberately the smallest thing that works: it reaches an agent somebody else
// runs, over NATS, and speaks ACP on stdin and stdout. Nothing is hosted here, which is
// the arrangement worth proving, since it is the one that carries failover and
// geographic placement.
//
// What it does not do is as deliberate as what it does. No session/load, list, resume
// or delete, no modes, no slash commands, no plan, no usage, no filesystem, no
// terminals, and no deltas. Each of those is a capability left undeclared rather than a
// method that answers badly.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	acp "github.com/coder/acp-go-sdk"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	_ "github.com/choria-io/fisk-ai/internal/a2a/nats"
	"github.com/choria-io/fisk-ai/internal/conns"
)

// clientSender is what this bridge calls itself on the wire. Like the terminal's, it is
// a name rather than a credential: nothing verifies it.
const clientSender = "fisk-acp"

func main() {
	var (
		configFile  string
		natsContext string
		agentName   string
		debug       bool
	)

	flag.StringVar(&configFile, "config", "client.yaml", "Configuration file to read identity and nats_context from")
	flag.StringVar(&natsContext, "context", "", "NATS context name, instead of reading it from the config")
	flag.StringVar(&agentName, "agent", "", "Identity of the agent to reach, instead of reading it from the config")
	flag.BoolVar(&debug, "debug", false, "Log protocol diagnostics to stderr")
	flag.Parse()

	if err := run(configFile, natsContext, agentName, debug); err != nil {
		fmt.Fprintf(os.Stderr, "fisk-acp: %v\n", err)
		os.Exit(1)
	}
}

func run(configFile string, natsContext string, agentName string, debug bool) error {
	// A client is interrupted by its host closing the pipe rather than by a signal, but
	// a person running this by hand to see the traffic expects Ctrl-C to end it.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	level := slog.LevelWarn
	if debug {
		level = slog.LevelDebug
	}

	// stdout is the protocol, so every log line goes to stderr. A single stray write to
	// stdout corrupts the framing and the client sees a parse error rather than a log.
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := config.ParseConfigFileForMode(configFile, config.ModeMCP)
	if err != nil {
		return fmt.Errorf("reading %q: %w", configFile, err)
	}

	if natsContext == "" {
		natsContext = cfg.NatsContext
	}
	if natsContext == "" {
		return fmt.Errorf("no nats_context in %q; set it or pass -context", configFile)
	}

	if agentName == "" {
		agentName = cfg.Identity
	}
	if agentName == "" {
		return fmt.Errorf("no identity in %q; set it or pass -agent", configFile)
	}

	provider, err := conns.ConnectNatsContext(ctx, natsContext, conns.Config{Product: cfg.ProductName(), Name: clientSender})
	if err != nil {
		return err
	}
	defer provider.Close()

	transport, err := a2a.NewTransport(cfg.A2ATransport(), a2a.TransportConfig{
		Resources: provider,
		Identity:  clientSender,
		Timeout:   cfg.A2ARequestTimeout(),
		Logger:    log,
	})
	if err != nil {
		return err
	}
	defer transport.Close()

	client, err := a2a.NewClient(transport, clientSender)
	if err != nil {
		return err
	}

	log.Debug("Serving ACP for a remote agent", "agent", agentName, "context", natsContext)

	bridge := newBridge(client, agentName, log)
	conn := acp.NewAgentSideConnection(bridge, os.Stdout, os.Stdin)
	conn.SetLogger(log)
	bridge.setConnection(conn)

	select {
	case <-conn.Done():
	case <-ctx.Done():
	}

	return nil
}
