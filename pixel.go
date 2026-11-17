// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// pixel is the type of a sample of a picture: a byte at 8 bits, a 16-bit word above.
type pixel interface{ ~uint8 | ~uint16 }

// Plane is one colour component of a decoded picture: Width x Height samples, rows Stride
// samples apart (Stride is at least Width; sample (x, y) is at index y*Stride+x).
//
// The samples are in Pix8 when the component has a bit depth of 8, and in Pix16 for every other
// depth: exactly one of the two is non-nil (a plane with no samples has neither). Use Is8 to
// tell, and At to read a sample of either without caring.
type Plane struct {
	Width, Height, Stride int
	Pix8                  []uint8
	Pix16                 []uint16
}

// Is8 reports whether the plane holds its samples as bytes (a bit depth of 8).
func (p *Plane) Is8() bool { return p.Pix8 != nil }

// At returns the sample at column x, row y, of a plane of either kind. It is for tests and
// tools: a decoder's loops work on the slice.
func (p *Plane) At(x, y int) uint16 {
	if p.Pix8 != nil {
		return uint16(p.Pix8[y*p.Stride+x])
	}
	return p.Pix16[y*p.Stride+x]
}

// Len is the number of samples of the slice that holds them, whichever it is.
func (p *Plane) Len() int {
	if p.Pix8 != nil {
		return len(p.Pix8)
	}
	return len(p.Pix16)
}

// get and set read and write the sample at index i of the slice, of either kind: for the code that
// is not on the hot path, which would rather not have two bodies.
func (p *Plane) get(i int) int {
	if p.Pix8 != nil {
		return int(p.Pix8[i])
	}
	return int(p.Pix16[i])
}

func (p *Plane) set(i, v int) {
	if p.Pix8 != nil {
		p.Pix8[i] = uint8(v)
		return
	}
	p.Pix16[i] = uint16(v)
}

// newPlane allocates a plane of the given size and bit depth.
func newPlane(w, h, bitDepth int) Plane {
	if bitDepth == 8 {
		return Plane{Width: w, Height: h, Stride: w, Pix8: make([]uint8, w*h)}
	}
	return Plane{Width: w, Height: h, Stride: w, Pix16: make([]uint16, w*h)}
}

// window is the plane of the w x h block at index first of a plane, sharing its memory, with the
// slice cut to the last sample of the block (and its capacity too).
func (p *Plane) window(first, w, h int) Plane {
	end := first + (h-1)*p.Stride + w
	out := Plane{Width: w, Height: h, Stride: p.Stride}
	if p.Pix8 != nil {
		out.Pix8 = p.Pix8[first:end:end]
	} else {
		out.Pix16 = p.Pix16[first:end:end]
	}
	return out
}

// fill sets every sample, the slice included past the picture, to v.
func (p *Plane) fill(v int) {
	if p.Pix8 != nil {
		for i := range p.Pix8 {
			p.Pix8[i] = uint8(v)
		}
		return
	}
	for i := range p.Pix16 {
		p.Pix16[i] = uint16(v)
	}
}

// DecodedPicture is a decoded picture: Components planes, each with its bit depth.
type DecodedPicture struct {
	Components int
	BitDepth   [3]int
	Planes     [3]Plane
}

// Is8 reports whether the picture is made of byte planes: every component has a bit depth of 8.
func (p *DecodedPicture) Is8() bool {
	for c := 0; c < p.Components; c++ {
		if !p.Planes[c].Is8() {
			return false
		}
	}
	return p.Components > 0
}

// copyBlock copies the w x h block at index di of dst from the block at index si of src.
func copyBlock[T pixel](dst []T, di, ds int, src []T, si, ss, w, h int) {
	for y := 0; y < h; y++ {
		copy(dst[di+y*ds:di+y*ds+w], src[si+y*ss:si+y*ss+w])
	}
}

// widen copies the w x h block of src at index si into dst (a uint16 slice with rows ds apart) at
// index di: what a kernel that takes 16-bit samples is given of a byte plane.
func widen[T pixel](dst []uint16, di, ds int, src []T, si, ss, w, h int) {
	for y := 0; y < h; y++ {
		row := src[si+y*ss : si+y*ss+w]
		out := dst[di+y*ds : di+y*ds+w : di+y*ds+w]
		for x, v := range row {
			out[x] = uint16(v)
		}
	}
}

// narrow copies the w x h block of src (uint16) at index si into dst at index di, the other way
// (a kernel's output into a byte plane); the samples are within the plane's bit depth.
func narrow[T pixel](dst []T, di, ds int, src []uint16, si, ss, w, h int) {
	for y := 0; y < h; y++ {
		row := src[si+y*ss : si+y*ss+w]
		out := dst[di+y*ds : di+y*ds+w : di+y*ds+w]
		for x, v := range row {
			out[x] = T(v)
		}
	}
}
