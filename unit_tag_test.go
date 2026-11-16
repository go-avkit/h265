// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"os"
	"testing"
)

// TestTheTagOfAPicturesFirstSliceComesOutWithIt: every unit of the bench seeds is tagged with
// its index plus one; each output picture carries the tag of the first slice segment of a
// distinct coded picture, whatever the reordering (the seeds have B-frames), and the tags
// of the pictures' other slice segments and of the other units are not carried.
func TestTheTagOfAPicturesFirstSliceComesOutWithIt(t *testing.T) {
	for _, s := range benchSeeds {
		t.Run(s.file, func(t *testing.T) {
			data, err := os.ReadFile("testdata/bench/" + s.file)
			if err != nil {
				t.Fatal(err)
			}
			units, err := SplitAnnexB(data)
			if err != nil {
				t.Fatal(err)
			}
			firstSlice := map[uint64]bool{}
			for i := range units {
				units[i].Tag = uint64(i) + 1
				if units[i].Type.IsSliceSegment() && units[i].Payload[0]&0x80 != 0 { // first_slice_segment_in_pic_flag
					firstSlice[uint64(i)+1] = true
				}
			}
			d := NewDecoder(Options{})
			outs, errs := decodeUnits(d, units)
			if len(errs) != 0 || len(outs) != 12 {
				t.Fatalf("%d pictures, %v", len(outs), errs)
			}
			seen := map[uint64]bool{}
			reordered := false
			for i, o := range outs {
				if !firstSlice[o.Tag] || seen[o.Tag] {
					t.Errorf("picture %d (POC %d) carries tag %d, which is not the first slice of a picture not yet seen", i, o.POC, o.Tag)
				}
				seen[o.Tag] = true
				if i > 0 && o.Tag < outs[i-1].Tag {
					reordered = true
				}
			}
			if s.file != "fade-main-12.265" && !reordered {
				t.Error("the stream was not reordered, so the tags were never carried across a reordering")
			}
		})
	}
}
