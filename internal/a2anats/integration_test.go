//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2anats

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/choria-io/fisk"
	natsd "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/conns"
	"github.com/choria-io/fisk-ai/internal/util"
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

// servingApp builds tools whose single command runs a stand-in executable, so a
// served tool call actually executes.
func servingApp(name, body string) []*util.Tool {
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

var _ = Describe("Integration: a2a NATS round-trip", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("Should discover an agent and invoke one of its tools", func() {
		nc := runNATS()

		tools := servingApp("ping", "#!/bin/sh\necho pong\n")
		srv, err := NewServer(nc, tools, ServerOptions{Identity: "svc", Version: "v1", LogOutput: io.Discard})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(srv.Stop)

		client, err := NewClient(nc, "caller", time.Second)
		Expect(err).NotTo(HaveOccurred())

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

	It("Should discover and invoke through a client built from a connection Provider", func() {
		nc := runNATS()

		srv, err := NewServer(nc, servingApp("ping", "#!/bin/sh\necho pong\n"), ServerOptions{Identity: "svc", Version: "v1", LogOutput: io.Discard})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(srv.Stop)

		client, err := NewClientFromProvider(conns.New(conns.WithNats(nc)), "caller", time.Second)
		Expect(err).NotTo(HaveOccurred())

		card, err := client.Discover(ctx, "svc")
		Expect(err).NotTo(HaveOccurred())
		Expect(card.Name).To(Equal("svc"))

		reply, err := client.InvokeTool(ctx, "svc", "ping", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(reply.IsError).To(BeFalse())
		Expect(reply.Output).To(Equal("pong\n"))
	})

	It("Should report a tool that does not exist as an in-band error", func() {
		nc := runNATS()

		srv, err := NewServer(nc, servingApp("ping", "#!/bin/sh\necho pong\n"), ServerOptions{Identity: "svc", Version: "v1", LogOutput: io.Discard})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(srv.Stop)

		client, err := NewClient(nc, "caller", time.Second)
		Expect(err).NotTo(HaveOccurred())

		reply, err := client.InvokeTool(ctx, "svc", "missing", nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(reply.IsError).To(BeTrue())
		Expect(reply.Output).To(ContainSubstring("not available"))
	})

	It("Should report an unknown agent as unavailable", func() {
		nc := runNATS()

		client, err := NewClient(nc, "caller", 500*time.Millisecond)
		Expect(err).NotTo(HaveOccurred())

		_, err = client.Discover(ctx, "nobody")
		Expect(err).To(MatchError(ErrAgentUnavailable))
	})

	It("Should reject a tool request that arrives on the discovery subject", func() {
		nc := runNATS()

		srv, err := NewServer(nc, servingApp("ping", "#!/bin/sh\necho pong\n"), ServerOptions{Identity: "svc", Version: "v1", LogOutput: io.Discard})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(srv.Stop)

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
