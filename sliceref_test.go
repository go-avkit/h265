// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// sliceRefs says what a test wants a slice segment to state about its
// references.
type sliceRefs struct {
	named    bool // the short-term set is named out of the sequence's
	index    uint32
	inline   [2][]refGap // stated outright, when not named
	predict  bool        // stated as a difference from the sequence's last set
	ltFromS  int         // long-term entries taken from the sequence's list
	ltStated []uint32    // long-term entries stated here, by their low bits
	ltMSB    bool        // each entry also states its high bits
}

// sliceRefUnit builds a first slice segment carrying a picture order count and
// then the references above.
func sliceRefUnit(t *testing.T, sps SPS, pps PPS, lsb uint32, refs sliceRefs) Unit {
	t.Helper()
	var w bitbuf
	w.bit(1) // first_slice_segment_in_pic_flag
	w.ue(0)  // slice_pic_parameter_set_id
	for i := uint8(0); i < pps.ExtraSliceHeaderBits; i++ {
		w.bit(0)
	}
	w.ue(uint32(SliceP))
	if pps.OutputFlagPresent {
		w.bit(1)
	}
	if sps.SeparatePlanes {
		w.bits(0, 2)
	}
	w.bits(lsb, int(sps.Log2MaxPOCLSB))

	if refs.named {
		w.bit(1)
		if n := ceilLog2(len(sps.ShortTermRefPicSets)); n > 0 {
			w.bits(refs.index, n)
		}
	} else {
		w.bit(0)
		if refs.predict {
			w.bit(1) // inter_ref_pic_set_prediction_flag
			w.ue(0)  // delta_idx_minus1: the sequence's last set
			w.bit(0) // delta_rps_sign
			w.ue(0)  // abs_delta_rps_minus1
			from := sps.ShortTermRefPicSets[len(sps.ShortTermRefPicSets)-1]
			for range from.Count() + 1 {
				w.bit(1) // used_by_curr_pic_flag
			}
		} else {
			w.bit(0) // inter_ref_pic_set_prediction_flag
			w.ue(uint32(len(refs.inline[0])))
			w.ue(uint32(len(refs.inline[1])))
			for _, e := range refs.inline[0] {
				w.ue(e.gap - 1)
				w.bit(boolBit(e.used))
			}
			for _, e := range refs.inline[1] {
				w.ue(e.gap - 1)
				w.bit(boolBit(e.used))
			}
		}
	}

	if sps.LongTermRefPicsPresent {
		if len(sps.LongTermRefPics) > 0 {
			w.ue(uint32(refs.ltFromS))
		}
		w.ue(uint32(len(refs.ltStated)))
		for i := 0; i < refs.ltFromS; i++ {
			if n := ceilLog2(len(sps.LongTermRefPics)); n > 0 {
				w.bits(uint32(i), n)
			}
			w.bit(boolBit(refs.ltMSB))
			if refs.ltMSB {
				w.ue(1)
			}
		}
		for _, lsb := range refs.ltStated {
			w.bits(lsb, int(sps.Log2MaxPOCLSB))
			w.bit(1) // used_by_curr_pic_lt_flag
			w.bit(boolBit(refs.ltMSB))
			if refs.ltMSB {
				w.ue(2)
			}
		}
	}
	w.bit(1)
	for w.n != 0 {
		w.bit(0)
	}
	return Unit{Type: UnitTrailR, Payload: w.b}
}

func spsWithSets(sets ...[2][]refGap) SPS {
	s := SPS{Log2MaxPOCLSB: 8}
	for _, g := range sets {
		var rps ShortTermRPS
		prev := int32(0)
		for _, e := range g[0] {
			prev -= int32(e.gap)
			rps.Before = append(rps.Before, RefPic{prev, e.used})
		}
		prev = 0
		for _, e := range g[1] {
			prev += int32(e.gap)
			rps.After = append(rps.After, RefPic{prev, e.used})
		}
		s.ShortTermRefPicSets = append(s.ShortTermRefPicSets, rps)
	}
	return s
}

