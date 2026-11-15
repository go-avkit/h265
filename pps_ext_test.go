// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func u32(v uint32) *uint32 { return &v }

func TestPPSFieldsSliceDataNeeds(t *testing.T) {
	p, err := ParsePPS(pset{
		cuQPDelta: true, tiles: true, tileCols: 3, tileRows: 2, mergeMinus2: 3,
		deblockControl: true, scalingLists: true,
	}.build())
	if err != nil {
		t.Fatal(err)
	}
	if p.DiffCuQPDeltaDepth != 2 || p.Log2ParMrgLevel != 5 || !p.LoopFilterAcrossTiles ||
		!p.LoopFilterAcrossSlices || !p.ScalingListPresent {
		t.Errorf("fields %+v", p)
	}
	if len(p.ColumnWidthMinus1) != 2 || p.ColumnWidthMinus1[0] != 3 || len(p.RowHeightMinus1) != 1 || p.RowHeightMinus1[0] != 2 {
		t.Errorf("tile sizes %v %v", p.ColumnWidthMinus1, p.RowHeightMinus1)
	}
	if p.Range.Log2MaxTransformSkipSize != 2 {
		t.Errorf("an absent range extension infers a transform skip size of 4: %+v", p.Range)
	}
	plain, _ := ParsePPS(pset{}.build())
	if plain.DiffCuQPDeltaDepth != 0 || plain.Log2ParMrgLevel != 2 || plain.ScalingListPresent || len(plain.ColumnWidthMinus1) != 0 {
		t.Errorf("a plain set: %+v", plain)
	}
}

func ppsRangeExt(fn func(*writer)) func(*writer) {
	return func(w *writer) {
		w.bits(0b1000, 4) // range, multilayer, 3d, scc
		w.bits(0, 4)
		fn(w)
	}
}

func TestPPSRangeExtension(t *testing.T) {
	p, err := ParsePPS(pset{transformSkip: true, ext: ppsRangeExt(func(w *writer) {
		w.ue(2)  // log2_max_transform_skip_block_size_minus2
		w.bit(1) // cross_component_prediction_enabled_flag
		w.bit(1) // chroma_qp_offset_list_enabled_flag
		w.ue(1)  // diff_cu_chroma_qp_offset_depth
		w.ue(1)  // chroma_qp_offset_list_len_minus1: two entries
		w.se(-12)
		w.se(12)
		w.se(3)
		w.se(-4)
		w.ue(4) // log2_sao_offset_scale_luma
		w.ue(6) // log2_sao_offset_scale_chroma
	})}.build())
	if err != nil {
		t.Fatal(err)
	}
	x := p.Range
	if x.Log2MaxTransformSkipSize != 4 || !x.CrossComponentPrediction || !x.ChromaQPOffsetListEnabled ||
		x.DiffCuChromaQPOffsetDepth != 1 || len(x.CbQPOffsetList) != 2 || x.CbQPOffsetList[0] != -12 ||
		x.CrQPOffsetList[0] != 12 || x.CbQPOffsetList[1] != 3 || x.CrQPOffsetList[1] != -4 ||
		x.Log2SAOOffsetScaleLuma != 4 || x.Log2SAOOffsetScaleChroma != 6 {
		t.Errorf("range extension %+v", x)
	}
	// Without transform skip the size field is absent and a list is optional.
	p, err = ParsePPS(pset{ext: ppsRangeExt(func(w *writer) {
		w.bit(0)
		w.bit(0)
		w.ue(0)
		w.ue(0)
	})}.build())
	if err != nil || p.Range.Log2MaxTransformSkipSize != 2 || p.Range.ChromaQPOffsetListEnabled {
		t.Errorf("minimal range extension: %v %+v", err, p.Range)
	}
}

func TestPPSOtherExtensions(t *testing.T) {
	// Extension data under pps_extension_4bits is ignored, not a leftover.
	if _, err := ParsePPS(pset{ext: func(w *writer) { w.bits(0, 4); w.bits(1, 4); w.bits(0xA5, 8) }}.build()); err != nil {
		t.Errorf("extension data: %v", err)
	}
	// A range extension flag, no range extension syntax requested by 4bits.
	if _, err := ParsePPS(pset{ext: func(w *writer) { w.bits(0, 4); w.bits(0, 4) }}.build()); err != nil {
		t.Errorf("an extension block with nothing in it: %v", err)
	}
	for name, flags := range map[string]uint32{"multilayer": 0b0100, "3D": 0b0010, "screen content": 0b0001} {
		u := pset{ext: func(w *writer) { w.bits(flags, 4); w.bits(0, 4) }}.build()
		if _, err := ParsePPS(u); !errors.Is(err, ErrUnsupportedPPS) {
			t.Errorf("%s extension: %v", name, err)
		}
	}
}

