// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// listSPS is a sequence with three usable short-term pictures, so that
// NumPicTotalCurr is 3 and a list_entry is two bits wide.
func listSPS() SPS {
	s := spsWithSets([2][]refGap{{{1, true}, {2, true}}, {{3, true}}})
	s.Log2MaxPOCLSB = 8
	// ⛔ 4:2:0, not the zero value. A monochrome sequence states no chroma
	// field at all, so a fixture left at zero cannot reach half of what the
	// weight table says.
	s.ChromaFormat = 1
	return s
}

func listPPS() PPS {
	return PPS{NumRefIdxL0: 2, NumRefIdxL1: 2, ListsModification: true}
}

// TestASliceStatesHowManyEntriesItsListsHave.
func TestASliceStatesHowManyEntriesItsListsHave(t *testing.T) {
	sps, pps := listSPS(), listPPS()
	// Without an override the counts are the picture parameter set's.
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true})
	refs, err := ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.NumRefIdxL0Active != 2 || refs.NumRefIdxL1Active != 2 {
		t.Errorf("counts %d/%d, want the set's 2/2", refs.NumRefIdxL0Active, refs.NumRefIdxL1Active)
	}
	if refs.Type != SliceP {
		t.Errorf("type %v, want P", refs.Type)
	}

	// With one, the slice's own -- and a B slice states both.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, asB: true, override: true, l0Minus1: 2, l1Minus1: 0})
	refs, err = ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.NumRefIdxL0Active != 3 || refs.NumRefIdxL1Active != 1 {
		t.Errorf("counts %d/%d, want 3/1", refs.NumRefIdxL0Active, refs.NumRefIdxL1Active)
	}
	// ⛔ A P slice states only list 0's count. Reading a second one would
	// consume a field that is not there.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, override: true, l0Minus1: 2})
	refs, err = ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.NumRefIdxL0Active != 3 {
		t.Errorf("a P slice's list 0 count is %d, want 3", refs.NumRefIdxL0Active)
	}
}

// TestASliceStatesTheOrderOfItsLists reads ref_pic_lists_modification, 7.3.6.2.
func TestASliceStatesTheOrderOfItsLists(t *testing.T) {
	sps, pps := listSPS(), listPPS()
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true, asB: true, modL0: []uint32{2, 0}, modL1: []uint32{1, 2},
	})
	refs, err := ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		got  []uint32
		want []uint32
	}{{"L0", refs.ListEntryL0, []uint32{2, 0}}, {"L1", refs.ListEntryL1, []uint32{1, 2}}} {
		if len(c.got) != len(c.want) {
			t.Errorf("%s has %d entries, want %d", c.name, len(c.got), len(c.want))
			continue
		}
		for i := range c.want {
			if c.got[i] != c.want[i] {
				t.Errorf("%s[%d] = %d, want %d", c.name, i, c.got[i], c.want[i])
			}
		}
	}

	// ⛔ A stated-nothing is not an empty list. nil means the default order
	// stands; an empty slice would say "a list of no entries".
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, asB: true})
	refs, err = ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.ListEntryL0 != nil || refs.ListEntryL1 != nil {
		t.Errorf("a slice stating no modification gave %v / %v, want nil",
			refs.ListEntryL0, refs.ListEntryL1)
	}

	// One list may be reordered and the other not.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, asB: true, modL1: []uint32{2, 1}})
	refs, err = ReferencesOf(u, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.ListEntryL0 != nil || len(refs.ListEntryL1) != 2 {
		t.Errorf("L0=%v L1=%v, want nil and two entries", refs.ListEntryL0, refs.ListEntryL1)
	}
}

// TestTheModificationIsNotStatedWhenThereIsNoChoice: nothing is written when
// the picture parameter set does not allow it, or when there is one picture.
func TestTheModificationIsNotStatedWhenThereIsNoChoice(t *testing.T) {
	sps := listSPS()
	off := listPPS()
	off.ListsModification = false
	u := sliceRefUnit(t, sps, off, 7, sliceRefs{named: true, asB: true})
	if refs, err := ReferencesOf(u, sps, off); err != nil || refs.ListEntryL0 != nil {
		t.Errorf("with the set forbidding it: %v, %v", refs.ListEntryL0, err)
	}

	// One usable picture: there is no other order it could be put in, so the
	// flag is not sent. A reader that read one anyway is misaligned.
	one := spsWithSets([2][]refGap{{{1, true}}, nil})
	one.Log2MaxPOCLSB = 8
	pps := listPPS()
	u = sliceRefUnit(t, one, pps, 7, sliceRefs{named: true, asB: true})
	refs, err := ReferencesOf(u, one, pps)
	if err != nil {
		t.Fatal(err)
	}
	if refs.ListEntryL0 != nil {
		t.Errorf("a single picture gave a modification %v", refs.ListEntryL0)
	}
}

// TestTheTwoFlagsBeforeTheListsAreConsumed.
//
// ⛔ Neither flag is kept, and both are CONDITIONAL on the sequence. A reader
// that did not consume them is misaligned from that point on, which shows up as
// a wrong count rather than as an error.
func TestTheTwoFlagsBeforeTheListsAreConsumed(t *testing.T) {
	for _, c := range []struct {
		name            string
		sao, mvp, split bool
		chroma          uint8
	}{
		{name: "neither", chroma: 1},
		{name: "sao only", sao: true, chroma: 1},
		{name: "mvp only", mvp: true, chroma: 1},
		{name: "both", sao: true, mvp: true, chroma: 1},
		{name: "sao, monochrome", sao: true, chroma: 0},
		// ⛔ Separate colour planes make ChromaArrayType zero although the
		// format is 4:4:4, so the chroma flag is NOT sent. A reader using the
		// format rather than the array type reads a bit that is not there.
		{name: "sao, separate planes", sao: true, split: true, chroma: 3},
	} {
		sps := listSPS()
		sps.SAOEnabled, sps.TemporalMVPEnabled = c.sao, c.mvp
		sps.SeparatePlanes, sps.ChromaFormat = c.split, c.chroma
		pps := listPPS()
		u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, override: true, l0Minus1: 4})
		refs, err := ReferencesOf(u, sps, pps)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if refs.NumRefIdxL0Active != 5 {
			t.Errorf("%s: count %d, want 5 -- the reader is misaligned",
				c.name, refs.NumRefIdxL0Active)
		}
	}
}

