// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

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
	mid  [71 * 64]int16
	pred [2][64 * 64]int32
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
	if x0 >= 0 && y0 >= 0 && x0+ww <= pl.Width && y0+hh <= pl.Height {
		src, ss = pl.Pix[y0*pl.Stride+x0:], pl.Stride
	} else {
		ss = ww
		src = sc.edge[:ww*hh]
		for y := 0; y < hh; y++ {
			row := pl.Pix[clip3(0, pl.Height-1, y0+y)*pl.Stride:]
			for x := 0; x < ww; x++ {
				src[y*ss+x] = row[clip3(0, pl.Width-1, x0+x)]
			}
		}
	}
	switch {
	case xFrac == 0 && yFrac == 0:
		for y := 0; y < h; y++ {
			row, o := src[y*ss:y*ss+w], out[y*w:y*w+w]
			for x := range o {
				o[x] = int32(row[x]) << shift3
			}
		}
	case yFrac == 0:
		hpass(src, ss, h, w, taps[xFrac], shift1, out)
	case xFrac == 0:
		vpass(src, ss, w, h, taps[yFrac], shift1, out)
	default:
		mid := sc.mid[:hh*w]
		hpass(src, ss, hh, w, taps[xFrac], shift1, mid)
		vpass(mid, w, w, h, taps[yFrac], 6, out)
	}
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
