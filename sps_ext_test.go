// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

func base() set { return set{subLayers: 1, chroma: 1, width: 64, height: 64} }

func TestSPSBlockGeometry(t *testing.T) {
	s := base()
	s.sizes = []uint32{0, 3, 0, 3, 2, 3} // 8..64 coding, 4..32 transform, depths 2 and 3
	s.pcm = true
	got, err := ParseSPS(s.build())
	if err != nil {
		t.Fatal(err)
	}
	if got.Log2MinCbSize != 3 || got.Log2CtbSize != 6 || got.Log2MinTbSize != 2 || got.Log2MaxTbSize != 5 ||
		got.MaxTransformHierarchyDepthInter != 2 || got.MaxTransformHierarchyDepthIntra != 3 {
		t.Errorf("geometry %+v", got)
	}
	p := got.PCM
	if !p.Enabled || p.BitDepthLuma != 8 || p.BitDepthChroma != 8 || p.Log2MinCbSize != 3 || p.Log2MaxCbSize != 4 || !p.LoopFilterDisabled {
		t.Errorf("PCM %+v", p)
	}
}

// TestSPSRefusalsOfTheReview: each of these was accepted before, and each must
// now fail by assertion with ErrUnsupportedSPS.
func TestSPSRefusalsOfTheReview(t *testing.T) {
	for name, s := range map[string]set{
		// 7.4.3.2.1: SubWidthC * ( left + right ) < width. Width 64 with a window
		// of 40+40 used to give a width of 4294967200.
		"window wider than the picture":        {chroma: 1, width: 64, height: 64, window: [4]uint32{40, 40, 0, 0}},
		"window taller than the picture":       {chroma: 1, width: 64, height: 64, window: [4]uint32{0, 0, 17, 16}},
		"window exactly the picture, 4:2:0":    {chroma: 1, width: 64, height: 64, window: [4]uint32{16, 16, 0, 0}},
		"window exactly the picture, 4:4:4":    {chroma: 3, width: 64, height: 64, window: [4]uint32{32, 32, 0, 0}},
		"window exactly the picture, 4:2:2 h":  {chroma: 2, width: 64, height: 64, window: [4]uint32{0, 0, 32, 32}},
		"window offsets that wrap in 32 bits":  {chroma: 1, width: 64, height: 64, window: [4]uint32{1 << 31, 1 << 31, 0, 0}},
		"window offsets that wrap, the height": {chroma: 1, width: 64, height: 64, window: [4]uint32{0, 0, 1 << 31, 1 << 31}},
		"sps id 16":                            {chroma: 1, width: 64, height: 64, spsID: 16},
		"chroma format wrapping to 1":          {chroma: 257, width: 64, height: 64},
		"33 long-term pictures in the set":     {chroma: 1, width: 64, height: 64, longTerm: 33},
	} {
		s.subLayers = 1
		if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
			t.Errorf("%s: err = %v, want ErrUnsupportedSPS", name, err)
		}
	}
	// One short of each boundary parses, and the size is right.
	for name, s := range map[string]set{
		"4:2:0 window":  {chroma: 1, width: 64, height: 64, window: [4]uint32{15, 16, 0, 0}},
		"4:4:4 window":  {chroma: 3, width: 64, height: 64, window: [4]uint32{31, 32, 0, 0}},
		"sps id 15":     {chroma: 1, width: 64, height: 64, spsID: 15},
		"32 long terms": {chroma: 1, width: 64, height: 64, longTerm: 32},
	} {
		s.subLayers = 1
		if _, err := ParseSPS(s.build()); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if sps, _ := ParseSPS(set{subLayers: 1, chroma: 1, width: 64, height: 64, window: [4]uint32{15, 16, 0, 0}}.build()); sps.Width != 2 {
		t.Errorf("Width = %d, want 2", sps.Width)
	}
	// sps_max_sub_layers_minus1 is 0 to 6.
	if _, err := ParseSPS(set{subLayers: 8, chroma: 1, width: 64, height: 64}.build()); !errors.Is(err, ErrUnsupportedSPS) {
		t.Errorf("sps_max_sub_layers_minus1 of 7: %v", err)
	}
	if _, err := ParseSPS(set{subLayers: 7, chroma: 1, width: 64, height: 64}.build()); err != nil {
		t.Errorf("sps_max_sub_layers_minus1 of 6: %v", err)
	}
	// An Annex F set (nuh_layer_id above 0).
	u := base().build()
	u.LayerID = 1
	if _, err := ParseSPS(u); !errors.Is(err, ErrUnsupportedSPS) {
		t.Errorf("nuh_layer_id 1: %v", err)
	}
}

