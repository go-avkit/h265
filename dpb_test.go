// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
)

// refDPB is C.5.2 and 8.3.2 as the text has them, one clause to one function, over
// plain records. It shares nothing with DPB but the clause numbers.
type refPic struct {
	poc       int32
	mark      int // 0 unused, 1 short-term, 2 long-term
	needed    bool
	latency   int
	generated bool
}

type refDPB struct {
	pics []*refPic
	out  []int32
}

func (d *refDPB) outputOne() bool { // C.5.2.4
	var pick *refPic
	for _, p := range d.pics {
		if p.needed && (pick == nil || p.poc < pick.poc) {
			pick = p
		}
	}
	if pick == nil {
		return false
	}
	d.out = append(d.out, pick.poc)
	pick.needed = false
	if pick.mark == 0 {
		d.drop(func(p *refPic) bool { return p == pick })
	}
	return true
}

func (d *refDPB) drop(f func(*refPic) bool) {
	var keep []*refPic
	for _, p := range d.pics {
		if !f(p) {
			keep = append(keep, p)
		}
	}
	d.pics = keep
}

func (d *refDPB) count() (n int) {
	for _, p := range d.pics {
		if p.needed {
			n++
		}
	}
	return
}

func (d *refDPB) latent(sps SPS) bool {
	if sps.MaxLatencyIncreasePlus1 == 0 {
		return false
	}
	limit := int(sps.MaxNumReorderPics + sps.MaxLatencyIncreasePlus1 - 1)
	for _, p := range d.pics {
		if p.needed && p.latency >= limit {
			return true
		}
	}
	return false
}

// begin returns the POCs of the five lists as found, and whether a Curr entry was missing.
func (d *refDPB) begin(in PictureInfo) (lists [5][]int32, missing bool) {
	mod := int32(1) << in.SPS.Log2MaxPOCLSB
	if in.Type >= UnitBLAWLP && in.Type <= UnitCRA && in.NoRaslOutput {
		for _, p := range d.pics {
			p.mark = 0
		}
	}
	const none = int32(-1 << 31)
	lt := func(set []LtPicture) []*refPic {
		var out []*refPic
		for _, l := range set {
			var f *refPic
			for _, p := range d.pics {
				if p.mark == 0 {
					continue
				}
				if l.Exact && p.poc == l.POC || !l.Exact && p.poc%mod == l.POC || !l.Exact && p.poc < 0 && ((p.poc%mod)+mod)%mod == l.POC {
					f = p
					break
				}
			}
			out = append(out, f)
		}
		return out
	}
	ltCurr, ltFoll := lt(in.Set.LtCurr), lt(in.Set.LtFoll)
	for _, p := range append(append([]*refPic{}, ltCurr...), ltFoll...) {
		if p != nil {
			p.mark = 2
		}
	}
	st := func(pocs []int32) []*refPic {
		var out []*refPic
		for _, q := range pocs {
			var f *refPic
			for _, p := range d.pics {
				if p.mark == 1 && p.poc == q {
					f = p
					break
				}
			}
			out = append(out, f)
		}
		return out
	}
	before, after, foll := st(in.Set.StCurrBefore), st(in.Set.StCurrAfter), st(in.Set.StFoll)
	member := map[*refPic]bool{}
	for _, l := range [][]*refPic{ltCurr, ltFoll, before, after, foll} {
		for _, p := range l {
			member[p] = true
		}
	}
	for _, p := range d.pics {
		if !member[p] {
			p.mark = 0
		}
	}
	// C.5.2.2
	if in.Type >= UnitBLAWLP && in.Type <= UnitCRA && in.NoRaslOutput && !in.First {
		discard := in.NoOutputOfPriorPics || in.Type == UnitCRA
		if !discard {
			for d.outputOne() {
			}
		}
		d.pics = nil
	} else {
		d.drop(func(p *refPic) bool { return !p.needed && p.mark == 0 })
		for d.count() > int(in.SPS.MaxNumReorderPics) || d.latent(in.SPS) || len(d.pics) >= int(in.SPS.MaxDecPicBufferingMinus1)+1 {
			if !d.outputOne() {
				break
			}
		}
	}
	gen := in.Type >= UnitBLAWLP && in.Type <= UnitBLANLP || in.Type == UnitCRA && in.NoRaslOutput
	if gen {
		for i, p := range foll {
			if p == nil {
				foll[i] = &refPic{poc: in.Set.StFoll[i], mark: 1, generated: true}
				d.pics = append(d.pics, foll[i])
			}
		}
		for i, p := range ltFoll {
			if p == nil {
				ltFoll[i] = &refPic{poc: in.Set.LtFoll[i].POC, mark: 2, generated: true}
				d.pics = append(d.pics, ltFoll[i])
			}
		}
	}
	pocs := func(l []*refPic) (out []int32) {
		for _, p := range l {
			if p == nil {
				out = append(out, none)
				continue
			}
			out = append(out, p.poc)
		}
		return
	}
	lists = [5][]int32{pocs(before), pocs(after), pocs(foll), pocs(ltCurr), pocs(ltFoll)}
	return lists, false
}

