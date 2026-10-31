// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"os"
	"reflect"
	"testing"
)

// ⛔ A real x265 stream, read end to end: the sequence carries no short-term
// set (num_short_term_ref_pic_sets = 0, x265's default), so every slice states
// its own -- and with stRpsIdx zero, 7.3.7 sends no
// inter_ref_pic_set_prediction_flag. The reader used to read one anyway, and
// every field after it was misread: the first P slice came out predicting from
// a picture AFTER it, and two later slices were refused outright.
//
// The expected sets are ffmpeg's, read with
//
//	ffmpeg -i hevc.265 -c copy -bsf:v trace_headers -f null -
//
// (num_negative_pics / num_positive_pics / delta_poc_s*_minus1 /
// used_by_curr_pic_s*_flag of the first three non-IDR slices).
func TestX265StreamWithoutSequenceSets(t *testing.T) {
	data, err := os.ReadFile("testdata/fuzzseed/hevc.265")
	if err != nil {
		t.Fatal(err)
	}
	units, err := SplitAnnexB(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []ShortTermRPS{
		{Before: []RefPic{{-3, true}}},                                        // POC 3, P
		{Before: []RefPic{{-2, true}}, After: []RefPic{{1, true}}},            // POC 2, B
		{Before: []RefPic{{-1, true}}, After: []RefPic{{1, true}, {2, true}}}, // POC 1, B
	}
	var sps SPS
	var pps PPS
	var got []ShortTermRPS
	for _, u := range units {
		switch {
		case u.Type == UnitSPS:
			if sps, err = ParseSPS(u); err != nil {
				t.Fatalf("SPS: %v", err)
			}
		case u.Type == UnitPPS:
			if pps, err = ParsePPS(u); err != nil {
				t.Fatalf("PPS: %v", err)
			}
		case u.Type.IsSliceSegment():
			refs, err := ReferencesOf(u, sps, pps)
			if err != nil {
				t.Fatalf("slice of type %d: %v", u.Type, err)
			}
			if !u.Type.IsIDR() {
				got = append(got, refs.ShortTerm)
			}
		}
	}
	if len(sps.ShortTermRefPicSets) != 0 {
		t.Fatalf("the sequence carries %d sets; this test is about one that carries none",
			len(sps.ShortTermRefPicSets))
	}
	if len(got) < len(want) {
		t.Fatalf("%d non-IDR slices, want at least %d", len(got), len(want))
	}
	for i, w := range want {
		if !reflect.DeepEqual(got[i], w) {
			t.Errorf("slice %d: set %+v, want %+v", i+1, got[i], w)
		}
	}
}
