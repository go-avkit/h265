// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// The offsets of a block go to the kernels as 16-bit numbers; one that does not fit (none
// does in a valid stream: 31 << log2OffsetScale) keeps the block in Go.
func TestSAOOffsetsThatDoNotFitStayInGo(t *testing.T) {
	if o, ok := saoOffsets(&[4]int32{-124, 0, 31, 124}); !ok || o != [4]int16{-124, 0, 31, 124} {
		t.Errorf("%v %v", o, ok)
	}
	for _, bad := range [][4]int32{{1 << 15, 0, 0, 0}, {0, -1<<15 - 1, 0, 0}, {0, 0, 0, 1 << 20}} {
		if _, ok := saoOffsets(&bad); ok {
			t.Errorf("%v was accepted", bad)
		}
	}
}
