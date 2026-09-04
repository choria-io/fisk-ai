//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

// Command fisk-web is a proof of concept that serves a remote Fisk agent over the
// Vercel AI SDK UI Message Stream, so a browser page built on @ai-sdk/react can drive it.
//
// It is the smallest thing that works: it reaches an agent somebody else runs, over
// NATS, and answers one HTTP route with the server-sent event format the AI SDK's own
// server produces. Nothing is hosted here, which is the arrangement worth proving, since
// it is the one that carries failover and geographic placement.
//
// What it does not do is as deliberate as what it does. Nothing survives the process, so
// a restart loses every thread. There is no authentication, no caller verification, no
// warnings, no plan or state, no usage metadata, no regenerate, no resumable stream GET,
// no thread listing, and no experimental_toolApprovalSecret. That last one is the AI
// SDK's answer to a page forging an approval response, and this endpoint sidesteps it by
// correlating on ids the worker minted.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/choria-io/fisk-ai/config"
	"github.com/choria-io/fisk-ai/internal/a2a"
	_ "github.com/choria-io/fisk-ai/internal/a2a/nats"
	"github.com/choria-io/fisk-ai/internal/conns"
)

// clientSender is what this bridge calls itself on the wire. Like the terminal's, it is
// a name rather than a credential: nothing verifies it.
const clientSender = "fisk-web"

// shutdownTimeout is how long a turn already in flight has to finish once the process is
// asked to stop. A turn is a model call and a person reading it, so this is generous
// rather than snappy.
const shutdownTimeout = 30 * time.Second

func main() {
	var (
		configFile  string
		natsContext string
		agentName   string
		listen      string
		origin      string
		debug       bool
	)

	flag.StringVar(&configFile, "config", "client.yaml", "Configuration file to read identity and nats_context from")
	flag.StringVar(&natsContext, "context", "", "NATS context name, instead of reading it from the config")
	flag.StringVar(&agentName, "agent", "", "Identity of the agent to reach, instead of reading it from the config")
	flag.StringVar(&listen, "listen", "127.0.0.1:8080", "Address to serve the chat endpoint on")
	flag.StringVar(&origin, "origin", "http://localhost:5173", "Browser origin allowed to call the chat endpoint")
	flag.BoolVar(&debug, "debug", false, "Log protocol diagnostics")
	flag.Parse()

	if err := run(configFile, natsContext, agentName, listen, origin, debug); err != nil {
		fmt.Fprintf(os.Stderr, "fisk-web: %v\n", err)
		os.Exit(1)
	}
}

func run(configFile string, natsContext string, agentName string, listen string, origin string, debug bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

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

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}

	bridge := newBridge(client, agentName, origin, log)

	// The listener's address rather than the flag, so a person who asked for port 0 is
	// told which port they got.
	log.Info("Serving a remote agent over the AI SDK UI message stream",
		"agent", agentName, "context", natsContext, "address", ln.Addr(), "origin", origin)

	srv := &http.Server{Handler: bridge.mux()}

	errCh := make(chan error, 1)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err

	case <-ctx.Done():
		// Shutdown does not cancel a request context, so a turn in flight runs to its own
		// end rather than being cut off part way through the model call somebody is
		// reading.
		stop, done := context.WithTimeout(context.Background(), shutdownTimeout)
		defer done()

		return srv.Shutdown(stop)
	}
}
