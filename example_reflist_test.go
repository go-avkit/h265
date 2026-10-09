// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265_test

import (
	"fmt"

	"github.com/go-avkit/h265"
)

// ExampleLists is the snippet the README shows, compiled. A documented call
// that does not build is a claim, not a capability.
func ExampleLists() {
	var (
		sps  h265.SPS
		pps  = h265.PPS{NumRefIdxL0: 3, NumRefIdxL1: 2}
		hdr  = h265.SliceSegmentHeader{Type: h265.SliceB}
		refs = h265.PictureRefs{
			ShortTerm: h265.ShortTermRPS{
				Before: []h265.RefPic{{DeltaPOC: -8, Used: true}, {DeltaPOC: -4, Used: true}},
				After:  []h265.RefPic{{DeltaPOC: 4, Used: true}},
			},
		}
	)
	set := h265.Derive(refs, 16, sps)
	l0, l1 := h265.Lists(set, hdr.Type, int(pps.NumRefIdxL0), int(pps.NumRefIdxL1))

	fmt.Println("list 0:", order(l0))
	fmt.Println("list 1:", order(l1))
	// Output:
	// list 0: [8 12 20]
	// list 1: [20 8]
}

func order(l []h265.RefListEntry) []int32 {
	out := make([]int32, len(l))
	for i, e := range l {
		out[i] = e.POC
	}
	return out
}
