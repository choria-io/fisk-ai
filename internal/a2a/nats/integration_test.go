//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package nats

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/choria-io/fisk"
	fisk2 "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/a2a"
	"github.com/choria-io/fisk-ai/internal/conns"
)

// runNATS starts an embedded NATS server on a random port and returns a client
// connection. Both are torn down when the spec ends. The Describe is labeled
// Integration so the unit suite (ginkgo --skip Integration) does not run it.
func runNATS() *nats.Conn {
	GinkgoHelper()

	ns, err := natsd.NewServer(&natsd.Options{Host: "127.0.0.1", Port: -1})
	Expect(err).NotTo(HaveOccurred())

	go ns.Start()
	Expect(ns.ReadyForConnections(10 * time.Second)).To(BeTrue())
	DeferCleanup(ns.Shutdown)

	nc, err := nats.Connect(ns.ClientURL())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(nc.Close)

	return nc
}

// serveOver builds an a2a server for tools reachable at identity over a nats
// transport on nc.
func serveOver(nc *nats.Conn, identity string, tools []*fisk2.FiskCommandTool) *a2a.Server {
	GinkgoHelper()

	transport, err := a2a.NewTransport("nats", conns.New(conns.WithNats(nc)), a2a.TransportConfig{Identity: identity})
	Expect(err).NotTo(HaveOccurred())

	srv, err := a2a.NewServer(transport, tools, a2a.ServerOptions{Identity: identity, Version: "v1", LogOutput: io.Discard})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(srv.Stop)

	return srv
}

// clientOver builds an a2a client that sends as sender over a nats transport on nc.
func clientOver(nc *nats.Conn, sender string, timeout time.Duration) *a2a.Client {
	GinkgoHelper()

	transport, err := a2a.NewTransport("nats", conns.New(conns.WithNats(nc)), a2a.TransportConfig{Identity: sender, Timeout: timeout})
	Expect(err).NotTo(HaveOccurred())

	client, err := a2a.NewClient(transport, sender)
	Expect(err).NotTo(HaveOccurred())

	return client
}

// servingApp builds tools whose single command runs a stand-in executable, so a
// served tool call actually executes.
func servingApp(name, body string) []*fisk2.FiskCommandTool {
	GinkgoHelper()

	app := fisk.New("app", "an app")
	app.Command(name, "a command")

	tools := toolsFor(app)
	path := filepath.Join(GinkgoT().TempDir(), "app")
	Expect(os.WriteFile(path, []byte(body), 0o700)).To(Succeed())
	for _, t := range tools {
		t.AppPath = path
	}

	return tools
}

// toolsFor builds tools from an in-process fisk application's introspection.
func toolsFor(app *fisk.Application) []*fisk2.FiskCommandTool {
	GinkgoHelper()

	tools, err := fisk2.ApplicationTools(introspect(app))
	Expect(err).NotTo(HaveOccurred())

	return tools
}

var _ = Describe("Integration: a2a NATS round-trip", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("Should discover an agent and invoke one of its tools", func() {
		nc := runNATS()

		serveOver(nc, "svc", servingApp("ping", "#!/bin/sh\necho pong\n"))
		client := clientOver(nc, "caller", time.Second)

		card, err := client.Discover(ctx, "svc")
		Expect(err).NotTo(HaveOccurred())
		Expect(card.Name).To(Equal("svc"))
		Expect(card.Tools).To(HaveLen(1))
		Expect(card.Tools[0].Name).To(Equal("ping"))

		reply, err := client.InvokeTool(ctx, "svc", "ping", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(reply.IsError).To(BeFalse())
		Expect(reply.Output).To(Equal("pong\n"))
		Expect(reply.Exec.ExitCode).To(Equal(0))
	})

	It("Should report a tool that does not exist as an in-band error", func() {
		nc := runNATS()

		serveOver(nc, "svc", servingApp("ping", "#!/bin/sh\necho pong\n"))
		client := clientOver(nc, "caller", time.Second)

		reply, err := client.InvokeTool(ctx, "svc", "missing", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(reply.IsError).To(BeTrue())
		Expect(reply.Output).To(ContainSubstring("not available"))
	})

	It("Should report an unknown agent as unavailable", func() {
		nc := runNATS()

		client := clientOver(nc, "caller", 500*time.Millisecond)

		_, err := client.Discover(ctx, "nobody")
		Expect(err).To(MatchError(a2a.ErrAgentUnavailable))
	})

	It("Should reject a tool request that arrives on the discovery subject", func() {
		nc := runNATS()

		serveOver(nc, "svc", servingApp("ping", "#!/bin/sh\necho pong\n"))

		// A tool request published to the discovery subject must be refused: each
		// subject carries only the one message type it is contracted for.
		toolReq := []byte(`{"protocol":"io.choria.fisk-ai.v1.tool.request","id":"x","request":"x","conversation":"x","sequence":0,"time":"2026-01-01T00:00:00Z","sender":{"name":"caller"},"name":"ping"}`)
		msg, err := nc.Request(DiscoverySubject("svc"), toolReq, time.Second)
		Expect(err).NotTo(HaveOccurred())

		// micro encodes a handler error in the reply headers rather than the body.
		Expect(msg.Header.Get("Nats-Service-Error-Code")).To(Equal("400"))
		Expect(msg.Header.Get("Nats-Service-Error")).To(ContainSubstring("want"))
	})
})

// introspect drives an application's --fisk-introspect handler in-process and
// returns the parsed model with its per-command schemas populated.
func introspect(app *fisk.Application) *fisk.ApplicationModel {
	GinkgoHelper()

	app.Terminate(func(int) {})

	r, w, err := os.Pipe()
	Expect(err).NotTo(HaveOccurred())

	stdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = stdout }()

	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	_, err = app.Parse([]string{"--fisk-introspect"})
	Expect(err).NotTo(HaveOccurred())
	Expect(w.Close()).To(Succeed())

	var model fisk.ApplicationModel
	Expect(json.Unmarshal(<-captured, &model)).To(Succeed())

	return &model
}
