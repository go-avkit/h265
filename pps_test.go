// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// pset describes the picture parameter set a test wants written.
type pset struct {
	dependentSlices bool
	refIdxMinus1    uint32
	// refIdxWideN writes the num_ref_idx fields as a raw exp-Golomb code with
	// n leading zeros instead of ue(refIdxMinus1). ⛔ writer.ue cannot emit
	// 2^32-1 -- it comes back as 1 -- and that is the one value the +1 the
	// syntax carries wraps on, so the test writer cannot reach it by itself.
	refIdxWideN     int
	extraBits       uint32
	cuQPDelta       bool
	tiles           bool
	tileCols        uint32
	tileRows        uint32
	uniform         bool
	deblockControl  bool
	deblockDisabled bool
	scalingLists    bool
	extension       bool
}

// build writes p as a NAL unit, ending it the way a payload must end.
func (p pset) build() Unit {
	w := &writer{}
	w.ue(0) // pps_pic_parameter_set_id
	w.ue(0) // pps_seq_parameter_set_id
	w.flag(p.dependentSlices)
	w.bit(0) // output_flag_present_flag
	w.bits(p.extraBits, 3)
	w.bit(0)                 // sign_data_hiding
	w.bit(1)                 // cabac_init_present
	for i := 0; i < 2; i++ { // num_ref_idx_l0/l1_default_active_minus1
		if n := p.refIdxWideN; n > 0 {
			for j := 0; j < n; j++ {
				w.bit(0)
			}
			w.bit(1)
			for j := 0; j < n; j++ {
				w.bit(0)
			}
			continue
		}
		w.ue(p.refIdxMinus1)
	}
	w.se(0)  // init_qp_minus26
	w.bit(0) // constrained_intra_pred
	w.bit(0) // transform_skip
	w.flag(p.cuQPDelta)
	if p.cuQPDelta {
		w.ue(2) // diff_cu_qp_delta_depth
	}
	w.se(-1) // pps_cb_qp_offset
	w.se(2)  // pps_cr_qp_offset
	w.bit(0) // slice chroma qp offsets present
	w.bit(0) // weighted_pred
	w.bit(0) // weighted_bipred
	w.bit(0) // transquant_bypass
	w.flag(p.tiles)
	w.bit(0) // entropy_coding_sync
	if p.tiles {
		w.ue(p.tileCols - 1)
		w.ue(p.tileRows - 1)
		w.flag(p.uniform)
		if !p.uniform {
			// ⛔ One LESS than the count, each way: the last column and row are
			// what is left over and are not stated.
			for i := uint32(1); i < p.tileCols; i++ {
				w.ue(3)
			}
			for i := uint32(1); i < p.tileRows; i++ {
				w.ue(2)
			}
		}
		w.bit(1) // loop_filter_across_tiles
	}
	w.bit(1) // pps_loop_filter_across_slices
	w.flag(p.deblockControl)
	if p.deblockControl {
		w.bit(1) // deblocking_filter_override_enabled
		w.flag(p.deblockDisabled)
		if !p.deblockDisabled {
			w.se(-2) // beta offset
			w.se(3)  // tc offset
		}
	}
	w.flag(p.scalingLists)
	if p.scalingLists {
		// The first list of each size class is given outright and the rest are
		// predicted, which exercises both arms without writing thousands of bits.
		for sizeID := 0; sizeID < 4; sizeID++ {
			step := 1
			if sizeID == 3 {
				step = 3
			}
			first := true
			for matrixID := 0; matrixID < 6; matrixID += step {
				if first {
					w.bit(1) // given outright
					if sizeID > 1 {
						w.se(0) // dc coefficient
					}
					n := 64
					if sizeID == 0 {
						n = 16
					}
					for i := 0; i < n; i++ {
						w.se(0)
					}
					first = false
					continue
				}
				w.bit(0) // predicted from another list
				w.ue(1)
			}
		}
	}
	w.bit(1) // lists_modification_present
	w.ue(0)  // log2_parallel_merge_level_minus2
	w.bit(0) // slice_segment_header_extension_present
	w.flag(p.extension)
	if p.extension {
		w.bits(0, 8) // four named extension flags and four reserved bits
	}
	// rbsp_trailing_bits: a one, then zeros to the byte.
	w.bit(1)
	for w.pos%8 != 0 {
		w.bit(0)
	}
	return Unit{Type: UnitPPS, Payload: w.data}
}

func TestAPlainSetReadsItsFields(t *testing.T) {
	p, err := ParsePPS(pset{}.build())
	if err != nil {
		t.Fatalf("ParsePPS: %v", err)
	}
	if p.InitQP != 26 {
		t.Errorf("InitQP = %d, want 26", p.InitQP)
	}
	if p.CbQPOffset != -1 || p.CrQPOffset != 2 {
		t.Errorf("chroma offsets %d/%d, want -1/2", p.CbQPOffset, p.CrQPOffset)
	}
	if p.NumRefIdxL0 != 1 || p.NumRefIdxL1 != 1 {
		t.Errorf("ref idx %d/%d, want 1/1", p.NumRefIdxL0, p.NumRefIdxL1)
	}
	if !p.CABACInitPresent {
		t.Error("cabac_init_present was not read")
	}
	if p.TileColumns != 1 || p.TileRows != 1 {
		t.Errorf("tiles %dx%d, want 1x1 when the set states none", p.TileColumns, p.TileRows)
	}
	if !p.ListsModification {
		t.Error("lists_modification_present was not read")
	}
}

