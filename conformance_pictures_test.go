// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
	"testing"
)

// confPicture is the picture of a stream being decoded.
type confPicture struct {
	state  *PictureState
	frame  *DecodedPicture
	sps    SPS
	pps    PPS
	poc    int32
	set    RefPicSet
	rp     RefPics
	output bool
	tid    uint8
	failed error
	hash   *PictureHash
	// intra says every slice so far is an I slice.
	intra bool
}

// TestPicturesOfTheConformanceSuiteMatchTheirHashes decodes every stream of the suite
// from its units to its pictures: the order counts, the reference picture sets and
// the decoded picture buffer, the reference lists of each slice, the walk of the slice
// data with intra and inter reconstruction, then the two in-loop filters. Each picture
// is compared with its decoded picture hash SEI message (D.3.20), the oracle no part
// of this package wrote. A picture that names a reference the buffer lacks, or whose
// slices fail, is counted and listed, never skipped silently.
func TestPicturesOfTheConformanceSuiteMatchTheirHashes(t *testing.T) {
	p := conformance(t)
	var verified, mismatched, unhashed, failed, skippedRASL, interVerified, pictures int
	var perStream []string
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		layouts, epoch := map[[2]int]*CtbLayout{}, 0
		var counter POCCounter
		var seq Sequence
		var prev *SliceHeader
		dpb := &DPB{}
		var cur *confPicture
		streamBad := 0
		note := func(format string, args ...any) {
			streamBad++
			if streamBad <= 2 {
				perStream = append(perStream, rel+": "+fmt.Sprintf(format, args...))
			}
		}
		finish := func() {
			if cur == nil {
				return
			}
			c := cur
			cur = nil
			pictures++
			if c.failed == nil {
				c.state.Deblock(c.frame, c.sps, c.pps)
				c.state.SAO(c.frame, c.sps, c.pps)
				switch {
				case c.hash == nil:
					unhashed++
				default:
					if err := c.hash.Verify(c.frame); err != nil {
						mismatched++
						note("picture %d (POC %d, intra %v): %v", pictures, c.poc, c.intra, err)
					} else {
						verified++
						if !c.intra {
							interVerified++
						}
					}
				}
			} else {
				failed++
				note("picture POC %d: %v", c.poc, c.failed)
			}
			dpb.Finish(&DPBPicture{POC: c.poc, Frame: c.frame, State: c.state, SPS: c.sps, TemporalID: c.tid}, c.output)
		}
		skipping := false
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
			case u.Type == UnitEOS:
				finish()
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
					continue // the header suite counts these (56 refused)
				}
				if !h.Dependent {
					hh := h
					prev = &hh
				}
				if first {
					finish()
					skipping = false
					isFirst, noRasl, skip := seq.Next(u.Type)
					if skip {
						skippedRASL++
						skipping = true
						continue
					}
					poc, err := counter.Next(u, sps, pps)
					if err != nil {
						t.Fatalf("%s: %v", rel, err)
					}
					set, err := Derive(h.Refs, poc.Value, sps)
					if err != nil {
						t.Fatalf("%s: %v", rel, err)
					}
					key := [2]int{int(pps.ID), epoch}
					l := layouts[key]
					if l == nil {
						if l, err = NewCtbLayout(sps, pps); err != nil {
							t.Fatalf("%s: %v", rel, err)
						}
						layouts[key] = l
					}
					in := PictureInfo{Type: u.Type, TemporalID: u.TemporalID, POC: poc.Value, Set: set, SPS: sps,
						NoOutputOfPriorPics: h.NoOutputOfPriorPics, PicOutput: h.PicOutput, NoRaslOutput: noRasl, First: isFirst}
					rp, err := dpb.Begin(in)
					cur = &confPicture{state: NewPictureState(sps, l), frame: NewDecodedPicture(sps), sps: sps, pps: pps,
						poc: poc.Value, set: set, rp: rp, output: h.PicOutput, tid: u.TemporalID, failed: err, intra: true}
				}
				if skipping || cur == nil || cur.failed != nil {
					continue
				}
				rec := NewReconstructor(cur.frame, sps, pps)
				cur.state.Inter = nil
				if h.Type != SliceI {
					cur.intra = false
					sl, err := confInterSlice(cur, h, pps)
					if err != nil {
						cur.failed = err
						continue
					}
					cur.state.Inter, rec.Inter = sl, sl
				}
				if err := DecodeSliceData(cur.state, u, h, pps, rec.Decoder()); err != nil {
					cur.failed = err
				} else if err := rec.Err(); err != nil {
					cur.failed = err
				}
			case u.Type == UnitSuffixSEI:
				msgs, err := ParseSEI(u)
				if err != nil || cur == nil {
					continue
				}
				for _, m := range msgs {
					if m.Type != SEIPayloadDecodedPictureHash {
						continue
					}
					hash, valid, err := ParsePictureHash(m.Payload, int(cur.sps.ChromaFormat))
					if err == nil && valid {
						cur.hash = &hash
					}
				}
			}
		}
		finish()
		dpb.Flush()
	}
	for _, s := range perStream {
		t.Log(s)
	}
	t.Log(fmt.Sprintf("conformance-count: picture-decode verified=%d inter-verified=%d mismatched=%d failed=%d unhashed=%d skipped-rasl=%d",
		verified, interVerified, mismatched, failed, unhashed, skippedRASL))
	if verified == 0 || mismatched != 0 || failed != 0 {
		t.Fatalf("%d pictures match their hash, %d do not, %d could not be decoded", verified, mismatched, failed)
	}
}

// confInterSlice makes the InterSlice of a P or B slice of the picture: the reference
// lists of 8.3.4 with the pictures of the buffer behind each entry.
func confInterSlice(c *confPicture, h SliceHeader, pps PPS) (*InterSlice, error) {
	l0, l1, err := Lists(c.set, h.Refs)
	if err != nil {
		return nil, err
	}
	find := func(e RefListEntry) (*DPBPicture, error) {
		if e.LongTerm {
			for i, lt := range c.set.LtCurr {
				if lt.POC == e.POC {
					return c.rp.LtCurr[i], nil
				}
			}
		} else {
			for i, poc := range c.set.StCurrBefore {
				if poc == e.POC {
					return c.rp.StCurrBefore[i], nil
				}
			}
			for i, poc := range c.set.StCurrAfter {
				if poc == e.POC {
					return c.rp.StCurrAfter[i], nil
				}
			}
		}
		return nil, errors.New("a list entry names no picture of the reference picture set")
	}
	var lists [2][]RefEntry
	for l, entries := range [][]RefListEntry{l0, l1} {
		for _, e := range entries {
			pic, err := find(e)
			if err != nil {
				return nil, err
			}
			lists[l] = append(lists[l], RefEntry{POC: pic.POC, LongTerm: e.LongTerm, Pic: pic})
		}
	}
	return NewInterSlice(h, pps, c.poc, lists[0], lists[1])
}
