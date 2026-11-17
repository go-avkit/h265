// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"

	"github.com/go-videodsp/kernels/hevc"
)

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
		return int32(pl.get(clip3(0, pl.Height-1, y)*pl.Stride + clip3(0, pl.Width-1, x)))
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
func weighted(sc *interpScratch, pl *Plane, x0, y0, w, h int, p [2][]int32, predFlag [2]bool, bitDepth int, ew *explicitWeights) {
	bi := predFlag[0] && predFlag[1]
	l := bit(predFlag[1]) // the list of a uni-predicted block
	if weightedKernel(sc, pl, x0, y0, w, h, p, bi, l, bitDepth, ew) {
		return
	}
	if pl.Pix8 != nil {
		weightedGo(pl.Pix8, y0*pl.Stride+x0, pl.Stride, w, h, p, bi, l, bitDepth, ew)
	} else {
		weightedGo(pl.Pix16, y0*pl.Stride+x0, pl.Stride, w, h, p, bi, l, bitDepth, ew)
	}
}

// weightedGo is the loops of weighted, for a plane of either sample type.
func weightedGo[T pixel](pix []T, base, stride, w, h int, p [2][]int32, bi bool, l int, bitDepth int, ew *explicitWeights) {
	maxVal := int32(1)<<uint(bitDepth) - 1
	shift1 := uint(max(2, 14-bitDepth))
	for y := 0; y < h; y++ {
		o := pix[base+y*stride : base+y*stride+w]
		var a, b []int32
		if bi {
			a, b = p[0][y*w:y*w+w], p[1][y*w:y*w+w]
		} else {
			a = p[l][y*w : y*w+w]
		}
		switch {
		case ew == nil && bi:
			shift2 := shift1 + 1
			for x := range o {
				o[x] = T(min(max((a[x]+b[x]+1<<(shift2-1))>>shift2, 0), maxVal)) // (8-264)
			}
		case ew == nil:
			for x := range o {
				o[x] = T(min(max((a[x]+1<<(shift1-1))>>shift1, 0), maxVal)) // (8-262), (8-263)
			}
		case bi:
			w0, w1, off, sh := ew.w[0], ew.w[1], (ew.o[0]+ew.o[1]+1)<<uint(ew.log2Wd), uint(ew.log2Wd+1)
			for x := range o {
				o[x] = T(min(max((a[x]*w0+b[x]*w1+off)>>sh, 0), maxVal)) // (8-277)
			}
		default:
			wl, ol, rnd, sh := ew.w[l], ew.o[l], int32(1)<<uint(ew.log2Wd-1), uint(ew.log2Wd)
			for x := range o {
				o[x] = T(min(max(((a[x]*wl+rnd)>>sh)+ol, 0), maxVal)) // (8-275), (8-276)
			}
		}
	}
}

// weightedKernel runs the block through the kernels' WeightUni or WeightBi, whose
// default case is w = 1, o = 0 and log2Wd = max(2, 14 - bitDepth) (8-262 to 8-264 are
// 8-275 to 8-277 with those values). It reports false when the kernels refuse the block
// (a width they do not take, a weight outside their bounds), and the loops of weighted
// then compute it.
func weightedKernel(sc *interpScratch, pl *Plane, x0, y0, w, h int, p [2][]int32, bi bool, l int, bitDepth int, ew *explicitWeights) bool {
	log2Wd, w0, w1, o0, o1 := max(2, 14-bitDepth), int32(1), int32(1), int32(0), int32(0)
	if ew != nil {
		log2Wd, w0, w1, o0, o1 = ew.log2Wd, ew.w[0], ew.w[1], ew.o[0], ew.o[1]
	}
	off, stride := y0*pl.Stride+x0, pl.Stride
	var err error
	name := "WeightUni"
	if bi {
		name = "WeightBi"
	}
	switch {
	case pl.Pix8 != nil && bitDepth != 8:
		return false // the byte kernels clip at 255
	case pl.Pix8 != nil && bi:
		err = hevc.TryWeightBiBytes(pl.Pix8, off, stride, p[0], p[1], 0, w, w, h, log2Wd, w0, w1, o0, o1)
	case pl.Pix8 != nil:
		if l == 1 {
			w0, o0 = w1, o1
		}
		err = hevc.TryWeightUniBytes(pl.Pix8, off, stride, p[l], 0, w, w, h, log2Wd, w0, o0)
	case bi:
		err = hevc.TryWeightBi(pl.Pix16, off, stride, p[0], p[1], 0, w, w, h, bitDepth, log2Wd, w0, w1, o0, o1)
	default:
		if l == 1 {
			w0, o0 = w1, o1
		}
		err = hevc.TryWeightUni(pl.Pix16, off, stride, p[l], 0, w, w, h, bitDepth, log2Wd, w0, o0)
	}
	return !kernelRefused(name, err)
}

