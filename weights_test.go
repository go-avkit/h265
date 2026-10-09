// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// weightPPS turns weighting on for both kinds of slice, with two entries each.
func weightPPS() PPS {
	p := listPPS()
	p.WeightedPred, p.WeightedBipred = true, true
	return p
}

// TestASliceSaysHowItWeighsEachPicture.
func TestASliceSaysHowItWeighsEachPicture(t *testing.T) {
	sps, pps := listSPS(), weightPPS()
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true,
		weights: &weightTable{
			lumaDenom: 2, chromaDelta: 0,
			l0: []weightEntry{
				{luma: true, dLuma: 3, lumaOffset: -5, chroma: true, dChroma: [2]int32{2, -1}},
				{}, // neither stated: the neutral weight and no offset
			},
		},
	})
	refs, err := ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	w := refs.Weights
	if w == nil {
		t.Fatal("a slice the set weights carried no table")
	}
	if w.LumaLog2Denom != 2 || w.ChromaLog2Denom != 2 {
		t.Errorf("denominators %d/%d, want 2/2", w.LumaLog2Denom, w.ChromaLog2Denom)
	}
	if len(w.L0) != 2 {
		t.Fatalf("list 0 has %d weights, want 2", len(w.L0))
	}
	// ⛔ The weight is a VALUE, not the difference the slice states: the
	// neutral weight is 1<<denom and the difference is added to it. A reader
	// handing back the raw 3 would scale by almost nothing.
	if w.L0[0].LumaWeight != 1<<2+3 {
		t.Errorf("luma weight %d, want %d", w.L0[0].LumaWeight, 1<<2+3)
	}
	if w.L0[0].LumaOffset != -5 {
		t.Errorf("luma offset %d, want -5", w.L0[0].LumaOffset)
	}
	if !w.L0[0].LumaStated || !w.L0[0].ChromaStated {
		t.Error("the first entry states both and says it states neither")
	}
	// An entry that states nothing still carries the neutral weight, so a
	// caller never has to ask whether the field was there.
	if w.L0[1].LumaStated || w.L0[1].LumaWeight != 1<<2 || w.L0[1].LumaOffset != 0 {
		t.Errorf("the unstated entry is %+v, want the neutral weight 4", w.L0[1])
	}
	if w.L0[1].ChromaWeight != [2]int32{4, 4} {
		t.Errorf("unstated chroma weights %v, want the neutral 4/4", w.L0[1].ChromaWeight)
	}
	// A P slice weighs one list.
	if w.L1 != nil {
		t.Errorf("a P slice carried %d weights for list 1", len(w.L1))
	}
}

// TestAChromaOffsetIsNotTheDifferenceItIsStatedAs.
//
// ⛔ 7.4.7.3 folds the weight back out of the stated difference and clips the
// result to a byte. A reader that handed back the difference would be wrong by
// the whole of that term, and wrong SILENTLY: both are small numbers.
func TestAChromaOffsetIsNotTheDifferenceItIsStatedAs(t *testing.T) {
	sps, pps := listSPS(), weightPPS()
	for _, c := range []struct {
		name     string
		denom    uint32
		dWeight  int32
		dOffset  int32
		want     int32
		wantWeig int32
	}{
		// weight 6: 0 - (128*6)>>2 + 128 = -64. The stated difference is zero,
		// so a reader passing it through would answer 0.
		{"folded", 2, 2, 0, -64, 6},
		// weight 8: 0 - 256 + 128 = -128. ⛔ EXACTLY the floor, so nothing is
		// clipped -- a test that stopped here would never run the clip.
		{"at the floor", 2, 4, 0, -128, 8},
		// weight 9: 0 - 288 + 128 = -160, past it.
		{"clipped low", 2, 5, 0, -128, 9},
		// weight 1: 100 - 32 + 128 = 196, past the ceiling.
		{"clipped high", 2, -3, 100, 127, 1},
	} {
		u := sliceRefUnit(t, sps, pps, 7, sliceRefs{
			named: true,
			weights: &weightTable{lumaDenom: c.denom, l0: []weightEntry{
				{chroma: true, dChroma: [2]int32{c.dWeight, c.dWeight},
					dChromaOff: [2]int32{c.dOffset, c.dOffset}},
				{},
			}},
		})
		refs, err := ReferencesOf(u, sps, pps)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		e := refs.Weights.L0[0]
		if e.ChromaWeight[0] != c.wantWeig {
			t.Errorf("%s: chroma weight %d, want %d", c.name, e.ChromaWeight[0], c.wantWeig)
		}
		if e.ChromaOffset[0] != c.want {
			t.Errorf("%s: chroma offset %d, want %d (the stated difference was %d)",
				c.name, e.ChromaOffset[0], c.want, c.dOffset)
		}
	}
}

