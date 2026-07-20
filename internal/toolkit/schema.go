//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package toolkit

import (
	"slices"
)

// AnnotateOptional returns a copy of the JSON schema properties map in which the
// description of every property not in required has "(optional)" appended. It
// copies rather than mutates: properties is the tool's shared schema, reused on
// every request, so an in-place edit would append the marker again each call.
//
// JSON schema marks optionality only by absence from the required list, a signal
// models reliably under-weight: they tend to treat a lone, meaningful-looking
// parameter as mandatory and ask the user for it rather than relying on the
// command's own default. The explicit description text states optionality in the
// way models respond to most. It is provider-neutral; a provider codec calls it
// while rendering a tool's input schema to its wire shape.
//
// A property with no description gets "Optional." A non-map properties value, or
// a property whose value is not a map, is returned unchanged since there is no
// description to annotate.
func AnnotateOptional(properties any, required []string) any {
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
