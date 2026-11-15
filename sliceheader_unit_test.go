// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

func mustSPS(t testing.TB, s set) SPS {
	t.Helper()
	s.subLayers = 1
	got, err := ParseSPS(s.build())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func mustPPS(t testing.TB, p pset) PPS {
	t.Helper()
	got, err := ParsePPS(p.build())
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// sliceUnit finishes a header with byte_alignment( ) and one byte of slice data
// (0xAB), so that DataOffset can be checked against it.
func hdrUnit(typ UnitType, fn func(w *writer)) Unit { return hdrUnitN(typ, fn, 1) }

// hdrUnitN is hdrUnit with n bytes of slice data (0xAB, then zeros). The payload
// is escaped as a NAL unit's is: a header with a long run of zero bits would
// otherwise contain 00 00 03, which Unescape would take for an escape.
func hdrUnitN(typ UnitType, fn func(w *writer), n int) Unit {
	w := &writer{}
	fn(w)
	w.bit(1)
	for w.pos%8 != 0 {
		w.bit(0)
	}
	raw := append(w.data, 0xAB)
	raw = append(raw, make([]byte, n-1)...)
	return Unit{Type: typ, Payload: escapeRBSP(raw)}
}

// escapeRBSP inserts an emulation prevention byte after every two zeros that
// are followed by a byte of 3 or less (7.4.2).
func escapeRBSP(raw []byte) []byte {
	var out []byte
	zeros := 0
	for _, b := range raw {
		if zeros >= 2 && b <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// 64x64 in coding tree blocks of 16: 16 blocks, 4 rows.
func smallSPS(t *testing.T) SPS {
	return mustSPS(t, set{chroma: 1, width: 64, height: 64, sao: true})
}

func TestSliceHeaderEverythingOn(t *testing.T) {
	sps := smallSPS(t)
	pps := mustPPS(t, pset{
		extraBits: 2, outputFlag: true, sliceChroma: true, deblockControl: true, entropySync: true,
		sliceExt: true, dependentSlices: true,
		ext: ppsRangeExt(func(w *writer) {
			w.bit(0)
			w.bit(1) // chroma_qp_offset_list_enabled_flag
			w.ue(0)
			w.ue(0)
			w.se(1)
			w.se(1)
			w.ue(0)
			w.ue(0)
		}),
	})
	u := hdrUnitN(UnitCRA, func(w *writer) {
		w.bit(1)      // first_slice_segment_in_pic_flag
		w.bit(0)      // no_output_of_prior_pics_flag
		w.ue(0)       // slice_pic_parameter_set_id
		w.bits(2, 2)  // slice_reserved_flag
		w.ue(2)       // slice_type I
		w.bit(0)      // pic_output_flag
		w.bits(77, 8) // slice_pic_order_cnt_lsb
		w.bit(0)      // short_term_ref_pic_set_sps_flag
		w.ue(0)       // num_negative_pics
		w.ue(0)       // num_positive_pics
		w.bit(1)      // slice_sao_luma_flag
		w.bit(0)      // slice_sao_chroma_flag
		w.se(-3)      // slice_qp_delta
		w.se(4)       // slice_cb_qp_offset
		w.se(-5)      // slice_cr_qp_offset
		w.bit(1)      // cu_chroma_qp_offset_enabled_flag
		w.bit(1)      // deblocking_filter_override_flag
		w.bit(0)      // slice_deblocking_filter_disabled_flag
		w.se(-6)      // slice_beta_offset_div2
		w.se(6)       // slice_tc_offset_div2
		w.bit(0)      // slice_loop_filter_across_slices_enabled_flag
		w.ue(2)       // num_entry_point_offsets
		w.ue(7)       // offset_len_minus1
		w.bits(4, 8)
		w.bits(9, 8)
		w.ue(2) // slice_segment_header_extension_length
		w.bits(0x12, 8)
		w.bits(0x34, 8)
	}, 16) // 16 bytes of slice data: the two offsets are 5 and 10
	h, err := ParseSliceSegmentHeaderFull(u, sps, pps, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !h.FirstSliceSegmentInPic || h.NoOutputOfPriorPics || h.PPSID != 0 || h.Dependent || h.Type != SliceI ||
		h.PicOutput || h.POCLSB != 77 || !h.Refs.SAOLuma || h.Refs.SAOChroma || h.QPDelta != -3 || h.SliceQP != 23 ||
		h.CbQPOffset != 4 || h.CrQPOffset != -5 || !h.CuChromaQPOffsetEnabled || h.DeblockingDisabled ||
		h.BetaOffsetDiv2 != -6 || h.TCOffsetDiv2 != 6 || h.LoopFilterAcrossSlices ||
		len(h.EntryPoints) != 2 || h.EntryPoints[0] != 5 || h.EntryPoints[1] != 10 {
		t.Errorf("header %+v", h)
	}
	if un := u.Unescape(); h.DataOffset != len(un)-16 || un[h.DataOffset] != 0xAB {
		t.Errorf("DataOffset %d of %d: not the slice data", h.DataOffset, len(u.Payload))
	}
	if h.HeaderBits <= 0 || h.HeaderBits >= h.DataOffset*8 {
		t.Errorf("HeaderBits %d, DataOffset %d", h.HeaderBits, h.DataOffset)
	}
	// Every shorter payload is refused.
	for n := 0; n < len(u.Payload); n++ {
		cut := Unit{Type: u.Type, Payload: u.Payload[:n]}
		if _, err := ParseSliceSegmentHeaderFull(cut, sps, pps, nil); err == nil && n < h.DataOffset {
			t.Errorf("%d of %d bytes parsed cleanly", n, len(u.Payload))
		}
	}
}

func TestSliceHeaderInferences(t *testing.T) {
	sps := smallSPS(t)
	// The PPS states deblocking (beta -2, tc 3, enabled) and loop filtering; a
	// slice that overrides nothing gets them.
	pps := mustPPS(t, pset{deblockControl: true})
	u := hdrUnit(UnitIDRWRADL, func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bit(0) // sao luma
		w.bit(0) // sao chroma
		w.se(0)
		w.bit(0) // deblocking_filter_override_flag
		w.bit(1) // slice_loop_filter_across_slices_enabled_flag (deblocking is on)
	})
	h, err := ParseSliceSegmentHeaderFull(u, sps, pps, nil)
	if err != nil || h.DeblockingDisabled || h.BetaOffsetDiv2 != -2 || h.TCOffsetDiv2 != 3 || !h.LoopFilterAcrossSlices || !h.PicOutput || h.CbQPOffset != 0 {
		t.Errorf("%+v, %v", h, err)
	}
	// With deblocking disabled by the PPS and no SAO, the loop filter flag is
	// absent and inferred from the PPS.
	off := mustPPS(t, pset{deblockControl: true, deblockDisabled: true})
	plain := mustSPS(t, set{chroma: 1, width: 64, height: 64})
	u = hdrUnit(UnitIDRNLP, func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.se(0)
		w.bit(0) // deblocking_filter_override_flag
	})
	h, err = ParseSliceSegmentHeaderFull(u, plain, off, nil)
	if err != nil || !h.DeblockingDisabled || !h.LoopFilterAcrossSlices {
		t.Errorf("%+v, %v", h, err)
	}
	// An override that disables deblocking states no offsets.
	u = hdrUnit(UnitIDRNLP, func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bit(0)
		w.bit(0)
		w.se(0)
		w.bit(1) // deblocking_filter_override_flag
		w.bit(1) // slice_deblocking_filter_disabled_flag
	})
	h, err = ParseSliceSegmentHeaderFull(u, sps, pps, nil)
	if err != nil || !h.DeblockingDisabled || h.BetaOffsetDiv2 != -2 {
		t.Errorf("%+v, %v", h, err)
	}
}

func TestSliceHeaderSegments(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 64, height: 48}) // 4x3 = 12 blocks: 4-bit addresses
	pps := mustPPS(t, pset{dependentSlices: true})
	first := hdrUnit(UnitTrailR, func(w *writer) {
		w.bit(1)
		w.ue(0)
		w.ue(2)
		w.bits(0, 8)
		w.bit(0) // short_term_ref_pic_set_sps_flag
		w.ue(0)
		w.ue(0)
		w.se(-4)
		w.bit(1) // slice_loop_filter_across_slices_enabled_flag
	})
	h0, err := ParseSliceSegmentHeaderFull(first, sps, pps, nil)
	if err != nil {
		t.Fatal(err)
	}
	dep := func(addr uint32) Unit {
		return hdrUnit(UnitTrailR, func(w *writer) {
			w.bit(0)
			w.ue(0)
			w.bit(1) // dependent_slice_segment_flag
			w.bits(addr, 4)
		})
	}
	h1, err := ParseSliceSegmentHeaderFull(dep(5), sps, pps, &h0)
	if err != nil || !h1.Dependent || h1.SegmentAddress != 5 || h1.FirstSliceSegmentInPic || h1.QPDelta != -4 || h1.Type != SliceI {
		t.Errorf("dependent segment: %+v, %v", h1, err)
	}
	if _, err := ParseSliceSegmentHeaderFull(dep(5), sps, pps, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a dependent segment with nothing before it: %v", err)
	}
	if _, err := ParseSliceSegmentHeaderFull(dep(12), sps, pps, &h0); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("an address of 12 in a picture of 12 blocks: %v", err)
	}
	// An independent segment that is not the first states its own fields.
	indep := hdrUnit(UnitTrailR, func(w *writer) {
		w.bit(0)
		w.ue(0)
		w.bit(0) // dependent_slice_segment_flag
		w.bits(7, 4)
		w.ue(2)
		w.bits(0, 8)
		w.bit(0)
		w.ue(0)
		w.ue(0)
		w.se(2)
		w.bit(1) // slice_loop_filter_across_slices_enabled_flag
	})
	h2, err := ParseSliceSegmentHeaderFull(indep, sps, pps, &h0)
	if err != nil || h2.Dependent || h2.SegmentAddress != 7 || h2.QPDelta != 2 {
		t.Errorf("independent segment: %+v, %v", h2, err)
	}
}

