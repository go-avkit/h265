// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// ffmpegPOC is ff_hevc_compute_poc2 from FFmpeg's libavcodec/hevc/ps.c,
// transcribed as it stands (read 2026-10-08). It is here as a SECOND
// INSTRUMENT: an independent statement of the same derivation, so agreement
// across every input is evidence about the derivation and not about one example
// each implementation happens to get right.
//
//	int max_poc_lsb  = 1 << log2_max_poc_lsb;
//	int prev_poc_lsb = pocTid0 % max_poc_lsb;
//	int prev_poc_msb = pocTid0 - prev_poc_lsb;
//	if (poc_lsb < prev_poc_lsb && prev_poc_lsb - poc_lsb >= max_poc_lsb / 2)
//	    poc_msb = prev_poc_msb + max_poc_lsb;
//	else if (poc_lsb > prev_poc_lsb && poc_lsb - prev_poc_lsb > max_poc_lsb / 2)
//	    poc_msb = prev_poc_msb - max_poc_lsb;
//	else
//	    poc_msb = prev_poc_msb;
//	return poc_msb + poc_lsb;
func ffmpegPOC(log2MaxPOCLSB uint8, pocTid0 int32, pocLSB int32) int32 {
	maxLSB := int32(1) << log2MaxPOCLSB
	prevLSB := pocTid0 % maxLSB
	prevMSB := pocTid0 - prevLSB
	var msb int32
	switch {
	case pocLSB < prevLSB && prevLSB-pocLSB >= maxLSB/2:
		msb = prevMSB + maxLSB
	case pocLSB > prevLSB && pocLSB-prevLSB > maxLSB/2:
		msb = prevMSB - maxLSB
	default:
		msb = prevMSB
	}
	return msb + pocLSB
}

// Every count the carry can hold, against every low value it can state, for two
// widths. A disagreement anywhere is a disagreement about the rule.
func TestPOCAgreesWithASecondImplementationEverywhere(t *testing.T) {
	for _, log2 := range []uint8{4, 8} {
		maxLSB := int32(1) << log2
		sps := SPS{Log2MaxPOCLSB: log2}
		checked := 0
		for prev := int32(0); prev < maxLSB*4; prev++ {
			for lsb := int32(0); lsb < maxLSB; lsb++ {
				c := POCCounter{prevLSB: prev % maxLSB, prevMSB: prev - prev%maxLSB, started: true}
				got, err := c.Next(sliceUnit(t, UnitTrailR, 0, sps, PPS{}, uint32(lsb)), sps, PPS{})
				if err != nil {
					t.Fatalf("log2=%d prev=%d lsb=%d: %v", log2, prev, lsb, err)
				}
				if want := ffmpegPOC(log2, prev, lsb); got.Value != want {
					t.Fatalf("log2=%d prev=%d lsb=%d: got %d, the second implementation says %d",
						log2, prev, lsb, got.Value, want)
				}
				checked++
			}
		}
		if checked != int(maxLSB*4*maxLSB) {
			t.Fatalf("log2=%d: checked %d pairs, not the %d the loops should cover",
				log2, checked, maxLSB*4*maxLSB)
		}
		t.Logf("log2=%d: %d pairs agree", log2, checked)
	}
}

// A wrap is the whole reason this is a counter. With four bits of low count the
// order has to keep rising past sixteen.
func TestPOCKeepsRisingThroughAWrap(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 4}
	var c POCCounter
	var got []int32
	// An IDR, then counts that wrap twice.
	units := []struct {
		typ UnitType
		lsb uint32
	}{{UnitIDRWRADL, 0}}
	for i := uint32(1); i < 40; i++ {
		units = append(units, struct {
			typ UnitType
			lsb uint32
		}{UnitTrailR, i % 16})
	}
	for i, u := range units {
		p, err := c.Next(sliceUnit(t, u.typ, 0, sps, PPS{}, u.lsb), sps, PPS{})
		if err != nil {
			t.Fatalf("picture %d: %v", i, err)
		}
		got = append(got, p.Value)
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[i-1]+1 {
			t.Fatalf("picture %d has count %d after %d: the order did not rise by one",
				i, got[i], got[i-1])
		}
	}
	if got[len(got)-1] != 39 {
		t.Errorf("last count = %d; want 39", got[len(got)-1])
	}
}

