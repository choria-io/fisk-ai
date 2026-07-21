//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build unix

package fisk

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureProcessGroup puts a tool subprocess in its own process group and, on
// context cancellation, kills the whole group rather than only the direct child. A
// wrapped CLI that forks (a shell backgrounding a job, a tool spawning a helper) would
// otherwise leave orphaned grandchildren holding file descriptors and memory after the
// run is canceled or times out: exec.CommandContext signals only the immediate child,
// and WaitDelay bounds the parent's I/O wait, not the descendants. Invisible in a CLI
// that exits, this accumulates in a long-lived server.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid signals the whole process group; the child is its leader, since
		// Setpgid made the group id equal the child pid. ESRCH means it already exited.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}
