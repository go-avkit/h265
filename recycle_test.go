// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"testing"
)

func benchUnits(t *testing.T, file string) []Unit {
	t.Helper()
	data, err := os.ReadFile("testdata/bench/" + file)
	if err != nil {
		t.Fatal(err)
	}
	units, err := SplitAnnexB(data)
	if err != nil {
		t.Fatal(err)
	}
	return units
}

// runSeed decodes units (two passes of one decoder, so a pool carries over) and calls each
// with every output picture; it returns the md5 of the whole output of each pass.
func runSeed(t *testing.T, d *Decoder, units []Unit, each func(o Output)) []string {
	t.Helper()
	var sums []string
	for pass := 0; pass < 2; pass++ {
		h := md5.New()
		emit := func(outs []Output) {
			for _, o := range outs {
				writePicture(h, o.Picture)
				each(o)
			}
		}
		for _, u := range units {
			outs, err := d.Decode(u)
			if err != nil {
				t.Fatal(err)
			}
			emit(outs)
		}
		outs, err := d.Flush()
		if err != nil {
			t.Fatal(err)
		}
		emit(outs)
		sums = append(sums, hex.EncodeToString(h.Sum(nil)))
	}
	return sums
}

// TestRecycledPicturesDecodeTheSameOutput: with the option on and every picture released the
// moment it is hashed, the output of both passes equals the shipped md5 (and the output without
// the option), and pictures really were reused.
func TestRecycledPicturesDecodeTheSameOutput(t *testing.T) {
	for _, s := range benchSeeds {
		t.Run(s.file, func(t *testing.T) {
			units := benchUnits(t, s.file)
			off := runSeed(t, NewDecoder(Options{}), units, func(o Output) { noPanic(t, "Release without the option", o.Picture.Release) })
			d := NewDecoder(Options{RecyclePictures: true})
			var released int
			on := runSeed(t, d, units, func(o Output) { o.Picture.Release(); released++ })
			for pass := range on {
				if on[pass] != s.md5 || off[pass] != s.md5 {
					t.Errorf("pass %d: option on %s, off %s, want %s", pass, on[pass], off[pass], s.md5)
				}
			}
			if d.pool.reused == 0 || released != 24 {
				t.Errorf("%d pictures reused, %d released: the pool was not used", d.pool.reused, released)
			}
		})
	}
}

// TestAnUnreleasedPictureIsNeverReused is the aliasing witness of recycling. Every other
// output picture is kept, not released; the pool is poisoned (every sample of a picture that
// goes back to it is overwritten), the rest of the stream decodes twice over, and each kept
// picture must hash as it did when it came out, with its generation unchanged and no sign of
// having been pooled. The released ones are the pool's: the generation counter says how many
// times their memory went back.
func TestAnUnreleasedPictureIsNeverReused(t *testing.T) {
	for _, s := range benchSeeds {
		t.Run(s.file, func(t *testing.T) {
			units := benchUnits(t, s.file)
			d := NewDecoder(Options{RecyclePictures: true})
			poison := uint16(0xAB)
			d.pool.poison = &poison
			var kept []*DecodedPicture
			var sums []string
			var gens []uint64
			n := 0
			runSeed(t, d, units, func(o Output) {
				n++
				if n%2 == 0 {
					kept = append(kept, o.Picture)
					sums = append(sums, pictureMD5(o.Picture))
					gens = append(gens, o.Picture.rec.gen)
					return
				}
				o.Picture.Release()
			})
			if len(kept) != 12 || d.pool.reused == 0 {
				t.Fatalf("%d kept, %d reused", len(kept), d.pool.reused)
			}
			for i, p := range kept {
				if got := pictureMD5(p); got != sums[i] {
					t.Errorf("kept picture %d changed: %s, was %s", i, got, sums[i])
				}
				if r := p.rec; r.gen != gens[i] || r.pooled || !r.pool.isLiveOrUnreleased(r) {
					t.Errorf("kept picture %d: generation %d (was %d), pooled %v", i, r.gen, gens[i], r.pooled)
				}
			}
		})
	}
}

func (pl *picPool) isLiveOrUnreleased(r *recycle) bool {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	return !r.released
}

