//  Copyright (c) 2026, R.I. Pienaar and the Choria Project contributors
//
//  SPDX-License-Identifier: Apache-2.0

package rag

import (
	"math"
	"strconv"
	"strings"
)

// normalize L2-normalizes a vector so sqlite-vec's default L2 distance orders
// identically to cosine similarity, and query and document vectors live in one
// space. A zero vector is returned unchanged (it has no direction to normalize).
// The rag package normalizes every vector itself, regardless of the embedder, so
// the invariant lives in one place and the manifest can pin normalized=true.
func normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return v
	}

	inv := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = f * inv
	}

	return out
}

// vecJSON serializes a vector to the JSON-array text sqlite-vec accepts as a bound
// parameter for both storage and KNN query.
func vecJSON(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')

	return b.String()
}
