// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// TestEveryDecodedPictureHashOfTheConformanceSuiteParses reads the decoded picture
// hash SEI message of every picture of every stream: the unit parses, the payload
// is the size its hash_type and the picture's chroma_format_idc need, and each
// hash follows a slice of the picture it hashes. These are the hashes that
// reconstruction is checked against; here only their parsing is.
func TestEveryDecodedPictureHashOfTheConformanceSuiteParses(t *testing.T) {
	p := conformance(t)
	var pictures, hashed, bare, orphans int
	var byType [3]int
	var streamsWithout, unreadable int
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		var cur *SPS
		refusedSets := false
		picHashes, streamPics := 0, 0
		hashesOfPic := 0
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
			case u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA):
				r := newSticky(u.Unescape())
				first := r.bit() == 1
				if u.Type.IsRandomAccess() {
					r.bit()
				}
				pps, ok := ppsByID[r.ue()]
				s, ok2 := spsByID[pps.SPSID]
				if !ok || !ok2 {
					cur, refusedSets = nil, true
					continue
				}
				cur = &s
				if first {
					pictures++
					streamPics++
					hashesOfPic = 0
				}
			case u.Type == UnitSuffixSEI:
				msgs, err := ParseSEI(u)
				if err != nil {
					t.Errorf("%s: %v", rel, err)
					continue
				}
				for _, m := range msgs {
					if m.Type != SEIPayloadDecodedPictureHash {
						continue
					}
					if cur == nil {
						// A picture whose parameter sets are refused (bypass alignment).
						if refusedSets {
							unreadable++
						} else {
							orphans++
						}
						continue
					}
					h, ok, err := ParsePictureHash(m.Payload, int(cur.ChromaFormat))
					if err != nil {
						t.Errorf("%s: %v", rel, err)
						continue
					}
					if !ok {
						t.Errorf("%s: a reserved hash_type %d", rel, h.Type)
						continue
					}
					hashesOfPic++
					if hashesOfPic > 1 {
						t.Errorf("%s: a picture with two decoded picture hashes", rel)
					}
					hashed++
					picHashes++
					byType[h.Type]++
				}
			}
		}
		if picHashes == 0 {
			streamsWithout++
		} else if picHashes != streamPics {
			bare += streamPics - picHashes
		}
	}
	t.Logf("conformance-count: picture-hash pictures=%d hashed=%d md5=%d crc=%d checksum=%d streams-without=%d unhashed-in-hashed-streams=%d refused=%d orphans=%d",
		pictures, hashed, byType[0], byType[1], byType[2], streamsWithout, bare, unreadable, orphans)
	if hashed == 0 || orphans != 0 {
		t.Fatalf("%d hashes, %d without a picture", hashed, orphans)
	}
}