// A slice usually NAMES one of the sequence's sets rather than restating it,
// which is the whole reason a sequence carries them.
func TestASliceNamesOneOfTheSequencesSets(t *testing.T) {
	sps := spsWithSets(
		[2][]refGap{{{1, true}}, nil},
		[2][]refGap{{{2, true}, {1, true}}, {{1, true}}},
		[2][]refGap{{{4, true}}, nil},
	)
	for idx := uint32(0); idx < 3; idx++ {
		got, err := ReferencesOf(sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{named: true, index: idx}), sps, PPS{})
		if err != nil {
			t.Fatalf("set %d: %v", idx, err)
		}
		if !got.NamedSet || got.SetIndex != int(idx) {
			t.Errorf("set %d: NamedSet=%v index=%d", idx, got.NamedSet, got.SetIndex)
		}
		want := sps.ShortTermRefPicSets[idx]
		if !sameInts(deltas(got.ShortTerm.Before), deltas(want.Before)) {
			t.Errorf("set %d: before = %v; want %v",
				idx, deltas(got.ShortTerm.Before), deltas(want.Before))
		}
	}
}

// ⛔ The index is as wide as the number of sets needs, and ONE set means no
// index at all. A reader that always read bits would take the next field as the
// index and misread everything after it.
func TestTheIndexIsOnlySentWhenThereIsAChoice(t *testing.T) {
	one := spsWithSets([2][]refGap{{{3, true}}, nil})
	got, err := ReferencesOf(sliceRefUnit(t, one, PPS{}, 7, sliceRefs{named: true}), one, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int32{-3}; !sameInts(deltas(got.ShortTerm.Before), want) {
		t.Errorf("before = %v; want %v -- an index was read where none was sent",
			deltas(got.ShortTerm.Before), want)
	}
}

// A slice may state a set of its own instead, and that set may itself be a
// difference from one of the sequence's -- counted back from the END of them,
// where a sequence's own set counts back from itself.
func TestASliceStatesASetOfItsOwn(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}, {1, true}}, {{1, true}}})

	t.Run("outright", func(t *testing.T) {
		got, err := ReferencesOf(sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{
			inline: [2][]refGap{{{5, true}}, {{2, false}}},
		}), sps, PPS{})
		if err != nil {
			t.Fatal(err)
		}
		if got.NamedSet {
			t.Error("a set stated outright was reported as named")
		}
		if want := []int32{-5}; !sameInts(deltas(got.ShortTerm.Before), want) {
			t.Errorf("before = %v; want %v", deltas(got.ShortTerm.Before), want)
		}
		if want := []int32{2}; !sameInts(deltas(got.ShortTerm.After), want) {
			t.Errorf("after = %v; want %v", deltas(got.ShortTerm.After), want)
		}
	})

	t.Run("as a difference from the sequence's last set", func(t *testing.T) {
		got, err := ReferencesOf(sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{predict: true}), sps, PPS{})
		if err != nil {
			t.Fatal(err)
		}
		// From {-1,-2} and {1} shifted by +1: -1 becomes 0 and is dropped, -2
		// becomes -1, 1 becomes 2, and the set predicted from sits at +1.
		if want := []int32{-1}; !sameInts(deltas(got.ShortTerm.Before), want) {
			t.Errorf("before = %v; want %v", deltas(got.ShortTerm.Before), want)
		}
		if want := []int32{1, 2}; !sameInts(deltas(got.ShortTerm.After), want) {
			t.Errorf("after = %v; want %v", deltas(got.ShortTerm.After), want)
		}
	})
}

// Long-term pictures come from two places at once: some named out of the
// sequence's list, some stated by the slice, and they are counted as one run.
func TestLongTermPicturesComeFromBothPlaces(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}}, nil})
	sps.LongTermRefPicsPresent = true
	sps.LongTermRefPics = []LongTermRefPic{{POCLSB: 10, Used: true}, {POCLSB: 20, Used: false}}

	got, err := ReferencesOf(sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{
		named: true, ltFromS: 2, ltStated: []uint32{33, 44},
	}), sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LongTerm) != 4 {
		t.Fatalf("read %d long-term pictures, want 4", len(got.LongTerm))
	}
	for i, want := range []uint32{10, 20, 33, 44} {
		if got.LongTerm[i].POCLSB != want {
			t.Errorf("long-term %d = %d; want %d", i, got.LongTerm[i].POCLSB, want)
		}
	}
	if !got.LongTerm[0].FromSPS || got.LongTerm[2].FromSPS {
		t.Error("the two sources were not told apart")
	}
	// The sequence's own entries keep the sequence's used flags.
	if !got.LongTerm[0].Used || got.LongTerm[1].Used {
		t.Errorf("used flags = %v, %v; want true, false", got.LongTerm[0].Used, got.LongTerm[1].Used)
	}
}

