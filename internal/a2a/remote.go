//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/choria-io/fisk-ai/internal/toolkit"
	"github.com/choria-io/fisk-ai/internal/toolkit/functool"
)

// RemoteInvoker performs a single tool call against a remote agent. It is the
// narrow surface a remote tool needs from the transport, kept as an interface so a
// remote tool depends on the a2a message types rather than on the NATS binding,
// which avoids an import cycle and lets tests drive a remote tool with a fake.
type RemoteInvoker interface {
	// InvokeTool calls tool on agent with the given input and returns its reply. A
	// failed or denied call is reported in-band on the reply (IsError set); a Go
	// error means the call could not be made or answered.
	InvokeTool(ctx context.Context, agent, tool string, input json.RawMessage) (*ToolReply, error)
}

// NewRemoteTool builds a remote function tool from a discovered descriptor.
// localName is the alias-prefixed name presented to the model; the descriptor's own
// name is used on the wire. The descriptor's input schema, which comes from an
// untrusted remote agent, must be a JSON object; an absent or non-object schema is an
// error rather than something forwarded to the model API. A descriptor that
// advertises no description is likewise rejected: a description-less tool gives the
// model nothing to decide on and is never forwarded.
//
// The tool is presented to the model like a local one but, when called, invokes the
// tool on the remote agent over the transport rather than running a local command.
// Its handler maps the reply so the model sees a remote tool identically to a local
// one: a transport failure or a remote harness failure becomes an error result, while
// a command that ran (even one that exited non-zero) becomes a successful result
// carrying the same CommandResult JSON a local command tool produces.
func NewRemoteTool(localName, agent string, desc ToolDescriptor, invoker RemoteInvoker) (*functool.Tool, error) {
	schema := map[string]any{"type": "object"}
	if len(desc.InputSchema) > 0 {
		if err := json.Unmarshal(desc.InputSchema, &schema); err != nil {
			return nil, fmt.Errorf("tool %q from agent %q has an unparsable input schema: %w", desc.Name, agent, err)
		}
	}

	if desc.Description == "" {
		return nil, fmt.Errorf("tool %q from agent %q advertises no description", desc.Name, agent)
	}

	remoteName := desc.Name
	handler := func(ctx context.Context, input json.RawMessage, _ *functool.CallContext) (string, error) {
		reply, err := invoker.InvokeTool(ctx, agent, remoteName, input)
		if err != nil {
			return "", fmt.Errorf("calling tool %q on agent %q: %w", remoteName, agent, err)
		}

		if reply.IsError {
			return "", errors.New(reply.Output)
		}

		result := toolkit.CommandResult{Output: reply.Output}
		if reply.Exec != nil {
			result.Command = reply.Exec.Command
			result.ExitCode = reply.Exec.ExitCode
			result.Truncated = reply.Exec.Truncated
		}

		return functool.Result(result)
	}

	return functool.New(functool.Spec{
		Name:        localName,
		Description: desc.Description,
		Schema:      schema,
		Handler:     handler,
		Remote:      &functool.RemoteSpec{Agent: agent},
	})
}
