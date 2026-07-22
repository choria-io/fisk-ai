//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/choria-io/fisk-ai/internal/llm"
)

// traceCtxKey is the context key under which the agent loop stashes the current
// iteration so the trace middleware can label requests with it.
type traceCtxKey struct{}

// WithTraceIteration returns a context carrying the agent loop iteration n, which
// the Tracer middleware reads off the request context to label its events. It is
// harmless for callers that do not trace.
func WithTraceIteration(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, traceCtxKey{}, n)
}

// traceIteration returns the iteration stored by WithTraceIteration, if any.
func traceIteration(ctx context.Context) (int, bool) {
	n, ok := ctx.Value(traceCtxKey{}).(int)
	return n, ok
}

// Tracer records every Anthropic API request and response as JSON lines, one
// object per line, to a file. It is installed as an SDK middleware so it sees the
// literal wire bodies, including any retries. All methods are safe to call on a
// nil Tracer, so the run path can wire it up unconditionally.
type Tracer struct {
	mu     sync.Mutex
	w      io.WriteCloser
	id     int
	closed bool
	warned bool
	// warn reports the first trace write failure. It exists because the tracer cannot
	// reach the run's event sink (util is a dependency of the agent, not the other way
	// around), so the caller injects a func that routes to it; nil falls back to stderr.
	warn func(error)
}

