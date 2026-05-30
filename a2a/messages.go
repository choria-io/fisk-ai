//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import "encoding/json"

// Request starts a task. It is sent by a caller to an agent.
type Request struct {
	Header

	Prompt    string   `json:"prompt"`
	Context   string   `json:"context,omitempty"`
	ToolHints []string `json:"tool_hints,omitempty"`
	Budget    *Budget  `json:"budget,omitempty"`
	// Stream, when false, asks for only a terminal result with no event stream.
	// A nil value means the default, which is to stream.
	Stream *bool `json:"stream,omitempty"`
}

// NewRequest builds a Request with the protocol id set.
func NewRequest(prompt string) *Request {
	r := &Request{Prompt: prompt}
	r.Protocol = RequestProtocol

	return r
}

// WantsStream reports whether the caller wants the event stream. It defaults to
// true when Stream is unset.
func (r *Request) WantsStream() bool {
	return r.Stream == nil || *r.Stream
}

// Event carries one streamed content block from an agent to a caller.
type Event struct {
	Header

	Block Block `json:"block"`
}

// NewEvent builds an Event wrapping the block, with the protocol id set.
func NewEvent(block Block) *Event {
	e := &Event{Block: block}
	e.Protocol = EventProtocol

	return e
}

// Result is the terminal success message of a task.
type Result struct {
	Header

	StopReason StopReason `json:"stop_reason"`
	Text       string     `json:"text,omitempty"`
	Usage      *Usage     `json:"usage,omitempty"`
}

// NewResult builds a Result with the protocol id set.
func NewResult(reason StopReason) *Result {
	r := &Result{StopReason: reason}
	r.Protocol = ResultProtocol

	return r
}

// ErrorMessage is the terminal failure message of a task. It implements the
// error interface.
type ErrorMessage struct {
	Header

	StopReason StopReason `json:"stop_reason,omitempty"`
	Err        string     `json:"error"`
	Code       string     `json:"code,omitempty"`
}

// NewError builds an ErrorMessage with the protocol id set.
func NewError(message string) *ErrorMessage {
	e := &ErrorMessage{Err: message}
	e.Protocol = ErrorProtocol

	return e
}

// Error implements the error interface.
func (e *ErrorMessage) Error() string { return e.Err }

// Cancel asks an agent to cancel an in-flight task, identified by Header.Request.
type Cancel struct {
	Header

	Reason string `json:"reason,omitempty"`
}

// NewCancel builds a Cancel with the protocol id set.
func NewCancel() *Cancel {
	c := &Cancel{}
	c.Protocol = CancelProtocol

	return c
}

// Ack reports whether an agent accepted a request.
type Ack struct {
	Header

	Accepted bool   `json:"accepted"`
	Reason   string `json:"reason,omitempty"`
}

// NewAck builds an Ack with the protocol id set.
func NewAck(accepted bool) *Ack {
	a := &Ack{Accepted: accepted}
	a.Protocol = AckProtocol

	return a
}

// ToolRequest invokes a single tool on a remote agent directly, without engaging
// the remote agentic loop. It is a request-reply interaction used when one agent
// imports or exports tools to another; the reply is a ToolReply correlated by
// Header.Request.
type ToolRequest struct {
	Header

	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
}

// NewToolRequest builds a ToolRequest with the protocol id set.
func NewToolRequest(name string, input json.RawMessage) *ToolRequest {
	r := &ToolRequest{Name: name, Input: input}
	r.Protocol = ToolRequestProtocol

	return r
}

// ToolReply is the result of a ToolRequest. It carries the shared ToolResult
// outcome, the same shape as a streamed ToolResultBlock. A failed or denied call
// is reported in-band with IsError true and an explanatory Output.
type ToolReply struct {
	Header
	ToolResult
}

// NewToolReply builds a ToolReply with the protocol id set.
func NewToolReply(output string, isError bool) *ToolReply {
	r := &ToolReply{ToolResult: ToolResult{Output: output, IsError: isError}}
	r.Protocol = ToolReplyProtocol

	return r
}

// ToolDescriptor describes a tool an agent exposes, as reported in a discovery
// reply. It carries enough to import the tool and later invoke it with a
// ToolRequest.
type ToolDescriptor struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

// AgentCard is an agent's self description: who it is, its version, and the
// tools it exposes.
type AgentCard struct {
	Name        string           `json:"name"`
	Version     string           `json:"version"`
	Description string           `json:"description,omitempty"`
	Protocols   []string         `json:"protocols,omitempty"`
	Tools       []ToolDescriptor `json:"tools,omitempty"`
}

// DiscoveryRequest asks an agent to describe itself. The reply is a
// DiscoveryReply.
type DiscoveryRequest struct {
	Header
}

// NewDiscoveryRequest builds a DiscoveryRequest with the protocol id set.
func NewDiscoveryRequest() *DiscoveryRequest {
	r := &DiscoveryRequest{}
	r.Protocol = DiscoveryRequestProtocol

	return r
}

// DiscoveryReply describes the replying agent, correlated to the request by
// Header.Request.
type DiscoveryReply struct {
	Header
	AgentCard
}

// NewDiscoveryReply builds a DiscoveryReply with the protocol id set.
func NewDiscoveryReply(name, version string) *DiscoveryReply {
	r := &DiscoveryReply{AgentCard: AgentCard{Name: name, Version: version}}
	r.Protocol = DiscoveryReplyProtocol

	return r
}