// TestEveryLumaFlagComesBeforeEveryValue.
//
// ⛔ HEVC states every luma flag as ONE field of n bits, then every chroma
// flag, then the values. H.264 interleaves a flag with its own values. A reader
// carrying the sibling's shape over reads the first entry's weight out of the
// second entry's flag -- which is a wrong number, not an error.
//
// The witness is a list whose two entries differ: only the SECOND states a
// luma weight. Interleaved, the first entry's flag would be followed by the
// second's, and the values would land on the wrong entry.
func TestEveryLumaFlagComesBeforeEveryValue(t *testing.T) {
	sps, pps := listSPS(), weightPPS()
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true, asB: true,
		weights: &weightTable{lumaDenom: 1, l0: []weightEntry{
			{},
			{luma: true, dLuma: 5, lumaOffset: 9},
		}},
	})
	refs, err := ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	l0 := refs.Weights.L0
	if l0[0].LumaStated {
		t.Error("the FIRST entry was read as stating a weight")
	}
	if !l0[1].LumaStated || l0[1].LumaWeight != 1<<1+5 || l0[1].LumaOffset != 9 {
		t.Errorf("the second entry is %+v, want weight %d offset 9", l0[1], 1<<1+5)
	}
	// A B slice weighs both lists, and the second is read after the first.
	if len(refs.Weights.L1) != 2 {
		t.Errorf("list 1 has %d weights, want 2", len(refs.Weights.L1))
	}
}

// TestAWeightTableIsOnlyThereWhenTheSetAsksForIt.
func TestAWeightTableIsOnlyThereWhenTheSetAsksForIt(t *testing.T) {
	sps := listSPS()
	for _, c := range []struct {
		name       string
		pred, bi   bool
		asB        bool
		wantWeight bool
	}{
		{name: "P, neither", wantWeight: false},
		{name: "P, weighted_pred", pred: true, wantWeight: true},
		{name: "P, weighted_bipred only", bi: true, wantWeight: false},
		{name: "B, weighted_pred only", pred: true, asB: true, wantWeight: false},
		{name: "B, weighted_bipred", bi: true, asB: true, wantWeight: true},
	} {
		pps := listPPS()
		pps.WeightedPred, pps.WeightedBipred = c.pred, c.bi
		u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, asB: c.asB})
		refs, err := ReferencesOf(u, sps, pps)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got := refs.Weights != nil; got != c.wantWeight {
			t.Errorf("%s: carried a table = %v, want %v", c.name, got, c.wantWeight)
		}
	}
}

