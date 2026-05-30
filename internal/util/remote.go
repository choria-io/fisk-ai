//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/choria-io/fisk-ai/a2a"
)

// RemoteInvoker performs a single tool call against a remote agent. It is the
// narrow surface a RemoteTool needs from the transport, kept as an interface so a
// RemoteTool depends on the a2a message types rather than on the NATS binding,
// which avoids an import cycle and lets tests drive a RemoteTool with a fake.
type RemoteInvoker interface {
	// InvokeTool calls tool on agent with the given input and returns its reply. A
	// failed or denied call is reported in-band on the reply (IsError set); a Go
	// error means the call could not be made or answered.
	InvokeTool(ctx context.Context, agent, tool string, input json.RawMessage) (*a2a.ToolReply, error)
}

// RemoteTool is a tool imported from a remote agent. It presents to the model
// like a local tool but, when called, invokes the tool on the remote agent over
// the transport rather than running a local command. The name presented to the
// model (localName) is prefixed with the host alias so it stays distinct from
// local tools and other hosts; the unprefixed remoteName and the agent identity
// are what travel on the wire.
type RemoteTool struct {
	localName   string
	remoteName  string
	agent       string
	description string
	inputSchema map[string]any
	invoker     RemoteInvoker
}

// NewRemoteTool builds a RemoteTool from a discovered descriptor. localName is the
// alias-prefixed name presented to the model; the descriptor's own name is used on
// the wire. The descriptor's input schema, which comes from an untrusted remote
// agent, must be a JSON object; an absent or non-object schema is an error rather
// than something forwarded to the model API.
func NewRemoteTool(localName, agent string, desc a2a.ToolDescriptor, invoker RemoteInvoker) (*RemoteTool, error) {
	schema := map[string]any{"type": "object"}
	if len(desc.InputSchema) > 0 {
		if err := json.Unmarshal(desc.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("tool %q from agent %q has an unparsable input schema: %w", desc.Name, agent, err)
		}
	}

	return &RemoteTool{
		localName:   localName,
		remoteName:  desc.Name,
		agent:       agent,
		description: desc.Description,
		inputSchema: schema,
		invoker:     invoker,
	}, nil
}

// Name is the tool name presented to the model: the alias-prefixed local name.
func (r *RemoteTool) Name() string { return r.localName }

// RemoteName is the tool name on the remote agent, used on the wire.
func (r *RemoteTool) RemoteName() string { return r.remoteName }

// Agent is the identity of the remote agent that exposes the tool.
func (r *RemoteTool) Agent() string { return r.agent }

// Description is the tool description presented to the model, as advertised by the
// remote agent.
func (r *RemoteTool) Description() string { return r.description }

// InputSchema is the tool's JSON schema, as advertised by the remote agent.
func (r *RemoteTool) InputSchema() map[string]any { return r.inputSchema }

// ToolParam renders the remote tool as an Anthropic tool definition. Deferral is
// decided the same way as for local tools (see BuildToolParams); a remote tool
// carries no tags, so it is always eligible for deferral.
func (r *RemoteTool) ToolParam(deferLoading bool) anthropic.ToolUnionParam {
	return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
		Type:         anthropic.ToolTypeCustom,
		Name:         r.localName,
		Description:  anthropic.String(r.description),
		DeferLoading: anthropic.Bool(deferLoading),
		InputSchema:  anthropicInputSchema(r.inputSchema),
	}}
}

// ExecuteRemoteUse invokes a remote tool for a model tool_use block and returns
// the matching tool_result content block. The result is mapped so the model sees
// a remote tool identically to a local one: a transport failure or a remote
// harness failure becomes an error result, while a command that ran (even one that
// exited non-zero) becomes a successful result carrying the same CommandResult
// JSON a local command tool produces.
func ExecuteRemoteUse(ctx context.Context, r *RemoteTool, use anthropic.ToolUseBlock) anthropic.ContentBlockParamUnion {
	reply, err := r.invoker.InvokeTool(ctx, r.agent, r.remoteName, use.Input)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, fmt.Sprintf("calling tool %q on agent %q: %v", r.remoteName, r.agent, err), true)
	}

	if reply.IsError {
		return anthropic.NewToolResultBlock(use.ID, reply.Output, true)
	}

	result := CommandResult{Output: reply.Output}
	if reply.Exec != nil {
		result.Command = reply.Exec.Command
		result.ExitCode = reply.Exec.ExitCode
		result.Truncated = reply.Exec.Truncated
	}

	data, err := json.Marshal(result)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, fmt.Sprintf("marshaling tool result: %v", err), true)
	}

	return anthropic.NewToolResultBlock(use.ID, string(data), false)
}