// TestThePoisonedPoolCatchesAnAliasedPicture is the control of the test above: a live picture
// put into the pool by mistake IS overwritten by the decode, and the check notices. Without
// this the check could be passing because it cannot fail.
func TestThePoisonedPoolCatchesAnAliasedPicture(t *testing.T) {
	s := benchSeeds[0]
	units := benchUnits(t, s.file)
	d := NewDecoder(Options{RecyclePictures: true})
	var victim *DecodedPicture
	var sum string
	n := 0
	for _, u := range units {
		outs, err := d.Decode(u)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range outs {
			if n++; n == 1 {
				victim, sum = o.Picture, pictureMD5(o.Picture)
				// the mutation: the pool is handed a picture the caller still uses
				r := o.Picture.rec
				d.pool.mu.Lock()
				d.pool.free[r.key] = append(d.pool.free[r.key], r.frame)
				d.pool.mu.Unlock()
			} else {
				o.Picture.Release()
			}
		}
	}
	if _, err := d.Flush(); err != nil {
		t.Fatal(err)
	}
	if victim == nil || pictureMD5(victim) == sum {
		t.Fatal("a picture that was put in the pool while in use was not overwritten: the aliasing check cannot fail")
	}
}

// noPanic runs f and fails the test if it panics: a guard that only a crash catches is not
// held by an assertion.
func noPanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("%s panicked: %v", what, r)
		}
	}()
	f()
}

// TestReleaseOfAPictureThatIsNotTheDecoders: no pool, no panic, nothing changes (the guard on
// a nil recycling state); and At on an emptied plane reads 0 at either sample kind.
func TestReleaseOfAPictureThatIsNotTheDecoders(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 32, height: 16})
	f := NewDecodedPicture(sps)
	f.Planes[0].set(5, 77)
	noPanic(t, "Release of a picture with no pool", func() { f.Release(); f.Release() })
	if f.Planes[0].get(5) != 77 || f.Planes[0].Width != 32 {
		t.Error("Release of a picture with no pool changed it")
	}
	for _, pl := range []Plane{{Width: 4, Height: 4, Stride: 4}, {Width: 4, Height: 4, Stride: 4, Pix16: nil}} {
		noPanic(t, "At on an emptied plane", func() {
			if v := pl.At(1, 1); v != 0 {
				t.Errorf("At on an emptied plane = %d", v)
			}
		})
	}
	wide := Plane{Width: 2, Height: 1, Stride: 2, Pix16: []uint16{7, 9}}
	if wide.At(1, 0) != 9 {
		t.Error("At on a 16-bit plane")
	}
}

// TestReleaseIsSafe: a second Release does nothing, a released view is empty and reads as 0,
// Release without the option or on a Clone leaves the picture valid.
func TestReleaseIsSafe(t *testing.T) {
	units := benchUnits(t, benchSeeds[0].file)

	// option off: Release is a no-op
	d := NewDecoder(Options{})
	var off *DecodedPicture
	var sum string
	for _, u := range units {
		outs, _ := d.Decode(u)
		for _, o := range outs {
			if off == nil {
				off, sum = o.Picture, pictureMD5(o.Picture)
			}
		}
	}
	d.Flush()
	noPanic(t, "Release without the option", func() { off.Release(); off.Release() })
	if pictureMD5(off) != sum || off.Planes[0].Width == 0 {
		t.Error("Release without the option changed the picture")
	}

	// option on
	d = NewDecoder(Options{RecyclePictures: true})
	var p *DecodedPicture
	for _, u := range units {
		outs, _ := d.Decode(u)
		for _, o := range outs {
			if p == nil {
				p = o.Picture
				cl := p.Clone()
				noPanic(t, "Release of a Clone", cl.Release) // a Clone is not the pool's
				if pictureMD5(cl) != pictureMD5(p) {
					t.Error("Release of a Clone changed it")
				}
			}
		}
	}
	d.Flush()
	p.Release()
	p.Release() // twice
	if p.Components != 3 || p.Planes[0].Width != 0 || p.Planes[0].Len() != 0 || p.Planes[0].At(3, 3) != 0 || p.Planes[1].At(0, 0) != 0 {
		t.Errorf("a released picture is not empty: %+v", p.Planes[0])
	}
	if p.Is8() {
		t.Error("a released picture claims byte planes")
	}
}