func (d *refDPB) finish(poc int32, out bool, sps SPS) {
	if out {
		for _, p := range d.pics {
			if p.needed && p.poc > poc {
				p.latency++
			}
		}
	}
	d.pics = append(d.pics, &refPic{poc: poc, mark: 1, needed: out})
	for d.count() > int(sps.MaxNumReorderPics) || d.latent(sps) {
		if !d.outputOne() {
			break
		}
	}
}

func (d *refDPB) marks() (out [][3]int32) {
	for _, p := range d.pics {
		out = append(out, [3]int32{p.poc, int32(p.mark), int32(b2n(p.needed))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return
}

func dpbSPS(rng *rand.Rand) SPS {
	var s SPS
	s.CodedWidth, s.CodedHeight, s.ChromaFormat, s.BitDepthLuma, s.BitDepthChroma = 16, 16, 1, 8, 8
	s.Width, s.Height = 16, 16
	s.Log2MaxPOCLSB = uint8(4 + rng.IntN(4))
	s.MaxNumReorderPics = uint32(rng.IntN(5))
	s.MaxDecPicBufferingMinus1 = s.MaxNumReorderPics + uint32(rng.IntN(5))
	s.MaxLatencyIncreasePlus1 = uint32(rng.IntN(5))
	return s
}

// TestDPBAgainstTheOutputOrderProcess runs random coded video sequences through the
// buffer and through the model: the pictures the lists find, the markings, and the
// order pictures are output in must agree after every picture.
func TestDPBAgainstTheOutputOrderProcess(t *testing.T) {
	rng := rand.New(rand.NewPCG(181, 182))
	var outputs, generated, discards, longTerm int
	for iter := 0; iter < 600; iter++ {
		sps := dpbSPS(rng)
		mod := int32(1) << sps.Log2MaxPOCLSB
		var got []int32
		d := &DPB{Output: func(p *DPBPicture) { got = append(got, p.POC) }}
		ref := &refDPB{}
		var seq Sequence
		poc := int32(0)
		for n := 0; n < 40; n++ {
			// the picture type: IDR or CRA or BLA now and then, a trailing picture otherwise
			typ := UnitTrailR
			switch r := rng.IntN(25); {
			case n == 0:
				typ = []UnitType{UnitIDRWRADL, UnitCRA, UnitBLAWLP}[rng.IntN(3)]
			case r == 0:
				typ = UnitIDRNLP
			case r == 1:
				typ = UnitCRA
			case r == 2:
				typ = UnitBLANLP
			case r == 3: // a coded video sequence ends, and the next picture is a CRA picture
				seq.EndOfSequence()
				typ = UnitCRA
			}
			first, noRasl, _ := seq.Next(typ)
			irap := typ.IsRandomAccess()
			if irap && noRasl {
				poc = 0
				if typ == UnitCRA || typ.IsBLA() {
					poc = int32(rng.IntN(100)) // a CRA or BLA states its own count
				}
			} else {
				poc += int32(rng.IntN(5)) - 1
			}
			// The reference picture set: a random subset of the pictures held that are marked.
			var set RefPicSet
			if !typ.IsIDR() {
				for _, p := range ref.pics {
					if p.mark == 0 || rng.IntN(3) == 0 || p.poc == poc {
						continue
					}
					switch {
					case p.mark == 2 || rng.IntN(5) == 0:
						// long-term: by its low bits, unless two held pictures share them
						exact := rng.IntN(2) == 0
						dup := false
						for _, q := range ref.pics {
							if q != p && q.mark != 0 && ((q.poc%mod)+mod)%mod == ((p.poc%mod)+mod)%mod {
								dup = true
							}
						}
						lt := LtPicture{POC: p.poc, Exact: true}
						if !exact && !dup {
							lt = LtPicture{POC: ((p.poc % mod) + mod) % mod}
						}
						if rng.IntN(2) == 0 {
							set.LtCurr = append(set.LtCurr, lt)
						} else {
							set.LtFoll = append(set.LtFoll, lt)
						}
						longTerm++
					case rng.IntN(2) == 0 && p.poc < poc:
						set.StCurrBefore = append(set.StCurrBefore, p.poc)
					case rng.IntN(2) == 0 && p.poc > poc:
						set.StCurrAfter = append(set.StCurrAfter, p.poc)
					default:
						set.StFoll = append(set.StFoll, p.poc)
					}
				}
			}
			if rng.IntN(7) == 0 && (typ == UnitCRA || typ.IsBLA()) { // names pictures that are not there
				set.StFoll = append(set.StFoll, poc-1-int32(rng.IntN(3)))
				set.LtFoll = append(set.LtFoll, LtPicture{POC: int32(rng.IntN(int(mod))), Exact: false})
			}
			in := PictureInfo{Type: typ, POC: poc, Set: set, SPS: sps, NoOutputOfPriorPics: rng.IntN(3) == 0,
				PicOutput: rng.IntN(6) != 0, NoRaslOutput: noRasl, First: first}
			refs, err := d.Begin(in)
			wantLists, _ := ref.begin(in)
			if err != nil && !errors.Is(err, ErrMissingReference) {
				t.Fatal(err)
			}
			if err == nil {
				// the same pictures found in the same lists
				gotLists := [5][]int32{pocsOf(refs.StCurrBefore), pocsOf(refs.StCurrAfter), pocsOf(refs.StFoll), pocsOf(refs.LtCurr), pocsOf(refs.LtFoll)}
				for i := range gotLists {
					if !reflect.DeepEqual(gotLists[i], wantLists[i]) && !(len(gotLists[i]) == 0 && len(wantLists[i]) == 0) {
						t.Fatalf("iter %d picture %d (%d): list %d found %v, the model %v", iter, n, poc, i, gotLists[i], wantLists[i])
					}
				}
			}
			// Both models continue only when the sequence is sound.
			if err != nil {
				if !modelMissing(wantLists) {
					t.Fatalf("iter %d picture %d: the buffer reports a missing reference the model does not: %v", iter, n, err)
				}
				break
			}
			for _, p := range refs.StFoll {
				if p != nil && p.Generated {
					generated++
				}
			}
			cur := &DPBPicture{POC: poc, SPS: sps, Frame: NewDecodedPicture(sps)}
			d.Finish(cur, in.PicOutput)
			ref.finish(poc, in.PicOutput, sps)
			if !reflect.DeepEqual(got, ref.out) && !(len(got) == 0 && len(ref.out) == 0) {
				t.Fatalf("iter %d picture %d: output order %v, the model's %v", iter, n, got, ref.out)
			}
			var held [][3]int32
			for _, p := range d.Pictures() {
				held = append(held, [3]int32{p.POC, int32(p.Mark), int32(b2n(p.NeededForOutput))})
			}
			sort.Slice(held, func(i, j int) bool { return held[i][0] < held[j][0] })
			if m := ref.marks(); !reflect.DeepEqual(held, m) {
				t.Fatalf("iter %d picture %d: the buffer holds %v, the model's %v", iter, n, held, m)
			}
			if in.NoOutputOfPriorPics && irap && noRasl && !first {
				discards++
			}
		}
		d.Flush()
		ref.outFlush()
		if !reflect.DeepEqual(got, ref.out) && !(len(got) == 0 && len(ref.out) == 0) {
			t.Fatalf("iter %d: after the flush the output is %v, the model's %v", iter, got, ref.out)
		}
		outputs += len(got)
	}
	t.Logf("outputs %d, generated %d, discards %d, long-term entries %d", outputs, generated, discards, longTerm)
	if outputs == 0 || generated == 0 || discards == 0 || longTerm == 0 {
		t.Error("a case was never exercised")
	}
}

func (d *refDPB) outFlush() {
	for d.outputOne() {
	}
	d.pics = nil
}

func pocsOf(l []*DPBPicture) (out []int32) {
	for _, p := range l {
		if p == nil {
			out = append(out, -1<<31)
		} else {
			out = append(out, p.POC)
		}
	}
	return
}

func modelMissing(lists [5][]int32) bool {
	for _, i := range []int{0, 1, 3} {
		for _, v := range lists[i] {
			if v == -1<<31 {
				return true
			}
		}
	}
	return false
}

func TestSequenceClassification(t *testing.T) {
	var s Sequence
	first, noRasl, skip := s.Next(UnitCRA)
	if !first || !noRasl || skip {
		t.Errorf("a CRA first in the bitstream: %v %v %v", first, noRasl, skip)
	}
	if _, _, skip := s.Next(UnitRASLR); !skip {
		t.Error("a RASL picture of a CRA with NoRaslOutputFlag is not skipped")
	}
	if _, _, skip := s.Next(UnitRADLR); skip {
		t.Error("a RADL picture is skipped")
	}
	if first, noRasl, _ := s.Next(UnitCRA); first || noRasl {
		t.Errorf("a CRA mid-stream: %v %v", first, noRasl)
	}
	if _, _, skip := s.Next(UnitRASLN); skip {
		t.Error("a RASL picture of a CRA mid-stream is skipped")
	}
	s.EndOfSequence()
	if _, noRasl, _ := s.Next(UnitCRA); !noRasl {
		t.Error("a CRA after an end of sequence has NoRaslOutputFlag 0")
	}
	if _, noRasl, _ := s.Next(UnitIDRNLP); !noRasl {
		t.Error("an IDR has NoRaslOutputFlag 0")
	}
	if _, noRasl, _ := s.Next(UnitBLAWLP); !noRasl {
		t.Error("a BLA has NoRaslOutputFlag 0")
	}
}

func TestCropToTheConformanceWindow(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 32, height: 16, window: [4]uint32{1, 2, 1, 0}})
	f := NewDecodedPicture(sps)
	for c := 0; c < 3; c++ {
		for i := 0; i < f.Planes[c].Len(); i++ {
			f.Planes[c].set(i, i%251)
		}
	}
	got := f.Crop(sps)
	// the window is in chroma units: luma 2 columns left, 4 right, 2 rows top, none bottom
	if got.Planes[0].Width != 26 || got.Planes[0].Height != 14 || got.Planes[1].Width != 13 || got.Planes[1].Height != 7 {
		t.Fatalf("sizes %dx%d and %dx%d", got.Planes[0].Width, got.Planes[0].Height, got.Planes[1].Width, got.Planes[1].Height)
	}
	if got.Planes[0].At(0, 0) != f.Planes[0].At(2, 2) || got.Planes[1].At(0, 0) != f.Planes[1].At(1, 1) {
		t.Errorf("the window starts at the wrong sample")
	}
}