func TestSPSGeometryRefusals(t *testing.T) {
	for name, s := range map[string]set{
		"min coding block of 128":         {sizes: []uint32{4, 0, 0, 1, 0, 0}},
		"coding tree past 64":             {sizes: []uint32{1, 3, 0, 1, 0, 0}},
		"coding tree below 16":            {sizes: []uint32{0, 0, 0, 0, 0, 0}},
		"transform size field past range": {sizes: []uint32{0, 1, 4, 0, 0, 0}},
		"transform past 32":               {sizes: []uint32{0, 3, 0, 4, 0, 0}},
		"min transform not below min cb":  {sizes: []uint32{0, 1, 1, 0, 0, 0}},
		"max transform past the tree":     {sizes: []uint32{0, 1, 0, 3, 0, 0}},
		"inter depth past the tree":       {sizes: []uint32{0, 1, 0, 1, 3, 0}},
		"intra depth past the tree":       {sizes: []uint32{0, 1, 0, 1, 0, 3}},
		"PCM minimum past 5":              {pcm: true, pcmFields: []uint32{7, 7, 3, 0}},
		"PCM maximum past the tree":       {pcm: true, pcmFields: []uint32{7, 7, 1, 1}},
		"PCM luma deeper than coded":      {pcm: true, pcmFields: []uint32{8, 7, 0, 1}},
		"PCM chroma deeper than coded":    {pcm: true, pcmFields: []uint32{7, 8, 0, 1}},
	} {
		s.subLayers, s.chroma, s.width, s.height = 1, 1, 64, 64
		if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
			t.Errorf("%s: err = %v, want ErrUnsupportedSPS", name, err)
		}
	}
}

func rangeExt(flags ...bool) func(*writer) {
	return func(w *writer) {
		w.bits(0b1000, 4) // sps_range_extension_flag, multilayer, 3d, scc
		w.bits(0, 4)      // sps_extension_4bits
		for _, f := range flags {
			w.flag(f)
		}
	}
}

func TestSPSRangeExtension(t *testing.T) {
	s := base()
	s.ext = rangeExt(true, false, true, false, true, false, true, false, false)
	got, err := ParseSPS(s.build())
	if err != nil {
		t.Fatal(err)
	}
	want := RangeExtension{
		TransformSkipRotation: true, ImplicitRDPCM: true, ExtendedPrecisionProcessing: true,
		HighPrecisionOffsets: true,
	}
	if got.Range != want {
		t.Errorf("Range = %+v, want %+v", got.Range, want)
	}
	// cabac_bypass_alignment_enabled_flag is read, not refused (9.3.4.3.6).
	s.ext = rangeExt(false, false, false, false, false, false, false, false, true)
	if got, err := ParseSPS(s.build()); err != nil || got.Range != (RangeExtension{CabacBypassAlignment: true}) {
		t.Errorf("bypass alignment: Range = %+v, err = %v", got.Range, err)
	}
	// Every other flag in its own position.
	for i := 0; i < 9; i++ {
		flags := make([]bool, 9)
		flags[i] = true
		s.ext = rangeExt(flags...)
		got, err := ParseSPS(s.build())
		if err != nil {
			t.Fatal(err)
		}
		r := got.Range
		set := []bool{r.TransformSkipRotation, r.TransformSkipContext, r.ImplicitRDPCM, r.ExplicitRDPCM,
			r.ExtendedPrecisionProcessing, r.IntraSmoothingDisabled, r.HighPrecisionOffsets,
			r.PersistentRiceAdaptation, r.CabacBypassAlignment}
		for j, v := range set {
			if v != (j == i) {
				t.Errorf("flag %d set: field %d is %v", i, j, v)
			}
		}
	}
	// Without the extension, everything is false.
	if got, _ := ParseSPS(base().build()); got.Range != (RangeExtension{}) {
		t.Errorf("no extension: %+v", got.Range)
	}
}

