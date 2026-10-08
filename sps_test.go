// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// writer builds a sequence parameter set field by field, so a test states the set
// it means rather than a hex string nobody can check.
type writer struct {
	data []byte
	pos  int
}

func (w *writer) bit(v uint32) {
	if w.pos%8 == 0 {
		w.data = append(w.data, 0)
	}
	if v&1 == 1 {
		w.data[w.pos/8] |= 1 << (7 - uint(w.pos%8))
	}
	w.pos++
}

func (w *writer) bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i) & 1)
	}
}

func (w *writer) zeros(n int) {
	for i := 0; i < n; i++ {
		w.bit(0)
	}
}

// flag writes one bit from a bool.
func (w *writer) flag(v bool) {
	if v {
		w.bit(1)
		return
	}
	w.bit(0)
}

// se writes a signed Exp-Golomb code.
func (w *writer) se(v int32) {
	if v > 0 {
		w.ue(uint32(v)*2 - 1)
		return
	}
	w.ue(uint32(-v) * 2)
}

// ue writes an unsigned Exp-Golomb code.
func (w *writer) ue(v uint32) {
	v++
	n := 0
	for t := v; t > 1; t >>= 1 {
		n++
	}
	w.zeros(n)
	for i := n; i >= 0; i-- {
		w.bit(v >> uint(i) & 1)
	}
}

// set describes the set a test wants written.
type set struct {
	subLayers       uint32 // sps_max_sub_layers_minus1 + 1
	chroma          uint32
	separatePlanes  bool
	subLayerProfile bool // the sub-layers state a profile as well as a level
	width           uint32
	height          uint32
	window          [4]uint32 // left, right, top, bottom, in chroma units
	// scalingList writes a scaling list, whose length depends on itself: the
	// reference picture sets come after it, so getting it wrong misaligns them.
	scalingList bool
	// pcm writes the PCM fields, which are also conditional.
	pcm bool
	// stRPS are short-term reference picture sets, each stated outright as
	// (negative gaps, positive gaps). An entry's second field says whether the
	// current picture may use it.
	stRPS [][2][]refGap
	// predictLast adds one more set, written as a difference from the one
	// before it rather than listed, with every entry kept.
	//
	// ⛔ The difference cannot be zero: abs_delta_rps_minus1 makes the smallest
	// step one, so the syntax has no way to say "the same set". The smallest
	// difference it can state is +1, which shifts every entry by one and moves
	// across zero any entry that crosses it.
	predictLast bool
	// longTerm writes that many long-term pictures.
	longTerm int
	// pocLSBMinus4 overrides log2_max_pic_order_cnt_lsb_minus4, so a test can
	// state a width the format does not allow.
	pocLSBMinus4 uint32
	// badSetCount writes a number of sets past what a sequence may carry.
	badSetCount bool
}

// refGap is one entry of a set a test writes: the gap from the previous entry,
// and whether the current picture uses it.
type refGap struct {
	gap  uint32
	used bool
}

func (s set) build() Unit {
	w := &writer{}
	w.bits(0, 4)             // sps_video_parameter_set_id
	w.bits(s.subLayers-1, 3) // sps_max_sub_layers_minus1
	w.bit(1)                 // sps_temporal_id_nesting_flag
	w.bits(0, 2)             // general_profile_space
	w.bit(0)                 // general_tier_flag
	w.bits(1, 5)             // general_profile_idc
	w.zeros(32 + 4 + 43 + 1) // compatibility, source and packing, the rest
	w.bits(120, 8)           // general_level_idc
	if n := int(s.subLayers) - 1; n > 0 {
		for i := 0; i < n; i++ {
			if s.subLayerProfile {
				w.bit(1)
			} else {
				w.bit(0)
			}
			w.bit(1) // sub_layer_level_present_flag
		}
		w.zeros(2 * (8 - n)) // the alignment to sixteen flags
		for i := 0; i < n; i++ {
			if s.subLayerProfile {
				// The same structure as the general one, without its level.
				w.zeros(2 + 1 + 5 + 32 + 4 + 43 + 1)
			}
			w.bits(90, 8) // sub_layer_level_idc
		}
	}
	w.ue(0) // sps_seq_parameter_set_id
	w.ue(s.chroma)
	if s.chroma == 3 {
		if s.separatePlanes {
			w.bit(1)
		} else {
			w.bit(0)
		}
	}
	w.ue(s.width)
	w.ue(s.height)
	if s.window != [4]uint32{} {
		w.bit(1)
		for _, v := range s.window {
			w.ue(v)
		}
	} else {
		w.bit(0)
	}
	w.ue(0) // bit_depth_luma_minus8
	w.ue(0) // bit_depth_chroma_minus8
	if s.pocLSBMinus4 != 0 {
		w.ue(s.pocLSBMinus4)
	} else {
		w.ue(4) // log2_max_pic_order_cnt_lsb_minus4
	}
	s.buildTail(w)
	return Unit{Type: UnitSPS, Payload: w.data}
}

