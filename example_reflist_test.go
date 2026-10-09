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
	var sps h265.SPS
	// What ReferencesOf reads out of the slice segment header. A B slice that
	// names, for each of its three list 0 entries, the picture it wants there.
	refs := h265.PictureRefs{
		Type:              h265.SliceB,
		NumRefIdxL0Active: 3,
		NumRefIdxL1Active: 2,
		ShortTerm: h265.ShortTermRPS{
			Before: []h265.RefPic{{DeltaPOC: -8, Used: true}, {DeltaPOC: -4, Used: true}},
			After:  []h265.RefPic{{DeltaPOC: 4, Used: true}},
		},
		ListEntryL0: []uint32{2, 0, 1},
	}

	set := h265.Derive(refs, 16, sps)
	l0, l1, err := h265.Lists(set, refs)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("list 0:", order(l0))
	fmt.Println("list 1:", order(l1))
	// Output:
	// list 0: [20 8 12]
	// list 1: [20 8]
}

func order(l []h265.RefListEntry) []int32 {
	out := make([]int32, len(l))
	for i, e := range l {
		out[i] = e.POC
	}
	return out
}
