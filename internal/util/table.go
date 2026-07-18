//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package util

import (
	"io"

	"github.com/jedib0t/go-pretty/v6/table"
)

// NewTable returns a table writer preconfigured with the standard fisk-ai
// appearance (rounded borders, trailing spaces suppressed) with its output
// mirrored to out. Centralizing construction here keeps every command's table
// looking the same and lets the shared style be adjusted in one place. Append a
// header and rows on the returned writer with table.Row and call Render to emit.
func NewTable(out io.Writer) table.Writer {
	t := table.NewWriter()
	t.SetOutputMirror(out)
	t.SetStyle(table.StyleRounded)
	t.SuppressTrailingSpaces()

	return t
}
