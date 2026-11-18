// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"bufio"
	"bytes"
	"errors"
	"path/filepath"
	"strconv"
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
		// ⛔ 2^32-1: the +1 wrapped it to an empty list that passed every bound.
		"chroma list length 2^32-1": {ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(1); w.ue(0); wideUE(w, 32) })},
		"pps id 64":                 {ppsID: 64},
		"sps id 16":                 {spsID: 16},
		"pps id 2^32-2":             {ppsID: 1<<32 - 2},
		"beta offset 7":             {deblockControl: true, deblockOffsets: &[2]int32{7, 0}},
		"beta offset -7":            {deblockControl: true, deblockOffsets: &[2]int32{-7, 0}},
		"tc offset 7":               {deblockControl: true, deblockOffsets: &[2]int32{0, 7}},
		"tc offset -7":              {deblockControl: true, deblockOffsets: &[2]int32{0, -7}},
		"chroma list depth":         {ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(1); w.ue(4); w.ue(0) })},
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
		"ids 63 and 15":       {ppsID: 63, spsID: 15},
		"deblocking -6 and 6": {deblockControl: true, deblockOffsets: &[2]int32{-6, 6}},
		"chroma list of 6": {ext: ppsRangeExt(func(w *writer) {
			w.bit(0)
			w.bit(1)
			w.ue(0)
			w.ue(5)
			for i := 0; i < 12; i++ {
				w.se(0)
			}
			w.ue(0)
			w.ue(0)
		})},
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

// ppsGroups splits a trace into one map per PPS it prints: from a
// pps_pic_parameter_set_id line to the next one or the first slice header, first
// value of each element within the group.
func ppsGroups(trace []byte) []map[string]int64 {
	var out []map[string]int64
	var cur map[string]int64
	sc := bufio.NewScanner(bytes.NewReader(trace))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		m := traceRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		v, _ := strconv.ParseInt(m[2], 10, 64)
		switch {
		case m[1] == "pps_pic_parameter_set_id":
			cur = map[string]int64{}
			out = append(out, cur)
		case m[1] == "first_slice_segment_in_pic_flag":
			cur = nil
		}
		if cur != nil {
			if _, seen := cur[m[1]]; !seen {
				cur[m[1]] = v
			}
		}
	}
	return out
}

// ppsOptional are the elements that exist only when a flag says so: each is
// compared when the trace prints it, and must be printed exactly when the
// parsed set says it is present.
var ppsOptional = []struct {
	name    string
	present func(PPS) bool
	get     func(PPS) int64
}{
	{"diff_cu_qp_delta_depth", func(p PPS) bool { return p.CUQPDeltaEnabled }, func(p PPS) int64 { return int64(p.DiffCuQPDeltaDepth) }},
	{"num_tile_columns_minus1", func(p PPS) bool { return p.TilesEnabled }, func(p PPS) int64 { return int64(p.TileColumns) - 1 }},
	{"num_tile_rows_minus1", func(p PPS) bool { return p.TilesEnabled }, func(p PPS) int64 { return int64(p.TileRows) - 1 }},
	{"uniform_spacing_flag", func(p PPS) bool { return p.TilesEnabled }, func(p PPS) int64 { return b2i(p.UniformTileSpacing) }},
	{"loop_filter_across_tiles_enabled_flag", func(p PPS) bool { return p.TilesEnabled }, func(p PPS) int64 { return b2i(p.LoopFilterAcrossTiles) }},
	{"pps_beta_offset_div2", func(p PPS) bool { return p.DeblockingControl && !p.DeblockingDisabled }, func(p PPS) int64 { return int64(p.BetaOffsetDiv2) }},
	{"pps_tc_offset_div2", func(p PPS) bool { return p.DeblockingControl && !p.DeblockingDisabled }, func(p PPS) int64 { return int64(p.TCOffsetDiv2) }},
	{"deblocking_filter_override_enabled_flag", func(p PPS) bool { return p.DeblockingControl }, func(p PPS) int64 { return b2i(p.DeblockingOverride) }},
	{"pps_deblocking_filter_disabled_flag", func(p PPS) bool { return p.DeblockingControl }, func(p PPS) int64 { return b2i(p.DeblockingDisabled) }},
}

