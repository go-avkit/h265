// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-avkit/bitstream"
)

// header builds the two bytes this format spends on a unit header.
func header(typ UnitType, layer, temporalID uint8) []byte {
	return []byte{
		byte(typ)<<1 | layer>>5,
		layer<<3 | (temporalID + 1), // nuh_temporal_id_plus1
	}
}

// TestBothHeaderBytesAreRead.
//
// ⛔ Two bytes, where H.264 spends one, and the layer identifier straddles them:
// its top bit is the low bit of the first byte. A reader that took only the first
// would report a layer of zero for every unit of a scalable stream and read the
// sub-layer out of payload.
func TestBothHeaderBytesAreRead(t *testing.T) {
	for _, tc := range []struct {
		typ        UnitType
		layer      uint8
		temporalID uint8
	}{
		{UnitIDRWRADL, 0, 0},
		{UnitTrailR, 1, 2},
		{UnitSPS, 0, 0},
		{UnitPrefixSEI, 63, 6},
		{UnitCRA, 32, 3},
	} {
		stream := append([]byte{0, 0, 1}, header(tc.typ, tc.layer, tc.temporalID)...)
		stream = append(stream, 'x')
		units, err := SplitAnnexB(stream)
		if err != nil {
			t.Errorf("type %d: %v", tc.typ, err)
			continue
		}
		if len(units) != 1 {
			t.Errorf("type %d: %d units", tc.typ, len(units))
			continue
		}
		u := units[0]
		if u.Type != tc.typ {
			t.Errorf("Type = %d, want %d", u.Type, tc.typ)
		}
		if u.LayerID != tc.layer {
			t.Errorf("type %d: LayerID = %d, want %d", tc.typ, u.LayerID, tc.layer)
		}
		if u.TemporalID != tc.temporalID {
			t.Errorf("type %d: TemporalID = %d, want %d", tc.typ, u.TemporalID, tc.temporalID)
		}
		if string(u.Payload) != "x" {
			t.Errorf("type %d: payload %q, want the byte after the header", tc.typ, u.Payload)
		}
	}
}

// TestAUnitTooShortForItsHeaderIsRefused: the second byte carries the sub-layer, so
// a unit holding only the first would have it read out of whatever followed.
func TestAUnitTooShortForItsHeaderIsRefused(t *testing.T) {
	var short *ShortHeaderError
	_, err := SplitAnnexB([]byte{0, 0, 1, 0x40})
	if !errors.As(err, &short) {
		t.Fatalf("err = %v, want a ShortHeaderError", err)
	}
	if short.Unit != 1 || short.Bytes != 1 {
		t.Errorf("%+v, want unit 1 of 1 byte", short)
	}
	if got := short.Error(); got == "" || !bytes.Contains([]byte(got), []byte("needs two")) {
		t.Errorf("Error() = %q", got)
	}
	// And in the length-prefixed form.
	if _, err := SplitLengthPrefixed([]byte{0, 0, 0, 1, 0x40}, 4); !errors.As(err, &short) {
		t.Errorf("length-prefixed: err = %v, want a ShortHeaderError", err)
	}
}

func TestTheFramingIsBitstreamsAndItsRefusalsTravel(t *testing.T) {
	if _, err := SplitAnnexB([]byte{'a', 'b'}); !errors.Is(err, bitstream.ErrNoStartCode) {
		t.Errorf("err = %v, want bitstream.ErrNoStartCode", err)
	}
	if _, err := SplitLengthPrefixed([]byte{0, 0, 0, 99, 1, 2}, 4); !errors.Is(err, bitstream.ErrLengthOverrun) {
		t.Errorf("err = %v, want bitstream.ErrLengthOverrun", err)
	}
	// The length-prefixed form reads the same header.
	stream := append([]byte{0, 0, 0, 3}, header(UnitSPS, 0, 0)...)
	stream = append(stream, 'z')
	units, err := SplitLengthPrefixed(stream, 4)
	if err != nil {
		t.Fatalf("SplitLengthPrefixed: %v", err)
	}
	if len(units) != 1 || units[0].Type != UnitSPS || string(units[0].Payload) != "z" {
		t.Errorf("%+v", units)
	}
}