func TestSliceHeaderEntryPoints(t *testing.T) {
	sps := smallSPS(t) // 4 CTB rows, 16 blocks
	header := func(n uint32, lenMinus1 uint32, vals ...uint32) func(*writer) {
		return func(w *writer) {
			w.bit(1)
			w.bit(0)
			w.ue(0)
			w.ue(2)
			w.bit(0)
			w.bit(0)
			w.se(0)
			w.bit(1) // slice_loop_filter_across_slices_enabled_flag
			w.ue(n)
			if n > 0 {
				w.ue(lenMinus1)
				for _, v := range vals {
					w.bits(v, int(lenMinus1)+1)
				}
			}
		}
	}
	wpp := mustPPS(t, pset{entropySync: true})
	tiles := mustPPS(t, pset{tiles: true, tileCols: 2, tileRows: 2, uniform: true})
	both := mustPPS(t, pset{tiles: true, tileCols: 2, tileRows: 2, uniform: true, entropySync: true})
	for name, c := range map[string]struct {
		pps  PPS
		fn   func(*writer)
		fail bool
		want []uint32
	}{
		"none":                    {wpp, header(0, 0), false, nil},
		"WPP, 3 of at most 3":     {wpp, header(3, 3, 0, 1, 2), false, []uint32{1, 2, 3}},
		"WPP, 4 of at most 3":     {wpp, header(4, 3, 0, 1, 2, 3), true, nil},
		"tiles, 3 of at most 3":   {tiles, header(3, 3, 0, 1, 2), false, []uint32{1, 2, 3}},
		"tiles, 4 of at most 3":   {tiles, header(4, 3, 0, 1, 2, 3), true, nil},
		"tiles and WPP, 7 of 7":   {both, header(7, 3, 0, 1, 2, 3, 4, 5, 6), false, []uint32{1, 2, 3, 4, 5, 6, 7}},
		"tiles and WPP, 8 of 7":   {both, header(8, 3, 0, 1, 2, 3, 4, 5, 6, 7), true, nil},
		"20-bit offsets":          {wpp, header(1, 19, 0xA), false, []uint32{0xB}},
		"32-bit offsets":          {wpp, header(1, 31, 2), false, []uint32{3}},
		"32-bit offsets, the top": {wpp, header(1, 31, 0xFFFFFFFE), true, nil}, // 2^32-1 bytes of data are not there
		"32-bit offsets, 2^32":    {wpp, header(1, 31, 0xFFFFFFFF), true, nil},
		"offset_len_minus1 of 32": {wpp, func(w *writer) { header(1, 0)(w); w.zeros(0) }, true, nil},
	} {
		data := 1 // slice data after the header: enough for the offsets
		for _, v := range c.want {
			data += int(v)
		}
		u := hdrUnitN(UnitIDRWRADL, c.fn, data)
		if name == "offset_len_minus1 of 32" {
			u = hdrUnit(UnitIDRWRADL, func(w *writer) {
				w.bit(1)
				w.bit(0)
				w.ue(0)
				w.ue(2)
				w.bit(0)
				w.bit(0)
				w.se(0)
				w.bit(1)
				w.ue(1)
				w.ue(32)
				w.bits(0, 32)
			})
		}
		h, err := ParseSliceSegmentHeaderFull(u, sps, c.pps, nil)
		if c.fail != (err != nil) {
			t.Errorf("%s: err = %v, want failure %v", name, err, c.fail)
			continue
		}
		if err == nil {
			if len(h.EntryPoints) != len(c.want) {
				t.Errorf("%s: entry points %v, want %v", name, h.EntryPoints, c.want)
				continue
			}
			for i := range c.want {
				if h.EntryPoints[i] != c.want[i] {
					t.Errorf("%s: entry points %v, want %v", name, h.EntryPoints, c.want)
				}
			}
		}
	}
}