// traceEvent is one line in the trace file. Fields are omitted when empty so each
// event type carries only what applies to it. A request, its response (or error),
// and any retries of the same HTTP attempt share an ID; retries reuse Iter but get
// a new ID and an incremented Attempt.
type traceEvent struct {
	Type        string          `json:"type"`
	ID          int             `json:"id,omitempty"`
	Iter        *int            `json:"iter,omitempty"`
	Attempt     int             `json:"attempt,omitempty"`
	Time        string          `json:"time"`
	Method      string          `json:"method,omitempty"`
	URL         string          `json:"url,omitempty"`
	Status      string          `json:"status,omitempty"`
	StatusCode  int             `json:"status_code,omitempty"`
	DurationMS  int64           `json:"duration_ms,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	BodyMissing bool            `json:"body_unavailable,omitempty"`
	Error       string          `json:"error,omitempty"`

	// Session header fields.
	Model   string `json:"model,omitempty"`
	Config  string `json:"config,omitempty"`
	Version string `json:"version,omitempty"`

	// Summary footer fields.
	Session           string `json:"session,omitempty"`
	Suspended         bool   `json:"suspended,omitempty"`
	LLMCalls          int64  `json:"llm_calls,omitempty"`
	ToolCalls         int64  `json:"tool_calls,omitempty"`
	RemoteToolCalls   int64  `json:"remote_tool_calls,omitempty"`
	InTokens          int64  `json:"input_tokens,omitempty"`
	OutTokens         int64  `json:"output_tokens,omitempty"`
	CacheReadTokens   int64  `json:"cache_read_tokens,omitempty"`
	CacheCreateTokens int64  `json:"cache_create_tokens,omitempty"`
}

// NewTracer creates a trace file at path and returns a Tracer writing to it. The
// file must not already exist: an existing path is an error so a prior trace is
// never clobbered. It is created 0600 because the trace holds full prompts and
// responses. warn reports the first write failure to the caller (typically routed to
// the run's event sink so it stays attributable when many runs share one process); a
// nil warn falls back to stderr.
func NewTracer(path string, warn func(error)) (*Tracer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating trace file: %w", err)
	}

	return &Tracer{w: f, warn: warn}, nil
}

// Middleware records the request and its response (or error) as trace lines. Its
// http-shaped signature is an llm.Middleware, so the provider installs it without
// the caller naming an SDK type. Tracing never changes the call's outcome: the
// request body is read non-destructively, the response body is buffered and
// restored, and a trace-side read failure is logged rather than propagated.
func (t *Tracer) Middleware(req *http.Request, next llm.MiddlewareNext) (*http.Response, error) {
	if t == nil {
		return next(req)
	}

	id := t.nextID()

	var iter *int
	if n, ok := traceIteration(req.Context()); ok {
		iter = &n
	}

	attempt := 0
	if v := req.Header.Get("X-Stainless-Retry-Count"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			attempt = n
		}
	}

	reqEv := traceEvent{
		Type:    "request",
		ID:      id,
		Iter:    iter,
		Attempt: attempt,
		Time:    nowString(),
		Method:  req.Method,
		URL:     req.URL.String(),
	}
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err == nil {
			raw, _ := io.ReadAll(body)
			body.Close()
			reqEv.Body = jsonBody(raw)
		} else {
			reqEv.BodyMissing = true
		}
	} else {
		reqEv.BodyMissing = true
	}
	t.emit(reqEv)

	start := time.Now()
	resp, err := next(req)
	dur := time.Since(start).Milliseconds()

	if err != nil {
		t.emit(traceEvent{
			Type:       "error",
			ID:         id,
			Iter:       iter,
			Attempt:    attempt,
			Time:       nowString(),
			DurationMS: dur,
			Error:      err.Error(),
		})
		return resp, err
	}

	respEv := traceEvent{
		Type:       "response",
		ID:         id,
		Iter:       iter,
		Attempt:    attempt,
		Time:       nowString(),
		DurationMS: dur,
	}
	if resp != nil {
		respEv.Status = resp.Status
		respEv.StatusCode = resp.StatusCode
		if resp.Body != nil {
			raw, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			// Restore the body even on a read error so the SDK still parses
			// whatever was read; a trace-side failure must not change the outcome.
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			if rerr != nil {
				respEv.Error = fmt.Sprintf("reading response body for trace: %v", rerr)
			} else {
				respEv.Body = jsonBody(raw)
			}
		}
	}
	t.emit(respEv)

	return resp, nil
}

// RecordSession writes the session header line so a trace file is self-describing.
func (t *Tracer) RecordSession(model, configPath, version string) {
	t.emit(traceEvent{
		Type:    "session",
		Time:    nowString(),
		Model:   model,
		Config:  configPath,
		Version: version,
	})
}

// RecordSummary writes the summary footer line from the run counters.
func (t *Tracer) RecordSummary(stats *RunStats) {
	if t == nil || stats == nil {
		return
	}

	t.emit(traceEvent{
		Type:              "summary",
		Time:              nowString(),
		Session:           stats.Session,
		Suspended:         stats.Suspended,
		LLMCalls:          stats.LlmCalls,
		ToolCalls:         stats.ToolCalls,
		RemoteToolCalls:   stats.RemoteToolCalls,
		InTokens:          stats.InTokens,
		OutTokens:         stats.OutTokens,
		CacheReadTokens:   stats.CacheReadTokens,
		CacheCreateTokens: stats.CacheCreateTokens,
		DurationMS:        time.Since(stats.Start).Milliseconds(),
	})
}

// Close closes the trace file. After Close, further events are dropped.
func (t *Tracer) Close() error {
	if t == nil {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true

	return t.w.Close()
}

// nextID assigns the next monotonic event id.
func (t *Tracer) nextID() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.id++

	return t.id
}

// emit serializes ev as one line. A write failure warns once to stderr and is
// otherwise ignored so a broken trace never aborts the run.
func (t *Tracer) emit(ev traceEvent) {
	if t == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}

	line, err := json.Marshal(ev)
	if err != nil {
		t.warnOnce(fmt.Errorf("marshaling trace event: %w", err))
		return
	}
	line = append(line, '\n')

	_, err = t.w.Write(line)
	if err != nil {
		t.warnOnce(fmt.Errorf("writing trace: %w", err))
	}
}

// warnOnce reports the first trace write failure and stays quiet thereafter. It
// must be called with the mutex held.
func (t *Tracer) warnOnce(err error) {
	if t.warned {
		return
	}
	t.warned = true
	if t.warn != nil {
		t.warn(err)
		return
	}
	fmt.Fprintf(os.Stderr, "warning: trace write failed, trace will be incomplete: %v\n", err)
}

// jsonBody returns raw as an embeddable JSON value: the bytes themselves when
// they are valid JSON, otherwise the bytes wrapped as a JSON string so a non-JSON
// body can never corrupt the one-object-per-line format.
func jsonBody(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}

	s, err := json.Marshal(string(raw))
	if err != nil {
		return nil
	}

	return json.RawMessage(s)
}

// nowString is the timestamp format used for every trace line.
func nowString() string {
	return time.Now().Format(time.RFC3339Nano)
}