// TestWhatASliceHeaderCannotBeReadWithout: these two fields decide how a slice
// segment header begins, so a set that reported them wrongly would misalign every
// slice referring to it.
func TestWhatASliceHeaderCannotBeReadWithout(t *testing.T) {
	p, err := ParsePPS(pset{dependentSlices: true, extraBits: 3}.build())
	if err != nil {
		t.Fatalf("ParsePPS: %v", err)
	}
	if !p.DependentSlicesEnabled {
		t.Error("dependent_slice_segments_enabled was not read")
	}
	if p.ExtraSliceHeaderBits != 3 {
		t.Errorf("ExtraSliceHeaderBits = %d, want 3", p.ExtraSliceHeaderBits)
	}
}

// TestTheVariablePartsAreConsumedExactly.
//
// ⛔ Each of these shapes ends on the payload's own trailing bits, and the parser
// refuses anything else. That identity is the whole witness: a parameter set has no
// field to check its values against, so a scaling list read with the wrong count
// would give a plausible quantiser and nothing would notice. Here it gives a
// refusal.
func TestTheVariablePartsAreConsumedExactly(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    pset
	}{
		{"scaling lists", pset{scalingLists: true}},
		{"uniform tiles", pset{tiles: true, tileCols: 3, tileRows: 2, uniform: true}},
		{"tiles with stated spacing", pset{tiles: true, tileCols: 4, tileRows: 3}},
		{"one tile each way, stated", pset{tiles: true, tileCols: 1, tileRows: 1}},
		{"a quantiser delta depth", pset{cuQPDelta: true}},
		{"deblocking control, filtering on", pset{deblockControl: true}},
		{"deblocking control, filtering off", pset{deblockControl: true, deblockDisabled: true}},
		{"an extension present", pset{extension: true}},
		{"all of them at once", pset{scalingLists: true, tiles: true, tileCols: 2, tileRows: 2,
			cuQPDelta: true, deblockControl: true, extension: true, dependentSlices: true, extraBits: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePPS(tc.p.build())
			if err != nil {
				t.Fatalf("ParsePPS: %v", err)
			}
			if tc.p.tiles {
				if got.TileColumns != tc.p.tileCols || got.TileRows != tc.p.tileRows {
					t.Errorf("tiles %dx%d, want %dx%d",
						got.TileColumns, got.TileRows, tc.p.tileCols, tc.p.tileRows)
				}
			}
			// The field after every variable part: if any of them was consumed
			// wrongly this would be false, and the identity would have refused
			// the set before we got here.
			if !got.ListsModification {
				t.Error("the field after the variable parts read wrongly")
			}
		})
	}
}

func TestPPSRefusals(t *testing.T) {
	t.Run("not a PPS", func(t *testing.T) {
		if _, err := ParsePPS(Unit{Type: UnitSPS}); !errors.Is(err, ErrNotPPS) {
			t.Errorf("err = %v, want ErrNotPPS", err)
		}
	})

	// ⛔ The identity, stated as a refusal: a set that does not end on its trailing
	// bits was read wrongly.
	t.Run("bits left over", func(t *testing.T) {
		u := pset{}.build()
		u.Payload = append(u.Payload, 0x5A)
		if _, err := ParsePPS(u); !errors.Is(err, ErrUnsupportedPPS) {
			t.Errorf("err = %v, want ErrUnsupportedPPS", err)
		}
	})

	t.Run("every truncation", func(t *testing.T) {
		whole := pset{scalingLists: true, tiles: true, tileCols: 2, tileRows: 2,
			cuQPDelta: true, deblockControl: true}.build()
		if _, err := ParsePPS(whole); err != nil {
			t.Fatalf("the fixture does not parse: %v", err)
		}
		for n := 0; n < len(whole.Payload); n++ {
			cut := Unit{Type: UnitPPS, Payload: whole.Payload[:n]}
			if _, err := ParsePPS(cut); err == nil {
				t.Errorf("%d of %d bytes parsed cleanly", n, len(whole.Payload))
			}
		}
	})
}

func TestPPSRefusesAnOversizedRefIdx(t *testing.T) {
	// 7.4.3.3 puts the field in 0..14. The boundary is pinned on BOTH sides,
	// because an off-by-one here either refuses conformant streams or leaves
	// the hole open.
	for _, c := range []struct {
		minus1 uint32
		wideN  int
		refuse bool
	}{
		{minus1: 0},
		{minus1: 14}, // the largest conformant value: 15 active entries
		{minus1: 15, refuse: true},
		{minus1: 1 << 20, refuse: true},
		{minus1: 1<<32 - 2, refuse: true},
		// ⛔ 2^32-1, hand-written: the +1 the syntax carries wraps it to ZERO,
		// so a bound applied after the increment lets exactly this one past --
		// and the set then claims a list of no entries at all.
		{wideN: 32, refuse: true},
	} {
		u := pset{refIdxMinus1: c.minus1, refIdxWideN: c.wideN}.build()
		pps, err := ParsePPS(u)
		switch {
		case c.refuse && !errors.Is(err, ErrRefIdxRange):
			t.Errorf("minus1=%d wideN=%d in %d bytes: got %v (NumRefIdxL0=%d), want ErrRefIdxRange",
				c.minus1, c.wideN, len(u.Payload), err, pps.NumRefIdxL0)
		case !c.refuse && err != nil:
			t.Errorf("minus1=%d: got %v, want it read", c.minus1, err)
		case !c.refuse && pps.NumRefIdxL0 != c.minus1+1:
			t.Errorf("minus1=%d: NumRefIdxL0=%d, want %d", c.minus1, pps.NumRefIdxL0, c.minus1+1)
		}
	}
}
