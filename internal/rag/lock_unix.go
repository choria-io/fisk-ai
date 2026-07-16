//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build !windows

package rag

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// writeLock is the held cross-process advisory lock guarding a single knowledge
// index writer. It wraps an open file descriptor with an exclusive flock; the OS
// releases the lock automatically if the process dies, so a crashed writer never
// leaves a stale lock wedging future indexing.
type writeLock struct {
	f *os.File
}

// acquireWriteLock takes a non-blocking exclusive flock on the lock file, creating
// it if needed. A second concurrent writer fails fast with ErrLocked rather than
// interleaving writes under the busy timeout. The lock file itself is never read
// or written; only the flock state matters.
func acquireWriteLock(path string) (*writeLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|openNoFollow, dbFileMode)
	if err != nil {
		return nil, fmt.Errorf("opening knowledge index lock %q: %w", path, err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		f.Close()
		return nil, ErrLocked
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("locking knowledge index %q: %w", path, err)
	}

	return &writeLock{f: f}, nil
}

// release drops the flock and closes the descriptor. Closing the file releases the
// flock even without the explicit unlock, but the unlock is explicit for clarity.
func (l *writeLock) release() error {
	if l.f == nil {
		return nil
	}

	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	err := l.f.Close()
	l.f = nil

	return err
}
