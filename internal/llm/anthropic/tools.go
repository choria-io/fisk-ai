//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package anthropic

import (
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/choria-io/fisk-ai/internal/llm"
	"github.com/choria-io/fisk-ai/internal/toolkit"
)

// ProviderName is the neutral provider id this codec speaks. It is stamped into
// the run fingerprint so a resume against a different provider is refused (the
// on-disk format is one neutral shape, but a foreign turn is still incoherent);
// see SECURITY.md finding 4.
const ProviderName = "anthropic"

// ToolDefToAnthropic renders a neutral tool definition as an Anthropic custom
// tool. defer_loading is emitted unconditionally, including a present false, so
// the rendered tool is a pure function of the neutral value rather than varying
// with its zero state. False is the API default, so a present false and an
// omitted field request the same thing: send the tool directly.
func ToolDefToAnthropic(td llm.ToolDef) sdk.ToolUnionParam {
	return sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
		Type:         sdk.ToolTypeCustom,
		Name:         td.Name,
		Description:  sdk.String(td.Description),
		DeferLoading: sdk.Bool(td.DeferLoading),
		InputSchema:  toolInputSchema(td.InputSchema),
	}}
}

// toolInputSchema renders a fisk restricted JSON schema as the Anthropic tool
// input schema. The schema's properties and required list map to dedicated fields;
// every other key (notably additionalProperties, which strict mode requires to be
// false) is forwarded verbatim through ExtraFields. fisk's restricted schema
// already targets the Anthropic strict subset, so no further sanitizing is done
// here. The type is always object and is filled in by the SDK. Optional properties
// are annotated by toolkit.AnnotateOptional so the model treats them as such.
func toolInputSchema(schema map[string]any) sdk.ToolInputSchemaParam {
	required := toolkit.SchemaRequired(schema["required"])

	out := sdk.ToolInputSchemaParam{
		Properties: toolkit.AnnotateOptional(schema["properties"], required),
		Required:   required,
	}

	var extra map[string]any
	for k, v := range schema {
		switch k {
		case "type", "properties", "required":
			// type is fixed to object by the SDK; properties and required are
			// mapped to their own fields above.
		default:
			if extra == nil {
				extra = make(map[string]any)
			}
			extra[k] = v
		}
	}
	out.ExtraFields = extra

	return out
}

// ToolUseToNeutral converts an Anthropic tool_use block into the neutral model,
// so a tool kind is handed a request in neutral terms.
func ToolUseToNeutral(use sdk.ToolUseBlock) llm.ToolUseBlock {
	return llm.ToolUseBlock{ID: use.ID, Name: use.Name, Input: use.Input}
}

// ToolResultToAnthropic renders a neutral tool result as the Anthropic
// tool_result content block that answers the matching tool_use.
func ToolResultToAnthropic(tr llm.ToolResultBlock) sdk.ContentBlockParamUnion {
	return sdk.NewToolResultBlock(tr.ToolUseID, tr.Content, tr.IsError)
}

// ToolResultFromAnthropic converts an Anthropic tool_result content block into
// the neutral model, for the journal emit boundary. Every tool result this
// codebase produces is a single text block, so a result of any other shape is an
// error rather than a lossy best-effort.
func ToolResultFromAnthropic(block sdk.ContentBlockParamUnion) (llm.ToolResultBlock, error) {
	r := block.OfToolResult
	if r == nil {
		return llm.ToolResultBlock{}, fmt.Errorf("content block is not a tool_result")
	}

	content := ""
	switch {
	case len(r.Content) == 0:
	case len(r.Content) == 1 && r.Content[0].OfText != nil:
		content = r.Content[0].OfText.Text
	default:
		return llm.ToolResultBlock{}, fmt.Errorf("tool_result content is not a single text block")
	}

	return llm.ToolResultBlock{ToolUseID: r.ToolUseID, Content: content, IsError: r.IsError.Or(false)}, nil
}
