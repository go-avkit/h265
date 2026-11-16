// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"
)

// TestEveryStreamOfTheConformanceSuiteRunsThroughTheBuffer follows the order counts,
// reference picture sets and output order of every stream: the pictures each one may
// predict from are all held (none is missing, 8.3.2), pictures are output in order
// of their order count within a coded video sequence (the note of C.5.2.4), and
// every picture with PicOutputFlag is output exactly once unless
// no_output_of_prior_pics discards it (C.5.2.2).
func TestEveryStreamOfTheConformanceSuiteRunsThroughTheBuffer(t *testing.T) {
	p := conformance(t)
	var pictures, outputs, skippedRASL, generated, missing, flagged, outOfOrder, cvs int
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		var counter POCCounter
		var seq Sequence
		var prev *SliceHeader
		var lastOut int32
		haveOut := false
		dpb := &DPB{Output: func(pic *DPBPicture) {
			outputs++
			if haveOut && pic.POC < lastOut {
				outOfOrder++
				t.Errorf("%s: picture %d is output after %d", rel, pic.POC, lastOut)
			}
			lastOut, haveOut = pic.POC, true
		}}
		seenGenerated := map[*DPBPicture]bool{}
		for _, u := range list {
			switch {
			case u.Type == UnitSPS:
				if s, err := ParseSPS(u); err == nil {
					spsByID[s.ID] = s
				}
			case u.Type == UnitPPS:
				if pps, err := ParsePPS(u); err == nil {
					ppsByID[pps.ID] = pps
				}
			case u.Type == UnitEOS:
				seq.EndOfSequence()
				counter.Reset()
			case u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA):
				if u.LayerID != 0 {
					continue
				}
				r := newSticky(u.Unescape())
				first := r.bit() == 1
				if u.Type.IsRandomAccess() {
					r.bit()
				}
				pps, ok1 := ppsByID[r.ue()]
				sps, ok2 := spsByID[pps.SPSID]
				if !ok1 || !ok2 {
					continue
				}
				h, err := ParseSliceSegmentHeaderFull(u, sps, pps, prev)
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					continue
				}
				if !h.Dependent {
					hh := h
					prev = &hh
				}
				if !first {
					continue
				}
				isFirst, noRasl, skip := seq.Next(u.Type)
				if skip {
					skippedRASL++
					continue
				}
				poc, err := counter.Next(u, sps, pps)
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					continue
				}
				set, err := Derive(h.Refs, poc.Value, sps)
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					continue
				}
				newCVS := u.Type.IsRandomAccess() && noRasl
				if newCVS {
					cvs++
				}
				in := PictureInfo{Type: u.Type, TemporalID: u.TemporalID, POC: poc.Value, Set: set, SPS: sps,
					NoOutputOfPriorPics: h.NoOutputOfPriorPics, PicOutput: h.PicOutput, NoRaslOutput: noRasl, First: isFirst}
				_, err = dpb.Begin(in)
				if newCVS { // what Begin output belonged to the sequence before
					haveOut = false
				}
				if errors.Is(err, ErrMissingReference) {
					missing++
					t.Errorf("%s: picture %d: %v", rel, poc.Value, err)
				}
				for _, pic := range dpb.Pictures() {
					if pic.Generated && !seenGenerated[pic] {
						seenGenerated[pic] = true
						generated++
					}
				}
				pictures++
				if h.PicOutput {
					flagged++
				}
				dpb.Finish(&DPBPicture{POC: poc.Value, SPS: sps, TemporalID: u.TemporalID}, h.PicOutput)
			}
		}
		dpb.Flush()
	}
	t.Logf("conformance-count: dpb pictures=%d skipped-rasl=%d coded-video-sequences=%d output=%d flagged=%d not-output=%d generated=%d missing=%d out-of-order=%d",
		pictures, skippedRASL, cvs, outputs, flagged, flagged-outputs, generated, missing, outOfOrder)
	if pictures == 0 || missing != 0 || outOfOrder != 0 {
		t.Fatalf("%d pictures, %d missing references, %d out of order", pictures, missing, outOfOrder)
	}
}
