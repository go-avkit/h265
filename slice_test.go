// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// seg describes the slice segment header a test wants written.
type seg struct {
	first     bool
	dependent bool
	typ       uint32
	extraBits uint8
	unitType  UnitType
}

// build writes the header as a NAL unit, reading the conditionals out of the
// picture set exactly as the parser will.
func (s seg) build(pps PPS) Unit {
	w := &writer{}
	w.flag(s.first)
	if s.unitType.IsRandomAccess() {
		w.bit(1) // no_output_of_prior_pics_flag
	}
	w.ue(0) // slice_pic_parameter_set_id
	if !s.first {
		if pps.DependentSlicesEnabled {
			w.flag(s.dependent)
		}
		// The address would follow; nothing reads it, so nothing writes it.
	} else {
		for i := uint8(0); i < s.extraBits; i++ {
			w.bit(1)
		}
		w.ue(s.typ)
	}
	// Coded data would follow; a byte stands in so nothing depends on the header
	// ending the payload.
	w.bits(0xAB, 8)
	return Unit{Type: s.unitType, Payload: w.data}
}

func TestEachSliceTypeIsNamed(t *testing.T) {
	// ⛔ HEVC numbers them the other way round from H.264: zero is B here and P
	// there. A reader carrying the other format's numbering over would report every
	// B picture as a P.
	for stated, want := range map[uint32]SliceType{0: SliceB, 1: SliceP, 2: SliceI} {
		h, err := ParseSliceSegmentHeader(seg{first: true, typ: stated, unitType: UnitTrailR}.build(PPS{}), PPS{})
		if err != nil {
			t.Errorf("type %d: %v", stated, err)
			continue
		}
		if h.Type != want || !h.TypeRead {
			t.Errorf("stated %d read as %v (read=%v), want %v", stated, h.Type, h.TypeRead, want)
		}
	}
	for typ, want := range map[SliceType]string{SliceB: "B", SliceP: "P", SliceI: "I", SliceType(9): "slice type 9"} {
		if got := typ.String(); got != want {
			t.Errorf("%d = %q, want %q", uint8(typ), got, want)
		}
	}
}

// TestTheReservedBitsMoveTheType: the picture set says how many bits the stream
// reserves before the type, and a reader ignoring them reads the type out of them.
func TestTheReservedBitsMoveTheType(t *testing.T) {
	for _, bits := range []uint8{0, 1, 3, 7} {
		pps := PPS{ExtraSliceHeaderBits: bits}
		h, err := ParseSliceSegmentHeader(seg{first: true, typ: 2, extraBits: bits, unitType: UnitTrailR}.build(pps), pps)
		if err != nil {
			t.Errorf("%d reserved bits: %v", bits, err)
			continue
		}
		if h.Type != SliceI {
			t.Errorf("%d reserved bits: type read as %v, want I", bits, h.Type)
		}
	}
}