func TestPPSExtensionFlagsPastTheEnd(t *testing.T) {
	// pps_extension_present_flag is the last bit: eight flag bits are missing.
	if _, err := (&PPS{}).readExtensions(newSticky([]byte{0x80})); err == nil {
		t.Error("extension flags past the end accepted")
	}
}

func TestPPSBoundsRefusals(t *testing.T) {
	big := [2]int32{13, 0}
	for name, p := range map[string]pset{
		"init_qp_minus26 too low":  {initQP: -75},
		"init_qp_minus26 too high": {initQP: 26},
		"cu_qp_delta depth":        {cuQPDelta: true, diffDepth: u32(4)},
		"cb offset":                {chromaOffsets: &big},
		"cr offset":                {chromaOffsets: &[2]int32{0, -13}},
		"tile columns":             {tiles: true, tileCols: 1025, tileRows: 1, uniform: true},
		"tile rows":                {tiles: true, tileCols: 1, tileRows: 1025, uniform: true},
		"parallel merge level":     {mergeMinus2: 5},
		"transform skip size":      {transformSkip: true, ext: ppsRangeExt(func(w *writer) { w.ue(4) })},
		"chroma list too long":     {ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(1); w.ue(0); w.ue(6) })},
		"chroma list depth":        {ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(1); w.ue(4); w.ue(0) })},
		"chroma list entry": {ext: ppsRangeExt(func(w *writer) {
			w.bit(0)
			w.bit(1)
			w.ue(0)
			w.ue(0)
			w.se(13)
			w.se(0)
		})},
		"SAO offset scale, luma":   {ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(0); w.ue(7); w.ue(0) })},
		"SAO offset scale, chroma": {ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(0); w.ue(0); w.ue(7) })},
	} {
		if _, err := ParsePPS(p.build()); !errors.Is(err, ErrUnsupportedPPS) {
			t.Errorf("%s: err = %v, want ErrUnsupportedPPS", name, err)
		}
	}
	// The boundaries themselves are accepted.
	for name, p := range map[string]pset{
		"init_qp_minus26 -74": {initQP: -74},
		"init_qp_minus26 25":  {initQP: 25},
		"depth 3":             {cuQPDelta: true, diffDepth: u32(3)},
		"offsets -12 and 12":  {chromaOffsets: &[2]int32{-12, 12}},
		"1024 tile columns":   {tiles: true, tileCols: 1024, tileRows: 1, uniform: true},
		"merge level 4":       {mergeMinus2: 4},
	} {
		if _, err := ParsePPS(p.build()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPPSNeedsItsStopBit(t *testing.T) {
	found := false
	for q := int32(-20); q <= 25 && !found; q++ {
		u := pset{initQP: q}.build()
		if u.Payload[len(u.Payload)-1] == 0x80 {
			found = true
			u.Payload = u.Payload[:len(u.Payload)-1]
			if _, err := ParsePPS(u); !errors.Is(err, ErrUnsupportedPPS) {
				t.Errorf("no stop bit: %v", err)
			}
		}
	}
	if !found {
		t.Fatal("no fixture puts the stop bit alone in a byte")
	}
}

var ppsAliases = []struct {
	names []string
	get   func(PPS) int64
}{
	{[]string{"dependent_slice_segments_enabled_flag"}, func(p PPS) int64 { return b2i(p.DependentSlicesEnabled) }},
	{[]string{"output_flag_present_flag"}, func(p PPS) int64 { return b2i(p.OutputFlagPresent) }},
	{[]string{"num_extra_slice_header_bits"}, func(p PPS) int64 { return int64(p.ExtraSliceHeaderBits) }},
	{[]string{"sign_data_hiding_flag", "sign_data_hiding_enabled_flag"}, func(p PPS) int64 { return b2i(p.SignDataHiding) }},
	{[]string{"cabac_init_present_flag"}, func(p PPS) int64 { return b2i(p.CABACInitPresent) }},
	{[]string{"num_ref_idx_l0_default_active_minus1"}, func(p PPS) int64 { return int64(p.NumRefIdxL0) - 1 }},
	{[]string{"num_ref_idx_l1_default_active_minus1"}, func(p PPS) int64 { return int64(p.NumRefIdxL1) - 1 }},
	{[]string{"init_qp_minus26"}, func(p PPS) int64 { return int64(p.InitQP) - 26 }},
	{[]string{"constrained_intra_pred_flag"}, func(p PPS) int64 { return b2i(p.ConstrainedIntraPred) }},
	{[]string{"transform_skip_enabled_flag"}, func(p PPS) int64 { return b2i(p.TransformSkip) }},
	{[]string{"cu_qp_delta_enabled_flag"}, func(p PPS) int64 { return b2i(p.CUQPDeltaEnabled) }},
	{[]string{"pps_cb_qp_offset"}, func(p PPS) int64 { return int64(p.CbQPOffset) }},
	{[]string{"pps_cr_qp_offset"}, func(p PPS) int64 { return int64(p.CrQPOffset) }},
	{[]string{"pps_slice_chroma_qp_offsets_present_flag"}, func(p PPS) int64 { return b2i(p.SliceChromaQPOffsets) }},
	{[]string{"weighted_pred_flag"}, func(p PPS) int64 { return b2i(p.WeightedPred) }},
	{[]string{"weighted_bipred_flag"}, func(p PPS) int64 { return b2i(p.WeightedBipred) }},
	{[]string{"transquant_bypass_enable_flag", "transquant_bypass_enabled_flag"}, func(p PPS) int64 { return b2i(p.TransquantBypass) }},
	{[]string{"tiles_enabled_flag"}, func(p PPS) int64 { return b2i(p.TilesEnabled) }},
	{[]string{"entropy_coding_sync_enabled_flag"}, func(p PPS) int64 { return b2i(p.EntropySyncEnabled) }},
	{[]string{"loop_filter_across_slices_enabled_flag", "pps_loop_filter_across_slices_enabled_flag"}, func(p PPS) int64 { return b2i(p.LoopFilterAcrossSlices) }},
	{[]string{"pps_scaling_list_data_present_flag"}, func(p PPS) int64 { return b2i(p.ScalingListPresent) }},
	{[]string{"lists_modification_present_flag"}, func(p PPS) int64 { return b2i(p.ListsModification) }},
	{[]string{"log2_parallel_merge_level_minus2"}, func(p PPS) int64 { return int64(p.Log2ParMrgLevel) - 2 }},
	{[]string{"slice_segment_header_extension_present_flag"}, func(p PPS) int64 { return b2i(p.SliceHeaderExtension) }},
}

// TestPPSAgainstTheTraces: the first PPS of each stream whose trace carries
// the header, field by field.
func TestPPSAgainstTheTraces(t *testing.T) {
	p := conformance(t)
	checked, fields := 0, 0
	for _, rel := range p.streams() {
		var traceRel string
		for cand := range p.hash {
			if filepath.Dir(cand) == filepath.Dir(rel) && (strings.HasSuffix(cand, ".trc") || strings.HasSuffix(cand, "_trace.txt")) {
				traceRel = cand
			}
		}
		if traceRel == "" {
			continue
		}
		want := firstValues(p.read(t, traceRel))
		if _, ok := want["pps_pic_parameter_set_id"]; !ok {
			continue
		}
		units, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		var pps PPS
		for _, u := range units {
			if u.Type == UnitPPS {
				if pps, err = ParsePPS(u); err != nil {
					t.Fatalf("%s: %v", rel, err)
				}
				break
			}
		}
		for _, f := range ppsAliases {
			ok := false
			for _, name := range f.names {
				if v, present := want[name]; present {
					ok = true
					fields++
					if got := f.get(pps); v != got {
						t.Errorf("%s: %s: trace %d, parsed %d", rel, name, v, got)
					}
				}
			}
			if !ok {
				t.Errorf("%s: the trace has none of %v", rel, f.names)
			}
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d streams with a PPS trace", checked)
	}
	t.Logf("%d streams (%d field values)", checked, fields)
}

func TestEveryPPSOfTheConformanceSuiteParses(t *testing.T) {
	p := conformance(t)
	n := 0
	for _, rel := range p.streams() {
		units, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range units {
			if u.Type != UnitPPS {
				continue
			}
			n++
			if _, err := ParsePPS(u); err != nil {
				t.Errorf("%s: %v", rel, err)
			}
		}
	}
	t.Logf("%d picture parameter sets", n)
}
