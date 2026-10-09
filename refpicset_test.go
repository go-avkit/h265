// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

func ltPOCs(e []LtPicture) []int32 {
	out := make([]int32, len(e))
	for i, p := range e {
		out[i] = p.POC
	}
	return out
}

// ⛔ Used and not-used go to different lists, and the difference is not a
// detail: a picture in Foll must be KEPT and must not be PREDICTED FROM. A
// derivation that put everything in Curr would offer a decoder pictures it may
// not use; one that dropped Foll would break a later picture instead.
func TestUsedAndKeptGoToDifferentLists(t *testing.T) {
	refs := PictureRefs{ShortTerm: ShortTermRPS{
		Before: []RefPic{{-1, true}, {-2, false}, {-3, true}},
		After:  []RefPic{{1, false}, {2, true}},
	}}
	got := Derive(refs, 100, SPS{Log2MaxPOCLSB: 8})

	if want := []int32{99, 97}; !sameInts(got.StCurrBefore, want) {
		t.Errorf("before = %v; want %v", got.StCurrBefore, want)
	}
	if want := []int32{102}; !sameInts(got.StCurrAfter, want) {
		t.Errorf("after = %v; want %v", got.StCurrAfter, want)
	}
	if want := []int32{98, 101}; !sameInts(got.StFoll, want) {
		t.Errorf("kept but unused = %v; want %v", got.StFoll, want)
	}
	if got.Count() != 5 {
		t.Errorf("Count = %d; want 5, every picture the buffer must hold", got.Count())
	}
}

// A long-term picture without its high bits is named by the LOW bits of its
// count, and saying otherwise would hand a decoder a number that is not a count.
func TestALongTermPictureWithoutItsHighBitsIsNotAFullCount(t *testing.T) {
	refs := PictureRefs{LongTerm: []LongTermRef{
		{POCLSB: 30, Used: true},
		{POCLSB: 40, Used: false},
	}}
	got := Derive(refs, 100, SPS{Log2MaxPOCLSB: 8})
	if len(got.LtCurr) != 1 || len(got.LtFoll) != 1 {
		t.Fatalf("lists are %d and %d; want one each", len(got.LtCurr), len(got.LtFoll))
	}
	if got.LtCurr[0].POC != 30 || got.LtCurr[0].Exact {
		t.Errorf("used entry = %+v; want the low bits 30, not exact", got.LtCurr[0])
	}
	if got.LtFoll[0].POC != 40 || got.LtFoll[0].Exact {
		t.Errorf("kept entry = %+v; want the low bits 40, not exact", got.LtFoll[0])
	}
}

// ⛔ The high bits are stated as a number of wraps that ACCUMULATES across the
// entries: each is counted from the one before, not from zero. Two entries each
// stating one wrap are 256 and 512 back, not 256 twice.
func TestTheHighBitsAccumulateAcrossEntries(t *testing.T) {
	const poc = 100
	sps := SPS{Log2MaxPOCLSB: 8} // 256
	refs := PictureRefs{LongTerm: []LongTermRef{
		{POCLSB: 200, Used: true, MSBPresent: true, DeltaMSBCycle: 1},
		{POCLSB: 200, Used: true, MSBPresent: true, DeltaMSBCycle: 1},
	}}
	got := Derive(refs, poc, sps)
	if want := []int32{-56, -312}; !sameInts(ltPOCs(got.LtCurr), want) {
		t.Errorf("counts = %v; want %v -- the wraps did not accumulate",
			ltPOCs(got.LtCurr), want)
	}
	for i, p := range got.LtCurr {
		if !p.Exact {
			t.Errorf("entry %d states its high bits and is not exact", i)
		}
	}
}

// The count afresh at each group: the sequence's entries and the slice's own
// are two runs, and the first of each starts over.
func TestEachGroupStartsItsCountAfresh(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 8}
	refs := PictureRefs{LongTerm: []LongTermRef{
		{POCLSB: 200, Used: true, MSBPresent: true, DeltaMSBCycle: 1, FromSPS: true},
		{POCLSB: 200, Used: true, MSBPresent: true, DeltaMSBCycle: 1},
	}}
	got := Derive(refs, 100, sps)
	// The second entry is the first the slice states, so it counts one wrap
	// back rather than two.
	if want := []int32{-56, -56}; !sameInts(ltPOCs(got.LtCurr), want) {
		t.Errorf("counts = %v; want %v -- the slice's own entries did not start over",
			ltPOCs(got.LtCurr), want)
	}
}

// What a decoder actually does: read the slice, derive, and get the pictures.
func TestFromASliceToThePicturesItNeeds(t *testing.T) {
	sps := spsWithSets([2][]refGap{{{1, true}, {1, false}}, {{2, true}}})
	sps.LongTermRefPicsPresent = true

	u := sliceRefUnit(t, sps, PPS{}, 50, sliceRefs{named: true, ltStated: []uint32{7}})
	refs, err := ReferencesOf(u, sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	var counter POCCounter
	poc, err := counter.Next(u, sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if poc.Value != 50 {
		t.Fatalf("order count = %d; want 50", poc.Value)
	}

	got := Derive(refs, poc.Value, sps)
	if want := []int32{49}; !sameInts(got.StCurrBefore, want) {
		t.Errorf("before = %v; want %v", got.StCurrBefore, want)
	}
	if want := []int32{48}; !sameInts(got.StFoll, want) {
		t.Errorf("kept but unused = %v; want %v", got.StFoll, want)
	}
	if want := []int32{52}; !sameInts(got.StCurrAfter, want) {
		t.Errorf("after = %v; want %v", got.StCurrAfter, want)
	}
	if len(got.LtCurr) != 1 || got.LtCurr[0].POC != 7 {
		t.Errorf("long-term = %+v; want the low bits 7", got.LtCurr)
	}
}

// A negative order count still gives low bits in range, so the resolution does
// not depend on the count being positive.
func TestTheResolutionHoldsForANegativeOrderCount(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 4} // 16
	refs := PictureRefs{LongTerm: []LongTermRef{
		{POCLSB: 3, Used: true, MSBPresent: true, DeltaMSBCycle: 1},
	}}
	// -5 has low bits 11 against high bits -16.
	got := Derive(refs, -5, sps)
	if len(got.LtCurr) != 1 {
		t.Fatal("no entry")
	}
	// 3 + (-5) - 16 - 11 = -29
	if got.LtCurr[0].POC != -29 {
		t.Errorf("count = %d; want -29", got.LtCurr[0].POC)
	}
}

// An IDR needs nothing, and that has to survive the derivation too.
func TestAnIDRDerivesAnEmptySet(t *testing.T) {
	got := Derive(PictureRefs{}, 0, SPS{Log2MaxPOCLSB: 8})
	if got.Count() != 0 {
		t.Errorf("an empty slice derived %d pictures", got.Count())
	}
}