func TestSliceHeaderRefusalsInTheFields(t *testing.T) {
	sps := smallSPS(t)
	pps := mustPPS(t, pset{extraBits: 0, sliceChroma: true, deblockControl: true, sliceExt: true})
	head := func(w *writer) { // an IDR I slice up to slice_qp_delta
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bit(0)
		w.bit(0)
	}
	for name, fn := range map[string]func(*writer){
		"slice_cb_qp_offset past the range":        func(w *writer) { head(w); w.se(0); w.se(13) },
		"slice_cr_qp_offset past the range":        func(w *writer) { head(w); w.se(0); w.se(0); w.se(-13) },
		"cb offset sums past 12 with the PPS's -1": func(w *writer) { head(w); w.se(0); w.se(-12); w.se(0) },
		"cr offset sums past 12 with the PPS's 2":  func(w *writer) { head(w); w.se(0); w.se(0); w.se(11) },
		"slice beta offset 7": func(w *writer) {
			head(w)
			w.se(0)
			w.se(0)
			w.se(0)
			w.bit(1)
			w.bit(0)
			w.se(7)
			w.se(0)
		},
		"slice tc offset -7": func(w *writer) {
			head(w)
			w.se(0)
			w.se(0)
			w.se(0)
			w.bit(1)
			w.bit(0)
			w.se(0)
			w.se(-7)
		},
		"header extension of 257": func(w *writer) {
			head(w)
			w.se(0)
			w.se(0)
			w.se(0)
			w.bit(0) // deblocking_filter_override_flag
			w.bit(1) // slice_loop_filter_across_slices_enabled_flag
			w.ue(257)
		},
	} {
		if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitIDRWRADL, fn), sps, pps, nil); !errors.Is(err, ErrSliceHeader) {
			t.Errorf("%s: err = %v, want ErrSliceHeader", name, err)
		}
	}
	// byte_alignment: the first bit must be a one, and the padding zeros.
	good := func(w *writer) {
		head(w)
		w.se(0)
		w.se(0)
		w.se(0)
		w.bit(0)
		w.bit(1)
		w.ue(0)
	}
	w := &writer{}
	good(w)
	w.bit(0) // not a one
	w.zeros(8)
	if _, err := ParseSliceSegmentHeaderFull(Unit{Type: UnitIDRWRADL, Payload: w.data}, sps, pps, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("alignment starting with a zero: %v", err)
	}
	w = &writer{}
	good(w)
	w.bit(1)
	w.bit(1) // a one in the padding
	for w.pos%8 != 0 {
		w.bit(0)
	}
	w.data = append(w.data, 0)
	if _, err := ParseSliceSegmentHeaderFull(Unit{Type: UnitIDRWRADL, Payload: w.data}, sps, pps, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("alignment padding with a one: %v", err)
	}
}