// The high bits are stated only when the low ones would be ambiguous, and the
// field that says so comes after every entry either way.
func TestTheHighBitsAreStatedWhenTheLowOnesWouldNotDo(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}}, nil})
	sps.LongTermRefPicsPresent = true
	got, err := ReferencesOf(sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{
		named: true, ltStated: []uint32{5}, ltMSB: true,
	}), sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.LongTerm) != 1 {
		t.Fatalf("read %d long-term pictures, want one", len(got.LongTerm))
	}
	if !got.LongTerm[0].MSBPresent || got.LongTerm[0].DeltaMSBCycle != 2 {
		t.Errorf("long-term = %+v; want the high bits stated as two", got.LongTerm[0])
	}
}

// An IDR begins a sequence: nothing before it is available and nothing may
// reach back. That is an empty answer, not an error.
func TestAnIDRNeedsNothing(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}}, nil})
	u := sliceRefUnit(t, sps, PPS{}, 0, sliceRefs{named: true})
	u.Type = UnitIDRWRADL
	got, err := ReferencesOf(u, sps, PPS{})
	if err != nil {
		t.Fatalf("an IDR was refused: %v", err)
	}
	if got.ShortTerm.Count() != 0 || len(got.LongTerm) != 0 {
		t.Errorf("an IDR named %d pictures", got.ShortTerm.Count()+len(got.LongTerm))
	}
}

func TestReferencesOfRefusesWhatItCannotRead(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}}, nil})
	if _, err := ReferencesOf(Unit{Type: UnitSPS}, sps, PPS{}); !errors.Is(err, ErrNotSlice) {
		t.Error("a parameter set was read as a slice")
	}
	// A slice naming a set where the sequence carries none.
	empty := SPS{Log2MaxPOCLSB: 8}
	u := sliceRefUnit(t, empty, PPS{}, 7, sliceRefs{named: true})
	if _, err := ReferencesOf(u, empty, PPS{}); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a slice naming a set from an empty sequence: %v", err)
	}
	// A later segment states no set of its own.
	u2 := sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{named: true})
	u2.Payload[0] &^= 0x80
	if _, err := ReferencesOf(u2, sps, PPS{}); !errors.Is(err, ErrNoOrder) {
		t.Errorf("a later segment was read: %v", err)
	}
	// One that stops before its references.
	u3 := sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{named: true})
	if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: u3.Payload[:1]}, sps, PPS{}); err == nil {
		t.Error("a slice that ends before its references was read")
	}
}

// Every refusal below is a way a slice can fail to name what it needs, and each
// one read wrongly would hand a decoder a picture list it cannot use.
func TestReferencesOfRefusesEveryWayItCan(t *testing.T) {
	// A wide order count, so a truncated payload really does run out rather
	// than reading the padding of its last byte as zeros.
	// Three sets, so the index is two bits wide, and three long-term pictures,
	// so that index is two bits too: with one of each the indices vanish and
	// the fields that read them are never reached.
	sps := spsWithSets(
		[2][]refGap{{{1, true}}, nil},
		[2][]refGap{{{2, true}}, nil},
		[2][]refGap{{{3, true}}, nil},
	)
	sps.Log2MaxPOCLSB = 16
	sps.LongTermRefPicsPresent = true
	sps.LongTermRefPics = []LongTermRefPic{{POCLSB: 1, Used: true}, {POCLSB: 2}, {POCLSB: 3}}

	whole := sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{
		named: true, index: 2, ltFromS: 3, ltStated: []uint32{9}, ltMSB: true,
	})
	if _, err := ReferencesOf(whole, sps, PPS{}); err != nil {
		t.Fatalf("the whole slice stopped parsing: %v", err)
	}
	// Cut at every length and check nothing short is ever accepted: each cut
	// lands in a different field, which is the point.
	for n := 1; n < len(whole.Payload); n++ {
		short := Unit{Type: UnitTrailR, Payload: whole.Payload[:n]}
		if _, err := ReferencesOf(short, sps, PPS{}); err == nil {
			t.Errorf("%d of %d bytes: accepted", n, len(whole.Payload))
		}
	}
}

