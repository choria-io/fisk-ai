//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package fisk

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// A FiskCommandTool is a model-facing Tool that can require operator confirmation
// and can pre-validate a call's required arguments.
var (
	_ toolkit.Tool              = (*FiskCommandTool)(nil)
	_ toolkit.Confirmable       = (*FiskCommandTool)(nil)
	_ toolkit.ArgumentValidator = (*FiskCommandTool)(nil)
)

// Definition renders the command tool as a neutral tool definition. A tool tagged
// ai:no_defer is always sent directly, even within a deferred set, so its deferral
// is suppressed here rather than in the caller.
func (t *FiskCommandTool) Definition(deferLoading bool) llm.ToolDef {
	return llm.ToolDef{
		Name:         t.Name(),
		Description:  t.ModelDescription(),
		InputSchema:  t.InputSchema(),
		DeferLoading: deferLoading && !slices.Contains(t.Tags(), noDeferTag),
	}
}

// ExecuteUse runs the command behind the tool for a model tool_use block and
// returns the matching tool result. A command that could not be run (a missing
// binary, a canceled context, unusable arguments) becomes an error result; a
// command that ran, including one that exited non-zero, becomes a normal result
// whose JSON body carries the exit code and output for the model. It uses only the
// WorkDir from ExecDeps (a command tool never prompts), running the command in the
// caller's per-run directory so concurrent runs do not collide.
func (t *FiskCommandTool) ExecuteUse(ctx context.Context, use llm.ToolUseBlock, deps toolkit.ExecDeps) llm.ToolResultBlock {
	result, err := t.Execute(ctx, use.Input, deps.WorkDir)
	if err != nil {
		return llm.ToolResultBlock{ToolUseID: use.ID, Content: err.Error(), IsError: true}
	}

	data, err := json.Marshal(result)
	if err != nil {
		return llm.ToolResultBlock{ToolUseID: use.ID, Content: fmt.Sprintf("marshaling tool result: %v", err), IsError: true}
	}

	return llm.ToolResultBlock{ToolUseID: use.ID, Content: string(data)}
}
