//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build windows

package rag

import (
	"errors"
	"fmt"
	"os"
)

// writeLock is the held cross-process advisory lock guarding a single knowledge
// index writer. Windows has no flock, so the lock is an exclusively-created lock
// file removed on release.
type writeLock struct {
	path string
	f    *os.File
}

// acquireWriteLock creates the lock file with O_CREATE|O_EXCL, which fails if it
// already exists, giving a fail-fast guard against a second concurrent writer.
// Unlike flock this does not auto-release if the process dies, so a crashed writer
// can leave a stale lock that must be removed by hand; that trade-off is accepted
// on Windows, which is not the primary target.
func acquireWriteLock(path string) (*writeLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, dbFileMode)
	if errors.Is(err, os.ErrExist) {
		return nil, ErrLocked
	}
	if err != nil {
		return nil, fmt.Errorf("opening knowledge index lock %q: %w", path, err)
	}

	return &writeLock{path: path, f: f}, nil
}

// release closes and removes the lock file.
func (l *writeLock) release() error {
	if l.f == nil {
		return nil
	}

	err := l.f.Close()
	l.f = nil
	if rerr := os.Remove(l.path); rerr != nil && err == nil {
		err = rerr
	}

	return err
}
