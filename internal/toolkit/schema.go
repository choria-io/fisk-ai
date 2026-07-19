//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	"slices"

	"github.com/anthropics/anthropic-sdk-go"
)

// AnthropicInputSchema turns a fisk restricted JSON schema into the Anthropic
// tool input schema. The schema's properties and required list map to dedicated
// fields; every other key (notably additionalProperties, which strict mode
// requires to be false) is forwarded verbatim. fisk's restricted schema already
// targets the Anthropic strict subset, so no further sanitizing is done here.
// The schema type is always object and is filled in by the SDK. It is shared by
// every tool kind's ToolParam, which is why it lives on the tool contract.
//
// Optional parameters (those not in the required list) have "(optional)" appended
// to their description. JSON schema marks optionality only by absence from the
// required list, a signal models reliably under-weight: they tend to treat a
// lone, meaningful-looking parameter as mandatory and ask the user for it rather
// than relying on the command's own default. The explicit description text states
// optionality in the way models respond to most.
func AnthropicInputSchema(schema map[string]any) anthropic.ToolInputSchemaParam {
	required := SchemaRequired(schema["required"])

	out := anthropic.ToolInputSchemaParam{
		Properties: annotateOptional(schema["properties"], required),
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

// annotateOptional returns a copy of the JSON schema properties map in which the
// description of every property not in required has "(optional)" appended. It
// copies rather than mutates: properties is the tool's shared schema, reused on
// every request, so an in-place edit would append the marker again each call.
//
// A property with no description gets "Optional." A non-map properties value, or
// a property whose value is not a map, is returned unchanged since there is no
// description to annotate.
func annotateOptional(properties any, required []string) any {
	props, ok := properties.(map[string]any)
	if !ok {
		return properties
	}

	out := make(map[string]any, len(props))
	for name, raw := range props {
		prop, ok := raw.(map[string]any)
		if !ok || slices.Contains(required, name) {
			out[name] = raw
			continue
		}

		// Shallow copy is enough: only the description key is changed, and it
		// holds a string.
		clone := make(map[string]any, len(prop)+1)
		for k, v := range prop {
			clone[k] = v
		}

		desc, _ := clone["description"].(string)
		switch desc {
		case "":
			clone["description"] = "Optional."
		default:
			clone["description"] = desc + " (optional)"
		}

		out[name] = clone
	}

	return out
}

// SchemaRequired extracts the JSON schema "required" list, tolerating both the
// []string form and the []any form a schema decoded from JSON carries.
func SchemaRequired(v any) []string {
	switch r := v.(type) {
	case []string:
		return r
	case []any:
		out := make([]string, 0, len(r))
		for _, item := range r {
			s, ok := item.(string)
			if !ok {
				continue
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}
