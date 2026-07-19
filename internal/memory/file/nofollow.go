//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build !windows

package file

import "syscall"

// openNoFollow makes readFile refuse to open a symlink, so a symlink planted in
// the memory directory cannot make the store return the contents of an unrelated
// file. Windows has no O_NOFOLLOW; see windows.go for the fallback.
const openNoFollow = syscall.O_NOFOLLOW
