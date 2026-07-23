//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package agent_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/onsi/gomega"

	"github.com/choria-io/fisk-ai/internal/agent"
	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// newSlogCapture returns a SlogEvents writing JSON to buf and the buf to read back.
// A JSON handler makes each record a parseable object so a test asserts on the
// structured attributes rather than on prose.
func newSlogCapture(verbose bool) (*agent.SlogEvents, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	return agent.NewSlogEvents(log, verbose), buf
}

// records parses the captured buffer into one map per JSON log line.
func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, rec)
	}

	return out
}

func TestSlogEvents_Starting(t *testing.T) {
	g := gomega.NewWithT(t)
	ev, buf := newSlogCapture(false)

	ev.Starting(agent.RunInfo{Tools: 3, SessionID: "sess-1", Resumed: true})

	recs := records(t, buf)
	g.Expect(recs).To(gomega.HaveLen(1))
	g.Expect(recs[0]["msg"]).To(gomega.Equal("agent run starting"))
	g.Expect(recs[0]["tools"]).To(gomega.BeEquivalentTo(3))
	g.Expect(recs[0]["session_id"]).To(gomega.Equal("sess-1"))
	g.Expect(recs[0]["resumed"]).To(gomega.Equal(true))
}

func TestSlogEvents_WarnCarriesKindAndFields(t *testing.T) {
	g := gomega.NewWithT(t)
	ev, buf := newSlogCapture(false)

	ev.Warn(agent.Warning{Kind: agent.WarnConfirmNoTerminal, Count: 2})

	recs := records(t, buf)
	g.Expect(recs).To(gomega.HaveLen(1))
	g.Expect(recs[0]["level"]).To(gomega.Equal("WARN"))
	g.Expect(recs[0]["kind"]).To(gomega.Equal("confirm_no_terminal"))
	g.Expect(recs[0]["count"]).To(gomega.BeEquivalentTo(2))
}

func TestSlogEvents_LLMRequestVerboseOnly(t *testing.T) {
	g := gomega.NewWithT(t)

	quiet, quietBuf := newSlogCapture(false)
	quiet.LLMRequest("one request")
	g.Expect(quietBuf.Len()).To(gomega.BeZero(), "non-verbose must drop LLMRequest")

	loud, loudBuf := newSlogCapture(true)
	loud.LLMRequest("one request")
	recs := records(t, loudBuf)
	g.Expect(recs).To(gomega.HaveLen(1))
	g.Expect(recs[0]["level"]).To(gomega.Equal("DEBUG"))
	g.Expect(recs[0]["summary"]).To(gomega.Equal("one request"))
}

func TestSlogEvents_ToolResultTruncates(t *testing.T) {
	g := gomega.NewWithT(t)
	ev, buf := newSlogCapture(false)

	big := strings.Repeat("x", 5000)
	ev.ToolResult(agent.ToolResultTrace{ProviderKind: toolkit.KindApplication, Output: big, IsError: true})

	recs := records(t, buf)
	g.Expect(recs).To(gomega.HaveLen(1))
	g.Expect(recs[0]["kind"]).To(gomega.Equal("application"))
	g.Expect(recs[0]["is_error"]).To(gomega.Equal(true))
	g.Expect(recs[0]["truncated"]).To(gomega.Equal(true))
	g.Expect(recs[0]["output"]).To(gomega.HaveLen(2048))
}

func TestSlogEvents_Panicked(t *testing.T) {
	g := gomega.NewWithT(t)
	ev, buf := newSlogCapture(false)

	ev.Panicked("boom", []byte("goroutine 1 [running]:\nmain.main()"))

	recs := records(t, buf)
	g.Expect(recs).To(gomega.HaveLen(1))
	g.Expect(recs[0]["level"]).To(gomega.Equal("ERROR"))
	g.Expect(recs[0]["value"]).To(gomega.Equal("boom"))
	g.Expect(recs[0]["stack"]).To(gomega.ContainSubstring("goroutine 1"))
}

func TestSlogEvents_Message(t *testing.T) {
	g := gomega.NewWithT(t)
	ev, buf := newSlogCapture(false)

	ev.Message(llm.Response{StopReason: llm.StopReason("end_turn"), Usage: llm.Usage{In: 10, Out: 20}}, true)

	recs := records(t, buf)
	g.Expect(recs).To(gomega.HaveLen(1))
	g.Expect(recs[0]["terminal"]).To(gomega.Equal(true))
	g.Expect(recs[0]["stop_reason"]).To(gomega.Equal("end_turn"))
	g.Expect(recs[0]["tokens_in"]).To(gomega.BeEquivalentTo(10))
	g.Expect(recs[0]["tokens_out"]).To(gomega.BeEquivalentTo(20))
}

// TestSlogEvents_ConcurrentUse points several goroutines at one SlogEvents, as a
// server aggregating many runs would, and asserts every record still lands intact.
func TestSlogEvents_ConcurrentUse(t *testing.T) {
	g := gomega.NewWithT(t)
	ev, buf := newSlogCapture(false)

	const runs = 20
	var wg sync.WaitGroup
	wg.Add(runs)
	for i := 0; i < runs; i++ {
		go func() {
			defer wg.Done()
			ev.ToolCall(agent.ToolTrace{Name: "t"})
		}()
	}
	wg.Wait()

	g.Expect(records(t, buf)).To(gomega.HaveLen(runs))
}