// TestChromaWeightsFollowChromaArrayType.
//
// ⛔ Separate colour planes make the format 4:4:4 and ChromaArrayType ZERO, so
// neither the chroma denominator nor the chroma flags are stated. FFmpeg's
// pred_weight_table tests chroma_format_idc here, as it does for the SAO flag.
func TestChromaWeightsFollowChromaArrayType(t *testing.T) {
	for _, c := range []struct {
		name   string
		split  bool
		chroma uint8
		want   bool
	}{
		{name: "4:2:0", chroma: 1, want: true},
		{name: "monochrome", chroma: 0, want: false},
		{name: "separate planes", split: true, chroma: 3, want: false},
	} {
		sps := listSPS()
		sps.SeparatePlanes, sps.ChromaFormat = c.split, c.chroma
		pps := weightPPS()
		u := sliceRefUnit(t, sps, pps, 7, sliceRefs{
			named: true,
			weights: &weightTable{lumaDenom: 3, chromaDelta: -1, l0: []weightEntry{
				{chroma: true, dChroma: [2]int32{1, 1}}, {},
			}},
		})
		refs, err := ReferencesOf(u, sps, pps)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		w := refs.Weights
		// With no chroma the denominator is never stated, so it stays at the
		// luma one, and no entry states chroma.
		gotDenom := w.ChromaLog2Denom
		if c.want && gotDenom != 2 {
			t.Errorf("%s: chroma denominator %d, want 2", c.name, gotDenom)
		}
		if !c.want && gotDenom != 3 {
			t.Errorf("%s: chroma denominator %d, want the luma 3", c.name, gotDenom)
		}
		if got := w.L0[0].ChromaStated; got != c.want {
			t.Errorf("%s: chroma stated = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestAWeightTableRefusesWhatItCannotMean.
func TestAWeightTableRefusesWhatItCannotMean(t *testing.T) {
	sps, pps := listSPS(), weightPPS()
	for _, c := range []struct {
		name string
		tbl  weightTable
	}{
		// 7.4.7.3 puts both denominators in 0..7.
		{"a luma denominator past seven", weightTable{lumaDenom: 8}},
		{"a chroma denominator past seven", weightTable{lumaDenom: 7, chromaDelta: 1}},
		{"a chroma denominator below zero", weightTable{lumaDenom: 0, chromaDelta: -1}},
		// The differences are read into eight bits by every decoder.
		{"a luma difference past a byte", weightTable{
			l0: []weightEntry{{luma: true, dLuma: 128}, {}}}},
		{"a chroma difference past a byte", weightTable{
			l0: []weightEntry{{chroma: true, dChroma: [2]int32{-129, 0}}, {}}}},
		{"a chroma offset difference past its range", weightTable{
			l0: []weightEntry{{chroma: true, dChromaOff: [2]int32{1 << 18, 0}}, {}}}},
	} {
		tbl := c.tbl
		u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, weights: &tbl})
		if _, err := ReferencesOf(u, sps, pps); !errors.Is(err, ErrSliceHeader) {
			t.Errorf("%s: err = %v, want ErrSliceHeader", c.name, err)
		}
	}
	// ⛔ On a 4:2:0 sequence a luma denominator of 8 also makes the CHROMA one
	// 8, so the chroma check masks the luma one and removing the latter leaves
	// the suite green. Monochrome states no chroma denominator at all, so only
	// the luma check can refuse this.
	mono := listSPS()
	mono.ChromaFormat = 0
	u8 := sliceRefUnit(t, mono, pps, 7, sliceRefs{named: true,
		weights: &weightTable{lumaDenom: 8}})
	if _, err := ReferencesOf(u8, mono, pps); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("monochrome, luma denominator 8: err = %v, want ErrSliceHeader", err)
	}

	// The boundary on the other side must be read: seven is allowed.
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true,
		weights: &weightTable{lumaDenom: 7, chromaDelta: 0}})
	if refs, err := ReferencesOf(u, sps, pps); err != nil || refs.Weights.LumaLog2Denom != 7 {
		t.Errorf("a denominator of seven gave %v", err)
	}
}