// fullPel is a block of a reference plane at a full-sample position inside the plane.
type fullPel struct {
	ref    *Plane
	xi, yi int
	ok     bool
}

// average writes the default weighted prediction of a block from two lists at full-sample
// positions, (A0 + A1 + 1) >> 1: (8-264) with the intermediates A << shift3 of 8.5.3.3.3.2,
// where shift2 is shift3 + 1.
func average(dst *Plane, x0, y0, w, h int, a, b fullPel) {
	if dst.Pix8 != nil {
		if !kernelRefused("AverageBytes", hevc.TryAverageBytes(dst.Pix8, y0*dst.Stride+x0, dst.Stride, a.ref.Pix8, a.yi*a.ref.Stride+a.xi, a.ref.Stride, b.ref.Pix8, b.yi*b.ref.Stride+b.xi, b.ref.Stride, w, h)) {
			return
		}
		averageGo(dst.Pix8, y0*dst.Stride+x0, dst.Stride, a.ref.Pix8, a.yi*a.ref.Stride+a.xi, a.ref.Stride, b.ref.Pix8, b.yi*b.ref.Stride+b.xi, b.ref.Stride, w, h)
		return
	}
	if !kernelRefused("Average", hevc.TryAverage(dst.Pix16, y0*dst.Stride+x0, dst.Stride, a.ref.Pix16, a.yi*a.ref.Stride+a.xi, a.ref.Stride, b.ref.Pix16, b.yi*b.ref.Stride+b.xi, b.ref.Stride, w, h)) {
		return
	}
	averageGo(dst.Pix16, y0*dst.Stride+x0, dst.Stride, a.ref.Pix16, a.yi*a.ref.Stride+a.xi, a.ref.Stride, b.ref.Pix16, b.yi*b.ref.Stride+b.xi, b.ref.Stride, w, h)
}

