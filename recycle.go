// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "sync"

// Picture recycling (Options.RecyclePictures).
//
// Every picture the decoder decodes is a fresh allocation of its planes, which the operating
// system has to fault in page by page: at 1080p that is about 1500 page faults a frame and a
// fifth of the run in the kernel. With RecyclePictures the planes of a picture the caller has
// Released go back to a pool once the decoded picture buffer no longer holds the picture, and
// the next picture is decoded into them. A picture the caller does not Release is never reused
// (it is the garbage collector's, as without the option).
//
// A picture is held by two parties, each of which can let go first: the decoded picture buffer
// (it is a reference picture, or waits for output) and the caller (it has the Output). Its
// planes go back to the pool when both have.

// recycle is the recycling state of one picture allocated by a pool: shared by the decoder's
// picture and the cropped views of it that were output. All fields are guarded by pool.mu.
type recycle struct {
	pool  *picPool
	frame *DecodedPicture
	key   poolKey
	// gen counts how many times the planes went back to the pool; a view that outlives a
	// reuse is the aliasing the tests look for.
	gen uint64
	// inDPB: the decoded picture buffer still holds the picture. released: the caller
	// released it. pooled: the planes are in the pool.
	inDPB, released, pooled bool
}

// poolKey is the shape of a picture: its planes' sizes and sample kinds, which is all a
// reused picture must have in common with the one it replaces.
type poolKey struct {
	components int
	w, h       [3]int
	is8        [3]bool
}

// shapeOf is the shape of the picture NewDecodedPicture makes of sps.
func shapeOf(sps SPS) poolKey {
	k := poolKey{components: 3}
	subW, subH := 1, 1
	switch sps.ChromaFormat {
	case 0:
		k.components = 1
	case 1:
		subW, subH = 2, 2
	case 2:
		subW = 2
	}
	for c := 0; c < k.components; c++ {
		k.w[c], k.h[c], k.is8[c] = int(sps.CodedWidth), int(sps.CodedHeight), sps.BitDepthLuma == 8
		if c > 0 {
			k.w[c], k.h[c], k.is8[c] = k.w[c]/subW, k.h[c]/subH, sps.BitDepthChroma == 8
		}
	}
	return k
}

// maxPooled bounds the pictures a pool keeps: more than a decoded picture buffer ever holds
// plus the pictures a caller can have in flight, so a pool that is not drained is bounded.
const maxPooled = 24

// picPool is a decoder's pool of pictures.
type picPool struct {
	mu   sync.Mutex
	free map[poolKey][]*DecodedPicture
	// poison, set by tests, is written over the samples of a picture that goes back to the
	// pool, so that a picture that is still in use shows it.
	poison *uint16
	// reused counts the pictures alloc took from the pool.
	reused int
}

func newPicPool() *picPool { return &picPool{free: map[poolKey][]*DecodedPicture{}} }

// alloc returns the picture the sequence parameter set describes, from the pool when one of
// that shape is there (every sample cleared, as a new allocation's are), else a new one.
func (pl *picPool) alloc(sps SPS) *DecodedPicture {
	k := shapeOf(sps)
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if list := pl.free[k]; len(list) > 0 {
		f := list[len(list)-1]
		pl.free[k] = list[:len(list)-1]
		r := f.rec
		pl.reused++
		r.pooled, r.released, r.inDPB = false, false, true
		for c := 0; c < f.Components; c++ {
			f.Planes[c].fill(0)
		}
		return f
	}
	// A shape that is not in the pool: the pictures of another shape (the stream changed its
	// size) are of no use any more and are let go.
	for other := range pl.free {
		if other != k {
			delete(pl.free, other)
		}
	}
	fresh := NewDecodedPicture(sps)
	fresh.rec = &recycle{pool: pl, frame: fresh, key: k, inDPB: true}
	return fresh
}

// put puts the planes of r in the pool; the caller holds pl.mu and has checked that nothing
// holds the picture.
func (pl *picPool) put(r *recycle) {
	r.pooled = true
	r.gen++
	if pl.poison != nil {
		for c := 0; c < r.frame.Components; c++ {
			r.frame.Planes[c].fill(int(*pl.poison))
		}
	}
	if list := pl.free[r.key]; len(list) < maxPooled {
		pl.free[r.key] = append(list, r.frame)
	}
}

// drop is the decoded picture buffer letting go of the picture.
func (pl *picPool) drop(r *recycle) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	r.inDPB = false
	if r.released && !r.pooled {
		pl.put(r)
	}
}

// release is the caller letting go of the picture.
func (pl *picPool) release(r *recycle) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	// A second release finds the picture released, and either pooled or still held by the
	// buffer, so it does nothing more: no guard of its own is needed.
	r.released = true
	if !r.inDPB && !r.pooled {
		pl.put(r)
	}
}

// motionPool recycles the motion fields of pictures (a 4x4 grid of Motion, as large as the
// planes at 1080p). Nothing outside the decoder reads a picture's motion, so it goes back
// when the decoded picture buffer lets go of the picture, whether or not the caller has
// released anything. It is used by the decoder's goroutine only. A nil pool allocates.
type motionPool struct{ free [][]Motion }

// maxMotions bounds the fields a pool keeps: the buffer's pictures and a few more.
const maxMotions = 20

// get returns a zeroed Motion slice of n entries.
func (m *motionPool) get(n int) []Motion {
	if m != nil {
		for len(m.free) > 0 {
			s := m.free[len(m.free)-1]
			m.free = m.free[:len(m.free)-1]
			if len(s) == n {
				clear(s)
				return s
			}
			// another picture size: the older fields are of no use
		}
	}
	return make([]Motion, n)
}

// put takes back the motion field of a picture the buffer let go.
func (m *motionPool) put(s []Motion) {
	if s != nil && len(m.free) < maxMotions {
		m.free = append(m.free, s)
	}
}

// Release says the caller is done with a picture the decoder output, so that the decoder may
// reuse its planes (Options.RecyclePictures). The planes are reused once the decoded picture
// buffer no longer needs the picture as a reference or for output: Release on a picture that is
// still in the buffer only defers the reuse until it has left. The picture is emptied (no
// planes), so reading it after Release gives nothing and cannot show another picture's
// samples; a second Release does nothing. Without Options.RecyclePictures, or on a picture
// that is not one the decoder output (a Clone, say), Release does nothing at all and the
// picture stays valid.
//
// A caller that keeps a plane slice taken from the picture before Release must not use it after:
// the slice is the decoder's memory, which will hold another picture. A picture that is not
// released is never reused.
func (p *DecodedPicture) Release() {
	r := p.rec
	if r == nil {
		return
	}
	if p != r.frame { // an output view: nothing of it may be read again
		p.rec = nil
		p.Planes = [3]Plane{}
	}
	r.pool.release(r)
}