// TestWhichPictureTheMotionComesFrom reads the collocated fields.
func TestWhichPictureTheMotionComesFrom(t *testing.T) {
	sps := listSPS()
	sps.TemporalMVPEnabled = true
	pps := listPPS()

	// ⛔ A P slice has only list 0 to take it from and states no flag. A reader
	// that read one anyway is misaligned from there on.
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, sliceMVP: true, collocIdx: 1})
	refs, err := ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if !refs.TemporalMVP || !refs.CollocatedFromL0 || refs.CollocatedRefIdx != 1 {
		t.Errorf("a P slice gave mvp=%v fromL0=%v idx=%d, want true/true/1",
			refs.TemporalMVP, refs.CollocatedFromL0, refs.CollocatedRefIdx)
	}

	// A B slice says which list, and the index is bounded by THAT list.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true, asB: true, sliceMVP: true, collocFromL1: true, collocIdx: 1,
		override: true, l0Minus1: 0, l1Minus1: 2,
	})
	refs, err = ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.CollocatedFromL0 || refs.CollocatedRefIdx != 1 {
		t.Errorf("fromL0=%v idx=%d, want false/1", refs.CollocatedFromL0, refs.CollocatedRefIdx)
	}

	// ⛔ The index is stated only where the list has a choice. With one entry
	// it is absent and the picture is the only one there is.
	//
	// ⛔ Asserting only that the index is zero is BLIND: a reader that read the
	// field anyway would consume five_minus_max_num_merge_cand, land on a zero
	// and answer zero too. The merge count is what tells the two apart, so the
	// fixture states one that is not the default.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true, sliceMVP: true, override: true, l0Minus1: 0, fiveMinusMerge: 3,
	})
	refs, err = ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.CollocatedRefIdx != 0 {
		t.Errorf("a single-entry list gave index %d, want 0", refs.CollocatedRefIdx)
	}
	if refs.MaxMergeCand != 2 {
		t.Errorf("merge candidates %d, want 2 -- the reader consumed a field that was not there",
			refs.MaxMergeCand)
	}

	// Nothing is stated at all when the sequence does not turn it on.
	plain := listSPS()
	u = sliceRefUnit(t, plain, pps, 7, sliceRefs{named: true, sliceMVP: true})
	refs, err = ReferencesOf(u, plain, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.TemporalMVP {
		t.Error("a sequence that does not enable it carried a slice flag")
	}
}

// TestACollocatedPicturePastItsListIsRefused.
func TestACollocatedPicturePastItsListIsRefused(t *testing.T) {
	sps := listSPS()
	sps.TemporalMVPEnabled = true
	pps := listPPS() // two entries per list
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, sliceMVP: true, collocIdx: 2})
	if _, err := ReferencesOf(u, sps, pps); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("err = %v, want ErrSliceHeader", err)
	}
	// One less is the last entry and must be read.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, sliceMVP: true, collocIdx: 1})
	if refs, err := ReferencesOf(u, sps, pps); err != nil || refs.CollocatedRefIdx != 1 {
		t.Errorf("the last entry gave %d, %v", refs.CollocatedRefIdx, err)
	}
}

// TestHowManyMergeCandidatesASliceUses: the field is stated as five minus the
// count, so the count is 1..5 and nothing else.
func TestHowManyMergeCandidatesASliceUses(t *testing.T) {
	sps, pps := listSPS(), listPPS()
	for five := uint32(0); five <= 4; five++ {
		u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, fiveMinusMerge: five})
		refs, err := ReferencesOf(u, sps, pps)
		if err != nil {
			t.Errorf("five=%d: %v", five, err)
			continue
		}
		if want := uint8(5 - five); refs.MaxMergeCand != want {
			t.Errorf("five=%d gave %d candidates, want %d", five, refs.MaxMergeCand, want)
		}
	}
	// Five would be no candidates at all, which no slice can mean.
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, fiveMinusMerge: 5})
	if _, err := ReferencesOf(u, sps, pps); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("err = %v, want ErrSliceHeader", err)
	}
}