// An index past what the sequence carries names nothing.
func TestAnIndexPastWhatTheSequenceCarriesIsRefused(t *testing.T) {
	// Three sets, so the index is two bits wide and can state a fourth.
	sps := spsWithSets(
		[2][]refGap{{{1, true}}, nil},
		[2][]refGap{{{2, true}}, nil},
		[2][]refGap{{{3, true}}, nil},
	)
	u := sliceRefUnit(t, sps, PPS{}, 7, sliceRefs{named: true, index: 3})
	if _, err := ReferencesOf(u, sps, PPS{}); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("set three of three was accepted: %v", err)
	}

	// The same for a long-term picture.
	lt := spsWithSets([2][]refGap{{{1, true}}, nil})
	lt.LongTermRefPicsPresent = true
	lt.LongTermRefPics = []LongTermRefPic{{POCLSB: 1}, {POCLSB: 2}, {POCLSB: 3}}
	var w bitbuf
	w.bit(1)
	w.ue(0)
	w.ue(uint32(SliceP))
	w.bits(7, int(lt.Log2MaxPOCLSB))
	w.bit(1) // the set is named
	w.ue(1)  // one long-term picture from the sequence
	w.ue(0)  // none stated here
	w.bits(3, 2)
	w.bit(0)
	w.bit(1)
	for w.n != 0 {
		w.bit(0)
	}
	if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: w.b}, lt, PPS{}); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("long-term picture three of three was accepted: %v", err)
	}
}

// A slice cannot name more of the sequence's long-term pictures than it has,
// nor more pictures in total than a set may hold.
func TestALongTermCountPastWhatIsThereIsRefused(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}}, nil})
	sps.LongTermRefPicsPresent = true
	sps.LongTermRefPics = []LongTermRefPic{{POCLSB: 1}}
	for _, c := range []struct {
		name          string
		fromSPS, here uint32
	}{
		{"more of the sequence's than it has", 2, 0},
		{"more in total than a set may hold", 1, 100},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w bitbuf
			w.bit(1)
			w.ue(0)
			w.ue(uint32(SliceP))
			w.bits(7, int(sps.Log2MaxPOCLSB))
			w.bit(1) // the set is named
			w.ue(c.fromSPS)
			w.ue(c.here)
			w.bit(1)
			for w.n != 0 {
				w.bit(0)
			}
			if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: w.b}, sps, PPS{}); !errors.Is(err, ErrSliceHeader) {
				t.Errorf("accepted: %v", err)
			}
		})
	}
}

// A slice's own set may be a difference from one of the sequence's, counted
// back from the end of them. Counting back past the first names nothing.
func TestASliceCannotPredictFromASetThatIsNotThere(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}}, nil})
	var w bitbuf
	w.bit(1)
	w.ue(0)
	w.ue(uint32(SliceP))
	w.bits(7, int(sps.Log2MaxPOCLSB))
	w.bit(0) // the set is stated here
	w.bit(1) // and predicted
	w.ue(5)  // delta_idx_minus1: six back, where there is one
	w.bit(0)
	w.ue(0)
	w.bit(1)
	for w.n != 0 {
		w.bit(0)
	}
	if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: w.b}, sps, PPS{}); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a set predicted from nothing was accepted: %v", err)
	}
}

// A payload that ends exactly where the set index begins.
//
// ⛔ Cutting a slice at byte boundaries does not reach this: the index is one or
// two bits, so a cut that removes it almost always leaves padding the reader
// consumes as zeros. The widths here are chosen so the header is a whole number
// of bytes with the index just past the end -- 1 + 1 + 3 + 10 + 1 = 16 bits.
func TestASliceEndingExactlyWhereItsIndexBeginsIsRefused(t *testing.T) {
	sps := spsWithSets(
		[2][]refGap{{{1, true}}, nil},
		[2][]refGap{{{2, true}}, nil},
		[2][]refGap{{{3, true}}, nil},
	)
	sps.Log2MaxPOCLSB = 10
	var w bitbuf
	w.bit(1)             // first_slice_segment_in_pic_flag
	w.ue(0)              // slice_pic_parameter_set_id
	w.ue(uint32(SliceP)) // slice_type
	w.bits(7, 10)        // slice_pic_order_cnt_lsb
	w.bit(1)             // short_term_ref_pic_set_sps_flag
	if w.n != 0 {
		t.Fatalf("the header is %d bits into a byte; this test needs it whole", 8-w.n)
	}
	if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: w.b}, sps, PPS{}); err == nil {
		t.Error("a slice that ends where its index begins was accepted")
	}
}
