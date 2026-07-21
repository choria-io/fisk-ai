//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// readTrace reads a JSON-lines trace file back into events.
func readTrace(path string) []traceEvent {
	f, err := os.Open(path)
	Expect(err).ToNot(HaveOccurred())
	defer f.Close()

	var events []traceEvent
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var ev traceEvent
		Expect(json.Unmarshal(sc.Bytes(), &ev)).To(Succeed())
		events = append(events, ev)
	}
	Expect(sc.Err()).ToNot(HaveOccurred())

	return events
}

// newTraceReq builds a POST request whose body is bodyJSON; http.NewRequest sets
// GetBody for a strings.Reader, mirroring how the SDK presents request bodies.
func newTraceReq(bodyJSON string) *http.Request {
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(bodyJSON))
	Expect(err).ToNot(HaveOccurred())

	return req
}

// okNext returns a middleware terminator yielding a fixed response.
func okNext(status int, body string) llm.MiddlewareNext {
	return func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d OK", status),
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	}
}

// errReadCloser is a response body that fails on Read.
type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errors.New("read boom") }
func (errReadCloser) Close() error             { return nil }

var _ = Describe("Tracer", func() {
	var path string

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), "trace.jsonl")
	})

	It("Should record a paired request and response sharing an id and restore the body", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		req := newTraceReq(`{"model":"x"}`)
		resp, err := tr.Middleware(req, okNext(200, `{"id":"msg_1"}`))
		Expect(err).ToNot(HaveOccurred())

		raw, err := io.ReadAll(resp.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(string(raw)).To(Equal(`{"id":"msg_1"}`))
		Expect(tr.Close()).To(Succeed())

		events := readTrace(path)
		Expect(events).To(HaveLen(2))
		Expect(events[0].Type).To(Equal("request"))
		Expect(events[1].Type).To(Equal("response"))
		Expect(events[0].ID).To(Equal(1))
		Expect(events[1].ID).To(Equal(events[0].ID))
		Expect(events[0].Method).To(Equal("POST"))
		Expect(string(events[0].Body)).To(Equal(`{"model":"x"}`))
		Expect(events[1].StatusCode).To(Equal(200))
		Expect(string(events[1].Body)).To(Equal(`{"id":"msg_1"}`))
	})

	It("Should label events with the iteration and retry attempt", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		req := newTraceReq(`{}`)
		req = req.WithContext(WithTraceIteration(req.Context(), 3))
		req.Header.Set("X-Stainless-Retry-Count", "2")

		_, err = tr.Middleware(req, okNext(200, `{}`))
		Expect(err).ToNot(HaveOccurred())
		Expect(tr.Close()).To(Succeed())

		events := readTrace(path)
		Expect(events[0].Iter).ToNot(BeNil())
		Expect(*events[0].Iter).To(Equal(3))
		Expect(events[0].Attempt).To(Equal(2))
		Expect(events[1].Attempt).To(Equal(2))
	})

	It("Should wrap a non-JSON body as a JSON string", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		_, err = tr.Middleware(newTraceReq(`{}`), okNext(500, "boom not json"))
		Expect(err).ToNot(HaveOccurred())
		Expect(tr.Close()).To(Succeed())

		events := readTrace(path)
		var s string
		Expect(json.Unmarshal(events[1].Body, &s)).To(Succeed())
		Expect(s).To(Equal("boom not json"))
	})

	It("Should record an error line and propagate the transport error", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		boom := errors.New("dial fail")
		next := func(_ *http.Request) (*http.Response, error) { return nil, boom }

		_, err = tr.Middleware(newTraceReq(`{}`), next)
		Expect(err).To(MatchError(boom))
		Expect(tr.Close()).To(Succeed())

		events := readTrace(path)
		Expect(events).To(HaveLen(2))
		Expect(events[1].Type).To(Equal("error"))
		Expect(events[1].Error).To(Equal("dial fail"))
		Expect(events[1].ID).To(Equal(events[0].ID))
	})

	It("Should not turn a response body read failure into a call failure", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		next := func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Status: "200 OK", Body: errReadCloser{}}, nil
		}

		resp, err := tr.Middleware(newTraceReq(`{}`), next)
		Expect(err).ToNot(HaveOccurred())
		Expect(resp).ToNot(BeNil())
		Expect(tr.Close()).To(Succeed())

		events := readTrace(path)
		Expect(events[1].Type).To(Equal("response"))
		Expect(events[1].Error).To(ContainSubstring("reading response body for trace"))
	})

	It("Should drop events written after Close", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		tr.RecordSession("m", "c", "v")
		Expect(tr.Close()).To(Succeed())

		_, err = tr.Middleware(newTraceReq(`{}`), okNext(200, `{}`))
		Expect(err).ToNot(HaveOccurred())

		events := readTrace(path)
		Expect(events).To(HaveLen(1))
		Expect(events[0].Type).To(Equal("session"))
	})

	It("Should write session and summary lines", func() {
		tr, err := NewTracer(path, nil)
		Expect(err).ToNot(HaveOccurred())

		tr.RecordSession("claude-x", "agent.yaml", "1.2.3")
		tr.RecordSummary(&RunStats{Session: "sess1", Suspended: true, LlmCalls: 2, ToolCalls: 1, InTokens: 10, OutTokens: 20, Start: time.Now()})
		Expect(tr.Close()).To(Succeed())

		events := readTrace(path)
		Expect(events).To(HaveLen(2))
		Expect(events[0].Type).To(Equal("session"))
		Expect(events[0].Model).To(Equal("claude-x"))
		Expect(events[0].Config).To(Equal("agent.yaml"))
		Expect(events[0].Version).To(Equal("1.2.3"))
		Expect(events[1].Type).To(Equal("summary"))
		Expect(events[1].LLMCalls).To(Equal(int64(2)))
		Expect(events[1].InTokens).To(Equal(int64(10)))
		Expect(events[1].Session).To(Equal("sess1"))
		Expect(events[1].Suspended).To(BeTrue())
	})

	It("Should error when the trace file already exists", func() {
		Expect(os.WriteFile(path, []byte("x"), 0o600)).To(Succeed())

		tr, err := NewTracer(path, nil)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("creating trace file"))
		Expect(tr).To(BeNil())
	})

	It("Should be safe to use on a nil Tracer", func() {
		var tr *Tracer

		resp, err := tr.Middleware(newTraceReq(`{}`), okNext(200, `{}`))
		Expect(err).ToNot(HaveOccurred())
		Expect(resp).ToNot(BeNil())
		Expect(tr.Close()).To(Succeed())
		tr.RecordSummary(nil)
	})
})

// failWriteCloser fails every write, to drive the tracer's write-failure path.
type failWriteCloser struct{}

func (failWriteCloser) Write([]byte) (int, error) { return 0, errors.New("disk full") }
func (failWriteCloser) Close() error              { return nil }

var _ = Describe("Tracer write-failure warn sink", func() {
	It("routes the first write failure to the injected sink instead of stderr", func() {
		var got error
		count := 0
		tr := &Tracer{w: failWriteCloser{}, warn: func(e error) {
			got = e
			count++
		}}

		tr.RecordSession("model", "config", "version")
		tr.RecordSession("model", "config", "version")

		Expect(got).To(HaveOccurred())
		// warnOnce: only the first failure is reported.
		Expect(count).To(Equal(1))
	})
})
