// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "github.com/go-videodsp/kernels/hevc"

// The fractional sample interpolation of 8.5.3.3.3 without a function call per sample.
// interpolate (inter.go) stays the reference this is tested against, and the path for
// bit depths above 12.
//
// The window of reference samples a block reads is (w + taps - 1) x (h + taps - 1) at
// most; where it lies inside the plane it is read in place, and otherwise copied once
// with its edge samples replicated (8-222, 8-223, 8-239, 8-240) so that the filters
// never clamp.
//
// Ranges (bit depth up to 12). The first stage keeps its results in int16: with
// shift1 = bitDepth - 8 its outputs lie in
//
//	-24 * (2^bd - 1) / 2^(bd-8) ... 88 * (2^bd - 1) / 2^(bd-8),
//
// 24 and 88 being the sums of the magnitudes of the negative and of the positive taps
// of the half-sample luma filter, the largest of the filters, so within -6144 ... 22528.
// A full-sample copy is A << (14 - bd) < 2^14. The second stage sums at most eight
// int16 values times taps of magnitude at most 58, far inside int32; its results can
// pass 16 bits (up to about 33 150 for 8-bit video), so they are kept as int32.

// interpScratch is the working memory of a block: the replicated window and the
// first-stage rows. The largest block is 64 x 64 with 7 more rows and columns.
type interpScratch struct {
	edge [71 * 71]uint16
	// edge8 is the replicated window of a byte plane.
	edge8 [71 * 71]uint8
	mid   [71 * 64]int16
	pred  [2][64 * 64]int32
	// blk is a block the kernels write as 16-bit samples before they are narrowed into a byte plane.
	blk [64 * 64]uint16
}

// clampedWindow fills dst (rows ss apart) with the ww x hh window of the plane at ( x0, y0 ),
// the samples outside the plane being those of its nearest edge (8-222, 8-223).
func clampedWindow[D, T pixel](dst []D, ss int, pix []T, pl *Plane, x0, y0, ww, hh int) {
	for y := 0; y < hh; y++ {
		row := pix[clip3(0, pl.Height-1, y0+y)*pl.Stride:]
		for x := 0; x < ww; x++ {
			dst[y*ss+x] = D(row[clip3(0, pl.Width-1, x0+x)])
		}
	}
}

// interpolateFast is interpolate for bitDepth <= 12 and blocks of at most 64 x 64.
func interpolateFast(sc *interpScratch, pl *Plane, xInt, yInt, xFrac, yFrac, w, h, bitDepth, before int, taps [][]int32, out []int32) {
	shift1, shift3 := uint(min(4, bitDepth-8)), uint(max(2, 14-bitDepth))
	n := len(taps[len(taps)-1]) // 8 or 4
	x0, y0, ww, hh := xInt, yInt, w, h
	if xFrac != 0 {
		x0, ww = xInt-before, w+n-1
	}
	if yFrac != 0 {
		y0, hh = yInt-before, h+n-1
	}
	var src []uint16
	var ss int
	inside := x0 >= 0 && y0 >= 0 && x0+ww <= pl.Width && y0+hh <= pl.Height
	switch {
	case inside && pl.Pix8 == nil:
		src, ss = pl.Pix16[y0*pl.Stride+x0:], pl.Stride
	case pl.Pix8 != nil:
		// the byte kernels read the plane (or its replicated window) as it is
		var src8 []uint8
		var ss8 int
		if inside {
			src8, ss8 = pl.Pix8[y0*pl.Stride+x0:], pl.Stride
		} else {
			src8, ss8 = sc.edge8[:ww*hh], ww
			clampedWindow(src8, ss8, pl.Pix8, pl, x0, y0, ww, hh)
		}
		if interpolateBytes(sc, src8, ss8, w, h, hh, xFrac, yFrac, taps, shift3, out) {
			return
		}
		// refused: widen the window and take the 16-bit path
		ss = ww
		src = sc.edge[:ww*hh]
		widen(src, 0, ss, src8, 0, ss8, ww, hh)
	default:
		ss = ww
		src = sc.edge[:ww*hh]
		clampedWindow(src, ss, pl.Pix16, pl, x0, y0, ww, hh)
	}
	switch {
	case xFrac == 0 && yFrac == 0:
		if !kernelRefused("Widen", hevc.TryWiden(out, 0, w, src, 0, ss, w, h, int(shift3))) {
			return
		}
		for y := 0; y < h; y++ {
			o := out[y*w : y*w+w]
			row := src[y*ss : y*ss+w][:len(o)]
			for x := range o {
				o[x] = int32(row[x]) << shift3
			}
		}
	case yFrac == 0:
		if kernelHW(src, ss, w, h, bitDepth, taps[xFrac], out) {
			return
		}
		hpass(src, ss, h, w, taps[xFrac], shift1, out)
	case xFrac == 0:
		if kernelV(src, ss, w, h, bitDepth, taps[yFrac], out) {
			return
		}
		vpass(src, ss, w, h, taps[yFrac], shift1, out)
	default:
		mid := sc.mid[:hh*w]
		if kernelHV(sc, src, ss, w, h, bitDepth, taps[xFrac], taps[yFrac], out) {
			return
		}
		hpass(src, ss, hh, w, taps[xFrac], shift1, mid)
		vpass(mid, w, w, h, taps[yFrac], 6, out)
	}
}

