// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"os"
	"runtime"
	"runtime/debug"
	"testing"
)

// seedUnits are the units of the x265 encode the fuzz seed is: B-frames, four references,
// weighted prediction.
func seedUnits(t testing.TB) []Unit {
	t.Helper()
	data, err := os.ReadFile("testdata/fuzzseed/hevc.265")
	if err != nil {
		t.Fatal(err)
	}
	units, err := SplitAnnexB(data)
	if err != nil {
		t.Fatal(err)
	}
	return units
}

func decodeUnits(d *Decoder, units []Unit) (outs []Output, errs []error) {
	for _, u := range units {
		out, err := d.Decode(u)
		outs = append(outs, out...)
		if err != nil {
			errs = append(errs, err)
		}
	}
	out, err := d.Flush()
	outs = append(outs, out...)
	if err != nil {
		errs = append(errs, err)
	}
	return
}

func hasErr(errs []error, target error) bool {
	for _, e := range errs {
		if errors.Is(e, target) {
			return true
		}
	}
	return false
}

func TestDecoderDecodesTheSeedStream(t *testing.T) {
	d := NewDecoder(Options{})
	units := seedUnits(t)
	outs, errs := decodeUnits(d, units)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	s := d.Stats()
	if len(outs) == 0 || len(outs) != s.Pictures || s.Failed != 0 {
		t.Fatalf("%d pictures output, stats %+v", len(outs), s)
	}
	for i, o := range outs {
		if i > 0 && o.POC <= outs[i-1].POC {
			t.Errorf("picture %d is output after %d", o.POC, outs[i-1].POC)
		}
		if o.Picture.Planes[0].Width == 0 || o.Picture.Planes[1].Width == 0 {
			t.Errorf("picture %d: planes %dx%d and %dx%d", o.POC, o.Picture.Planes[0].Width, o.Picture.Planes[0].Height, o.Picture.Planes[1].Width, o.Picture.Planes[1].Height)
		}
	}
	// the decoder can be used for another stream
	again, errs := decodeUnits(d, units)
	if len(errs) != 0 || len(again) != len(outs) {
		t.Errorf("second stream: %d pictures, %v", len(again), errs)
	}
}

// hashUnit is a suffix SEI unit holding a decoded picture hash.
func hashUnit(h PictureHash, typ byte) Unit {
	payload := []byte{typ}
	for c := 0; c < h.Components; c++ {
		payload = append(payload, h.MD5[c][:]...)
	}
	rbsp := append([]byte{byte(SEIPayloadDecodedPictureHash), byte(len(payload))}, payload...)
	return Unit{Type: UnitSuffixSEI, Payload: append(rbsp, 0x80)}
}

func TestDecoderChecksPictureHashes(t *testing.T) {
	units := seedUnits(t)
	// pass 1: the hash of each picture as it is output (all of them are), and the unit
	// each picture starts at
	d := NewDecoder(Options{})
	hashOf := map[int32]PictureHash{}
	output := d.dpb.Output
	d.dpb.Output = func(p *DPBPicture) {
		hashOf[p.POC] = p.Frame.Hash(HashMD5)
		output(p)
	}
	type start struct {
		unit int
		poc  int32
	}
	var starts []start
	var seen *decPicture
	for k, u := range units {
		if _, err := d.Decode(u); err != nil {
			t.Fatal(err)
		}
		if d.cur != nil && d.cur != seen {
			seen = d.cur
			starts = append(starts, start{k, d.cur.poc})
		}
	}
	if _, err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	n := len(starts) - 1
	// the stream again, each picture followed by its hash
	var hashed []Unit
	next := 0
	for k, u := range units {
		if next < len(starts) && starts[next].unit == k {
			if next > 0 {
				hashed = append(hashed, hashUnit(hashOf[starts[next-1].poc], HashMD5))
			}
			next++
		}
		hashed = append(hashed, u)
	}
	tail := hashOf[starts[n].poc]
	hashed = append(hashed, hashUnit(tail, HashMD5))

	v := NewDecoder(Options{VerifyHash: true})
	if _, errs := decodeUnits(v, hashed); len(errs) != 0 {
		t.Fatal(errs)
	}
	if s := v.Stats(); s.Verified != n+1 || s.Mismatched != 0 || s.Unhashed != 0 || s.VerifiedInter == 0 || s.VerifiedInter >= s.Verified {
		t.Errorf("stats %+v for %d pictures", s, n+1)
	}

	// one hash that is wrong
	bad := append([]Unit(nil), hashed...)
	for i, u := range bad {
		if u.Type == UnitSuffixSEI && i > 4 {
			p := append([]byte(nil), u.Payload...)
			p[5] ^= 1
			bad[i] = Unit{Type: UnitSuffixSEI, Payload: p}
			break
		}
	}
	m := NewDecoder(Options{VerifyHash: true})
	_, errs := decodeUnits(m, bad)
	if !hasErr(errs, ErrHashMismatch) || m.Stats().Mismatched != 1 || m.Stats().Verified != n {
		t.Errorf("a wrong hash: %v %+v", errs, m.Stats())
	}

	// no hash at all
	u := NewDecoder(Options{VerifyHash: true})
	if _, errs := decodeUnits(u, units); len(errs) != 0 || u.Stats().Unhashed != n+1 {
		t.Errorf("unhashed: %v %+v", errs, u.Stats())
	}

	// units the decoder reads only when verifying, and what it ignores in them
	odd := []Unit{
		hashUnit(tail, HashMD5), // before any picture: nothing to attach it to
	}
	odd = append(odd, units...)
	odd = append(odd,
		Unit{Type: UnitSuffixSEI, Payload: []byte{}},              // not an SEI payload
		Unit{Type: UnitSuffixSEI, Payload: []byte{5, 1, 7, 0x80}}, // another message type
		hashUnit(tail, 7), // a reserved hash type
		Unit{Type: UnitSuffixSEI, Payload: []byte{byte(SEIPayloadDecodedPictureHash), 2, 0, 1, 0x80}}, // too short
	)
	x := NewDecoder(Options{VerifyHash: true})
	if _, errs := decodeUnits(x, odd); !hasErr(errs, ErrSEI) {
		t.Errorf("a malformed SEI went unnoticed: %v", errs)
	}
	y := NewDecoder(Options{})
	if _, errs := decodeUnits(y, odd); len(errs) != 0 {
		t.Errorf("SEI units are ignored without VerifyHash: %v", errs)
	}
}