// averageGo is (a + b + 1) >> 1 of the w x h blocks at index ai of pa and bi of pb, written at
// index di of dst.
func averageGo[T pixel](dst []T, di, ds int, pa []T, ai, as int, pb []T, bi, bs int, w, h int) {
	for y := 0; y < h; y++ {
		ra, rb := pa[ai+y*as:ai+y*as+w], pb[bi+y*bs:bi+y*bs+w]
		o := dst[di+y*ds : di+y*ds+w]
		for x := range o {
			o[x] = T((uint32(ra[x]) + uint32(rb[x]) + 1) >> 1)
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
			copied := false
			var fp [2]fullPel
			for l := 0; l < 2; l++ {
				if !pu.Motion.Pred[l] {
					continue
				}
				e := sl.List[l][pu.Motion.RefIdx[l]]
				if e.Pic == nil || e.Pic.Frame == nil || e.Pic.Frame.Components != r.Frame.Components {
					return fmt.Errorf("h265: reference %d of list %d is not a decoded picture", pu.Motion.RefIdx[l], l)
				}
				ref := &e.Pic.Frame.Planes[c]
				if ref.Width != dst.Width || ref.Height != dst.Height || ref.Is8() != dst.Is8() {
					return fmt.Errorf("h265: a reference picture of %dx%d (bytes %v) for a picture of %dx%d (bytes %v)", ref.Width, ref.Height, ref.Is8(), dst.Width, dst.Height, dst.Is8())
				}
				pred[l] = r.sc.pred[l][:w*h]
				mv := pu.Motion.MV[l]
				xi, yi, xf, yf, before, taps := pu.X+int(mv[0])>>2, pu.Y+int(mv[1])>>2, int(mv[0])&3, int(mv[1])&3, 3, lumaTaps[:]
				if c > 0 {
					mvx, mvy := int(mv[0])*2/subW, int(mv[1])*2/subH // (8-210), (8-211)
					xi, yi, xf, yf, before, taps = x0+mvx>>3, y0+mvy>>3, mvx&7, mvy&7, 1, chromaTaps[:]
				}
				// A block predicted from one list at a full-sample position, with the default
				// weighting, is a copy: (A << shift3 + (1 << (shift1 - 1))) >> shift1 is A
				// (8-262) as shift1 and shift3 are the same number, Max( 2, 14 - bitDepth ).
				if pu.Motion.Pred[0] != pu.Motion.Pred[1] && sl.Weights == nil && xf == 0 && yf == 0 &&
					xi >= 0 && yi >= 0 && xi+w <= ref.Width && yi+h <= ref.Height {
					for y := 0; y < h; y++ {
						if dst.Pix8 != nil {
							copy(dst.Pix8[(y0+y)*dst.Stride+x0:(y0+y)*dst.Stride+x0+w], ref.Pix8[(yi+y)*ref.Stride+xi:(yi+y)*ref.Stride+xi+w])
						} else {
							copy(dst.Pix16[(y0+y)*dst.Stride+x0:(y0+y)*dst.Stride+x0+w], ref.Pix16[(yi+y)*ref.Stride+xi:(yi+y)*ref.Stride+xi+w])
						}
					}
					copied = true
					continue
				}
				// From both lists the block is the average (A0 + A1 + 1) >> 1 (8-264), made after
				// the loop when both lists are at full-sample positions.
				if pu.Motion.Pred[0] && pu.Motion.Pred[1] && sl.Weights == nil && xf == 0 && yf == 0 &&
					xi >= 0 && yi >= 0 && xi+w <= ref.Width && yi+h <= ref.Height {
					fp[l] = fullPel{ref, xi, yi, true}
					continue
				}
				if bitDepth <= 12 { // the ranges of interpfast.go hold
					interpolateFast(&r.sc, ref, xi, yi, xf, yf, w, h, bitDepth, before, taps, pred[l])
				} else {
					interpolate(ref, xi, yi, xf, yf, w, h, bitDepth, before, taps, pred[l])
				}
			}
			if copied {
				continue
			}
			if fp[0].ok && fp[1].ok {
				average(dst, x0, y0, w, h, fp[0], fp[1])
				continue
			}
			for l := range fp { // one list at a full-sample position, the other not: interpolate it as usual
				if fp[l].ok {
					taps, before := lumaTaps[:], 3
					if c > 0 {
						taps, before = chromaTaps[:], 1
					}
					if bitDepth <= 12 {
						interpolateFast(&r.sc, fp[l].ref, fp[l].xi, fp[l].yi, 0, 0, w, h, bitDepth, before, taps, pred[l])
					} else {
						interpolate(fp[l].ref, fp[l].xi, fp[l].yi, 0, 0, w, h, bitDepth, before, taps, pred[l])
					}
				}
			}
			var ew *explicitWeights
			if sl.Weights != nil {
				ew = explicitFor(sl.Weights, r.sps, &pu.Motion, c)
			}
			weighted(&r.sc, dst, x0, y0, w, h, pred, pu.Motion.Pred, bitDepth, ew)
		}
	}
	return nil
}
