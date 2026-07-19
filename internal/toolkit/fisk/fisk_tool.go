//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package fisk

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// A FiskCommandTool is a model-facing Tool that can require operator confirmation
// and can pre-validate a call's required arguments.
var (
	_ toolkit.Tool              = (*FiskCommandTool)(nil)
	_ toolkit.Confirmable       = (*FiskCommandTool)(nil)
	_ toolkit.ArgumentValidator = (*FiskCommandTool)(nil)
)

// ToolParam renders the command tool as an Anthropic tool definition. A tool
// tagged ai:no_defer is always sent directly, even within a deferred set, so its
// deferral is suppressed here rather than in the caller.
func (t *FiskCommandTool) ToolParam(deferLoading bool) anthropic.ToolUnionParam {
	return AnthropicTool(t, deferLoading && !slices.Contains(t.Tags(), noDeferTag))
}

// ExecuteUse runs the command behind the tool for a model tool_use block and
// returns the matching tool_result content block. A command that could not be run
// (a missing binary, a canceled context, unusable arguments) becomes an error
// result; a command that ran, including one that exited non-zero, becomes a normal
// result whose JSON body carries the exit code and output for the model. It takes
// no ExecDeps: a command tool never prompts.
func (t *FiskCommandTool) ExecuteUse(ctx context.Context, use anthropic.ToolUseBlock, _ toolkit.ExecDeps) anthropic.ContentBlockParamUnion {
	result, err := t.Execute(ctx, use.Input)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, err.Error(), true)
	}

	data, err := json.Marshal(result)
	if err != nil {
		return anthropic.NewToolResultBlock(use.ID, fmt.Sprintf("marshaling tool result: %v", err), true)
	}

	return anthropic.NewToolResultBlock(use.ID, string(data), false)
}
