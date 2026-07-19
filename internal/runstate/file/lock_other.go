//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build !unix

package file

import (
	"fmt"
	"os"
)

// fileLock is a best-effort marker on platforms without flock. It does not
// prevent concurrent access; those platforms rely on the operator not resuming a
// run twice.
type fileLock struct {
	f *os.File
}

func acquireLock(path string) (*fileLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	return &fileLock{f: f}, nil
}

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}

	l.f.Close()
}