// An IDR states no count at all, and a BLA restarts the high bits however far
// the count had got, because a broken link is a splice.
func TestIDRIsZeroAndBLARestarts(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 4}
	c := POCCounter{prevLSB: 1000 % 16, prevMSB: 1000 - 1000%16, started: true}
	idr, err := c.Next(sliceUnit(t, UnitIDRNLP, 0, sps, PPS{}, 0), sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if idr.Value != 0 {
		t.Errorf("IDR count = %d; want 0", idr.Value)
	}
	c2 := POCCounter{prevLSB: 1000 % 16, prevMSB: 1000 - 1000%16, started: true}
	bla, err := c2.Next(sliceUnit(t, UnitBLAWLP, 0, sps, PPS{}, 5), sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if bla.Value != 5 {
		t.Errorf("BLA count = %d; want 5, the low bits with nothing carried in", bla.Value)
	}
}

// What the carry rule is for: a thinned stream drops exactly the pictures that
// must not be carried from, so the order of what remains cannot depend on them.
func TestOnlySubLayerZeroReferencePicturesCarry(t *testing.T) {
	for _, c := range []struct {
		name     string
		typ      UnitType
		temporal uint8
		carries  bool
	}{
		{"a trailing reference picture of sub-layer zero", UnitTrailR, 0, true},
		{"the same picture in a higher sub-layer", UnitTrailR, 1, false},
		{"a sub-layer non-reference", UnitTrailN, 0, false},
		{"a leading picture that can be decoded", UnitRADLR, 0, false},
		{"a leading picture that may not be", UnitRASLR, 0, false},
		{"an IDR", UnitIDRWRADL, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			sps := SPS{Log2MaxPOCLSB: 4}
			var counter POCCounter
			p, err := counter.Next(sliceUnit(t, c.typ, c.temporal, sps, PPS{}, 3), sps, PPS{})
			if err != nil {
				t.Fatal(err)
			}
			if p.Carried != c.carries {
				t.Errorf("Carried = %v; want %v", p.Carried, c.carries)
			}
			if counter.started != c.carries {
				t.Errorf("the counter %s remember this picture",
					map[bool]string{true: "did not", false: "did"}[c.carries])
			}
		})
	}
}

func TestPOCRefusesWhatItCannotCount(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 4}
	var c POCCounter
	if _, err := c.Next(Unit{Type: UnitSPS}, sps, PPS{}); !errors.Is(err, ErrNotSlice) {
		t.Errorf("a parameter set was counted: %v", err)
	}
	if _, err := c.Next(sliceUnit(t, UnitTrailR, 0, sps, PPS{}, 1), SPS{}, PPS{}); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a sequence parameter set that was never parsed was accepted: %v", err)
	}
	// A segment that is not its picture's first states no order of its own.
	u := sliceUnit(t, UnitTrailR, 0, sps, PPS{}, 1)
	u.Payload[0] &^= 0x80 // first_slice_segment_in_pic_flag off
	if _, err := c.Next(u, sps, PPS{}); !errors.Is(err, ErrNoOrder) {
		t.Errorf("a later segment was counted: %v", err)
	}
}

// Reset is what a seek is.
func TestResetForgetsTheCarry(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 4}
	c := POCCounter{prevLSB: 1000 % 16, prevMSB: 1000 - 1000%16, started: true}
	c.Reset()
	p, err := c.Next(sliceUnit(t, UnitTrailR, 0, sps, PPS{}, 3), sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Value != 3 {
		t.Errorf("after a reset the count is %d; want 3, with nothing carried in", p.Value)
	}
}

// --- building a slice segment to count --------------------------------------

// bitw writes the few fields a slice header needs before the order count, so the
// tests start from bytes rather than from a struct the parser never sees.
type bitw struct {
	b []byte
	n uint8 // bits used in the last byte
}

func (w *bitw) bit(v uint32) {
	if w.n == 0 {
		w.b = append(w.b, 0)
		w.n = 8
	}
	w.n--
	if v&1 == 1 {
		w.b[len(w.b)-1] |= 1 << w.n
	}
}

func (w *bitw) bits(v uint32, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bit(v >> uint(i))
	}
}

// ue writes an unsigned Exp-Golomb value.
func (w *bitw) ue(v uint32) {
	v++
	n := 0
	for x := v; x > 1; x >>= 1 {
		n++
	}
	for i := 0; i < n; i++ {
		w.bit(0)
	}
	w.bits(v, n+1)
}

