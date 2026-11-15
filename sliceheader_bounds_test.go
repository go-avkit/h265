// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math"
	"runtime"
	"testing"
)

// TestEntryPointsAreBoundedByTheData: the offsets and their sum stay within the
// slice data that follows the header, and a count is checked against the bits
// left before anything is allocated.
func TestEntryPointsAreBoundedByTheData(t *testing.T) {
	sps := smallSPS(t)
	wpp := mustPPS(t, pset{entropySync: true})
	head := func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bit(0)
		w.bit(0)
		w.se(0)
		w.bit(1)
	}
	// Two offsets of 2^30+1 in a NAL of a dozen bytes.
	u := hdrUnit(UnitIDRWRADL, func(w *writer) {
		head(w)
		w.ue(2)
		w.ue(31)
		w.bits(1<<30, 32)
		w.bits(1<<30, 32)
	})
	if len(u.Payload) > 20 {
		t.Fatalf("the NAL is %d bytes", len(u.Payload))
	}
	if _, err := ParseSliceSegmentHeaderFull(u, sps, wpp, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("offsets of 2^30+1 in %d bytes: %v", len(u.Payload), err)
	}
	// An offset that exactly fits the data is accepted; one byte short is not.
	w := &writer{}
	head(w)
	w.ue(1)
	w.ue(7)
	w.bits(2, 8) // an offset of 3 bytes
	w.bit(1)
	for w.pos%8 != 0 {
		w.bit(0)
	}
	w.data = append(w.data, 1, 2, 3)
	if _, err := ParseSliceSegmentHeaderFull(Unit{Type: UnitIDRWRADL, Payload: w.data}, sps, wpp, nil); err != nil {
		t.Errorf("an offset that fits: %v", err)
	}
	w.data = w.data[:len(w.data)-1]
	if _, err := ParseSliceSegmentHeaderFull(Unit{Type: UnitIDRWRADL, Payload: w.data}, sps, wpp, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("an offset one byte past the data: %v", err)
	}

	// A count the header has no bits for, checked before the allocation: 1000
	// entry points of 32 bits where tiles allow them, in a header of a few bytes.
	big := mustSPS(t, set{chroma: 1, width: 512, height: 512})
	tiles := mustPPS(t, pset{tiles: true, tileCols: 32, tileRows: 32, uniform: true})
	few := hdrUnit(UnitIDRWRADL, func(w *writer) {
		head(w)
		w.ue(1000)
		w.ue(31)
	})
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := ParseSliceSegmentHeaderFull(few, big, tiles, nil)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrSliceHeader) {
		t.Errorf("1000 entry points with no bits left: %v", err)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 1<<20 {
		t.Errorf("a header of %d bytes allocated %d bytes", len(few.Payload), grown)
	}
	// And 2^24-1 of them: past the picture's limit, refused without a loop.
	if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitIDRWRADL, func(w *writer) {
		head(w)
		w.ue(1<<24 - 1)
		w.ue(31)
	}), sps, wpp, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("2^24-1 entry points: %v", err)
	}
}

// TestLongTermCyclesAreBounded: delta_poc_msb_cycle_lt is 0 to 2^(32 -
// log2_max_pic_order_cnt_lsb) as read, and the order count it gives is computed
// in 64 bits.
func TestLongTermCyclesAreBounded(t *testing.T) {
	lt := mustSPS(t, set{chroma: 1, width: 64, height: 64, longTerm: 1}) // 8-bit order counts
	pps := mustPPS(t, pset{})
	slice := func(cycle uint32) Unit {
		return hdrUnit(UnitTrailR, func(w *writer) {
			w.bit(1)
			w.ue(0)
			w.ue(2)
			w.bits(0, 8)
			w.bit(0) // short_term_ref_pic_set_sps_flag
			w.ue(0)
			w.ue(0)
			w.ue(0) // num_long_term_sps
			w.ue(1) // num_long_term_pics
			w.bits(5, 8)
			w.bit(1)
			w.bit(1) // delta_poc_msb_present_flag
			w.ue(cycle)
			w.se(0)
			w.bit(1)
		})
	}
	if h, err := ParseSliceSegmentHeaderFull(slice(1<<24), lt, pps, nil); err != nil || h.Refs.LongTerm[0].DeltaMSBCycle != 1<<24 {
		t.Errorf("the largest cycle: %+v, %v", h.Refs.LongTerm, err)
	}
	for _, c := range []uint32{1<<24 + 1, 1<<31 - 1} {
		if _, err := ParseSliceSegmentHeaderFull(slice(c), lt, pps, nil); !errors.Is(err, ErrSliceHeader) {
			t.Errorf("a cycle of %d: %v", c, err)
		}
	}
	// Derive with cycles that accumulate past 32 bits: refused with ErrPOCRange,
	// never wrapped into a plausible order count and never clamped (a clamped
	// 2^31-1 could be a real picture's).
	var entries []LongTermRef
	for i := 0; i < 32; i++ {
		entries = append(entries, LongTermRef{POCLSB: 5, Used: true, MSBPresent: true, DeltaMSBCycle: 1 << 24})
	}
	if _, err := Derive(PictureRefs{LongTerm: entries}, 100, lt); !errors.Is(err, ErrPOCRange) {
		t.Errorf("cycles past 32 bits: %v", err)
	}
}

