// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "fmt"

// Inter sample prediction: the fractional sample interpolation process (8.5.3.3.3) and
// the weighted sample prediction process (8.5.3.3.4).

// lumaTaps are the coefficients of the 8-tap filters of (8-224) to (8-226) for the
// quarter positions 1 to 3, applied to the samples at offsets -3 to 4.
var lumaTaps = [4][]int32{
	nil,
	{-1, 4, -10, 58, 17, -5, 1, 0},
	{-1, 4, -11, 40, 40, -11, 4, -1},
	{0, 1, -5, 17, 58, -10, 4, -1},
}

// chromaTaps are the coefficients of the 4-tap filters of (8-241) to (8-247) for the
// eighth positions 1 to 7, applied to the samples at offsets -1 to 2.
var chromaTaps = [8][]int32{
	nil,
	{-2, 58, 10, -2},
	{-4, 54, 16, -2},
	{-6, 46, 28, -4},
	{-4, 36, 36, -4},
	{-4, 28, 46, -6},
	{-2, 16, 54, -4},
	{-2, 10, 58, -2},
}

// interpolate fills out (w x h, row by row) with the prediction samples of the block
// whose top-left full-sample position in pl is ( xInt, yInt ) and whose fractional
// offset is ( xFrac, yFrac ): the luma process when before is 3 (8-214 to 8-238) and
// the chroma one when it is 1 (8-218 to 8-261). Reference samples outside the plane
// are those of its nearest edge (8-222, 8-223, 8-239, 8-240). The values are the
// 14-bit intermediate ones the weighted prediction takes.
func interpolate(pl *Plane, xInt, yInt, xFrac, yFrac, w, h, bitDepth, before int, taps [][]int32, out []int32) {
	shift1, shift3 := min(4, bitDepth-8), max(2, 14-bitDepth)
	at := func(x, y int) int32 {
		return int32(pl.Pix[clip3(0, pl.Height-1, y)*pl.Stride+clip3(0, pl.Width-1, x)])
	}
	switch {
	case xFrac == 0 && yFrac == 0:
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				out[y*w+x] = at(xInt+x, yInt+y) << uint(shift3)
			}
		}
	case yFrac == 0:
		tx := taps[xFrac]
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s int32
				for k, c := range tx {
					s += c * at(xInt+x+k-before, yInt+y)
				}
				out[y*w+x] = s >> uint(shift1)
			}
		}
	case xFrac == 0:
		ty := taps[yFrac]
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s int32
				for k, c := range ty {
					s += c * at(xInt+x, yInt+y+k-before)
				}
				out[y*w+x] = s >> uint(shift1)
			}
		}
	default:
		tx, ty := taps[xFrac], taps[yFrac]
		rows := h + len(ty) - 1
		mid := make([]int32, rows*w)
		for y := 0; y < rows; y++ {
			for x := 0; x < w; x++ {
				var s int32
				for k, c := range tx {
					s += c * at(xInt+x+k-before, yInt+y-before)
				}
				mid[y*w+x] = s >> uint(shift1)
			}
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var s int32
				for k, c := range ty {
					s += c * mid[(y+k)*w+x]
				}
				out[y*w+x] = s >> 6 // shift2
			}
		}
	}
}

// explicitWeights are the variables of 8.5.3.3.4.3 for one component of one block.
type explicitWeights struct {
	log2Wd int
	w, o   [2]int32
}

// weighted is the weighted sample prediction process (8.5.3.3.4): from the
// intermediate samples p[ X ] of the lists predFlag says are used it writes the w x h
// block at ( x0, y0 ) of pl. ew is nil for the default process (8.5.3.3.4.2).
func weighted(pl *Plane, x0, y0, w, h int, p [2][]int32, predFlag [2]bool, bitDepth int, ew *explicitWeights) {
	maxVal := int32(1)<<uint(bitDepth) - 1
	shift1 := uint(max(2, 14-bitDepth))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			var v int32
			switch {
			case ew == nil && predFlag[0] && predFlag[1]:
				shift2 := shift1 + 1
				v = (p[0][i] + p[1][i] + 1<<(shift2-1)) >> shift2 // (8-264)
			case ew == nil:
				l := bit(predFlag[1])
				v = (p[l][i] + 1<<(shift1-1)) >> shift1 // (8-262), (8-263)
			case predFlag[0] && predFlag[1]:
				v = (p[0][i]*ew.w[0] + p[1][i]*ew.w[1] + (ew.o[0]+ew.o[1]+1)<<uint(ew.log2Wd)) >> uint(ew.log2Wd+1) // (8-277)
			default:
				l := bit(predFlag[1])
				v = ((p[l][i]*ew.w[l] + 1<<uint(ew.log2Wd-1)) >> uint(ew.log2Wd)) + ew.o[l] // (8-275), (8-276)
			}
			pl.Pix[(y0+y)*pl.Stride+x0+x] = uint16(min(max(v, 0), maxVal))
		}
	}
}

