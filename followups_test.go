// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// TestWalkToPOCBoundsSliceTypeBeforeNarrowing: SliceType(256) is 0, a B slice;
// the value is checked as read.
func TestWalkToPOCBoundsSliceTypeBeforeNarrowing(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 4}
	header := func(typ func(*writer)) Unit {
		w := &writer{}
		w.bit(1) // first_slice_segment_in_pic_flag
		w.ue(0)  // slice_pic_parameter_set_id
		typ(w)
		w.bits(0, 16)
		return Unit{Type: UnitTrailR, Payload: w.data}
	}
	for name, typ := range map[string]func(*writer){
		"slice type 3":      func(w *writer) { w.ue(3) },
		"slice type 256":    func(w *writer) { w.ue(256) },
		"slice type 2^32-1": func(w *writer) { wideUE(w, 32) },
	} {
		if _, _, _, err := walkToPOC(header(typ), sps, PPS{}); !errors.Is(err, ErrSliceHeader) {
			t.Errorf("%s: err = %v, want ErrSliceHeader", name, err)
		}
	}
	for typ, want := range map[uint32]SliceType{0: SliceB, 1: SliceP, 2: SliceI} {
		_, _, got, err := walkToPOC(header(func(w *writer) { w.ue(typ) }), sps, PPS{})
		if err != nil || got != want {
			t.Errorf("slice type %d: %v, %v", typ, got, err)
		}
	}
}

// TestExplicitSetGapsAreBoundedAsRead: delta_poc_s0_minus1 and delta_poc_s1_minus1
// are 0 to 2^15-1; the check is on the raw value, not on what int32(raw)+1 left.
func TestExplicitSetGapsAreBoundedAsRead(t *testing.T) {
	explicit := func(nNeg, nPos uint32, gap func(*writer)) []byte {
		w := &writer{}
		w.ue(nNeg)
		w.ue(nPos)
		gap(w)
		w.bits(0, 16)
		return w.data
	}
	gaps := map[string]struct {
		write func(*writer)
		ok    bool
	}{
		"32767 (a gap of 32768)":     {func(w *writer) { w.ue(32767) }, true},
		"32768 (a gap of 32769)":     {func(w *writer) { w.ue(32768) }, false},
		"2^31":                       {func(w *writer) { w.ue(1 << 31) }, false},
		"2^31-1 (int32+1 overflows)": {func(w *writer) { w.ue(1<<31 - 1) }, false},
		"2^32-1 (int32+1 is 0)":      {func(w *writer) { wideUE(w, 32) }, false},
	}
	for name, g := range gaps {
		for side, counts := range map[string][2]uint32{"before": {1, 0}, "after": {0, 1}} {
			data := explicit(counts[0], counts[1], func(w *writer) { g.write(w); w.bit(1) })
			_, err := parseShortTermRPS(newSticky(data), 0, nil, false)
			if g.ok && err != nil {
				t.Errorf("%s, %s: %v", name, side, err)
			}
			if !g.ok && !errors.Is(err, ErrUnsupportedSPS) {
				t.Errorf("%s, %s: err = %v, want ErrUnsupportedSPS", name, side, err)
			}
		}
	}
}
