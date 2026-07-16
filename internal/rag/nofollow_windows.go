//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build windows

package rag

// openNoFollow is zero on Windows, which has no O_NOFOLLOW open flag. The symlink
// hardening the flag provides on Unix is therefore best-effort on Windows, matching
// the memory feature's platform split.
const openNoFollow = 0