func TestSliceHeaderRefusalsAtTheTop(t *testing.T) {
	sps := smallSPS(t)
	pps := mustPPS(t, pset{})
	i := func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bit(0)
		w.bit(0)
		w.se(0)
		w.bit(1)
	}
	if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitIDRWRADL, i), sps, pps, nil); err != nil {
		t.Fatal(err)
	}
	reserved := hdrUnit(UnitIDRWRADL, i)
	reserved.Type = 10
	if _, err := ParseSliceSegmentHeaderFull(reserved, sps, pps, nil); !errors.Is(err, ErrNotSlice) {
		t.Errorf("a reserved unit type: %v", err)
	}
	layered := hdrUnit(UnitIDRWRADL, i)
	layered.LayerID = 1
	if _, err := ParseSliceSegmentHeaderFull(layered, sps, pps, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("nuh_layer_id 1: %v", err)
	}
	other := mustPPS(t, pset{spsID: 1})
	if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitIDRWRADL, i), sps, other, nil); !errors.Is(err, ErrUnsupportedPPS) {
		t.Errorf("a PPS of another sequence: %v", err)
	}
	// ⛔ The activation is where PPS.Validate runs: tile widths that fill the row
	// (0xFFFFFFFE in the stream) are refused on the real path.
	filling := mustPPS(t, pset{tiles: true, tileCols: 2, tileRows: 1})
	filling.ColumnWidthMinus1 = []uint32{0xFFFFFFFE}
	if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitIDRWRADL, i), sps, filling, nil); !errors.Is(err, ErrUnsupportedPPS) {
		t.Errorf("a tile width of 2^32-1 blocks: %v", err)
	}
	huge := mustSPS(t, set{chroma: 1, width: 1 << 20, height: 1 << 20})
	if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitIDRWRADL, i), huge, pps, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a picture of 2^32 blocks: %v", err)
	}
}

