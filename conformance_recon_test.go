// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"
	"testing"
)

// TestIntraPicturesOfTheConformanceSuiteMatchTheirHashes reconstructs every picture
// whose slices are all intra coded and compares it with its decoded picture hash
// SEI message (D.3.20), the oracle no part of this package wrote. A picture can
// only be compared once the in-loop filters are in, since the hash is of the
// filtered picture: until then the pictures with deblocking and sample adaptive
// offset off in every slice are the ones that are, and the others are decoded for
// their reconstruction to run without error.
func TestIntraPicturesOfTheConformanceSuiteMatchTheirHashes(t *testing.T) {
	p := conformance(t)
	var verified, mismatched, unfiltered, unhashed, skippedPics int
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		layouts, epoch := map[[2]int]*CtbLayout{}, 0
		var prev *SliceHeader
		var pic *PictureState
		var frame *DecodedPicture
		var curSPS SPS
		var curPPS PPS
		allI, saoOff, ok, have, deblocked := true, true, true, false, false
		n := 0
		for _, u := range list {
			switch {
			case u.Type == UnitSPS:
				epoch++
				if s, err := ParseSPS(u); err == nil {
					spsByID[s.ID] = s
				}
			case u.Type == UnitPPS:
				epoch++
				if pps, err := ParsePPS(u); err == nil {
					ppsByID[pps.ID] = pps
				}
			case u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA):
				r := newSticky(u.Unescape())
				first := r.bit() == 1
				if u.Type.IsRandomAccess() {
					r.bit()
				}
				pps, ok1 := ppsByID[r.ue()]
				sps, ok2 := spsByID[pps.SPSID]
				if !ok1 || !ok2 {
					have = false
					continue
				}
				h, err := ParseSliceSegmentHeaderFull(u, sps, pps, prev)
				if err != nil {
					t.Errorf("%s: slice %d: %v", rel, n, err)
					continue
				}
				if !h.Dependent {
					hh := h
					prev = &hh
				}
				key := [2]int{int(pps.ID), epoch}
				l := layouts[key]
				if l == nil {
					if l, err = NewCtbLayout(sps, pps); err != nil {
						t.Fatalf("%s: %v", rel, err)
					}
					layouts[key] = l
				}
				if first || pic == nil {
					pic, frame, curSPS, curPPS = NewPictureState(sps, l), NewDecodedPicture(sps), sps, pps
					allI, saoOff, ok, have, deblocked = true, true, true, true, false
					n++
				}
				if h.Type != SliceI {
					allI = false
				}
				if h.Refs.SAOLuma || h.Refs.SAOChroma {
					saoOff = false
				}
				if !allI || !ok {
					continue
				}
				if err := DecodeSliceData(pic, u, h, pps, NewReconstructor(frame, sps, pps).Decoder()); err != nil {
					t.Errorf("%s: picture %d: %v", rel, n, err)
					ok = false
				}
			case u.Type == UnitSuffixSEI:
				msgs, err := ParseSEI(u)
				if err != nil || !have {
					continue
				}
				for _, m := range msgs {
					if m.Type != SEIPayloadDecodedPictureHash {
						continue
					}
					hash, valid, err := ParsePictureHash(m.Payload, int(curSPS.ChromaFormat))
					if err != nil || !valid {
						continue
					}
					if allI && ok && !deblocked {
						pic.Deblock(frame, curSPS, curPPS)
						deblocked = true
					}
					switch {
					case !allI || !ok:
						skippedPics++
					case !saoOff:
						unfiltered++
					default:
						if err := hash.Verify(frame); err != nil {
							mismatched++
							t.Errorf("%s: picture %d: %v", rel, n, err)
						} else {
							verified++
						}
					}
				}
			}
		}
		_ = unhashed
	}
	t.Log(fmt.Sprintf("conformance-count: intra-hash verified=%d mismatched=%d filtered-not-compared=%d", verified, mismatched, unfiltered))
	if verified == 0 || mismatched != 0 {
		t.Fatalf("%d pictures match their hash, %d do not", verified, mismatched)
	}
}