func without(units []Unit, drop ...UnitType) []Unit {
	var out []Unit
	for _, u := range units {
		keep := true
		for _, d := range drop {
			keep = keep && u.Type != d
		}
		if keep {
			out = append(out, u)
		}
	}
	return out
}

func TestDecoderRefusals(t *testing.T) {
	units := seedUnits(t)
	cases := []struct {
		name   string
		opt    Options
		units  []Unit
		target error
	}{
		{"no PPS", Options{}, without(units, UnitPPS), ErrMissingParameterSet},
		{"no SPS", Options{}, without(units, UnitSPS), ErrMissingParameterSet},
		{"a picture too large", Options{Limits: Limits{MaxPixels: 100}}, units, ErrPictureTooLarge},
		{"a buffer too large", Options{Limits: Limits{MaxDPBBytes: 1000}}, units, ErrDPBTooLarge},
		{"a damaged SPS", Options{}, []Unit{{Type: UnitSPS, Payload: []byte{1, 2, 3}}}, nil},
		{"a damaged PPS", Options{}, []Unit{{Type: UnitPPS, Payload: []byte{0}}}, nil},
		{"a slice with no header", Options{}, append(append([]Unit(nil), units[:3]...), Unit{Type: UnitTrailR}), ErrSliceHeader},
	}
	for _, c := range cases {
		_, errs := decodeUnits(NewDecoder(c.opt), c.units)
		if len(errs) == 0 || (c.target != nil && !hasErr(errs, c.target)) {
			t.Errorf("%s: %v", c.name, errs)
		}
	}

	// a slice whose header is cut short
	var headless []Unit
	for _, u := range units {
		if u.Type.IsSliceSegment() && len(u.Payload) > 3 {
			u.Payload = u.Payload[:3]
		}
		headless = append(headless, u)
	}
	if _, errs := decodeUnits(NewDecoder(Options{}), headless); len(errs) == 0 {
		t.Error("slice headers cut short went unnoticed")
	}

	// a decoder that has seen many parameter sets drops the layouts it made for old ones
	old := NewDecoder(Options{})
	for i := 0; i < 70; i++ {
		old.layouts[[2]int{1000 + i, 0}] = &CtbLayout{}
	}
	if _, errs := decodeUnits(old, units); len(errs) != 0 || len(old.layouts) > 64 {
		t.Errorf("%d layouts kept, %v", len(old.layouts), errs)
	}

	// a slice cut short: the picture fails, is still kept, and the stream goes on
	var cut []Unit
	slices := 0
	for _, u := range units {
		if u.Type.IsSliceSegment() {
			slices++
			if slices == 3 {
				u.Payload = u.Payload[:len(u.Payload)/2]
			}
		}
		cut = append(cut, u)
	}
	d := NewDecoder(Options{})
	outs, errs := decodeUnits(d, cut)
	if !hasErr(errs, ErrCorruptPicture) || d.Stats().Failed == 0 || len(outs) != d.Stats().Pictures {
		t.Errorf("a cut slice: %v %+v, %d output", errs, d.Stats(), len(outs))
	}

	// the first picture lost: the ones that predict from it have no reference
	d = NewDecoder(Options{})
	_, errs = decodeUnits(d, without(units, UnitIDRWRADL, UnitIDRNLP))
	if !hasErr(errs, ErrMissingReference) || d.Stats().Failed == 0 {
		t.Errorf("no first picture: %v %+v", errs, d.Stats())
	}

	// a layer above the base is not decoded
	var layered []Unit
	for _, u := range units {
		u.LayerID = 1
		layered = append(layered, u)
	}
	if outs, errs := decodeUnits(NewDecoder(Options{}), layered); len(outs) != 0 || len(errs) != 0 {
		t.Errorf("layer 1: %d pictures, %v", len(outs), errs)
	}
}

