// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

func deltas(e []RefPic) []int32 {
	out := make([]int32, len(e))
	for i, p := range e {
		out[i] = p.DeltaPOC
	}
	return out
}

func sameInts(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A set states GAPS, not positions: each entry is so much further from the one
// before. A reader taking them as positions gets -1, -1, -1 where the stream
// said -1, -2, -3.
func TestAStatedSetAccumulatesItsGaps(t *testing.T) {
	u := set{
		subLayers: 1, chroma: 1, width: 320, height: 240,
		stRPS: [][2][]refGap{{
			{{1, true}, {1, true}, {2, false}}, // before: -1, -2, -4
			{{3, true}, {1, false}},            // after: 3, 4
		}},
	}.build()
	sps, err := ParseSPS(u)
	if err != nil {
		t.Fatalf("ParseSPS: %v", err)
	}
	if len(sps.ShortTermRefPicSets) != 1 {
		t.Fatalf("read %d sets, want one", len(sps.ShortTermRefPicSets))
	}
	rps := sps.ShortTermRefPicSets[0]
	if want := []int32{-1, -2, -4}; !sameInts(deltas(rps.Before), want) {
		t.Errorf("before = %v; want %v", deltas(rps.Before), want)
	}
	if want := []int32{3, 4}; !sameInts(deltas(rps.After), want) {
		t.Errorf("after = %v; want %v", deltas(rps.After), want)
	}
	if rps.Count() != 5 {
		t.Errorf("Count = %d; want 5", rps.Count())
	}
	if used := []bool{true, true, false}; rps.Before[2].Used != used[2] || !rps.Before[0].Used {
		t.Errorf("the used flags did not follow their entries: %+v", rps.Before)
	}
}

// A set may be written as a DIFFERENCE from an earlier one. Shifting by +1
// moves every entry one step later, and an entry at -1 crosses zero: it leaves
// the half that holds pictures already seen.
func TestAPredictedSetShiftsTheSetItPredictsFrom(t *testing.T) {
	u := set{
		subLayers: 1, chroma: 1, width: 320, height: 240,
		stRPS: [][2][]refGap{{
			{{1, true}, {1, true}}, // before: -1, -2
			{{1, true}},            // after: 1
		}},
		predictLast: true,
	}.build()
	sps, err := ParseSPS(u)
	if err != nil {
		t.Fatalf("ParseSPS: %v", err)
	}
	if len(sps.ShortTermRefPicSets) != 2 {
		t.Fatalf("read %d sets, want two", len(sps.ShortTermRefPicSets))
	}
	got := sps.ShortTermRefPicSets[1]
	// From {-1, -2} and {1}, shifted by +1 and keeping everything, plus the
	// predicted-from picture itself at +1:
	//   -1 -> 0, which is neither before nor after and is dropped
	//   -2 -> -1
	//    1 -> 2
	//   the set it predicts from sits at +1
	if want := []int32{-1}; !sameInts(deltas(got.Before), want) {
		t.Errorf("before = %v; want %v", deltas(got.Before), want)
	}
	if want := []int32{1, 2}; !sameInts(deltas(got.After), want) {
		t.Errorf("after = %v; want %v", deltas(got.After), want)
	}
}

// Every set has to come back ordered, nearest first, however it was written.
// A later set may be a difference from this one, so disorder here would be
// inherited rather than noticed.
func TestBothHalvesComeBackOrderedNearestFirst(t *testing.T) {
	u := set{
		subLayers: 1, chroma: 1, width: 320, height: 240,
		stRPS: [][2][]refGap{{
			{{2, true}, {3, true}, {1, true}}, // before: -2, -5, -6
			{{1, true}, {4, true}},            // after: 1, 5
		}},
		predictLast: true,
	}.build()
	sps, err := ParseSPS(u)
	if err != nil {
		t.Fatal(err)
	}
	for i, rps := range sps.ShortTermRefPicSets {
		for j, e := range rps.Before {
			if e.DeltaPOC >= 0 {
				t.Errorf("set %d: before[%d] is %d, which is not before", i, j, e.DeltaPOC)
			}
			if j > 0 && e.DeltaPOC >= rps.Before[j-1].DeltaPOC {
				t.Errorf("set %d: before = %v, which is not nearest first", i, deltas(rps.Before))
			}
		}
		for j, e := range rps.After {
			if e.DeltaPOC <= 0 {
				t.Errorf("set %d: after[%d] is %d, which is not after", i, j, e.DeltaPOC)
			}
			if j > 0 && e.DeltaPOC <= rps.After[j-1].DeltaPOC {
				t.Errorf("set %d: after = %v, which is not nearest first", i, deltas(rps.After))
			}
		}
	}
}

// ⛔ The fields between the order-count width and the sets are CONDITIONAL and
// of variable length. If any of them is walked wrongly the sets are read from
// the wrong bits, and nothing in them would show it -- so each shape is parsed
// and the set read back out of it.
func TestTheSetsAreFoundPastEveryConditionalField(t *testing.T) {
	for _, c := range []struct {
		name string
		s    set
	}{
		{"nothing in between", set{subLayers: 1, chroma: 1, width: 320, height: 240}},
		{"a scaling list", set{subLayers: 1, chroma: 1, width: 320, height: 240, scalingList: true}},
		{"PCM fields", set{subLayers: 1, chroma: 1, width: 320, height: 240, pcm: true}},
		{"several sub-layers", set{subLayers: 3, chroma: 1, width: 320, height: 240}},
		{"all of them", set{subLayers: 2, chroma: 1, width: 320, height: 240,
			scalingList: true, pcm: true, longTerm: 2}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.s.stRPS = [][2][]refGap{{{{1, true}, {2, true}}, {{1, true}}}}
			sps, err := ParseSPS(c.s.build())
			if err != nil {
				t.Fatalf("ParseSPS: %v", err)
			}
			if len(sps.ShortTermRefPicSets) != 1 {
				t.Fatalf("read %d sets, want one: the walk landed on the wrong bits",
					len(sps.ShortTermRefPicSets))
			}
			rps := sps.ShortTermRefPicSets[0]
			if want := []int32{-1, -3}; !sameInts(deltas(rps.Before), want) {
				t.Errorf("before = %v; want %v", deltas(rps.Before), want)
			}
			if want := []int32{1}; !sameInts(deltas(rps.After), want) {
				t.Errorf("after = %v; want %v", deltas(rps.After), want)
			}
			if c.s.longTerm > 0 && len(sps.LongTermRefPics) != c.s.longTerm {
				t.Errorf("read %d long-term pictures, want %d", len(sps.LongTermRefPics), c.s.longTerm)
			}
		})
	}
}