// sliceUnit builds a first slice segment stating one picture order count.
func sliceUnit(t *testing.T, typ UnitType, temporal uint8, sps SPS, pps PPS, lsb uint32) Unit {
	t.Helper()
	var w bitw
	w.bit(1) // first_slice_segment_in_pic_flag
	if typ.IsRandomAccess() {
		w.bit(0) // no_output_of_prior_pics_flag
	}
	w.ue(uint32(pps.ID)) // slice_pic_parameter_set_id
	for i := uint8(0); i < pps.ExtraSliceHeaderBits; i++ {
		w.bit(0)
	}
	w.ue(uint32(SliceI)) // slice_type
	if pps.OutputFlagPresent {
		w.bit(1)
	}
	if sps.SeparatePlanes {
		w.bits(0, 2)
	}
	if !typ.IsIDR() {
		w.bits(lsb, int(sps.Log2MaxPOCLSB))
	}
	// Trailing bits, so the payload is whole bytes and nothing reads past it.
	w.bit(1)
	for w.n != 0 {
		w.bit(0)
	}
	return Unit{Type: typ, TemporalID: temporal, Payload: w.b}
}

// Where reconstructing the two halves from their sum stops working.
//
// FFmpeg derives the previous low bits as pocTid0 % max_poc_lsb. That is exact
// while the count is positive and wrong once it is not: in C as in Go the
// remainder of a negative number is negative. Brute-forced over every negative
// carry within four wraps at four bits, the two forms disagree on 84 of 1024
// cases.
//
// 8.3.1 keeps prevPicOrderCntLsb and prevPicOrderCntMsb APART, and the low part
// is what the picture's slice header STATED, which is always in [0, maxLsb). A
// count of -63 at four bits was stated as a low part of 1 against a high part of
// -64, not as a low part of -15. So the two-part form is the one that follows
// the bits on the wire, and it is what this keeps.
//
// Whether a conforming stream ever carries from a negative count is another
// question -- prevTid0Pic is a non-leading sub-layer-zero reference picture --
// and this does not depend on the answer.
func TestTheCarryIsKeptAsTwoPartsNotOneSum(t *testing.T) {
	const log2 = 4
	sps := SPS{Log2MaxPOCLSB: log2}
	// A picture whose count is -63: stated low part 1, high part -64.
	c := POCCounter{prevLSB: 1, prevMSB: -64, started: true}
	got, err := c.Next(sliceUnit(t, UnitTrailR, 0, sps, PPS{}, 10), sps, PPS{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != -70 {
		t.Errorf("count = %d; want -70", got.Value)
	}
	// And the contrast, so this test fails if the two stop differing here and
	// it quietly stops testing anything.
	if bad := ffmpegPOC(log2, -63, 10); bad == got.Value {
		t.Fatalf("the sum-and-remainder form now agrees (%d): this case no longer separates them", bad)
	} else if bad != -54 {
		t.Errorf("the sum-and-remainder form gives %d; this test was written against -54", bad)
	}
}

// The fields between slice_type and the order count have to be stepped over in
// the right order, and each is optional in its own way.
func TestTheOrderCountIsFoundPastEveryOptionalField(t *testing.T) {
	for _, c := range []struct {
		name string
		sps  SPS
		pps  PPS
	}{
		{"nothing in between", SPS{Log2MaxPOCLSB: 4}, PPS{}},
		{"reserved bits", SPS{Log2MaxPOCLSB: 4}, PPS{ExtraSliceHeaderBits: 3}},
		{"an output flag", SPS{Log2MaxPOCLSB: 4}, PPS{OutputFlagPresent: true}},
		{"a colour plane id", SPS{Log2MaxPOCLSB: 4, SeparatePlanes: true}, PPS{}},
		{"all of them, and a wider count",
			SPS{Log2MaxPOCLSB: 8, SeparatePlanes: true},
			PPS{ExtraSliceHeaderBits: 2, OutputFlagPresent: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var counter POCCounter
			p, err := counter.Next(sliceUnit(t, UnitTrailR, 0, c.sps, c.pps, 9), c.sps, c.pps)
			if err != nil {
				t.Fatal(err)
			}
			if p.LSB != 9 {
				t.Errorf("read %d as the stated low bits; want 9", p.LSB)
			}
		})
	}
}

// A payload that stops before the count states nothing, and must say so rather
// than report the zero a short read leaves behind.
func TestATruncatedHeaderIsRefused(t *testing.T) {
	sps := SPS{Log2MaxPOCLSB: 16}
	u := sliceUnit(t, UnitTrailR, 0, sps, PPS{}, 1)
	u.Payload = u.Payload[:1]
	var c POCCounter
	if _, err := c.Next(u, sps, PPS{}); err == nil {
		t.Fatal("a header that stops before the count was counted anyway")
	}
}
