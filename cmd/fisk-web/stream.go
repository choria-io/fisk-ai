//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// streamHeaders are the headers the AI SDK's own server sets on a UI message stream,
// copied from packages/ai/src/ui-message-stream/ui-message-stream-headers.ts.
//
// No client code reads the version header: the transport class the page constructs
// decides the format. It is sent because a proxy between the two might read it, and
// x-accel-buffering is there for the proxy that would otherwise hold every event until
// the response ended.
var streamHeaders = map[string]string{
	"content-type":                  "text/event-stream",
	"cache-control":                 "no-cache",
	"connection":                    "keep-alive",
	"x-vercel-ai-ui-message-stream": "v1",
	"x-accel-buffering":             "no",
}

// stream writes UI message stream parts as server-sent events.
//
// Every part is written by the goroutine driving one turn. Block runs on RunTask's
// reading goroutine and the question path records the question rather than writing it, so
// the parts a turn produces are already in order and nothing here takes a lock.
//
// The first write failure is kept and every write after it does nothing. A browser going
// away mid-turn is the ordinary case rather than a fault, and the turn is worth running
// out either way: it is the worker's journal that holds the conversation, not this
// response.
type stream struct {
	w   http.ResponseWriter
	f   http.Flusher
	err error
}

// newStream sends the response head and returns the writer for its body. Nothing may set
// a header after this.
func newStream(w http.ResponseWriter) (*stream, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("this response cannot be flushed, so no event would reach the browser before the turn ended")
	}

	for name, value := range streamHeaders {
		w.Header().Set(name, value)
	}

	w.WriteHeader(http.StatusOK)
	f.Flush()

	return &stream{w: w, f: f}, nil
}

// part sends one chunk as its own event.
//
// The flush is what makes this a stream. Without one per event the response is buffered
// until the handler returns, and a turn that took a minute arrives all at once at the end
// of it.
func (s *stream) part(p any) {
	if s.err != nil {
		return
	}

	body, err := json.Marshal(p)
	if err != nil {
		s.err = err

		return
	}

	_, s.err = fmt.Fprintf(s.w, "data: %s\n\n", body)
	if s.err != nil {
		return
	}

	s.f.Flush()
}

// done sends the terminator the AI SDK reads as the end of the message.
func (s *stream) done() {
	if s.err != nil {
		return
	}

	_, s.err = io.WriteString(s.w, "data: [DONE]\n\n")
	if s.err != nil {
		return
	}

	s.f.Flush()
}

// startPart opens the assistant message.
type startPart struct {
	Type      string `json:"type"`
	MessageID string `json:"messageId,omitempty"`
}

// finishPart closes it. FinishReason is one of stop, length, content-filter, tool-calls,
// error and other.
type finishPart struct {
	Type         string `json:"type"`
	FinishReason string `json:"finishReason,omitempty"`
}

// errorPart is the run saying something went wrong, which the client renders in place
// rather than as a failed request.
type errorPart struct {
	Type      string `json:"type"`
	ErrorText string `json:"errorText"`
}

// boundaryPart opens or closes a text or reasoning part: text-start, text-end,
// reasoning-start and reasoning-end all carry an id and nothing else.
type boundaryPart struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// deltaPart adds a fragment to the part its id names. It is separate from boundaryPart
// because delta is required and an omitted one fails the client's parse.
type deltaPart struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Delta string `json:"delta"`
}

// toolInputPart is a tool call with its whole input, which is how a call arrives here:
// Fisk sends the call once the model has written it rather than as it is written.
//
// Dynamic is always set. The page cannot know a remote agent's tool names when it is
// built, and dynamic is what makes the client build a dynamic-tool part rather than a
// typed one named after a tool it has no type for.
type toolInputPart struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
	Dynamic    bool            `json:"dynamic,omitempty"`
}

// toolOutputPart is what a call returned.
type toolOutputPart struct {
	Type       string `json:"type"`
	ToolCallID string `json:"toolCallId"`
	Output     string `json:"output"`
	Dynamic    bool   `json:"dynamic,omitempty"`
}

// toolErrorPart is a call that failed, which the client shows on the same part rather
// than as a message of its own.
type toolErrorPart struct {
	Type       string `json:"type"`
	ToolCallID string `json:"toolCallId"`
	ErrorText  string `json:"errorText"`
	Dynamic    bool   `json:"dynamic,omitempty"`
}

// approvalPart asks the person to allow the call it names.
//
// The client mutates the tool part toolCallId already created rather than making one, so
// this is only valid after that call's toolInputPart has gone out.
type approvalPart struct {
	Type       string `json:"type"`
	ApprovalID string `json:"approvalId"`
	ToolCallID string `json:"toolCallId"`
	Reason     string `json:"reason,omitempty"`
}

// dataPart carries a value the page reads for itself. A data-* part needs no schema on
// the client, which is what makes it the place for a question the AI SDK has no shape
// for.
type dataPart struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	Data any    `json:"data"`
}

// approvalInput stands in for the arguments of a call waiting on approval. An
// ElicitRequest carries no argument JSON, so the gate's rendered command line and the tag
// that triggered it are what the page has to show.
type approvalInput struct {
	Command string `json:"command"`
	Tag     string `json:"tag,omitempty"`
}

// questionData is a confirm, select or input question as the page receives it. It carries
// the ids an answer names, since the page sends them back rather than the run
// reconstructing them.
type questionData struct {
	QuestionID string   `json:"questionId"`
	ToolUseID  string   `json:"toolUseId"`
	Kind       string   `json:"kind"`
	Question   string   `json:"question"`
	Options    []string `json:"options,omitempty"`
	Default    string   `json:"default,omitempty"`
}