func TestDecoderSkipsRASLPicturesAndRestartsAfterEndOfSequence(t *testing.T) {
	units := seedUnits(t)
	// the second picture becomes a RASL one: it belongs to the random access point the
	// stream starts with, so it is not decoded
	var edited []Unit
	pictures := 0
	for _, u := range units {
		if u.Type.IsSliceSegment() && len(u.Payload) > 0 && u.Payload[0]&0x80 != 0 {
			pictures++
			if pictures == 2 {
				u.Type = UnitRASLR
			}
		}
		edited = append(edited, u)
	}
	d := NewDecoder(Options{})
	decodeUnits(d, edited)
	if d.Stats().SkippedRASL != 1 {
		t.Errorf("RASL pictures skipped: %+v", d.Stats())
	}
	// the stream, an end of sequence, the stream again: both decode, the counter restarts
	twice := append(append(append([]Unit(nil), units...), Unit{Type: UnitEOS}), units...)
	e := NewDecoder(Options{})
	outs, errs := decodeUnits(e, twice)
	one, _ := decodeUnits(NewDecoder(Options{}), units)
	if len(errs) != 0 || len(outs) != 2*len(one) {
		t.Errorf("two sequences: %d pictures against %d, %v", len(outs), len(one), errs)
	}
}

func TestDecoderInternalRefusals(t *testing.T) {
	d := NewDecoder(Options{})
	h := SliceHeader{}
	var sps SPS
	sps.Log2MaxPOCLSB = 4
	// the order count of a unit that is no slice
	if d.begin(Unit{Type: UnitVPS}, h, sps, PPS{}) || len(d.errs) != 1 {
		t.Errorf("a unit that is no slice: %v", d.errs)
	}
	// a reference picture set that 8.3.1 forbids
	h.Refs.ShortTerm.Before = []RefPic{{DeltaPOC: -40000, Used: true}}
	if d.begin(Unit{Type: UnitIDRNLP}, h, sps, PPS{}) || !hasErr(d.errs, ErrPOCRange) {
		t.Errorf("an order count out of range: %v", d.errs)
	}
	// the buffer holds too many pictures for the limit
	sps.CodedWidth, sps.CodedHeight, sps.ChromaFormat = 16, 16, 1
	d = NewDecoder(Options{Limits: Limits{MaxDPBBytes: 2 * pictureBytes(sps)}})
	for i := 0; i < 2; i++ {
		d.dpb.pics = append(d.dpb.pics, &DPBPicture{Mark: ShortTermReference})
	}
	if d.begin(Unit{Type: UnitIDRNLP}, SliceHeader{}, sps, PPS{}) || !hasErr(d.errs, ErrDPBTooLarge) {
		t.Errorf("a full buffer: %v", d.errs)
	}
	// a layout the parameter sets cannot make
	d = NewDecoder(Options{})
	if d.begin(Unit{Type: UnitIDRNLP}, SliceHeader{}, sps, PPS{}) {
		t.Error("a layout from empty parameter sets")
	}
	// lists that cannot be built, and a collocated index beyond its list
	c := &decPicture{}
	p := SliceHeader{}
	p.Refs.Type, p.Refs.NumRefIdxL0Active = SliceP, 1
	p.Type = SliceP
	p.Refs.TemporalMVP = true
	if _, err := d.interSlice(c, p, PPS{}); err == nil {
		t.Error("temporal prediction from a list with no reference")
	}
	c.set.StCurrBefore = []int32{3}
	c.rp.StCurrBefore = []*DPBPicture{{POC: 3}}
	p.Refs.ListEntryL0 = []uint32{99} // an entry beyond the temporary list
	if _, err := d.interSlice(c, p, PPS{}); err == nil {
		t.Error("a list entry beyond the pictures")
	}
	p.Refs.ListEntryL0 = nil
	p.Refs.CollocatedRefIdx = 1
	if _, err := d.interSlice(c, p, PPS{}); err == nil {
		t.Error("collocated_ref_idx beyond the list")
	}
	p.Refs.CollocatedRefIdx = 0
	if sl, err := d.interSlice(c, p, PPS{}); err != nil || sl.List[0][0].Pic != c.rp.StCurrBefore[0] {
		t.Errorf("%v", err)
	}
	// long-term entries are found among the long-term pictures
	c.set.LtCurr = []LtPicture{{POC: 9, Exact: true}}
	c.rp.LtCurr = []*DPBPicture{{POC: 9}}
	p.Refs.NumRefIdxL0Active = 2
	if sl, err := d.interSlice(c, p, PPS{}); err != nil || sl.List[0][1].Pic != c.rp.LtCurr[0] || !sl.List[0][1].LongTerm {
		t.Errorf("%v", err)
	}
}

