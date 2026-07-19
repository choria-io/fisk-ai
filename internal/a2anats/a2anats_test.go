//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2anats

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/choria-io/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/a2a"
	"github.com/choria-io/fisk-ai/internal/conns"
	"github.com/choria-io/fisk-ai/internal/util"
)

func TestA2ANats(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Internal/A2ANats")
}

// discardLogger is a slog logger that drops all output, for tests that build a
// Server directly without applyDefaults.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// toolsFor builds tools from an in-process fisk application's introspection, the
// same path the real commands use.
func toolsFor(app *fisk.Application) []*util.Tool {
	GinkgoHelper()

	tools, err := util.ApplicationTools(introspect(app))
	Expect(err).NotTo(HaveOccurred())

	return tools
}

var _ = Describe("Subjects", func() {
	It("Should namespace discovery and tool subjects under the prefix and identity", func() {
		Expect(DiscoverySubject("nats")).To(Equal("choria.fisk-ai.discovery.nats"))
		Expect(ToolSubject("orders-db")).To(Equal("choria.fisk-ai.tool.orders-db"))
	})
})

var _ = Describe("NewClientFromProvider", func() {
	It("Should fail when the provider carries no NATS connection", func() {
		c, err := NewClientFromProvider(conns.New(), "caller", time.Second)
		Expect(err).To(MatchError(ContainSubstring("requires a NATS connection")))
		Expect(c).To(BeNil())
	})
})

var _ = Describe("expectProtocol", func() {
	It("Should return the decoded message when the protocol matches", func() {
		req := a2a.NewToolRequest("ping", nil)
		stampRequest(&req.Header, "me", "you")
		data, err := json.Marshal(req)
		Expect(err).NotTo(HaveOccurred())

		msg, err := expectProtocol(data, a2a.ToolRequestProtocol)
		Expect(err).NotTo(HaveOccurred())
		Expect(msg).To(BeAssignableToTypeOf(&a2a.ToolRequest{}))
	})

	It("Should reject a message whose protocol is not the one the subject carries", func() {
		req := a2a.NewDiscoveryRequest()
		stampRequest(&req.Header, "me", "you")
		data, err := json.Marshal(req)
		Expect(err).NotTo(HaveOccurred())

		_, err = expectProtocol(data, a2a.ToolRequestProtocol)
		Expect(err).To(MatchError(ErrProtocolMismatch))
	})

	It("Should reject an undecodable body", func() {
		_, err := expectProtocol([]byte(`{"protocol":"nope"}`), a2a.ToolRequestProtocol)
		Expect(err).To(MatchError(ErrProtocolMismatch))
	})
})

var _ = Describe("buildCard", func() {
	It("Should describe the agent and its tools", func() {
		app := fisk.New("app", "an app")
		app.Command("ping", "ping it")

		card := buildCard("svc", "v1", toolsFor(app))
		Expect(card.Name).To(Equal("svc"))
		Expect(card.Version).To(Equal("v1"))
		Expect(card.Protocols).To(ConsistOf(a2a.ProtocolNamespace))
		Expect(card.Tools).To(HaveLen(1))
		Expect(card.Tools[0].Name).To(Equal("ping"))
		Expect(card.Tools[0].InputSchema).NotTo(BeEmpty())
	})
})

var _ = Describe("selectExposed", func() {
	server := func() *Server {
		return &Server{opts: ServerOptions{Logger: discardLogger()}, byName: map[string]*util.Tool{}}
	}

	It("Should drop confirmation-gated tools and keep the rest", func() {
		app := fisk.New("app", "an app")
		app.Command("keep", "kept")
		app.Command("danger", "gated").Tag("ai:confirm")

		s := server()
		exposed := s.selectExposed(toolsFor(app))

		names := make([]string, len(exposed))
		for i, t := range exposed {
			names[i] = t.Name()
		}
		Expect(names).To(ConsistOf("keep"))
		Expect(s.byName).To(HaveKey("keep"))
		Expect(s.byName).NotTo(HaveKey("danger"))
	})

	It("Should drop tools gated by a configured confirm tag", func() {
		app := fisk.New("app", "an app")
		app.Command("keep", "kept")
		app.Command("rw", "writes").Tag("impact:rw")

		s := &Server{opts: ServerOptions{Logger: discardLogger(), ConfirmTags: []string{"impact:rw"}}, byName: map[string]*util.Tool{}}
		exposed := s.selectExposed(toolsFor(app))
		Expect(exposed).To(HaveLen(1))
		Expect(exposed[0].Name()).To(Equal("keep"))
	})
})

var _ = Describe("resultToToolResult", func() {
	It("Should map a harness error to an error result", func() {
		res := resultToToolResult(nil, errors.New("could not run"))
		Expect(res.IsError).To(BeTrue())
		Expect(res.Output).To(Equal("could not run"))
		Expect(res.Exec).To(BeNil())
	})

	It("Should map a command outcome to a successful result with exec metadata", func() {
		res := resultToToolResult(&util.CommandResult{Command: "ping", ExitCode: 2, Output: "out", Truncated: true}, nil)
		Expect(res.IsError).To(BeFalse())
		Expect(res.Output).To(Equal("out"))
		Expect(res.Exec.Command).To(Equal("ping"))
		Expect(res.Exec.ExitCode).To(Equal(2))
		Expect(res.Exec.Truncated).To(BeTrue())
	})
})

var _ = Describe("normalizeInput", func() {
	It("Should drop empty and null input", func() {
		Expect(normalizeInput(nil)).To(BeNil())
		Expect(normalizeInput(json.RawMessage(`null`))).To(BeNil())
	})

	It("Should keep a real object", func() {
		Expect(normalizeInput(json.RawMessage(`{"a":1}`))).To(Equal(json.RawMessage(`{"a":1}`)))
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