// TestThePoolsStateMachine: the two ways of letting go in each order, a Release twice, the
// release of the decoder's own frame, the cap, and the flush of the shapes of an old size.
func TestThePoolsStateMachine(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 32, height: 16})
	pl := newPicPool()
	a := pl.alloc(sps)
	r := a.rec
	if !r.inDPB || r.released || r.pooled {
		t.Fatalf("a new picture: %+v", r)
	}
	// released first: waits for the buffer
	a.Release()
	a.Release()
	if r.pooled {
		t.Fatal("pooled while the buffer holds it")
	}
	pl.drop(r)
	if !r.pooled || r.gen != 1 || len(pl.free[r.key]) != 1 {
		t.Fatalf("after the drop: %+v", r)
	}
	pl.drop(r) // a second drop does nothing
	pl.release(r)
	if r.gen != 1 || len(pl.free[r.key]) != 1 {
		t.Fatal("pooled twice")
	}
	// reused, cleared, dropped first this time
	a.Planes[0].set(3, 99)
	b := pl.alloc(sps)
	if b != a || pl.reused != 1 || b.Planes[0].get(3) != 0 || r.pooled || r.released || !r.inDPB {
		t.Fatalf("not reused clean: reused %d, %+v", pl.reused, r)
	}
	pl.drop(r)
	if r.pooled {
		t.Fatal("pooled while the caller holds it")
	}
	// the decoder's own frame released: it stays whole
	b.Release()
	if !r.pooled || b.Planes[0].Width != 32 {
		t.Fatalf("the frame itself: pooled %v, width %d", r.pooled, b.Planes[0].Width)
	}
	// the cap
	var live []*DecodedPicture
	for i := 0; i < maxPooled+5; i++ {
		live = append(live, pl.alloc(sps))
	}
	for _, f := range live {
		f.Release()
		pl.drop(f.rec)
	}
	if got := len(pl.free[r.key]); got != maxPooled {
		t.Errorf("%d pictures kept, the cap is %d", got, maxPooled)
	}
	// a stream of another size lets the old shapes go
	other := mustSPS(t, set{chroma: 1, width: 64, height: 16})
	pl.alloc(other)
	if len(pl.free[r.key]) != 0 {
		t.Error("the pictures of the old shape were kept")
	}
}

// TestReleaseFromAnotherGoroutine: the consumer of the pictures is usually not the goroutine
// that decodes. Every picture is hashed and released by a second goroutine while the first goes
// on decoding; the output equals the shipped md5 (run under -race in CI).
func TestReleaseFromAnotherGoroutine(t *testing.T) {
	s := benchSeeds[1]
	units := benchUnits(t, s.file)
	d := NewDecoder(Options{RecyclePictures: true})
	ch := make(chan *DecodedPicture, 4)
	done := make(chan string)
	go func() {
		h := md5.New()
		for p := range ch {
			writePicture(h, p)
			p.Release()
		}
		done <- hex.EncodeToString(h.Sum(nil))
	}()
	send := func(outs []Output) {
		for _, o := range outs {
			ch <- o.Picture
		}
	}
	for _, u := range units {
		outs, err := d.Decode(u)
		if err != nil {
			t.Fatal(err)
		}
		send(outs)
	}
	outs, err := d.Flush()
	if err != nil {
		t.Fatal(err)
	}
	send(outs)
	close(ch)
	if got := <-done; got != s.md5 {
		t.Errorf("md5 %s, want %s", got, s.md5)
	}
}

// TestTheMotionPool: a taken field is zeroed, one of another size is let go, the pool is
// bounded, and a nil pool allocates.
func TestTheMotionPool(t *testing.T) {
	var nilPool *motionPool
	if got := nilPool.get(5); len(got) != 5 {
		t.Fatalf("a nil pool gave %d entries", len(got))
	}
	m := &motionPool{}
	a := m.get(8)
	a[3].Pred[0] = true
	m.put(nil) // an intra picture has no field
	m.put(a)
	if len(m.free) != 1 {
		t.Fatalf("%d fields kept", len(m.free))
	}
	b := m.get(8)
	if &b[0] != &a[0] || b[3].Pred[0] {
		t.Error("the field was not reused clean")
	}
	m.put(b)
	if c := m.get(16); len(c) != 16 || len(m.free) != 0 {
		t.Errorf("a field of another size: %d entries, %d kept", len(c), len(m.free))
	}
	for i := 0; i < maxMotions+3; i++ {
		m.put(make([]Motion, 4))
	}
	if len(m.free) != maxMotions {
		t.Errorf("%d fields kept, the cap is %d", len(m.free), maxMotions)
	}
}

// TestRecyclingCoversTheShapes: a monochrome and a 4:4:4 picture are keyed as NewDecodedPicture
// makes them.
func TestRecyclingCoversTheShapes(t *testing.T) {
	for _, chroma := range []uint32{0, 1, 2, 3} {
		for _, depth := range []uint8{8, 10} {
			sps := mustSPS(t, set{chroma: chroma, width: 32, height: 16})
			sps.BitDepthLuma, sps.BitDepthChroma = depth, depth
			f := NewDecodedPicture(sps)
			if k := shapeOf(sps); k != keyOf(f) {
				t.Errorf("chroma %d depth %d: shape %+v, picture %+v", chroma, depth, k, keyOf(f))
			}
		}
	}
}

func keyOf(p *DecodedPicture) poolKey {
	k := poolKey{components: p.Components}
	for c := 0; c < p.Components; c++ {
		k.w[c], k.h[c], k.is8[c] = p.Planes[c].Width, p.Planes[c].Height, p.Planes[c].Is8()
	}
	return k
}
