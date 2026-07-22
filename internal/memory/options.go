//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeOptions strictly decodes a backend's raw options block into T, rejecting an
// unknown key so an operator's mistyped option fails at run start rather than on the
// first tool call. The options arrive as canonical JSON (config parses with
// UseJSONUnmarshaler), so a stdlib decoder with DisallowUnknownFields catches a
// mistyped option the same way the YAML layer catches a mistyped top-level key. An
// empty block decodes to the zero value of T. what names the backend for the error,
// for example "jetstream memory". Every backend decodes through this so the strict
// rule the Factory contract requires cannot drift between them.
func DecodeOptions[T any](raw json.RawMessage, what string) (T, error) {
	var opts T
	if len(raw) == 0 {
		return opts, nil
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&opts); err != nil {
		return opts, fmt.Errorf("invalid %s options: %w", what, err)
	}

	return opts, nil
}