// TestZeroBytesAfterALengthPrefixedUnitAreTheFramings: ffmpeg's MP4 muxer leaves the zero byte of a
// four-byte start code on the end of the unit before it, and a unit never ends in 0x00 (7.4.2), so
// the length-prefixed form gives the units the byte stream does; the header is never trimmed.
func TestZeroBytesAfterALengthPrefixedUnitAreTheFramings(t *testing.T) {
	annexB := []byte{0, 0, 0, 1, 0x40, 1, 0xaa, 0xbb, 0, 0, 0, 1, 0x42, 1, 0xcc, 0, 0, 0, 1, 0x26, 1, 0xdd, 0xee}
	want, err := SplitAnnexB(annexB)
	if err != nil {
		t.Fatal(err)
	}
	var prefixed []byte
	for i, u := range want {
		body := append(header(u.Type, 0, 0), u.Payload...)
		if i < 2 { // the zero byte the muxer leaves, once and several times
			body = append(body, make([]byte, i+1)...)
		}
		prefixed = append(prefixed, 0, 0, 0, byte(len(body)))
		prefixed = append(prefixed, body...)
	}
	got, err := SplitLengthPrefixed(prefixed, 4)
	if err != nil || len(got) != len(want) {
		t.Fatalf("%d units, %v", len(got), err)
	}
	for i := range want {
		if got[i].Type != want[i].Type || !bytes.Equal(got[i].Payload, want[i].Payload) {
			t.Errorf("unit %d: %+v, the byte stream gives %+v", i, got[i], want[i])
		}
	}
	// Zero payload bytes only: the header stays, the payload is empty.
	units, err := SplitLengthPrefixed([]byte{0, 0, 0, 4, 0x04, 0x01, 0, 0}, 4)
	if err != nil || len(units) != 1 || units[0].Type != UnitTSAN || len(units[0].Payload) != 0 {
		t.Errorf("%+v, %v", units, err)
	}
	// A header that ends in a zero byte keeps it: the second byte of a unit is
	// nuh_temporal_id_plus1 and never 0, the first can be (type 0, layer 0 is 0x00 0x01).
	units, err = SplitLengthPrefixed([]byte{0, 0, 0, 3, 0x00, 0x01, 0}, 4)
	if err != nil || len(units) != 1 || units[0].Type != UnitTrailN || len(units[0].Payload) != 0 {
		t.Errorf("%+v, %v", units, err)
	}
}

// TestWhichTypesCarryPicturesAndWhichCanStandAlone.
//
// ⛔ HEVC names sixteen kinds of slice segment where H.264 has one flag, and which
// of them can be decoded with nothing before it is what a sample table needs. The
// boundary is written down in one place rather than scattered, and this is what
// says where it is.
func TestWhichTypesCarryPicturesAndWhichCanStandAlone(t *testing.T) {
	for typ := 0; typ < 64; typ++ {
		t2 := UnitType(typ)
		wantSlice := typ <= 31
		if got := t2.IsSliceSegment(); got != wantSlice {
			t.Errorf("type %d: IsSliceSegment = %v, want %v", typ, got, wantSlice)
		}
		wantSync := typ >= 16 && typ <= 21
		if got := t2.IsRandomAccess(); got != wantSync {
			t.Errorf("type %d: IsRandomAccess = %v, want %v", typ, got, wantSync)
		}
	}
	// The named ones, so a rename cannot quietly move the boundary.
	for _, typ := range []UnitType{UnitBLAWLP, UnitIDRWRADL, UnitIDRNLP, UnitCRA} {
		if !typ.IsRandomAccess() {
			t.Errorf("type %d is a random access point and was not reported as one", typ)
		}
	}
	for _, typ := range []UnitType{UnitTrailN, UnitTrailR} {
		if typ.IsRandomAccess() {
			t.Errorf("type %d is a trailing picture and was called a random access point", typ)
		}
	}
	for _, typ := range []UnitType{UnitVPS, UnitSPS, UnitPPS, UnitAUD, UnitEOS, UnitEOB, UnitFD, UnitPrefixSEI, UnitSuffixSEI} {
		if typ.IsSliceSegment() {
			t.Errorf("type %d carries no picture and was called a slice segment", typ)
		}
	}
}

func TestUnescapeIsBitstreams(t *testing.T) {
	u := Unit{Payload: []byte{0, 0, 3, 1}}
	if got := u.Unescape(); !bytes.Equal(got, []byte{0, 0, 1}) {
		t.Errorf("Unescape = %v", got)
	}
}

func TestItoaCoversWhatTheMessageNeeds(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "0"}, {1, "1"}, {9, "9"}, {10, "10"}, {4095, "4095"}} {
		if got := itoa(tc.n); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestTemporalIDIsThePlusOneMinusOne: the three bits on the wire are
// nuh_temporal_id_plus1, so a base layer unit states 1 and has TemporalId 0, and a
// unit stating 0 is refused (7.4.2.2). Reading the wire value as TemporalId plus one
// made every unit of a real stream look like an enhancement layer one.
func TestTemporalIDIsThePlusOneMinusOne(t *testing.T) {
	for wire := byte(1); wire <= 7; wire++ {
		units, err := SplitAnnexB([]byte{0, 0, 1, byte(UnitTrailR) << 1, wire, 'x'})
		if err != nil || len(units) != 1 || units[0].TemporalID != wire-1 {
			t.Errorf("wire value %d: %v %+v", wire, err, units)
		}
	}
	if _, err := SplitAnnexB([]byte{0, 0, 1, byte(UnitTrailR) << 1, 0, 'x'}); !errors.Is(err, ErrTemporalIDPlus1) {
		t.Errorf("nuh_temporal_id_plus1 of 0: %v", err)
	}
}
