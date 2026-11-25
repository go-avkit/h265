// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"bytes"
	"os"
	"testing"
)

// Both defects were found by an independent security review of go-images/heif; the
// reproducer is testdata/secfix/flat256.hevc, a 256x256 flat picture.

func flat256Units(t testing.TB) (sets, slices []Unit) {
	t.Helper()
	data, err := os.ReadFile("testdata/secfix/flat256.hevc")
	if err != nil {
		t.Fatal(err)
	}
	units, err := SplitAnnexB(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range units {
		switch {
		case u.Type.IsSliceSegment():
			slices = append(slices, u)
		case u.Type == UnitSPS || u.Type == UnitPPS:
			sets = append(sets, u)
		}
	}
	if len(slices) != 1 || len(sets) != 2 {
		t.Fatalf("flat256: %d slices, %d sets", len(slices), len(sets))
	}
	return sets, slices
}

func bitAt(b []byte, i int) uint8 { return b[i/8] >> (7 - i%8) & 1 }

// notFirst rewrites the first slice segment u of a picture as an independent segment
// that is not the first, at slice_segment_address addr, with sps and pps the sets
// the header is read against.
func notFirst(t testing.TB, u Unit, sps SPS, pps PPS, addr uint32) Unit {
	t.Helper()
	h, err := ParseSliceSegmentHeaderFull(u, sps, pps, nil)
	if err != nil {
		t.Fatal(err)
	}
	un := u.Unescape()
	pos := 0
	if u.Type.IsRandomAccess() {
		pos++
	}
	z := 0
	for bitAt(un, pos+1+z) == 0 {
		z++
	}
	end := pos + 1 + 2*z + 1 // after slice_pic_parameter_set_id
	var bits []uint8
	bits = append(bits, 0) // first_slice_segment_in_pic_flag
	for i := 1; i < end; i++ {
		bits = append(bits, bitAt(un, i))
	}
	if pps.DependentSlicesEnabled {
		bits = append(bits, 0)
	}
	for i := ceilLog2(int(picSizeInCtbs(sps))) - 1; i >= 0; i-- {
		bits = append(bits, uint8(addr>>uint(i)&1))
	}
	for i := end; i < h.HeaderBits; i++ {
		bits = append(bits, bitAt(un, i))
	}
	bits = append(bits, 1)
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	out := make([]byte, len(bits)/8, len(bits)/8+len(un))
	for i, b := range bits {
		out[i/8] |= b << (7 - i%8)
	}
	out = append(out, un[h.DataOffset:]...)
	return Unit{Type: u.Type, Payload: escapeRBSP(out)}
}

func flat256Params(t testing.TB, sets []Unit) (sps SPS, pps PPS) {
	t.Helper()
	var err error
	for _, u := range sets {
		if u.Type == UnitSPS {
			sps, err = ParseSPS(u)
		} else {
			pps, err = ParsePPS(u)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return sps, pps
}

// Parameter sets that arrive between the slice segments of a picture, under the ids the
// picture was begun with, do not apply to it (7.4.2.4.2): the segments are decoded
// against the sets the picture began with. Before, a non-first segment of a small
// picture read the sets of a larger one and indexed the small picture's maps with
// the larger geometry: index out of range.
func TestParameterSetsSwappedMidPictureDoNotApplyToIt(t *testing.T) {
	sps, small, sets, swapped := swapStream(t)
	d := NewDecoder(Options{})
	var errs []error
	feed := func(us ...Unit) {
		for _, u := range us {
			if _, err := d.Decode(u); err != nil {
				errs = append(errs, err)
			}
		}
	}
	feed(small...)
	feed(sets...)
	feed(swapped)
	if _, err := d.Flush(); err != nil {
		errs = append(errs, err)
	}
	// the fuzz seed is the same stream
	all := append(append(append([]Unit{}, small...), sets...), swapped)
	back, err := SplitAnnexB(annexB(all...))
	if err != nil || len(back) != len(all) || back[len(back)-1].Type != swapped.Type || !bytes.Equal(back[len(back)-1].Payload, swapped.Payload) {
		t.Errorf("the seed stream: %d units back of %d: %v", len(back), len(all), err)
	}
	if len(errs) == 0 {
		t.Error("a segment of the larger geometry was decoded into the smaller picture")
	}
	// the sets that arrived are in force for the next picture
	if d.spsM[sps.ID].CodedWidth != 256 {
		t.Errorf("the new sequence parameter set was not kept: %d wide", d.spsM[sps.ID].CodedWidth)
	}
}

// annexB is units as a byte stream: start codes and two-byte headers (7.3.1.1).
func annexB(units ...Unit) []byte {
	var out []byte
	for _, u := range units {
		out = append(out, 0, 0, 0, 1, byte(u.Type)<<1|u.LayerID>>5, u.LayerID<<3|u.TemporalID+1)
		out = append(out, u.Payload...)
	}
	return out
}

// swapStream is the stream of the review: a small picture's sets and first slice segment,
// then the sets of a 256x256 picture under the same ids, then a segment of that larger
// picture that is not the first, at its last address. It returns the larger sequence
// parameter set, the small picture's units, the larger sets, and the late segment.
func swapStream(t testing.TB) (sps SPS, small, sets []Unit, swapped Unit) {
	seed := seedUnits(t)
	// the sets and the first slice segment of the first picture of the seed
	for _, u := range seed {
		if u.Type.IsSliceSegment() {
			small = append(small, u)
			break
		}
		if u.Type == UnitSPS || u.Type == UnitPPS {
			small = append(small, u)
		}
	}
	sets, slices := flat256Units(t)
	sps, pps := flat256Params(t, sets)
	return sps, small, sets, notFirst(t, slices[0], sps, pps, uint32(picSizeInCtbs(sps)-1))
}

// A slice that names a picture parameter set the decoder never received is refused, and
// no picture is begun for it.
func TestASliceWithNoPictureParameterSetIsRefused(t *testing.T) {
	d := NewDecoder(Options{})
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("a slice with no picture parameter set was decoded: %v", r)
		}
	}()
	outs, errs := decodeUnits(d, without(seedUnits(t), UnitPPS))
	if !hasErr(errs, ErrMissingParameterSet) {
		t.Errorf("no picture parameter set: %v", errs)
	}
	if len(outs) != 0 || d.stats.Pictures != 0 {
		t.Errorf("%d outputs, %d pictures from slices with no picture parameter set", len(outs), d.stats.Pictures)
	}
}

// Slice segments of a picture come in increasing order of address: a segment that
// starts at or before the end of the one before is refused and the picture is lost,
// instead of each one decoding the same coding tree blocks again.
func TestASliceSegmentThatRestartsThePictureIsRefused(t *testing.T) {
	sets, slices := flat256Units(t)
	sps, pps := flat256Params(t, sets)
	again := notFirst(t, slices[0], sps, pps, 0)
	d := NewDecoder(Options{})
	units := append(append([]Unit{}, sets...), slices[0])
	for i := 0; i < 50; i++ {
		units = append(units, again)
	}
	_, errs := decodeUnits(d, units)
	if !hasErr(errs, ErrCorruptPicture) || !hasErr(errs, ErrSliceData) {
		t.Fatalf("a segment at address 0 after the first: %v", errs)
	}
	if len(errs) != 1 {
		t.Errorf("%d refusals: the later segments of a lost picture are skipped, not decoded", len(errs))
	}
	if d.stats.Failed != 1 {
		t.Errorf("failed pictures: %d", d.stats.Failed)
	}
}
