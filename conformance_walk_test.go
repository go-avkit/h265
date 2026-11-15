// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"
	"testing"
)

// TestEverySliceDataOfTheConformanceSuiteEndsOnItsLastBit walks the slice segment
// data of every slice of every stream of the suite: the coding quadtree, coding
// units, prediction units, transform trees and residuals of 7.3.8. DecodeSliceData
// succeeds only if the data ends exactly where end_of_slice_segment_flag says it
// does, every end_of_subset_one_bit is 1 and every substream starts where the
// entry points say. A syntax element read wrongly anywhere desynchronises the
// arithmetic decoder, and the last bit is where that shows: no reference decoder
// and no trace is needed for it, and it does not depend on how this package reads
// the specification, only on the stream having been written by encoders that did.
func TestEverySliceDataOfTheConformanceSuiteEndsOnItsLastBit(t *testing.T) {
	p := conformance(t)
	walked, refused, failed, units, tus, blocks, pcm, entries := 0, 0, 0, 0, 0, 0, 0, 0
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		layouts, epoch := map[[2]int]*CtbLayout{}, 0
		var prev *SliceHeader
		var pic *PictureState
		n := 0
		for _, u := range list {
			switch {
			case u.Type == UnitSPS:
				epoch++
				if s, err := ParseSPS(u); err == nil {
					spsByID[s.ID] = s
				}
			case u.Type == UnitPPS:
				epoch++
				if pps, err := ParsePPS(u); err == nil {
					ppsByID[pps.ID] = pps
				}
			case u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA):
				r := newSticky(u.Unescape())
				r.bit()
				if u.Type.IsRandomAccess() {
					r.bit()
				}
				pps, ok := ppsByID[r.ue()]
				sps, ok2 := spsByID[pps.SPSID]
				if !ok || !ok2 {
					refused++
					n++
					continue
				}
				h, err := ParseSliceSegmentHeaderFull(u, sps, pps, prev)
				if err != nil {
					t.Errorf("%s: slice %d: %v", rel, n, err)
					n++
					continue
				}
				if !h.Dependent {
					hh := h
					prev = &hh
				}
				key := [2]int{int(pps.ID), epoch}
				l := layouts[key]
				if l == nil {
					if l, err = NewCtbLayout(sps, pps); err != nil {
						t.Fatalf("%s: slice %d: %v", rel, n, err)
					}
					layouts[key] = l
				}
				if h.FirstSliceSegmentInPic || pic == nil {
					pic = NewPictureState(sps, l)
				}
				var cuCount, tuCount, blockCount, pcmCount int
				dec := CodingUnitDecoder(TransformTreeDecoder(func(w *SliceWalker, cu *CodingUnit, tu *TransformUnit) error {
					tuCount++
					blockCount += len(tu.Blocks)
					return nil
				}), func(cu *CodingUnit) {
					cuCount++
					if cu.PCM {
						pcmCount++
					}
				})
				if err := DecodeSliceData(pic, u, h, pps, dec); err != nil {
					failed++
					if failed <= 20 {
						t.Errorf("%s: slice %d (type %d, %d entry points): %v", rel, n, h.Type, len(h.EntryPoints), err)
					}
				} else {
					walked++
					units += cuCount
					tus += tuCount
					blocks += blockCount
					pcm += pcmCount
					entries += len(h.EntryPoints)
				}
				n++
			}
		}
	}
	t.Log(fmt.Sprintf("conformance-count: walk slices=%d failed=%d refused=%d units=%d transform-units=%d blocks=%d pcm=%d entry-points=%d",
		walked, failed, refused, units, tus, blocks, pcm, entries))
	// The same slices as the header test, all of them walked: none skipped, none refused
	// beyond the parameter sets that are.
	if walked != wantSlices || refused != wantSlicesRefused || failed != 0 {
		t.Fatalf("%d slices walked to their last bit, %d did not, %d refused; want %d walked and %d refused", walked, failed, refused, wantSlices, wantSlicesRefused)
	}
	if walked == 0 || failed != 0 {
		t.Fatalf("%d slices walked to their last bit, %d did not", walked, failed)
	}
}
