//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package functool

import (
	"encoding/json"
	"fmt"
)

// Result marshals a handler's outcome value to the JSON result string a Handler
// returns to the model. It is a convenience for the common case of returning a small
// struct; a handler may build the string itself instead.
func Result(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshaling tool result: %w", err)
	}

	return string(data), nil
}