// TestNothingShortOfTheWholePredictionBlockIsAccepted.
//
// ⛔ Every field here is read AFTER the reference lists, so a stream that ends
// inside one had already been read without error up to that point. Each cut
// lands in a different field, and the weight table alone has five kinds.
func TestNothingShortOfTheWholePredictionBlockIsAccepted(t *testing.T) {
	sps := listSPS()
	sps.TemporalMVPEnabled, sps.SAOEnabled = true, true
	pps := weightPPS()
	pps.CABACInitPresent = true
	whole := sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true, asB: true, sliceMVP: true, collocIdx: 1,
		override: true, l0Minus1: 2, l1Minus1: 1,
		modL0: []uint32{2, 1, 0}, modL1: []uint32{1, 0},
		weights: &weightTable{lumaDenom: 3, chromaDelta: -1,
			l0: []weightEntry{
				{luma: true, dLuma: 2, lumaOffset: -3, chroma: true,
					dChroma: [2]int32{1, -1}, dChromaOff: [2]int32{4, -4}},
				{luma: true, dLuma: -2, lumaOffset: 7},
				{chroma: true, dChroma: [2]int32{3, 3}},
			},
			l1: []weightEntry{{luma: true, dLuma: 1}, {}},
		},
		fiveMinusMerge: 2,
	})
	refs, err := ReferencesOf(whole, sps, pps)
	if err != nil {
		t.Fatalf("the whole slice stopped parsing: %v", err)
	}
	if refs.Weights == nil || len(refs.Weights.L0) != 3 || len(refs.Weights.L1) != 2 {
		t.Fatalf("the fixture did not reach the weights: %+v", refs.Weights)
	}
	shortest := len(whole.Payload)
	for n := 1; n < len(whole.Payload); n++ {
		if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: whole.Payload[:n]}, sps, pps); err == nil {
			shortest = n
			break
		}
	}
	for n := 1; n < shortest; n++ {
		if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: whole.Payload[:n]}, sps, pps); err == nil {
			t.Errorf("%d of %d bytes: accepted, yet %d is the shortest complete header",
				n, len(whole.Payload), shortest)
		}
	}
	if d := len(whole.Payload) - shortest; d > 1 {
		t.Errorf("the header ends %d bytes before the payload; the cuts test padding", d)
	}
}

// TestEveryFieldOfTheWeightTableIsChecked truncates several shapes of table.
//
// ⛔ One fixture only cuts where ITS fields happen to fall on byte boundaries.
// Varying the denominator and which components each entry states moves every
// field, so between them the cuts land in all of them.
func TestEveryFieldOfTheWeightTableIsChecked(t *testing.T) {
	sps := listSPS()
	pps := weightPPS()
	for _, tbl := range []weightTable{
		{lumaDenom: 0, l0: []weightEntry{{luma: true, dLuma: 1}, {}}},
		{lumaDenom: 7, chromaDelta: -7, l0: []weightEntry{{chroma: true}, {}}},
		{lumaDenom: 4, chromaDelta: 2, l0: []weightEntry{
			{luma: true, dLuma: 100, lumaOffset: -120, chroma: true,
				dChroma: [2]int32{60, -60}, dChromaOff: [2]int32{1 << 16, -(1 << 16)}},
			{luma: true, dLuma: -100, lumaOffset: 120},
		}},
		{lumaDenom: 1, chromaDelta: 1, l0: []weightEntry{
			{chroma: true, dChroma: [2]int32{7, 7}, dChromaOff: [2]int32{3, 3}},
			{luma: true, dLuma: 9, lumaOffset: 9},
		}},
	} {
		tbl := tbl
		whole := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, weights: &tbl})
		if _, err := ReferencesOf(whole, sps, pps); err != nil {
			t.Errorf("denom %d: the whole table stopped parsing: %v", tbl.lumaDenom, err)
			continue
		}
		for n := 1; n < len(whole.Payload)-1; n++ {
			if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: whole.Payload[:n]}, sps, pps); err == nil {
				t.Errorf("denom %d: %d of %d bytes accepted", tbl.lumaDenom, n, len(whole.Payload))
			}
		}
	}
}

// TestAWeightListForAnImpossibleCount.
//
// ⛔ PPS is a public struct, so a caller can hand one whose counts never came
// out of a stream. The counts size a read of n bits and a list of n entries.
func TestAWeightListForAnImpossibleCount(t *testing.T) {
	sps := listSPS()
	pps := weightPPS()
	pps.NumRefIdxL0 = 0 // no slice header can say this; a caller can
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true})
	if _, err := ReferencesOf(u, sps, pps); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("err = %v, want ErrSliceHeader", err)
	}
}
