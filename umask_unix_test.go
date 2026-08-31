//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import "syscall"

// withCreationMask sets the process file creation mask and returns a function that puts
// the old one back, along with true for a platform that has one. A spec asserting the
// mode of a file the code created reads the machine's umask without it, so the same
// spec passes on one developer's machine and fails on another's.
func withCreationMask(mask int) (func(), bool) {
	prior := syscall.Umask(mask)

	return func() { syscall.Umask(prior) }, true
}
