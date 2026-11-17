// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// TestPlanesOfEitherKind: a plane holds its samples as bytes at 8 bits and as 16-bit words
// above, exactly one of the two; the accessors read and write either.
func TestPlanesOfEitherKind(t *testing.T) {
	for _, depth := range []int{8, 10} {
		pl := newPlane(6, 4, depth)
		if pl.Is8() != (depth == 8) || (pl.Pix8 != nil) == (pl.Pix16 != nil) || pl.Len() != 24 || pl.Stride != 6 {
			t.Fatalf("depth %d: %+v", depth, pl)
		}
		for i := 0; i < pl.Len(); i++ {
			pl.set(i, i*7%(1<<depth))
		}
		if got := pl.At(5, 3); got != uint16(23*7%(1<<depth)) {
			t.Errorf("depth %d: At(5, 3) = %d", depth, got)
		}
		pl.fill(1 << (depth - 1))
		for i := 0; i < pl.Len(); i++ {
			if pl.get(i) != 1<<(depth-1) {
				t.Fatalf("depth %d: sample %d is %d after fill", depth, i, pl.get(i))
			}
		}
		// a window shares the memory and stops at its last sample
		w := pl.window(1*pl.Stride+2, 3, 2)
		if w.Width != 3 || w.Height != 2 || w.Stride != 6 || w.Len() != 6+3 || w.Is8() != pl.Is8() {
			t.Errorf("depth %d: window %+v", depth, w)
		}
		w.set(0, 77)
		if pl.get(1*pl.Stride+2) != 77 {
			t.Errorf("depth %d: the window is a copy", depth)
		}
	}
}

// TestPictureIs8: a picture is made of bytes when every component is.
func TestPictureIs8(t *testing.T) {
	sps := SPS{CodedWidth: 16, CodedHeight: 16, ChromaFormat: 1, BitDepthLuma: 8, BitDepthChroma: 8}
	if p := NewDecodedPicture(sps); !p.Is8() || p.Components != 3 {
		t.Errorf("8-bit picture: Is8 %v", p.Is8())
	}
	sps.BitDepthLuma, sps.BitDepthChroma = 10, 10
	if p := NewDecodedPicture(sps); p.Is8() {
		t.Error("a 10-bit picture is not a byte picture")
	}
	if (&DecodedPicture{}).Is8() {
		t.Error("a picture with no component is not a byte picture")
	}
}

// TestWidenAndNarrowRoundTrip: the bridge between a byte plane and the kernels' 16-bit samples
// keeps the samples, whatever the strides.
func TestWidenAndNarrowRoundTrip(t *testing.T) {
	src := make([]uint8, 7*5)
	for i := range src {
		src[i] = uint8(i * 11)
	}
	wide := make([]uint16, 4*3)
	widen(wide, 0, 4, src, 1*7+2, 7, 4, 3)
	back := make([]uint8, 7*5)
	narrow(back, 1*7+2, 7, wide, 0, 4, 4, 3)
	for y := 1; y < 4; y++ {
		for x := 2; x < 6; x++ {
			if back[y*7+x] != src[y*7+x] {
				t.Fatalf("(%d,%d): %d, was %d", x, y, back[y*7+x], src[y*7+x])
			}
		}
	}
}