// buildTail writes everything between the order-count width and the reference
// picture sets, which is most of a sequence parameter set.
func (s set) buildTail(w *writer) {
	w.bit(0) // sps_sub_layer_ordering_info_present_flag
	w.ue(1)  // sps_max_dec_pic_buffering_minus1, for the highest sub-layer only
	w.ue(0)  // sps_max_num_reorder_pics
	w.ue(0)  // sps_max_latency_increase_plus1
	w.ue(0)  // log2_min_luma_coding_block_size_minus3
	w.ue(1)  // log2_diff_max_min_luma_coding_block_size
	w.ue(0)  // log2_min_luma_transform_block_size_minus2
	w.ue(1)  // log2_diff_max_min_luma_transform_block_size
	w.ue(0)  // max_transform_hierarchy_depth_inter
	w.ue(0)  // max_transform_hierarchy_depth_intra
	if s.scalingList {
		w.bit(1) // scaling_list_enabled_flag
		w.bit(1) // sps_scaling_list_data_present_flag
		writeScalingList(w)
	} else {
		w.bit(0)
	}
	w.bit(0) // amp_enabled_flag
	w.bit(0) // sample_adaptive_offset_enabled_flag
	if s.pcm {
		w.bit(1)
		w.bits(7, 4) // pcm_sample_bit_depth_luma_minus1
		w.bits(7, 4) // pcm_sample_bit_depth_chroma_minus1
		w.ue(0)      // log2_min_pcm_luma_coding_block_size_minus3
		w.ue(1)      // log2_diff_max_min_pcm_luma_coding_block_size
		w.bit(1)     // pcm_loop_filter_disabled_flag
	} else {
		w.bit(0)
	}

	if s.badSetCount {
		w.ue(65)
		return
	}
	n := len(s.stRPS)
	if s.predictLast {
		n++
	}
	w.ue(uint32(n))
	for i, rps := range s.stRPS {
		if i != 0 {
			w.bit(0) // inter_ref_pic_set_prediction_flag
		}
		w.ue(uint32(len(rps[0])))
		w.ue(uint32(len(rps[1])))
		for _, e := range rps[0] {
			w.ue(e.gap - 1)
			w.bit(boolBit(e.used))
		}
		for _, e := range rps[1] {
			w.ue(e.gap - 1)
			w.bit(boolBit(e.used))
		}
	}
	if s.predictLast {
		w.bit(1) // inter_ref_pic_set_prediction_flag
		w.bit(0) // delta_rps_sign
		w.ue(0)  // abs_delta_rps_minus1: the smallest step the syntax allows, +1
		prev := s.stRPS[len(s.stRPS)-1]
		for range len(prev[0]) + len(prev[1]) + 1 {
			w.bit(1) // used_by_curr_pic_flag
		}
	}

	if s.longTerm > 0 {
		w.bit(1)
		w.ue(uint32(s.longTerm))
		for i := 0; i < s.longTerm; i++ {
			w.bits(uint32(i), 8) // lt_ref_pic_poc_lsb_sps, at the width above
			w.bit(1)             // used_by_curr_pic_lt_sps_flag
		}
	} else {
		w.bit(0)
	}
}