func TestSPSOtherExtensions(t *testing.T) {
	s := base()
	// The multilayer extension is one bit; sps_extension_4bits allows trailing data.
	s.ext = func(w *writer) {
		w.bits(0b0100, 4)
		w.bits(0b0001, 4)
		w.bit(1)        // inter_view_mv_vert_constraint_flag
		w.bits(0x5A, 8) // sps_extension_data_flag, ignored
	}
	if _, err := ParseSPS(s.build()); err != nil {
		t.Errorf("multilayer extension with data: %v", err)
	}
	for name, flags := range map[string]uint32{"3D": 0b0010, "screen content": 0b0001} {
		s.ext = func(w *writer) { w.bits(flags, 4); w.bits(0, 4) }
		if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
			t.Errorf("%s extension: %v", name, err)
		}
	}
	s.ext = func(w *writer) { w.bit(1) } // the extension flags run out
	if _, err := ParseSPS(s.build()); err == nil {
		t.Error("truncated extension flags accepted")
	}
	// A field read wrongly leaves bits behind: refused, not tolerated.
	u := base().build()
	u.Payload = append(u.Payload, 0xFF)
	if _, err := ParseSPS(u); !errors.Is(err, ErrUnsupportedSPS) {
		t.Errorf("leftover bits: %v", err)
	}
	// No stop bit: a payload that ends exactly on its last field. The width
	// moves the last field across a byte boundary until the stop bit stands
	// alone in the last byte, which is then cut.
	found := false
	for width := uint32(1); width < 400 && !found; width++ {
		s := base()
		s.width = width
		u = s.build()
		if u.Payload[len(u.Payload)-1] == 0x80 {
			found = true
			u.Payload = u.Payload[:len(u.Payload)-1]
			if _, err := ParseSPS(u); !errors.Is(err, ErrUnsupportedSPS) {
				t.Errorf("no stop bit: %v", err)
			}
		}
	}
	if !found {
		t.Fatal("no width puts the stop bit alone in a byte")
	}
	// Extension flags with fewer than eight bits behind them.
	if _, err := (&SPS{}).readExtensions(newSticky([]byte{0x80})); err == nil {
		t.Error("extension flags past the end accepted")
	}
}

// fullVUI writes every optional part of vui_parameters( ), the hrd_parameters
// with both kinds of sub-layer parameters and sub-picture parameters.
func fullVUI(sar bool, hrd func(*writer)) func(*writer) {
	return func(w *writer) {
		w.bit(1) // aspect_ratio_info_present_flag
		if sar {
			w.bits(255, 8)
			w.bits(4, 16)
			w.bits(3, 16)
		} else {
			w.bits(1, 8)
		}
		w.bit(1) // overscan_info_present_flag
		w.bit(1) // overscan_appropriate_flag
		w.bit(1) // video_signal_type_present_flag
		w.bits(5, 3)
		w.bit(0)
		w.bit(1) // colour_description_present_flag
		w.bits(0x010203, 24)
		w.bit(1) // chroma_loc_info_present_flag
		w.ue(1)
		w.ue(2)
		w.bits(0b010, 3) // neutral_chroma_indication_flag, field_seq_flag, frame_field_info_present_flag
		w.bit(1)         // default_display_window_flag
		for i := uint32(0); i < 4; i++ {
			w.ue(i)
		}
		w.bit(1) // vui_timing_info_present_flag
		// ⛔ Not 1001 and 60000: a zero byte pair followed by 3 is an emulation
		// prevention byte, and this writer does not escape.
		w.bits(0x01020304, 32)
		w.bits(0x0A0B0C0D, 32)
		w.bit(1) // vui_poc_proportional_to_timing_flag
		w.ue(0)
		if hrd != nil {
			w.bit(1)
			hrd(w)
		} else {
			w.bit(0)
		}
		w.bit(1) // bitstream_restriction_flag
		w.bits(0b101, 3)
		for i := uint32(0); i < 5; i++ {
			w.ue(i)
		}
	}
}

