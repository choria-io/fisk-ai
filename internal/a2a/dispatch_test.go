//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/choria-io/fisk"
	fisk2 "github.com/choria-io/fisk-ai/internal/toolkit/fisk"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// fakeTransport is an a2a.Transport that records the handlers the Server registers
// so a test can drive them directly, without a wire. RoundTrip is unused here.
type fakeTransport struct {
	handlers map[RouteHint]Handler
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{handlers: map[RouteHint]Handler{}}
}

func (f *fakeTransport) RoundTrip(context.Context, string, RouteHint, []byte) ([]byte, error) {
	return nil, nil
}

func (f *fakeTransport) Serve(op RouteHint, h Handler) error {
	f.handlers[op] = h
	return nil
}

func (f *fakeTransport) Describe(string) []DescLine { return nil }

func (f *fakeTransport) Close() error { return nil }

// fakeReplier captures the single reply a handler produces.
type fakeReplier struct {
	body      []byte
	code      string
	responded atomic.Bool
	errored   atomic.Bool
}

func (r *fakeReplier) Respond(body []byte) error {
	r.body = body
	r.responded.Store(true)
	return nil
}

func (r *fakeReplier) Error(code, _ string) error {
	r.code = code
	r.errored.Store(true)
	return nil
}

// toolRequestBody builds a schema-valid tool request body for name.
func toolRequestBody(name string) []byte {
	GinkgoHelper()

	req := NewToolRequest(name, nil)
	stampRequest(&req.Header, "caller", "svc")
	data, err := json.Marshal(req)
	Expect(err).NotTo(HaveOccurred())

	return data
}

var _ = Describe("handleTool dispatch", func() {
	It("Should hard-deny a confirm-gated tool: absent from the card and not invokable", func() {
		app := fisk.New("app", "an app")
		app.Command("keep", "kept")
		app.Command("danger", "gated").Tag("ai:confirm")

		ft := newFakeTransport()
		srv, err := NewServer(ft, toolsFor(app), ServerOptions{Identity: "svc", LogOutput: io.Discard})
		Expect(err).NotTo(HaveOccurred())

		// The gated tool is not advertised in the card.
		Expect(srv.ExposedTools()).To(ConsistOf("keep"))

		// A direct request for it is refused in-band, never run.
		rep := &fakeReplier{}
		ft.handlers[OpTool](context.Background(), toolRequestBody("danger"), rep)
		Expect(rep.responded.Load()).To(BeTrue())

		var reply ToolReply
		Expect(json.Unmarshal(rep.body, &reply)).To(Succeed())
		Expect(reply.IsError).To(BeTrue())
		Expect(reply.Output).To(ContainSubstring("not available"))
	})
})

// servingApp builds a single-command tool whose command runs a stand-in script,
// so a served tool call actually executes.
func servingApp(name, body string) []*fisk2.FiskCommandTool {
	GinkgoHelper()

	app := fisk.New("app", "an app")
	app.Command(name, "a command")

	tools, err := fisk2.ApplicationTools(introspect(app))
	Expect(err).NotTo(HaveOccurred())

	path := filepath.Join(GinkgoT().TempDir(), "app")
	Expect(os.WriteFile(path, []byte(body), 0o700)).To(Succeed())
	for _, t := range tools {
		t.AppPath = path
	}

	return tools
}

var _ = Describe("Integration: a2a intake back-pressure", func() {
	It("Should not enter a second tool request until the first releases its slot", func() {
		dir := GinkgoT().TempDir()
		runs := filepath.Join(dir, "runs")
		gate := filepath.Join(dir, "gate")

		// The script records that it entered (append to runs) then blocks until the
		// gate file appears. A second request that never enters never appends.
		body := fmt.Sprintf("#!/bin/sh\necho run >> %q\nwhile [ ! -f %q ]; do sleep 0.02; done\necho done\n", runs, gate)

		ft := newFakeTransport()
		// NewServer's side effect is what the test needs: it registers the tool
		// handler on ft and records the tool under byName.
		_, err := NewServer(ft, servingApp("block", body), ServerOptions{Identity: "svc", Concurrency: 1, LogOutput: io.Discard})
		Expect(err).NotTo(HaveOccurred())

		runLines := func() int {
			data, err := os.ReadFile(runs)
			if err != nil {
				return 0
			}
			n := 0
			for _, b := range data {
				if b == '\n' {
					n++
				}
			}
			return n
		}

		rep1 := &fakeReplier{}
		// The first request acquires the only slot and its worker starts; the call
		// itself returns once the worker is spawned.
		ft.handlers[OpTool](context.Background(), toolRequestBody("block"), rep1)
		Eventually(runLines).Should(Equal(1))

		// The second request must block at intake: with the slot held, its handler
		// call does not return and its worker never starts, so runs stays at 1.
		entered := make(chan struct{})
		rep2 := &fakeReplier{}
		go func() {
			ft.handlers[OpTool](context.Background(), toolRequestBody("block"), rep2)
			close(entered)
		}()

		Consistently(runLines, 200*time.Millisecond, 20*time.Millisecond).Should(Equal(1))
		Expect(rep2.responded.Load()).To(BeFalse())
		select {
		case <-entered:
			Fail("second tool request entered before the first released its slot")
		default:
		}

		// Releasing the first frees the slot; the second now enters and both answer.
		Expect(os.WriteFile(gate, []byte("go"), 0o600)).To(Succeed())
		Eventually(runLines).Should(Equal(2))
		Eventually(rep1.responded.Load).Should(BeTrue())
		Eventually(rep2.responded.Load).Should(BeTrue())
		Eventually(entered).Should(BeClosed())
	})
})
