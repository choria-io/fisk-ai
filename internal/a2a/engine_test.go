//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"

	"github.com/choria-io/fisk"
	"github.com/choria-io/fisk-ai/internal/toolkit"
	fisk2 "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// discardLogger is a slog logger that drops all output, for tests that build a
// Server directly without applyDefaults.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// toolsFor builds tools from an in-process fisk application's introspection, the
// same path the real commands use.
func toolsFor(app *fisk.Application) []*fisk2.FiskCommandTool {
	GinkgoHelper()

	tools, err := fisk2.ApplicationTools(introspect(app))
	Expect(err).NotTo(HaveOccurred())

	return tools
}

var _ = Describe("expectProtocol", func() {
	It("Should return the decoded message when the protocol matches", func() {
		req := NewToolRequest("ping", nil)
		stampRequest(&req.Header, "me", "you")
		data, err := json.Marshal(req)
		Expect(err).NotTo(HaveOccurred())

		msg, err := expectProtocol(data, ToolRequestProtocol)
		Expect(err).NotTo(HaveOccurred())
		Expect(msg).To(BeAssignableToTypeOf(&ToolRequest{}))
	})

	It("Should reject a message whose protocol is not the one the path carries", func() {
		req := NewDiscoveryRequest()
		stampRequest(&req.Header, "me", "you")
		data, err := json.Marshal(req)
		Expect(err).NotTo(HaveOccurred())

		_, err = expectProtocol(data, ToolRequestProtocol)
		Expect(err).To(MatchError(ErrProtocolMismatch))
	})

	It("Should reject an undecodable body", func() {
		_, err := expectProtocol([]byte(`{"protocol":"nope"}`), ToolRequestProtocol)
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
		Expect(card.Protocols).To(ConsistOf(ProtocolNamespace))
		Expect(card.Tools).To(HaveLen(1))
		Expect(card.Tools[0].Name).To(Equal("ping"))
		Expect(card.Tools[0].InputSchema).NotTo(BeEmpty())
	})
})

var _ = Describe("selectExposed", func() {
	server := func() *Server {
		return &Server{opts: ServerOptions{Logger: discardLogger()}, byName: map[string]*fisk2.FiskCommandTool{}}
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

		s := &Server{opts: ServerOptions{Logger: discardLogger(), ConfirmTags: []string{"impact:rw"}}, byName: map[string]*fisk2.FiskCommandTool{}}
		exposed := s.selectExposed(toolsFor(app))
		Expect(exposed).To(HaveLen(1))
		Expect(exposed[0].Name()).To(Equal("keep"))
	})

	It("Should drop a tool that advertises no description", func() {
		app := fisk.New("app", "an app")
		app.Command("keep", "kept")
		app.Command("bare", "")

		s := server()
		exposed := s.selectExposed(toolsFor(app))

		names := make([]string, len(exposed))
		for i, t := range exposed {
			names[i] = t.Name()
		}
		Expect(names).To(ConsistOf("keep"))
		Expect(s.byName).NotTo(HaveKey("bare"))
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
		res := resultToToolResult(&toolkit.CommandResult{Command: "ping", ExitCode: 2, Output: "out", Truncated: true}, nil)
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
