//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build windows

package main

// withCreationMask reports that Windows has no file creation mask, which leaves a spec
// asserting a Unix file mode nothing to assert.
func withCreationMask(_ int) (func(), bool) {
	return func() {}, false
}