// coefficients is a luma filter as the kernels take it.
func coefficients(taps []int32) (c [8]int16) {
	for i, t := range taps {
		c[i] = int16(t)
	}
	return
}

// coefficients4 is a chroma filter as the kernels take it.
func coefficients4(taps []int32) (c [4]int16) {
	for i, t := range taps {
		c[i] = int16(t)
	}
	return
}

// kernelHV runs both passes of the kernels package for a block of h rows: the
// horizontal one over h + 7 rows into sc.mid, then the vertical one into out; false when
// the kernels refuse either, which then run in Go.
func kernelHV(sc *interpScratch, src []uint16, ss, w, h, bitDepth int, tx, ty []int32, out []int32) bool {
	if len(tx) == 4 { // chroma: four taps, one row before and two after
		cy := coefficients4(ty)
		return kernelH(sc, src, ss, w, h+3, bitDepth, tx) &&
			!kernelRefused("VPass4", hevc.TryVPass4(out, 0, w, sc.mid[:(h+3)*w], w, w, w, h, &cy))
	}
	cy := coefficients(ty)
	return kernelH(sc, src, ss, w, h+7, bitDepth, tx) &&
		!kernelRefused("VPass8", hevc.TryVPass8(out, 0, w, sc.mid[:(h+7)*w], 3*w, w, w, h, &cy))
}

// bandRows is the most rows one call of the kernels' passes takes. A block of 64 rows
// needs 71 of them for the horizontal pass of a two-dimensional filter, so that pass runs
// in bands.
const bandRows = 64

// kernelH runs the horizontal pass of the kernels package over rows rows into sc.mid
// (int16, w per row), in bands of at most bandRows; false when the kernels refuse a call,
// which then runs in Go.
func kernelH(sc *interpScratch, src []uint16, ss, w, rows, bitDepth int, taps []int32) bool {
	dst := sc.mid[:rows*w]
	if len(taps) == 4 {
		c := coefficients4(taps)
		for r := 0; r < rows; r += bandRows {
			if kernelRefused("HPass4", hevc.TryHPass4(dst, r*w, w, src, 1+r*ss, ss, w, min(bandRows, rows-r), bitDepth, &c)) {
				return false
			}
		}
		return true
	}
	c := coefficients(taps)
	for r := 0; r < rows; r += bandRows {
		if kernelRefused("HPass8", hevc.TryHPass8(dst, r*w, w, src, 3+r*ss, ss, w, min(bandRows, rows-r), bitDepth, &c)) {
			return false
		}
	}
	return true
}

