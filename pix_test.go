// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// px is the samples of a plane as 16-bit values: the slice itself for a 16-bit plane, a widened
// copy for a byte plane (what a test compares or inspects).
func px(pl *Plane) []uint16 {
	if pl.Pix8 == nil {
		return pl.Pix16
	}
	out := make([]uint16, len(pl.Pix8))
	for i, v := range pl.Pix8 {
		out[i] = uint16(v)
	}
	return out
}

// setpx writes the sample at index i of a plane of either kind.
func setpx(pl *Plane, i int, v uint16) { pl.set(i, int(v)) }

// samePlace reports whether sample ai of plane a and sample bi of plane b are the same memory.
func samePlace(a *Plane, ai int, b *Plane, bi int) bool {
	if a.Pix8 != nil || b.Pix8 != nil {
		return a.Pix8 != nil && b.Pix8 != nil && &a.Pix8[ai] == &b.Pix8[bi]
	}
	return &a.Pix16[ai] == &b.Pix16[bi]
}

// plane16 is a 16-bit plane of the given samples.
func plane16(w, h, stride int, pix []uint16) Plane {
	return Plane{Width: w, Height: h, Stride: stride, Pix16: pix}
}