// TestPPSAgainstTheTraces: EVERY PPS of each stream whose trace carries the
// header, in order, field by field, including the tile, deblocking and
// quantiser-depth elements where the trace prints them.
func TestPPSAgainstTheTraces(t *testing.T) {
	p := conformance(t)
	checked, sets, fields := 0, 0, 0
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
		groups := ppsGroups(p.read(t, traceRel))
		if len(groups) == 0 {
			continue
		}
		units, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		var parsed []PPS
		for _, u := range units {
			if u.Type == UnitPPS {
				pps, err := ParsePPS(u)
				if err != nil {
					t.Fatalf("%s: %v", rel, err)
				}
				parsed = append(parsed, pps)
			}
		}
		if len(parsed) != len(groups) {
			t.Errorf("%s: %d picture parameter sets in the stream, %d in the trace", rel, len(parsed), len(groups))
			continue
		}
		for i, pps := range parsed {
			want := groups[i]
			for _, f := range ppsAliases {
				ok := false
				for _, name := range f.names {
					if v, present := want[name]; present {
						ok = true
						fields++
						if got := f.get(pps); v != got {
							t.Errorf("%s: PPS %d: %s: trace %d, parsed %d", rel, i, name, v, got)
						}
					}
				}
				if !ok {
					t.Errorf("%s: PPS %d: the trace has none of %v", rel, i, f.names)
				}
			}
			for _, f := range ppsOptional {
				v, present := want[f.name]
				if present != f.present(pps) {
					t.Errorf("%s: PPS %d: %s printed by the trace: %v, present in the parsed set: %v", rel, i, f.name, present, f.present(pps))
					continue
				}
				if present {
					fields++
					if got := f.get(pps); v != got {
						t.Errorf("%s: PPS %d: %s: trace %d, parsed %d", rel, i, f.name, v, got)
					}
				}
			}
			sets++
		}
		checked++
	}
	// The counts are asserted, and printed in the form the CI step greps.
	if checked != 27 || sets < 27 {
		t.Fatalf("%d streams, %d sets compared; want 27 streams", checked, sets)
	}
	t.Logf("conformance-count: pps-trace streams=%d sets=%d fields=%d", checked, sets, fields)
}

func TestEveryPPSOfTheConformanceSuiteParses(t *testing.T) {
	p := conformance(t)
	n, validated := 0, 0
	for _, rel := range p.streams() {
		units, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		// A PPS may precede the SPS it names (RPS_C and VPSSPSPPS_A send them in
		// other orders), so the sequence sets of the whole stream are gathered
		// first.
		spsByID := map[uint32]SPS{}
		for _, u := range units {
			if u.Type == UnitSPS {
				if s, err := ParseSPS(u); err == nil {
					spsByID[s.ID] = s
				}
			}
		}
		for _, u := range units {
			switch u.Type {
			case UnitPPS:
				n++
				pps, err := ParsePPS(u)
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					continue
				}
				// Checked against the sequence it names, when that one parsed.
				if s, ok := spsByID[pps.SPSID]; ok {
					validated++
					if err := pps.Validate(s); err != nil {
						t.Errorf("%s: %v", rel, err)
					}
				} else {
					t.Errorf("%s: a PPS names SPS %d, which is not there", rel, pps.SPSID)
				}
			}
		}
	}
	if n != wantPPSSets {
		t.Fatalf("%d picture parameter sets in the suite, want %d", n, wantPPSSets)
	}
	// The number validated is asserted too, not only the number parsed.
	if validated != wantPPSValidated {
		t.Fatalf("%d picture parameter sets validated, want %d", validated, wantPPSValidated)
	}
	t.Logf("conformance-count: pps sets=%d validated=%d", n, validated)
}

// wantPPSSets is how many picture parameter sets the pinned HEVC_v1 and RExt
// streams hold (counted when the manifests were written).
const wantPPSSets = 1310

// wantPPSValidated is how many of them name an SPS that parsed: all (the 29 sets
// of the streams of alignedStreams named a refused SPS until the alignment of
// 9.3.4.3.6 was decoded, h265#54).
const wantPPSValidated = 1310

// wideUE writes an Exp-Golomb code of n leading zeros by hand: writer.ue cannot
// reach 2^32-1, which is the value a +1 wraps on.
func wideUE(w *writer, n int) {
	w.zeros(n)
	w.bit(1)
	w.zeros(n)
}