// A sequence that offers long-term pictures names them by the low bits of their
// order count, which is what a slice header picks from.
func TestLongTermPicturesAreNamedByTheirLowBits(t *testing.T) {
	u := set{subLayers: 1, chroma: 1, width: 320, height: 240,
		stRPS: [][2][]refGap{{{{1, true}}, nil}}, longTerm: 3}.build()
	sps, err := ParseSPS(u)
	if err != nil {
		t.Fatal(err)
	}
	if len(sps.LongTermRefPics) != 3 {
		t.Fatalf("read %d long-term pictures, want 3", len(sps.LongTermRefPics))
	}
	for i, lt := range sps.LongTermRefPics {
		if lt.POCLSB != uint32(i) || !lt.Used {
			t.Errorf("long-term %d = %+v", i, lt)
		}
	}
}

// --- what it refuses --------------------------------------------------------
//
// These drive the readers with bytes rather than through a fixture builder: a
// builder that could write a set of two hundred pictures, or a gap of a
// million, would be a builder for streams that do not exist.

// bits writes fields in a straight line, for buffers these tests hand to the
// readers directly.
type bits struct {
	b []byte
	n uint8
}

func (w *bits) bit(v uint32) {
	if w.n == 0 {
		w.b = append(w.b, 0)
		w.n = 8
	}
	w.n--
	if v&1 == 1 {
		w.b[len(w.b)-1] |= 1 << w.n
	}
}