func TestSPSVUIIsConsumedExactly(t *testing.T) {
	// A flag after the VUI proves the VUI ended where it should: the range
	// extension is read from whatever bit the VUI leaves, so a misread VUI
	// shows as a wrong flag or a refusal.
	hrds := map[string]func(*writer){
		"none": nil,
		"nal and vcl, sub-picture parameters, three sub-layers": func(w *writer) {
			w.bit(1) // nal_hrd_parameters_present_flag
			w.bit(1) // vcl_hrd_parameters_present_flag
			w.bit(1) // sub_pic_hrd_params_present_flag
			w.bits(0, 8+5+1+5)
			w.bits(0, 4+4)
			w.bits(0, 4)
			w.bits(0, 15)
			for i := 0; i < 3; i++ {
				w.bit(uint32(i & 1)) // fixed_pic_rate_general_flag
				if i&1 == 0 {
					w.bit(uint32(i >> 1 & 1)) // fixed_pic_rate_within_cvs_flag
				}
				fixedCVS := i&1 == 1 || i>>1&1 == 1
				if fixedCVS {
					w.ue(3) // elemental_duration_in_tc_minus1
				} else {
					w.bit(uint32(i & 1)) // low_delay_hrd_flag, 0 here: cpb_cnt_minus1 follows
				}
				w.ue(1)                  // cpb_cnt_minus1: two coded picture buffers
				for k := 0; k < 2; k++ { // nal, vcl
					for j := 0; j < 2; j++ {
						w.ue(5)
						w.ue(6)
						w.ue(7) // cpb_size_du_value_minus1
						w.ue(8) // bit_rate_du_value_minus1
						w.bit(1)
					}
				}
			}
		},
		"neither, a low-delay sub-layer": func(w *writer) {
			w.bit(0)
			w.bit(0)
			w.bit(0) // fixed_pic_rate_general_flag
			w.bit(0) // fixed_pic_rate_within_cvs_flag
			w.bit(1) // low_delay_hrd_flag: no cpb_cnt_minus1
		},
	}
	for name, hrd := range hrds {
		for _, sar := range []bool{false, true} {
			s := base()
			if name == "nal and vcl, sub-picture parameters, three sub-layers" {
				s.subLayers = 3
			}
			s.vui = fullVUI(sar, hrd)
			s.ext = rangeExt(false, false, false, false, false, false, false, true, false)
			got, err := ParseSPS(s.build())
			if err != nil {
				t.Fatalf("%s, sar %v: %v", name, sar, err)
			}
			if !got.Range.PersistentRiceAdaptation {
				t.Errorf("%s, sar %v: the extension after the VUI was misread: %+v", name, sar, got.Range)
			}
		}
	}
	// A minimal VUI: nothing present.
	s := base()
	s.vui = func(w *writer) { w.zeros(10) }
	s.ext = rangeExt(true, false, false, false, false, false, false, false, false)
	if got, err := ParseSPS(s.build()); err != nil || !got.Range.TransformSkipRotation {
		t.Errorf("empty VUI: %v, %+v", err, got.Range)
	}
	// More coded picture buffers than a sequence may have.
	s.vui = func(w *writer) {
		w.zeros(4) // aspect, overscan, signal type, chroma location
		w.zeros(3) // neutral_chroma_indication_flag, field_seq_flag, frame_field_info_present_flag
		w.bit(0)   // default_display_window_flag
		w.bit(1)   // vui_timing_info_present_flag
		w.bits(0, 64)
		w.bit(0)
		w.bit(1) // vui_hrd_parameters_present_flag
		w.bit(1) // nal_hrd_parameters_present_flag
		w.bit(0)
		w.bit(0) // sub_pic_hrd_params_present_flag
		w.bits(0, 8+15)
		w.bit(1) // fixed_pic_rate_general_flag
		w.ue(0)
		w.ue(32) // cpb_cnt_minus1: 33
	}
	if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
		t.Errorf("33 coded picture buffers: %v", err)
	}
}
