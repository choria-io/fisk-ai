//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build !unix

package fisk

import "os/exec"

// configureProcessGroup is a no-op on platforms without POSIX process groups: a tool
// subprocess is signaled directly by exec.CommandContext there, so a forked grandchild
// may outlive a canceled run.
func configureProcessGroup(_ *exec.Cmd) {}