func boolBit(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

// writeScalingList writes every matrix as a copy of the default, which is the
// shortest legal form and still exercises the walk's shape.
func writeScalingList(w *writer) {
	for sizeID := 0; sizeID < 4; sizeID++ {
		step := 1
		if sizeID == 3 {
			step = 3
		}
		for matrixID := 0; matrixID < 6; matrixID += step {
			w.bit(0) // scaling_list_pred_mode_flag: copy
			w.ue(0)  // scaling_list_pred_matrix_id_delta
		}
	}
}

// TestTheWindowIsTakenOffInChromaUnits.
//
// ⛔ The offsets are in CHROMA units, so a 4:2:0 stream takes off two luma samples
// for every unit stated. The commonest shape there is has a bottom offset of four
// on a coded height of 1088, which is 1080 -- and a reader treating the offsets as
// samples reports 1084, a size that looks plausible and is wrong.
func TestTheWindowIsTakenOffInChromaUnits(t *testing.T) {
	for _, tc := range []struct {
		name          string
		s             set
		width, height uint32
	}{
		{
			"the commonest 1080p shape",
			set{subLayers: 1, chroma: 1, width: 1920, height: 1088, window: [4]uint32{0, 0, 0, 4}},
			1920, 1080,
		},
		{
			"cropped on both axes",
			set{subLayers: 1, chroma: 1, width: 1184, height: 2560, window: [4]uint32{0, 2, 0, 2}},
			1180, 2556,
		},
		{
			"no window at all",
			set{subLayers: 1, chroma: 1, width: 1280, height: 720},
			1280, 720,
		},
		{
			// 4:2:2 is half width and full height, so a vertical unit is one
			// sample where 4:2:0 makes it two.
			"4:2:2 takes off one line per unit",
			set{subLayers: 1, chroma: 2, width: 1920, height: 1088, window: [4]uint32{0, 0, 0, 4}},
			1920, 1084,
		},
		{
			"4:4:4 takes off one sample each way",
			set{subLayers: 1, chroma: 3, width: 1920, height: 1088, window: [4]uint32{0, 2, 0, 4}},
			1918, 1084,
		},
		{
			"monochrome has no chroma to scale by",
			set{subLayers: 1, chroma: 0, width: 1920, height: 1088, window: [4]uint32{0, 2, 0, 4}},
			1918, 1084,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := ParseSPS(tc.s.build())
			if err != nil {
				t.Fatalf("ParseSPS: %v", err)
			}
			if s.Width != tc.width || s.Height != tc.height {
				t.Errorf("%dx%d, want %dx%d", s.Width, s.Height, tc.width, tc.height)
			}
			if s.LevelIDC != 120 || s.ProfileIDC != 1 {
				t.Errorf("profile %d level %d -- the profile, tier and level were not consumed exactly",
					s.ProfileIDC, s.LevelIDC)
			}
		})
	}
}

// TestSubLayersAreConsumedWithTheirAlignment.
//
// ⛔ The reserved bits between the present flags and the sub-layer structures align
// the flags to sixteen: two for every sub-layer the stream does NOT have. A reader
// that skipped a fixed amount, or none, would be misaligned for every stream with
// more than one sub-layer -- and those are ordinary, since that is how a stream is
// made thinnable. The size asserted here is the witness.
func TestSubLayersAreConsumedWithTheirAlignment(t *testing.T) {
	for layers := uint32(1); layers <= 7; layers++ {
		s, err := ParseSPS(set{subLayers: layers, chroma: 1, width: 1920, height: 1088,
			window: [4]uint32{0, 0, 0, 4}}.build())
		if err != nil {
			t.Errorf("%d sub-layers: %v", layers, err)
			continue
		}
		if s.MaxSubLayers != uint8(layers) {
			t.Errorf("MaxSubLayers = %d, want %d", s.MaxSubLayers, layers)
		}
		if s.Width != 1920 || s.Height != 1080 {
			t.Errorf("%d sub-layers: %dx%d, want 1920x1080 -- the alignment was not consumed",
				layers, s.Width, s.Height)
		}
	}
}

func TestSPSRefusals(t *testing.T) {
	t.Run("not an SPS", func(t *testing.T) {
		if _, err := ParseSPS(Unit{Type: UnitPPS}); !errors.Is(err, ErrNotSPS) {
			t.Errorf("err = %v, want ErrNotSPS", err)
		}
	})

	t.Run("a chroma format the format has no value for", func(t *testing.T) {
		w := &writer{}
		w.bits(0, 4)
		w.bits(0, 3)
		w.bit(1)
		w.bits(0, 2)
		w.bit(0)
		w.bits(1, 5)
		w.zeros(32 + 4 + 43 + 1)
		w.bits(120, 8)
		w.ue(0)
		w.ue(4) // chroma_format_idc
		if _, err := ParseSPS(Unit{Type: UnitSPS, Payload: w.data}); !errors.Is(err, ErrUnsupportedSPS) {
			t.Errorf("err = %v, want ErrUnsupportedSPS", err)
		}
	})

	t.Run("a coded size of nothing", func(t *testing.T) {
		s := set{subLayers: 1, chroma: 1, width: 0, height: 0}
		if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
			t.Errorf("err = %v, want ErrUnsupportedSPS", err)
		}
	})

	// Every prefix must be refused: a set completed from nothing describes a
	// picture nobody encoded.
	t.Run("every truncation", func(t *testing.T) {
		whole := set{subLayers: 3, chroma: 1, width: 1920, height: 1088,
			window: [4]uint32{0, 0, 0, 4}}.build()
		if _, err := ParseSPS(whole); err != nil {
			t.Fatalf("the fixture does not parse: %v", err)
		}
		for n := 0; n < len(whole.Payload); n++ {
			cut := Unit{Type: UnitSPS, Payload: whole.Payload[:n]}
			if _, err := ParseSPS(cut); err == nil {
				t.Errorf("%d of %d bytes parsed cleanly", n, len(whole.Payload))
			}
		}
	})
}

