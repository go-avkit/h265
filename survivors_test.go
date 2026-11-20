// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"strings"
	"testing"
)

// The tests below hold guards that a mutation sweep found nothing holding (decoder.go's skip
// guard before a slice is decoded, slicedata.go's two checks in substreamStarts).

// segmentDecoder is a decoder that has the parameter sets of a picture of 12 coding tree
// blocks with dependent slice segments allowed.
func segmentDecoder(t *testing.T) *Decoder {
	t.Helper()
	d := NewDecoder(Options{})
	s := set{chroma: 1, width: 64, height: 48, subLayers: 1}
	for _, u := range []Unit{s.build(), pset{dependentSlices: true}.build()} {
		if _, err := d.Decode(u); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// firstSegment is the first slice segment of a picture of the type typ (an I slice, one byte of
// data, which is not a picture); next is a dependent segment of the same picture at block 5.
func firstSegment(typ UnitType) Unit {
	return hdrUnit(typ, func(w *writer) {
		w.bit(1) // first_slice_segment_in_pic_flag
		if typ.IsRandomAccess() {
			w.bit(0) // no_output_of_prior_pics_flag
		}
		w.ue(0) // slice_pic_parameter_set_id
		w.ue(2) // slice_type I
		if !typ.IsIDR() {
			w.bits(0, 8) // slice_pic_order_cnt_lsb
			w.bit(0)     // short_term_ref_pic_set_sps_flag
			w.ue(0)
			w.ue(0)
		}
		w.se(0)  // slice_qp_delta
		w.bit(1) // slice_loop_filter_across_slices_enabled_flag
	})
}

func nextSegment(typ UnitType) Unit {
	return hdrUnit(typ, func(w *writer) {
		w.bit(0) // first_slice_segment_in_pic_flag
		if typ.IsRandomAccess() {
			w.bit(0) // no_output_of_prior_pics_flag
		}
		w.ue(0)
		w.bit(1) // dependent_slice_segment_flag
		w.bits(5, 4)
	})
}

// TestASegmentOfAFailedPictureIsNotDecoded: the first segment of a picture fails (its data is
// one byte), and a later segment of the same picture is passed over without a second error.
func TestASegmentOfAFailedPictureIsNotDecoded(t *testing.T) {
	d := segmentDecoder(t)
	if _, err := d.Decode(firstSegment(UnitIDRNLP)); !hasErr([]error{err}, ErrCorruptPicture) {
		t.Fatalf("the first segment: %v, want a corrupt picture", err)
	}
	var err error
	noPanic(t, "a segment after a failure", func() { _, err = d.Decode(nextSegment(UnitIDRNLP)) })
	if err != nil {
		t.Errorf("a segment of a failed picture was decoded: %v", err)
	}
}

// TestASegmentOfASkippedPictureIsNotDecoded: a RASL picture that follows the random access point
// the stream starts with is skipped, and so are its later segments (no picture is being decoded
// for them).
func TestASegmentOfASkippedPictureIsNotDecoded(t *testing.T) {
	d := segmentDecoder(t)
	d.Decode(firstSegment(UnitCRA))
	if _, err := d.Decode(firstSegment(UnitRASLN)); err != nil || d.Stats().SkippedRASL != 1 {
		t.Fatalf("the RASL picture: %v, %+v", err, d.Stats())
	}
	var err error
	noPanic(t, "a segment of a skipped picture", func() { _, err = d.Decode(nextSegment(UnitRASLN)) })
	if err != nil {
		t.Errorf("a segment of a skipped picture was decoded: %v", err)
	}
}

// TestSubstreamStartsChecksTheDataOffsetOnlyWithEntryPoints: with no entry points nothing is
// looked up and the offset is not examined here (DecodeSliceData bounds it); with entry points
// an offset that is no byte of the payload is refused as such, not as a bad entry point.
func TestSubstreamStartsChecksTheDataOffsetOnlyWithEntryPoints(t *testing.T) {
	payload := []byte{1, 2, 3, 4, 5, 6}
	if starts, err := substreamStarts(payload, SliceHeader{DataOffset: 99}); starts != nil || err != nil {
		t.Errorf("no entry points: %v, %v", starts, err)
	}
	_, err := substreamStarts(payload, SliceHeader{DataOffset: 99, EntryPoints: []uint32{1}})
	if err == nil || !strings.Contains(err.Error(), "slice data offset") {
		t.Errorf("an offset past the payload: %v", err)
	}
	// the offset one past the last byte is the end of the data, and is accepted
	if _, err := substreamStarts(payload, SliceHeader{DataOffset: 6, EntryPoints: []uint32{0}}); err != nil {
		t.Errorf("an offset at the end of the payload: %v", err)
	}
}
