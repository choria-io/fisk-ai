//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package runstate

import (
	"fmt"
	"regexp"
)

// idPattern constrains a run id to a safe, single path component. It is also a
// valid NATS subject token, so the same ids carry over to the JetStream backend.
// Operator-chosen names and machine-generated KSUIDs both satisfy it.
var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// maxIDLen caps a run id so it stays a safe filename and subject token. The
// charset is ASCII, so runes equal bytes.
const maxIDLen = 128

// ValidateID rejects a run id that is not a safe, bounded single path component
// (and a valid NATS subject token). Every backend calls it before an id is used as
// a key or path component, so it is a path-traversal defense as well as a format
// rule and the format cannot drift between backends. It bounds length as well as
// charset: the pattern alone is unbounded, and an oversized id would produce an
// oversized filename.
func ValidateID(id string) error {
	if len(id) > maxIDLen || !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %q (use letters, digits, '-' or '_')", ErrInvalidID, id)
	}

	return nil
}

// CheckAppend is the append contract shared by every Journal: it decides, from the
// last written seq and the seq being appended, whether the append is a duplicate
// to skip or a gap to reject. It is a decision only. The caller still performs the
// write and, crucially, advances its own last-seq only after the record is durably
// stored, so a failed or torn write re-appends the same seq on retry rather than
// silently losing it. Do not fold the last-seq advance into this helper.
//
//   - seq <= lastSeq is an already-recorded duplicate (a crash-retry of the most
//     recent event); skip is true, err is nil, and the append is a no-op.
//   - seq == lastSeq+1 is the next record; skip is false, err is nil.
//   - seq > lastSeq+1 skips ahead of the journal and is ErrSeqGap.
func CheckAppend(lastSeq, seq uint64) (skip bool, err error) {
	if seq <= lastSeq {
		return true, nil
	}
	if seq > lastSeq+1 {
		return false, fmt.Errorf("%w: seq %d skips ahead of %d", ErrSeqGap, seq, lastSeq)
	}

	return false, nil
}
