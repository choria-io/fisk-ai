//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

//go:build windows

package file

// openNoFollow is a no-op on Windows, which has no O_NOFOLLOW open flag. The
// readFile symlink defense instead rests on the follow-up Stat rejecting any
// non-regular file, and creating a symlink on Windows requires privileges an
// unprivileged planter is unlikely to hold.
const openNoFollow = 0