// TestASliceRefusesWhatItCannotPredictFrom.
func TestASliceRefusesWhatItCannotPredictFrom(t *testing.T) {
	sps, pps := listSPS(), listPPS()

	// An override past the 0..14 that 7.4.3.3 allows.
	u := sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, override: true, l0Minus1: MaxRefIdxActive})
	if _, err := ReferencesOf(u, sps, pps); !errors.Is(err, ErrRefIdxRange) {
		t.Errorf("err = %v, want ErrRefIdxRange", err)
	}
	// One less is the largest conformant value and must be read.
	u = sliceRefUnit(t, sps, pps, 7, sliceRefs{named: true, override: true, l0Minus1: MaxRefIdxActive - 1})
	if refs, err := ReferencesOf(u, sps, pps); err != nil || refs.NumRefIdxL0Active != MaxRefIdxActive {
		t.Errorf("the largest conformant count gave %d, %v", refs.NumRefIdxL0Active, err)
	}

	// ⛔ A P slice that may predict from no picture at all. The set is read
	// without error -- every entry is simply marked unused -- so nothing else
	// would notice.
	none := spsWithSets([2][]refGap{{{1, false}}, nil})
	none.Log2MaxPOCLSB = 8
	u = sliceRefUnit(t, none, pps, 7, sliceRefs{named: true})
	if _, err := ReferencesOf(u, none, pps); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a P slice with no usable picture gave %v, want ErrSliceHeader", err)
	}
	// An I slice states no lists at all, so the same set is fine for one.
	u = sliceRefUnit(t, none, pps, 7, sliceRefs{named: true, asI: true})
	if refs, err := ReferencesOf(u, none, pps); err != nil || refs.Type != SliceI {
		t.Errorf("an I slice gave %v, %v", refs.Type, err)
	}
}

// TestASliceTypeTheFormatDoesNotNameIsRefused.
//
// ⛔ 7.4.7.1 names three. A fourth would make every test below answer "not P,
// not B", which reads as an I slice rather than as the malformed header it is.
func TestASliceTypeTheFormatDoesNotNameIsRefused(t *testing.T) {
	sps, pps := listSPS(), listPPS()
	var w bitbuf
	w.bit(1)
	w.ue(0)
	w.ue(3) // one past slice_type I
	w.bits(7, int(sps.Log2MaxPOCLSB))
	w.bit(1)
	for w.n != 0 {
		w.bit(0)
	}
	u := Unit{Type: UnitTrailR, Payload: w.b}
	if _, err := ReferencesOf(u, sps, pps); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("err = %v, want ErrSliceHeader", err)
	}
}

// TestNothingShortOfTheWholeListHeaderIsAccepted.
//
// ⛔ The fields this adds are read AFTER the reference sets, so a stream that
// ends inside them had already been read without error up to that point. Each
// cut lands in a different one.
func TestNothingShortOfTheWholeListHeaderIsAccepted(t *testing.T) {
	sps, pps := listSPS(), listPPS()
	sps.SAOEnabled, sps.TemporalMVPEnabled = true, true
	whole := sliceRefUnit(t, sps, pps, 7, sliceRefs{
		named: true, asB: true, override: true, l0Minus1: 3, l1Minus1: 2,
		modL0: []uint32{2, 1, 0, 2}, modL1: []uint32{0, 1, 2},
	})
	if _, err := ReferencesOf(whole, sps, pps); err != nil {
		t.Fatalf("the whole slice stopped parsing: %v", err)
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

// TestAHeaderThatEndsInTheFlagsBeforeTheCounts.
//
// ⛔ The two flags before the counts are read and thrown away, so a reader that
// did not CHECK whether they could be read would carry on to the counts with a
// reader that had already run out -- and report the parameter set's counts as
// though the slice had stated them.
func TestAHeaderThatEndsInTheFlagsBeforeTheCounts(t *testing.T) {
	sps, pps := listSPS(), listPPS()
	sps.SAOEnabled, sps.TemporalMVPEnabled = true, true

	var w bitbuf
	w.bit(1)                          // first_slice_segment_in_pic_flag
	w.ue(0)                           // slice_pic_parameter_set_id
	w.ue(uint32(SliceP))              // slice_type
	w.bits(7, int(sps.Log2MaxPOCLSB)) // slice_pic_order_cnt_lsb
	w.bit(1)                          // the set is named; one set, so no index
	w.bit(0)                          // slice_temporal_mvp_enabled_flag
	// Stop here: 15 bits. slice_sao_luma_flag is the sixteenth, and the payload
	// is cut to two bytes, so it is the first field with nothing behind it.
	for w.n != 0 {
		w.bit(0)
	}
	if len(w.b) != 2 {
		t.Fatalf("the fixture is %d bytes; it must end in the SAO flag", len(w.b))
	}
	if _, err := ReferencesOf(Unit{Type: UnitTrailR, Payload: w.b}, sps, pps); err == nil {
		t.Error("a header that ends in the SAO flag was accepted")
	}
}
