//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build !windows

package rag

import "syscall"

// openNoFollow makes an open refuse to traverse a final-component symlink, so a
// symlink planted at the index, lock, or a walked source file cannot redirect a
// read or write to an unrelated file. Windows has no O_NOFOLLOW; see the fallback.
const openNoFollow = syscall.O_NOFOLLOW