// kernelHW runs the horizontal filter of a block with no vertical fraction straight into out
// (int32; src starting at the leftmost tap column); false when the kernels refuse the call,
// which then runs in Go.
func kernelHW(src []uint16, ss, w, h, bitDepth int, taps []int32, out []int32) bool {
	if len(taps) == 4 {
		c := coefficients4(taps)
		return !kernelRefused("HPassW4", hevc.TryHPassW4(out, 0, w, src, 1, ss, w, h, bitDepth, &c))
	}
	c := coefficients(taps)
	return !kernelRefused("HPassW8", hevc.TryHPassW8(out, 0, w, src, 3, ss, w, h, bitDepth, &c))
}

// kernelV runs the vertical filter of a block with no horizontal fraction over the samples
// themselves (src starting at the topmost tap row) into out; false when the kernels refuse the
// call, which then runs in Go.
func kernelV(src []uint16, ss, w, h, bitDepth int, taps []int32, out []int32) bool {
	if len(taps) == 4 {
		c := coefficients4(taps)
		return !kernelRefused("VPassU4", hevc.TryVPassU4(out, 0, w, src, ss, ss, w, h, bitDepth, &c))
	}
	c := coefficients(taps)
	return !kernelRefused("VPassU8", hevc.TryVPassU8(out, 0, w, src, 3*ss, ss, w, h, bitDepth, &c))
}

// interpolateBytes is the kernels' interpolation over the bytes of an 8-bit window (src
// starting at the first sample the filter reads, rows ss apart, hh rows of it); false when
// the kernels refuse a call, which then runs on the widened window.
func interpolateBytes(sc *interpScratch, src []uint8, ss, w, h, hh, xFrac, yFrac int, taps [][]int32, shift3 uint, out []int32) bool {
	n := len(taps[len(taps)-1])
	switch {
	case xFrac == 0 && yFrac == 0:
		return !kernelRefused("WidenBytes", hevc.TryWidenBytes(out, 0, w, src, 0, ss, w, h, int(shift3)))
	case yFrac == 0 && n == 4:
		c := coefficients4(taps[xFrac])
		return !kernelRefused("HPassW4Bytes", hevc.TryHPassW4Bytes(out, 0, w, src, 1, ss, w, h, &c))
	case yFrac == 0:
		c := coefficients(taps[xFrac])
		return !kernelRefused("HPassW8Bytes", hevc.TryHPassW8Bytes(out, 0, w, src, 3, ss, w, h, &c))
	case xFrac == 0 && n == 4:
		c := coefficients4(taps[yFrac])
		return !kernelRefused("VPassU4Bytes", hevc.TryVPassU4Bytes(out, 0, w, src, ss, ss, w, h, &c))
	case xFrac == 0:
		c := coefficients(taps[yFrac])
		return !kernelRefused("VPassU8Bytes", hevc.TryVPassU8Bytes(out, 0, w, src, 3*ss, ss, w, h, &c))
	}
	// both fractions: the horizontal pass over hh rows into sc.mid (in bands), then the vertical
	mid := sc.mid[:hh*w]
	if n == 4 {
		c := coefficients4(taps[xFrac])
		for r := 0; r < hh; r += bandRows {
			if kernelRefused("HPass4Bytes", hevc.TryHPass4Bytes(mid, r*w, w, src, 1+r*ss, ss, w, min(bandRows, hh-r), &c)) {
				return false
			}
		}
		cy := coefficients4(taps[yFrac])
		return !kernelRefused("VPass4", hevc.TryVPass4(out, 0, w, mid, w, w, w, h, &cy))
	}
	c := coefficients(taps[xFrac])
	for r := 0; r < hh; r += bandRows {
		if kernelRefused("HPass8Bytes", hevc.TryHPass8Bytes(mid, r*w, w, src, 3+r*ss, ss, w, min(bandRows, hh-r), &c)) {
			return false
		}
	}
	cy := coefficients(taps[yFrac])
	return !kernelRefused("VPass8", hevc.TryVPass8(out, 0, w, mid, 3*w, w, w, h, &cy))
}