// A stream that declares a picture far beyond what memory allows is refused at its
// sequence parameter set, before anything is allocated for it: under a memory limit
// that the picture would break, the refusal comes and the heap does not grow.
func TestDecoderRefusesAPictureBomb(t *testing.T) {
	defer debug.SetMemoryLimit(debug.SetMemoryLimit(256 << 20))
	sps := set{subLayers: 1, chroma: 3, width: 16384, height: 16384, sizes: []uint32{0, 3, 0, 3, 0, 0}}
	d := NewDecoder(Options{})
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	_, err := d.Decode(sps.build())
	runtime.ReadMemStats(&m1)
	if !errors.Is(err, ErrPictureTooLarge) {
		t.Fatalf("a 16384x16384 4:4:4 sequence: %v", err)
	}
	if grew := int64(m1.HeapAlloc) - int64(m0.HeapAlloc); grew > 32<<20 {
		t.Errorf("the refusal cost %d bytes", grew)
	}
	// within the picture limit but beyond the buffer limit
	d = NewDecoder(Options{Limits: Limits{MaxDPBBytes: 1 << 20}})
	small := set{subLayers: 1, chroma: 1, width: 1024, height: 1024, sizes: []uint32{0, 3, 0, 3, 0, 0}}
	if _, err := d.Decode(small.build()); !errors.Is(err, ErrDPBTooLarge) {
		t.Errorf("a buffer of pictures over the byte limit: %v", err)
	}
}

// FuzzDecoder drives the whole decoder over a fuzzed elementary stream, with small
// limits so that a stream cannot make it slow or large. It holds the decoder to one
// thing: no stream, however malformed, panics. Every refusal is an acceptable answer.
//
//	go test -run='^$' -fuzz='^FuzzDecoder$' -fuzztime=60s
func FuzzDecoder(f *testing.F) {
	f.Add(append([]byte{0}, seedBytes(f)...))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		var units []Unit
		var err error
		if data[0]%4 == 0 {
			units, err = SplitAnnexB(data[1:])
		} else {
			units, err = SplitLengthPrefixed(data[1:], int(data[0]%4))
		}
		if err != nil {
			return
		}
		d := NewDecoder(Options{VerifyHash: data[0]&4 != 0, Limits: Limits{MaxPixels: 1 << 18, MaxDPBBytes: 64 << 20}})
		for _, u := range units {
			_, _ = d.Decode(u)
		}
		_, _ = d.Flush()
	})
}

func seedBytes(t testing.TB) []byte {
	data, err := os.ReadFile("testdata/fuzzseed/hevc.265")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A picture whose size is not a multiple of the smallest coding block would have blocks
// past its edge: the fuzzer found a panic there, and the sequence parameter set is
// refused (7.4.3.2.1).
func TestAPictureSizeThatIsNoMultipleOfTheCodingBlockIsRefused(t *testing.T) {
	for _, size := range [][2]uint32{{70, 64}, {64, 66}, {11, 23}} {
		s := set{subLayers: 1, chroma: 1, width: size[0], height: size[1], sizes: []uint32{0, 3, 0, 3, 0, 0}}
		if _, err := ParseSPS(s.build()); !errors.Is(err, ErrUnsupportedSPS) {
			t.Errorf("%dx%d: %v", size[0], size[1], err)
		}
	}
	good := set{subLayers: 1, chroma: 1, width: 72, height: 64, sizes: []uint32{0, 3, 0, 3, 0, 0}}
	if _, err := ParseSPS(good.build()); err != nil {
		t.Errorf("72x64: %v", err)
	}
}