func TestValidateAgainstTheSPS(t *testing.T) {
	sps := func(s set) SPS {
		s.subLayers = 1
		got, err := ParseSPS(s.build())
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	pic := set{chroma: 1, width: 64, height: 64} // 4x4 coding tree blocks of 16, diff 1, max transform 8
	short := set{chroma: 1, width: 64, height: 48}
	mono := set{chroma: 0, width: 64, height: 64}
	deep := set{chroma: 1, width: 64, height: 64, bdLuma: 4, bdChroma: 2} // 12 and 10 bits
	one := uint32(1)
	for name, c := range map[string]struct {
		s    set
		p    pset
		fail bool
	}{
		"a plain set":                     {pic, pset{}, false},
		"another sequence":                {pic, pset{spsID: 1}, true},
		"4 tile columns, uniform":         {pic, pset{tiles: true, tileCols: 4, tileRows: 1, uniform: true}, false},
		"5 tile columns":                  {pic, pset{tiles: true, tileCols: 5, tileRows: 1, uniform: true}, true},
		"5 tile rows":                     {pic, pset{tiles: true, tileCols: 1, tileRows: 5, uniform: true}, true},
		"stated widths filling the row":   {pic, pset{tiles: true, tileCols: 2, tileRows: 1}, true},
		"stated heights, room to spare":   {pic, pset{tiles: true, tileCols: 1, tileRows: 2}, false},
		"stated heights filling the rows": {short, pset{tiles: true, tileCols: 1, tileRows: 2}, true},
		"quantiser depth 2 of 1":          {pic, pset{cuQPDelta: true}, true},
		"quantiser depth 1 of 1":          {pic, pset{cuQPDelta: true, diffDepth: &one}, false},
		"init_qp below -26":               {pic, pset{initQP: -27}, true},
		"init_qp -26":                     {pic, pset{initQP: -26}, false},
		"init_qp -50 at 12 bits":          {deep, pset{initQP: -50}, false},
		"init_qp -51 at 12 bits":          {deep, pset{initQP: -51}, true},
		"merge level 5 of 4":              {pic, pset{mergeMinus2: 3}, true},
		"merge level 4 of 4":              {pic, pset{mergeMinus2: 2}, false},
		"transform skip 16 of 8": {pic, pset{transformSkip: true, ext: ppsRangeExt(func(w *writer) {
			w.ue(2)
			w.bit(0)
			w.bit(0)
			w.ue(0)
			w.ue(0)
		})}, true},
		"transform skip 8 of 8": {pic, pset{transformSkip: true, ext: ppsRangeExt(func(w *writer) {
			w.ue(1)
			w.bit(0)
			w.bit(0)
			w.ue(0)
			w.ue(0)
		})}, false},
		"chroma list without chroma": {mono, pset{ext: ppsRangeExt(func(w *writer) {
			w.bit(0)
			w.bit(1)
			w.ue(0)
			w.ue(0)
			w.se(0)
			w.se(0)
			w.ue(0)
			w.ue(0)
		})}, true},
		"chroma list too deep": {pic, pset{ext: ppsRangeExt(func(w *writer) {
			w.bit(0)
			w.bit(1)
			w.ue(2)
			w.ue(0)
			w.se(0)
			w.se(0)
			w.ue(0)
			w.ue(0)
		})}, true},
		"chroma list in range": {pic, pset{ext: ppsRangeExt(func(w *writer) {
			w.bit(0)
			w.bit(1)
			w.ue(1)
			w.ue(0)
			w.se(0)
			w.se(0)
			w.ue(0)
			w.ue(0)
		})}, false},
		"SAO luma scale at 8 bits":    {pic, pset{ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(0); w.ue(1); w.ue(0) })}, true},
		"SAO chroma scale at 8 bits":  {pic, pset{ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(0); w.ue(0); w.ue(1) })}, true},
		"SAO scales 2 and 0 at 12/10": {deep, pset{ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(0); w.ue(2); w.ue(0) })}, false},
		"SAO chroma scale 1 at 10":    {deep, pset{ext: ppsRangeExt(func(w *writer) { w.bit(0); w.bit(0); w.ue(0); w.ue(1) })}, true},
	} {
		pps, err := ParsePPS(c.p.build())
		if err != nil {
			t.Errorf("%s: the PPS itself is refused: %v", name, err)
			continue
		}
		err = pps.Validate(sps(c.s))
		if c.fail != (err != nil) || (err != nil && !errors.Is(err, ErrUnsupportedPPS)) {
			t.Errorf("%s: Validate = %v, want failure %v", name, err, c.fail)
		}
	}
}

// TestNoPlusOneWraps: every `ue() + 1` of a count or size is bounded as read.
// 2^32-1 is the value the +1 wraps on, and each of these used to be accepted.
func TestNoPlusOneWraps(t *testing.T) {
	// A coded picture buffer count of 2^32-1 in an HRD.
	s := base()
	s.vui = func(w *writer) {
		w.zeros(4)
		w.zeros(3)
		w.bit(0)
		w.bit(1) // vui_timing_info_present_flag
		w.bits(0, 64)
		w.bit(0)
		w.bit(1) // vui_hrd_parameters_present_flag
		w.bit(1) // nal_hrd_parameters_present_flag
		w.bit(0)
		w.bit(0) // sub_pic_hrd_params_present_flag
		w.bits(0, 8+15)
		w.bit(1) // fixed_pic_rate_general_flag
		w.ue(0)
		wideUE(w, 32) // cpb_cnt_minus1
	}
	if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
		t.Errorf("cpb_cnt_minus1 of 2^32-1: %v", err)
	}
	// abs_delta_rps_minus1 of 2^32-1 in a predicted short-term set: int32 made it
	// -1, so the delta came out 0 and passed.
	w := &writer{}
	w.bit(1) // inter_ref_pic_set_prediction_flag
	w.bit(0) // delta_rps_sign
	wideUE(w, 32)
	w.bits(0, 16)
	if _, err := parseShortTermRPS(newSticky(w.data), 1, []ShortTermRPS{{}}, false); !errors.Is(err, ErrUnsupportedSPS) {
		t.Errorf("abs_delta_rps_minus1 of 2^32-1: %v", err)
	}
	// delta_idx_minus1 of 2^32-1 in a slice header: +1 wraps to 0 on a 32-bit int.
	w = &writer{}
	w.bit(1) // inter_ref_pic_set_prediction_flag
	wideUE(w, 32)
	w.bits(0, 16)
	if _, err := parseShortTermRPS(newSticky(w.data), 2, []ShortTermRPS{{}, {}}, true); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("delta_idx_minus1 of 2^32-1: %v", err)
	}
}