// kernelRefusal, when set, is called with the name of the entry point each time the
// kernels refuse a call and the Go loops take over: a refusal is a silent slowdown, so the
// tests count them (a shape the kernels claim to take must not be refused).
var kernelRefusal func(name string, err error)

// kernelRefused reports whether err is a refusal, and tells kernelRefusal.
func kernelRefused(name string, err error) bool {
	if err == nil {
		return false
	}
	if kernelRefusal != nil {
		kernelRefusal(name, err)
	}
	return true
}

// hpass is the horizontal filter over rows rows of w samples: dst[y*w+x] is the sum of
// the taps c times src[y*ss+x ...], shifted.
func hpass[D int16 | int32](src []uint16, ss, rows, w int, c []int32, shift uint, dst []D) {
	if len(c) == 8 {
		c0, c1, c2, c3, c4, c5, c6, c7 := c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7]
		for y := 0; y < rows; y++ {
			row, o := src[y*ss:y*ss+w+7], dst[y*w:y*w+w]
			for x := range o {
				r := row[x : x+8 : x+8]
				o[x] = D((c0*int32(r[0]) + c1*int32(r[1]) + c2*int32(r[2]) + c3*int32(r[3]) +
					c4*int32(r[4]) + c5*int32(r[5]) + c6*int32(r[6]) + c7*int32(r[7])) >> shift)
			}
		}
		return
	}
	c0, c1, c2, c3 := c[0], c[1], c[2], c[3]
	for y := 0; y < rows; y++ {
		row, o := src[y*ss:y*ss+w+3], dst[y*w:y*w+w]
		for x := range o {
			r := row[x : x+4 : x+4]
			o[x] = D((c0*int32(r[0]) + c1*int32(r[1]) + c2*int32(r[2]) + c3*int32(r[3])) >> shift)
		}
	}
}

// vpass is the vertical filter: dst[y*w+x] is the sum of the taps c times
// src[(y+k)*ss+x], shifted.
func vpass[S uint16 | int16](src []S, ss, w, h int, c []int32, shift uint, dst []int32) {
	if len(c) == 8 {
		c0, c1, c2, c3, c4, c5, c6, c7 := c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7]
		for y := 0; y < h; y++ {
			r0, r1, r2, r3 := src[y*ss:y*ss+w], src[(y+1)*ss:(y+1)*ss+w], src[(y+2)*ss:(y+2)*ss+w], src[(y+3)*ss:(y+3)*ss+w]
			r4, r5, r6, r7 := src[(y+4)*ss:(y+4)*ss+w], src[(y+5)*ss:(y+5)*ss+w], src[(y+6)*ss:(y+6)*ss+w], src[(y+7)*ss:(y+7)*ss+w]
			o := dst[y*w : y*w+w]
			for x := range o {
				o[x] = (c0*int32(r0[x]) + c1*int32(r1[x]) + c2*int32(r2[x]) + c3*int32(r3[x]) +
					c4*int32(r4[x]) + c5*int32(r5[x]) + c6*int32(r6[x]) + c7*int32(r7[x])) >> shift
			}
		}
		return
	}
	c0, c1, c2, c3 := c[0], c[1], c[2], c[3]
	for y := 0; y < h; y++ {
		r0, r1, r2, r3 := src[y*ss:y*ss+w], src[(y+1)*ss:(y+1)*ss+w], src[(y+2)*ss:(y+2)*ss+w], src[(y+3)*ss:(y+3)*ss+w]
		o := dst[y*w : y*w+w]
		for x := range o {
			o[x] = (c0*int32(r0[x]) + c1*int32(r1[x]) + c2*int32(r2[x]) + c3*int32(r3[x])) >> shift
		}
	}
}
