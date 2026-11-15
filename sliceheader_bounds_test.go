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
	// Derive with cycles that accumulate past 32 bits: no wrap into a plausible
	// order count; the result is clamped to the range's end.
	var entries []LongTermRef
	for i := 0; i < 32; i++ {
		entries = append(entries, LongTermRef{POCLSB: 5, Used: true, MSBPresent: true, DeltaMSBCycle: 1 << 24})
	}
	got := Derive(PictureRefs{LongTerm: entries}, 100, lt)
	if len(got.LtCurr) != 32 {
		t.Fatalf("%d long-term pictures", len(got.LtCurr))
	}
	for i, p := range got.LtCurr {
		want := max(int64(5)+100-int64(i+1)*(1<<24)*256-100%256, math.MinInt32)
		if int64(p.POC) != want {
			t.Errorf("entry %d: POC %d, want %d", i, p.POC, want)
		}
	}
}