// TestDeriveRefusesOrderCountsThatCannotBe: 8.3.1 limits PicOrderCntVal to the
// 32-bit range and DiffPicOrderCnt( current, picture ) to -2^15..2^15-1.
func TestDeriveRefusesOrderCountsThatCannotBe(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 8}
	st := func(cur int32, deltas ...int32) PictureRefs {
		var r PictureRefs
		for _, d := range deltas {
			if d < 0 {
				r.ShortTerm.Before = append(r.ShortTerm.Before, RefPic{DeltaPOC: d, Used: true})
			} else {
				r.ShortTerm.After = append(r.ShortTerm.After, RefPic{DeltaPOC: d, Used: true})
			}
		}
		return r
	}
	for name, c := range map[string]struct {
		refs PictureRefs
		poc  int32
		fail bool
	}{
		"32768 before (diff 32768)":       {st(0, -32768), 40000, true},
		"32767 before (diff 32767)":       {st(0, -32767), 40000, false},
		"32768 after (diff -32768)":       {st(0, 32768), 40000, false},
		"32769 after (diff -32769)":       {st(0, 32769), 40000, true},
		"above the 32-bit range":          {st(0, 5), math.MaxInt32 - 4, true},
		"the top of the range":            {st(0, 5), math.MaxInt32 - 5, false},
		"below the 32-bit range":          {st(0, -5), math.MinInt32 + 4, true},
		"the bottom of the range":         {st(0, -5), math.MinInt32 + 5, false},
		"a long-term count past 2^31 - 1": {PictureRefs{LongTerm: []LongTermRef{{POCLSB: 5, Used: true, MSBPresent: true, DeltaMSBCycle: 0}}}, math.MaxInt32, false},
		"a long-term count 2^15 away":     {PictureRefs{LongTerm: []LongTermRef{{POCLSB: 5, Used: true, MSBPresent: true, DeltaMSBCycle: 129}}}, 40000, true},
		"a long-term count within 2^15":   {PictureRefs{LongTerm: []LongTermRef{{POCLSB: 5, Used: true, MSBPresent: true, DeltaMSBCycle: 100}}}, 40000, false},
	} {
		_, err := Derive(c.refs, c.poc, sps)
		if c.fail != (err != nil) || (err != nil && !errors.Is(err, ErrPOCRange)) {
			t.Errorf("%s: err = %v, want failure %v", name, err, c.fail)
		}
	}
	// A short-term picture whose count would wrap an int32 is refused, not wrapped.
	if _, err := Derive(st(0, 100), math.MaxInt32-50, sps); !errors.Is(err, ErrPOCRange) {
		t.Errorf("a wrapping short-term count: %v", err)
	}
	// The same limits are enforced on the unused entries of the sets (Foll).
	foll := PictureRefs{ShortTerm: ShortTermRPS{After: []RefPic{{DeltaPOC: 40000}}}}
	if _, err := Derive(foll, 0, sps); !errors.Is(err, ErrPOCRange) {
		t.Errorf("an unused entry 40000 away: %v", err)
	}
}