// TestASubLayerThatStatesItsProfileIsConsumed.
//
// ⛔ A sub-layer may state a whole profile of its own, which is the general
// structure without its level. Skipping only the level would leave 88 bits behind
// and every field after them would be read from the wrong place -- and the first of
// those is the picture size, so it would look plausible.
func TestASubLayerThatStatesItsProfileIsConsumed(t *testing.T) {
	s, err := ParseSPS(set{subLayers: 3, chroma: 1, subLayerProfile: true,
		width: 1920, height: 1088, window: [4]uint32{0, 0, 0, 4}}.build())
	if err != nil {
		t.Fatalf("ParseSPS: %v", err)
	}
	if s.Width != 1920 || s.Height != 1080 {
		t.Errorf("%dx%d, want 1920x1080 -- a sub-layer profile was not consumed whole", s.Width, s.Height)
	}
}

// TestSeparatePlanesLeaveNothingToScaleBy: with the colour planes coded separately
// there is no subsampling at all, whatever the format says.
func TestSeparatePlanesLeaveNothingToScaleBy(t *testing.T) {
	s, err := ParseSPS(set{subLayers: 1, chroma: 3, separatePlanes: true,
		width: 1920, height: 1088, window: [4]uint32{0, 2, 0, 4}}.build())
	if err != nil {
		t.Fatalf("ParseSPS: %v", err)
	}
	if !s.SeparatePlanes {
		t.Error("the flag was not read")
	}
	if s.Width != 1918 || s.Height != 1084 {
		t.Errorf("%dx%d, want 1918x1084", s.Width, s.Height)
	}
}

// The order-count width is bounded, and a sequence past it cannot be read at
// all: every slice header's count is that many bits wide.
func TestAnOrderCountWidthPastTheBoundIsRefused(t *testing.T) {
	u := set{subLayers: 1, chroma: 1, width: 320, height: 240}.build()
	// Rebuild with an impossible width by writing the fields up to it again.
	bad := set{subLayers: 1, chroma: 1, width: 320, height: 240, pocLSBMinus4: 13}.build()
	if _, err := ParseSPS(u); err != nil {
		t.Fatalf("the ordinary fixture stopped parsing: %v", err)
	}
	if _, err := ParseSPS(bad); err == nil {
		t.Error("a width of seventeen bits was accepted")
	}
}

// A sequence whose reference picture sets cannot be read is refused as a whole,
// rather than reported as a sequence with no sets.
func TestASequenceWithUnreadableSetsIsRefused(t *testing.T) {
	u := set{subLayers: 1, chroma: 1, width: 320, height: 240, badSetCount: true}.build()
	if _, err := ParseSPS(u); err == nil {
		t.Error("a sequence claiming sixty-five sets was accepted")
	}
	// And one that stops before its sets begin.
	short := set{subLayers: 1, chroma: 1, width: 320, height: 240}.build()
	short.Payload = short.Payload[:len(short.Payload)-2]
	if _, err := ParseSPS(short); err == nil {
		t.Error("a sequence that ends before its sets was accepted")
	}
}

// A sequence that stops inside its long-term pictures is refused. The reader
// keeps the first error rather than raising it at every field, so this is the
// one place it can surface -- and without a test it is a check nothing proves.
func TestASequenceEndingInsideItsLongTermPicturesIsRefused(t *testing.T) {
	u := set{subLayers: 1, chroma: 1, width: 320, height: 240,
		stRPS: [][2][]refGap{{{{1, true}}, nil}}, longTerm: 4}.build()
	whole := len(u.Payload)
	if _, err := ParseSPS(u); err != nil {
		t.Fatalf("the whole fixture stopped parsing: %v", err)
	}
	for _, cut := range []int{2, 3, 4} {
		short := Unit{Type: UnitSPS, Payload: u.Payload[:whole-cut]}
		if _, err := ParseSPS(short); err == nil {
			t.Errorf("cut %d bytes short: accepted anyway", cut)
		}
	}
}