func TestSliceHeaderNonIDRRefusals(t *testing.T) {
	sps := smallSPS(t)
	pps := mustPPS(t, pset{})
	lt := mustSPS(t, set{chroma: 1, width: 64, height: 64, longTerm: 1})
	typ := func(w *writer, slice uint32) { w.bit(1); w.ue(0); w.ue(slice); w.bits(0, 8) }
	for name, c := range map[string]struct {
		sps SPS
		fn  func(*writer)
	}{
		"a named set the sequence does not carry": {sps, func(w *writer) { typ(w, 2); w.bit(1) }},
		"too many long-term pictures": {lt, func(w *writer) {
			typ(w, 2)
			w.bit(0)
			w.ue(0)
			w.ue(0)
			w.ue(1)
			w.ue(33)
		}},
		"a P slice that may predict from nothing": {sps, func(w *writer) {
			typ(w, 1)
			w.bit(0)
			w.ue(0)
			w.ue(0)
			w.bit(0) // slice_sao_luma_flag
			w.bit(0) // slice_sao_chroma_flag
			w.bit(0) // num_ref_idx_active_override_flag
		}},
	} {
		if _, err := ParseSliceSegmentHeaderFull(hdrUnit(UnitTrailR, c.fn), c.sps, pps, nil); !errors.Is(err, ErrSliceHeader) {
			t.Errorf("%s: err = %v, want ErrSliceHeader", name, err)
		}
	}
	// Separate colour planes: colour_plane_id 0 to 2.
	planes := mustSPS(t, set{chroma: 3, separatePlanes: true, width: 64, height: 64})
	ok := hdrUnit(UnitIDRWRADL, func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bits(2, 2)
		w.se(0)
		w.bit(1)
	})
	if h, err := ParseSliceSegmentHeaderFull(ok, planes, pps, nil); err != nil || h.ColourPlaneID != 2 {
		t.Errorf("colour_plane_id 2: %+v, %v", h, err)
	}
	bad := hdrUnit(UnitIDRWRADL, func(w *writer) {
		w.bit(1)
		w.bit(0)
		w.ue(0)
		w.ue(2)
		w.bits(3, 2)
		w.se(0)
		w.bit(1)
	})
	if _, err := ParseSliceSegmentHeaderFull(bad, planes, pps, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("colour_plane_id 3: %v", err)
	}
}

// FuzzSliceHeader: hostile slice segment headers are refused or parsed within
// bounds, never a panic and never a data offset outside the payload.
func FuzzSliceHeader(f *testing.F) {
	sps := mustSPS(f, set{chroma: 1, width: 64, height: 64, sao: true, temporalMVP: true})
	planes := mustSPS(f, set{chroma: 3, separatePlanes: true, width: 64, height: 48})
	ppsA := mustPPS(f, pset{extraBits: 2, outputFlag: true, sliceChroma: true, deblockControl: true, entropySync: true, sliceExt: true, dependentSlices: true})
	ppsB := mustPPS(f, pset{tiles: true, tileCols: 2, tileRows: 2, uniform: true})
	f.Add(uint8(UnitIDRWRADL), []byte{0x80, 0x0a, 0xc0, 0x80})
	f.Add(uint8(UnitTrailR), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Add(uint8(UnitCRA), []byte{0xa8, 0x40, 0x10, 0x00, 0x04, 0x20})
	f.Fuzz(func(t *testing.T, typ uint8, payload []byte) {
		var prev *SliceHeader
		for _, c := range []struct {
			sps SPS
			pps PPS
		}{{sps, ppsA}, {sps, ppsB}, {planes, ppsB}} {
			h, err := ParseSliceSegmentHeaderFull(Unit{Type: UnitType(typ % 32), Payload: payload}, c.sps, c.pps, prev)
			if err != nil {
				continue
			}
			if h.DataOffset <= 0 || h.DataOffset > len(Unit{Payload: payload}.Unescape()) {
				t.Fatalf("data offset %d of %d bytes", h.DataOffset, len(payload))
			}
			if h.SliceQP < -48 || h.SliceQP > 51 || len(h.EntryPoints) > 1<<12 {
				t.Fatalf("out of bounds: %+v", h)
			}
			if !h.Dependent {
				hh := h
				prev = &hh
			}
		}
	})
}