func (w *bits) ue(v uint32) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	for i := 0; i < n; i++ {
		w.bit(0)
	}
	for i := n; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

func TestAStatedSetRefusesWhatCannotBeOne(t *testing.T) {
	for _, c := range []struct {
		name  string
		write func(*bits)
	}{
		{"more pictures before than a set may hold", func(w *bits) { w.ue(64); w.ue(0) }},
		{"more pictures after than a set may hold", func(w *bits) { w.ue(0); w.ue(64) }},
		{"a gap past the largest the format states", func(w *bits) {
			w.ue(1)
			w.ue(0)
			w.ue(40000) // delta_poc_s0_minus1, so a gap of 40001
			w.bit(1)
		}},
		{"a gap past the largest, on the other side", func(w *bits) {
			w.ue(0)
			w.ue(1)
			w.ue(40000)
			w.bit(1)
		}},
		{"it ends inside the counts", func(w *bits) { w.ue(3) }},
		{"it ends inside the entries", func(w *bits) { w.ue(2); w.ue(0); w.ue(0); w.bit(1) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w bits
			c.write(&w)
			if _, err := parseShortTermRPSExplicit(newSticky(w.b)); err == nil {
				t.Error("accepted")
			}
		})
	}
}

func TestAPredictedSetRefusesWhatCannotBeOne(t *testing.T) {
	prior := []ShortTermRPS{{Before: []RefPic{{-1, true}}}}
	for _, c := range []struct {
		name  string
		write func(*bits)
	}{
		{"a difference past the largest the format states", func(w *bits) {
			w.bit(1)    // inter_ref_pic_set_prediction_flag
			w.bit(0)    // delta_rps_sign
			w.ue(40000) // abs_delta_rps_minus1
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w bits
			c.write(&w)
			if _, err := parseShortTermRPS(newSticky(w.b), 1, prior); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// A buffer that stops inside the flags.
//
// ⛔ The first try at this wrote one flag where three were wanted and was
// ACCEPTED: the last byte still held seven bits of padding, which the reader
// consumed as zeros. Running out needs a set whose flags outlast the bytes, so
// the referenced set is made large enough that they do.
func TestAPredictedSetEndingInsideItsFlagsIsRefused(t *testing.T) {
	var from ShortTermRPS
	for i := int32(1); i <= 16; i++ {
		from.Before = append(from.Before, RefPic{-i, true})
	}
	var w bits
	w.bit(1) // predicted
	w.bit(0) // delta_rps_sign
	w.ue(0)  // abs_delta_rps_minus1
	// Seventeen pairs are wanted and three flags are given; the padding of the
	// last byte cannot cover the rest.
	w.bit(0)
	w.bit(0)
	w.bit(0)
	if _, err := parseShortTermRPS(newSticky(w.b), 1, []ShortTermRPS{from}); err == nil {
		t.Error("a set whose flags ran past the bytes was accepted")
	}
}

// A predicted set can name more pictures than a set may hold, since it adds the
// picture it predicts from to everything it keeps.
func TestAPredictedSetRefusesGrowingPastTheLimit(t *testing.T) {
	var from ShortTermRPS
	for i := int32(1); i <= 16; i++ {
		from.Before = append(from.Before, RefPic{-i, true})
	}
	var w bits
	w.bit(1) // predicted
	w.bit(1) // delta_rps_sign: negative
	w.ue(0)  // abs_delta_rps_minus1, so -1: every entry stays before, and the
	// predicted-from picture joins them, making seventeen.
	for range from.Count() + 1 {
		w.bit(1)
	}
	if _, err := parseShortTermRPS(newSticky(w.b), 1, []ShortTermRPS{from}); err == nil {
		t.Error("a set of seventeen pictures before was accepted")
	}
}

func TestASequenceRefusesTooManySets(t *testing.T) {
	var w bits
	w.ue(65) // num_short_term_ref_pic_sets
	s := &SPS{Log2MaxPOCLSB: 8}
	if err := s.readShortTermRefPicSets(newSticky(w.b)); err == nil {
		t.Error("sixty-five sets were accepted")
	}
	// And a count that ends inside its own field.
	if err := s.readShortTermRefPicSets(newSticky(nil)); err == nil {
		t.Error("a missing count was accepted")
	}
	// And a set that is itself bad is reported, not swallowed.
	var w2 bits
	w2.ue(1)
	w2.ue(64)
	w2.ue(0)
	s2 := &SPS{Log2MaxPOCLSB: 8}
	if err := s2.readShortTermRefPicSets(newSticky(w2.b)); err == nil {
		t.Error("a bad set inside a good count was accepted")
	}
}

func TestLongTermPicturesStopAtWhatIsThere(t *testing.T) {
	// A count past what a sequence may offer leaves the list empty rather than
	// allocating for it.
	var w bits
	w.bit(1)  // long_term_ref_pics_present_flag
	w.ue(100) // num_long_term_ref_pics_sps
	s := &SPS{Log2MaxPOCLSB: 8}
	s.readLongTermRefPics(newSticky(w.b))
	if len(s.LongTermRefPics) != 0 {
		t.Errorf("read %d long-term pictures from a count of a hundred", len(s.LongTermRefPics))
	}
	// A sequence that offers none says so in one bit.
	var w2 bits
	w2.bit(0)
	s2 := &SPS{Log2MaxPOCLSB: 8}
	s2.readLongTermRefPics(newSticky(w2.b))
	if len(s2.LongTermRefPics) != 0 {
		t.Error("pictures appeared where the sequence offered none")
	}
}

// A difference moves entries ACROSS zero, and an entry that crosses changes
// which half it belongs to. That is the whole reason the two halves are rebuilt
// rather than shifted in place.
func TestEntriesCrossZeroBetweenTheHalves(t *testing.T) {
	// predicted writes a predicted set: a sign, a magnitude, and every entry
	// kept.
	predicted := func(negative bool, abs uint32, n int) []byte {
		var w bits
		w.bit(1) // inter_ref_pic_set_prediction_flag
		if negative {
			w.bit(1)
		} else {
			w.bit(0)
		}
		w.ue(abs - 1)
		for range n + 1 {
			w.bit(1) // used_by_curr_pic_flag
		}
		return w.b
	}
	from := ShortTermRPS{Before: []RefPic{{-1, true}}, After: []RefPic{{1, true}}}

	t.Run("a picture already seen becomes one still to come", func(t *testing.T) {
		got, err := parseShortTermRPS(newSticky(predicted(false, 3, from.Count())), 1, []ShortTermRPS{from})
		if err != nil {
			t.Fatal(err)
		}
		// +3: -1 -> 2, 1 -> 4, and the predicted-from picture sits at 3.
		if want := []int32{2, 3, 4}; !sameInts(deltas(got.After), want) {
			t.Errorf("after = %v; want %v", deltas(got.After), want)
		}
		if len(got.Before) != 0 {
			t.Errorf("before = %v; want nothing left behind", deltas(got.Before))
		}
	})

	t.Run("a picture still to come becomes one already seen", func(t *testing.T) {
		got, err := parseShortTermRPS(newSticky(predicted(true, 3, from.Count())), 1, []ShortTermRPS{from})
		if err != nil {
			t.Fatal(err)
		}
		// -3: 1 -> -2, -1 -> -4, and the predicted-from picture sits at -3.
		if want := []int32{-2, -3, -4}; !sameInts(deltas(got.Before), want) {
			t.Errorf("before = %v; want %v", deltas(got.Before), want)
		}
		if len(got.After) != 0 {
			t.Errorf("after = %v; want nothing left ahead", deltas(got.After))
		}
	})
}