// explicitFor is what 8.5.3.3.4.3 derives for component c from the weights the slice
// states: log2Wd, w0, w1, o0 and o1 for the reference indices of m (8-265 to 8-274).
func explicitFor(pw *PredWeights, sps SPS, m *Motion, c int) *explicitWeights {
	bitDepth := int(sps.BitDepthLuma)
	denom := int(pw.LumaLog2Denom)
	if c > 0 {
		bitDepth, denom = int(sps.BitDepthChroma), int(pw.ChromaLog2Denom)
	}
	shift := bitDepth - 8 // WpOffsetBdShiftY and WpOffsetBdShiftC
	if sps.Range.HighPrecisionOffsets {
		shift = 0
	}
	ew := &explicitWeights{log2Wd: denom + max(2, 14-bitDepth)}
	for l := 0; l < 2; l++ {
		if !m.Pred[l] {
			continue
		}
		list := pw.L0
		if l == 1 {
			list = pw.L1
		}
		e := &list[m.RefIdx[l]]
		if c == 0 {
			ew.w[l], ew.o[l] = e.LumaWeight, e.LumaOffset<<uint(shift)
		} else {
			ew.w[l], ew.o[l] = e.ChromaWeight[c-1], e.ChromaOffset[c-1]<<uint(shift)
		}
	}
	return ew
}

// predictInter writes the inter prediction of every prediction block of cu into the
// picture (8.5.3.3.1); the residual is added afterwards, block by block.
func (r *Reconstructor) predictInter(cu *CodingUnit) error {
	sl := r.Inter
	if sl == nil {
		return fmt.Errorf("%w: an inter coding unit without the reference lists of its slice", ErrNotReconstructed)
	}
	for i := 0; i < cu.NumPU; i++ {
		pu := &cu.PU[i]
		for c := 0; c < r.Frame.Components; c++ {
			dst := &r.Frame.Planes[c]
			subW, subH := 1, 1
			bitDepth := int(r.sps.BitDepthLuma)
			if c > 0 {
				subW, subH = int(r.sps.CodedWidth)/dst.Width, int(r.sps.CodedHeight)/dst.Height
				bitDepth = int(r.sps.BitDepthChroma)
			}
			x0, y0, w, h := pu.X/subW, pu.Y/subH, pu.W/subW, pu.H/subH
			var pred [2][]int32
			for l := 0; l < 2; l++ {
				if !pu.Motion.Pred[l] {
					continue
				}
				e := sl.List[l][pu.Motion.RefIdx[l]]
				if e.Pic == nil || e.Pic.Frame == nil || e.Pic.Frame.Components != r.Frame.Components {
					return fmt.Errorf("h265: reference %d of list %d is not a decoded picture", pu.Motion.RefIdx[l], l)
				}
				ref := &e.Pic.Frame.Planes[c]
				if ref.Width != dst.Width || ref.Height != dst.Height {
					return fmt.Errorf("h265: a reference picture of %dx%d for a picture of %dx%d", ref.Width, ref.Height, dst.Width, dst.Height)
				}
				pred[l] = make([]int32, w*h)
				mv := pu.Motion.MV[l]
				if c == 0 {
					interpolate(ref, pu.X+int(mv[0])>>2, pu.Y+int(mv[1])>>2, int(mv[0])&3, int(mv[1])&3, w, h, bitDepth, 3, lumaTaps[:], pred[l])
				} else {
					mvx, mvy := int(mv[0])*2/subW, int(mv[1])*2/subH // (8-210), (8-211)
					interpolate(ref, x0+mvx>>3, y0+mvy>>3, mvx&7, mvy&7, w, h, bitDepth, 1, chromaTaps[:], pred[l])
				}
			}
			var ew *explicitWeights
			if sl.Weights != nil {
				ew = explicitFor(sl.Weights, r.sps, &pu.Motion, c)
			}
			weighted(dst, x0, y0, w, h, pred, pu.Motion.Pred, bitDepth, ew)
		}
	}
	return nil
}