// TestARandomAccessPictureStatesOneMoreFlag: that flag sits before the parameter set
// identifier, so a reader that skipped it for a random access picture would read the
// identifier out of it and the type from the wrong place after.
func TestARandomAccessPictureStatesOneMoreFlag(t *testing.T) {
	for _, typ := range []UnitType{UnitIDRWRADL, UnitIDRNLP, UnitCRA, UnitBLAWLP} {
		h, err := ParseSliceSegmentHeader(seg{first: true, typ: 2, unitType: typ}.build(PPS{}), PPS{})
		if err != nil {
			t.Errorf("unit type %d: %v", typ, err)
			continue
		}
		if !h.RandomAccess || !h.NoOutputOfPriorPics {
			t.Errorf("unit type %d: %+v", typ, h)
		}
		if h.Type != SliceI {
			t.Errorf("unit type %d: type read as %v, want I", typ, h.Type)
		}
	}
	// And a trailing picture states no such flag.
	h, err := ParseSliceSegmentHeader(seg{first: true, typ: 1, unitType: UnitTrailN}.build(PPS{}), PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if h.RandomAccess || h.NoOutputOfPriorPics {
		t.Errorf("a trailing picture reported itself as a random access point: %+v", h)
	}
	if h.Type != SliceP {
		t.Errorf("type read as %v, want P", h.Type)
	}
}

// TestALaterSegmentSaysItsTypeWasNotRead.
//
// ⛔ Reaching the type of a segment that does not begin a picture means first reading
// its address, whose width comes from the coding tree block size this package does not
// read yet. Reporting a type it had not read would let a picture's type come from
// whichever segment happened to be looked at, so it reports that it did not.
func TestALaterSegmentSaysItsTypeWasNotRead(t *testing.T) {
	for _, pps := range []PPS{{}, {DependentSlicesEnabled: true}} {
		h, err := ParseSliceSegmentHeader(seg{first: false, unitType: UnitTrailR}.build(pps), pps)
		if err != nil {
			t.Fatalf("dependent enabled %v: %v", pps.DependentSlicesEnabled, err)
		}
		if h.First {
			t.Error("a later segment reported itself as the first")
		}
		if h.TypeRead {
			t.Error("a later segment reported a type it cannot have read")
		}
	}
	// With dependent segments enabled the flag is there to be read.
	pps := PPS{DependentSlicesEnabled: true}
	h, err := ParseSliceSegmentHeader(seg{first: false, dependent: true, unitType: UnitTrailR}.build(pps), pps)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Dependent {
		t.Error("the dependent flag was not read")
	}
}

func TestSliceHeaderRefusals(t *testing.T) {
	t.Run("not a slice segment", func(t *testing.T) {
		if _, err := ParseSliceSegmentHeader(Unit{Type: UnitSPS}, PPS{}); !errors.Is(err, ErrNotSlice) {
			t.Errorf("err = %v, want ErrNotSlice", err)
		}
	})
	t.Run("a type the format does not have", func(t *testing.T) {
		u := seg{first: true, typ: 7, unitType: UnitTrailR}.build(PPS{})
		if _, err := ParseSliceSegmentHeader(u, PPS{}); !errors.Is(err, ErrSliceHeader) {
			t.Errorf("err = %v, want ErrSliceHeader", err)
		}
	})
	t.Run("every truncation", func(t *testing.T) {
		whole := seg{first: true, typ: 2, extraBits: 3, unitType: UnitIDRWRADL}.build(PPS{ExtraSliceHeaderBits: 3})
		if _, err := ParseSliceSegmentHeader(whole, PPS{ExtraSliceHeaderBits: 3}); err != nil {
			t.Fatalf("the fixture does not parse: %v", err)
		}
		for n := 0; n < len(whole.Payload)-1; n++ {
			cut := Unit{Type: whole.Type, Payload: whole.Payload[:n]}
			if _, err := ParseSliceSegmentHeader(cut, PPS{ExtraSliceHeaderBits: 3}); err == nil {
				t.Errorf("%d of %d bytes parsed cleanly", n, len(whole.Payload))
			}
		}
	})
	t.Run("a later segment cut before its flag", func(t *testing.T) {
		pps := PPS{DependentSlicesEnabled: true}
		if _, err := ParseSliceSegmentHeader(Unit{Type: UnitTrailR, Payload: nil}, pps); err == nil {
			t.Error("a segment with no bytes at all was accepted")
		}
	})
}

// TestOneFlagPerSegmentIsTheWholeBoundaryRule.
//
// ⛔ Where H.264 needs several fields compared between neighbouring slices, HEVC
// states it outright. Carrying the other format's machinery over would be work for
// nothing, and the simpler rule is what this asserts.
func TestOneFlagPerSegmentIsTheWholeBoundaryRule(t *testing.T) {
	pps := PPS{}
	units := []Unit{
		seg{first: true, typ: 2, unitType: UnitIDRWRADL}.build(pps),
		seg{first: false, unitType: UnitIDRWRADL}.build(pps),
		seg{first: true, typ: 1, unitType: UnitTrailR}.build(pps),
		seg{first: true, typ: 0, unitType: UnitTrailN}.build(pps),
	}
	pics, err := SplitPictures(units, pps)
	if err != nil {
		t.Fatalf("SplitPictures: %v", err)
	}
	if len(pics) != 3 {
		t.Fatalf("%d pictures, want 3 -- the second segment continues the first picture", len(pics))
	}
	if len(pics[0].Units) != 2 {
		t.Errorf("the first picture holds %d segments, want 2", len(pics[0].Units))
	}
	want := []struct {
		typ  SliceType
		sync bool
	}{{SliceI, true}, {SliceP, false}, {SliceB, false}}
	for i, w := range want {
		if pics[i].Type != w.typ || !pics[i].TypeKnown {
			t.Errorf("picture %d is %v (known %v), want %v", i, pics[i].Type, pics[i].TypeKnown, w.typ)
		}
		if pics[i].Sync != w.sync {
			t.Errorf("picture %d sync %v, want %v", i, pics[i].Sync, w.sync)
		}
	}
}

func TestParameterSetsBelongToThePictureTheyPrecede(t *testing.T) {
	pps := PPS{}
	sps := Unit{Type: UnitSPS, Payload: []byte{1, 2}}
	aud := Unit{Type: UnitAUD, Payload: []byte{3}}
	units := []Unit{
		aud, sps,
		seg{first: true, typ: 2, unitType: UnitIDRWRADL}.build(pps),
		aud,
		seg{first: true, typ: 1, unitType: UnitTrailR}.build(pps),
		// A delimiter after the last segment belongs to no picture.
		aud,
	}
	pics, err := SplitPictures(units, pps)
	if err != nil {
		t.Fatalf("SplitPictures: %v", err)
	}
	if len(pics) != 2 {
		t.Fatalf("%d pictures, want 2", len(pics))
	}
	if len(pics[0].Units) != 3 || pics[0].Units[0].Type != UnitAUD {
		t.Errorf("the first picture gathered %+v", pics[0].Units)
	}
	if len(pics[1].Units) != 2 {
		t.Errorf("the second picture holds %d units, want the delimiter and its segment", len(pics[1].Units))
	}
	want := 0
	for _, u := range pics[0].Units {
		want += len(u.Payload) + 2
	}
	if pics[0].Bytes != want {
		t.Errorf("Bytes = %d, want %d -- a header is two bytes here, not one", pics[0].Bytes, want)
	}
}

// TestSegmentsBeforeAnyPictureBeganAreKept covers a stream joined part way through a
// picture: nothing has begun, and dropping them would make a count of pictures
// disagree with a count of segments for a reason nobody could see.
func TestSegmentsBeforeAnyPictureBeganAreKept(t *testing.T) {
	pps := PPS{}
	units := []Unit{
		seg{first: false, unitType: UnitTrailR}.build(pps),
		seg{first: true, typ: 2, unitType: UnitIDRWRADL}.build(pps),
	}
	pics, err := SplitPictures(units, pps)
	if err != nil {
		t.Fatalf("SplitPictures: %v", err)
	}
	if len(pics) != 2 {
		t.Fatalf("%d pictures, want 2 -- the joined one and the whole one", len(pics))
	}
	if pics[0].TypeKnown {
		t.Error("the joined picture reported a type nothing stated")
	}
	if !pics[1].TypeKnown || pics[1].Type != SliceI {
		t.Errorf("the whole picture: %+v", pics[1])
	}
}

func TestASegmentThatCannotBeReadKeepsWhatCameBefore(t *testing.T) {
	pps := PPS{}
	good := seg{first: true, typ: 2, unitType: UnitIDRWRADL}.build(pps)
	bad := Unit{Type: UnitTrailR, Payload: nil}
	pics, err := SplitPictures([]Unit{good, bad}, pps)
	if err == nil {
		t.Fatal("a segment with no bytes was accepted")
	}
	if len(pics) != 1 {
		t.Errorf("%d pictures kept, want the one that did read", len(pics))
	}
}

func TestAStreamWithNoSegmentsHoldsNoPictures(t *testing.T) {
	pics, err := SplitPictures([]Unit{{Type: UnitVPS, Payload: []byte{1}}}, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pics) != 0 {
		t.Errorf("%d pictures from a stream with no segment", len(pics))
	}
}
