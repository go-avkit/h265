// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"
	"sync"

	"github.com/go-videodsp/kernels/hevc"
)

// The sample adaptive offset process (8.7.3). It runs on the deblocked picture, one
// coding tree block at a time, reading the deblocked samples and writing the picture.

// saoHPos and saoVPos are Table 8-13: the two neighbours of an edge offset class.
var (
	saoHPos = [4][2]int{{-1, 1}, {0, 0}, {-1, 1}, {1, -1}}
	saoVPos = [4][2]int{{0, 0}, {-1, 1}, {-1, 1}, {-1, 1}}
)

// saoScratch holds the buffers of the copy SAO reads its neighbours from, one pool for each
// sample type.
var (
	saoScratch8  = sync.Pool{New: func() any { return new([]uint8) }}
	saoScratch16 = sync.Pool{New: func() any { return new([]uint16) }}
)

// SAO applies sample adaptive offset to the deblocked picture frame (8.7.3.1) with
// the parameters the walk read for each coding tree block.
func (ps *PictureState) SAO(frame *DecodedPicture, sps SPS, pps PPS) {
	anyNoFilter := false
	for _, nf := range ps.NoFilter {
		anyNoFilter = anyNoFilter || nf
	}
	for c := 0; c < frame.Components; c++ {
		used := false
		for i := range ps.Sao {
			used = used || ps.Sao[i].Type[c] != 0
		}
		if !used {
			continue
		}
		pl := &frame.Planes[c]
		bitDepth := int(sps.BitDepthLuma)
		if c > 0 {
			bitDepth = int(sps.BitDepthChroma)
		}
		// the copy of the deblocked plane is scratch: a decoder runs one picture after another,
		// so the buffer is kept between them instead of allocated and cleared for each
		if pl.Pix8 != nil {
			bp := saoScratch8.Get().(*[]uint8)
			src := append((*bp)[:0], pl.Pix8...) // recPicture: the deblocked samples
			saoPlane(ps, pl, pl.Pix8, src, c, sps, pps, bitDepth, anyNoFilter)
			*bp = src
			saoScratch8.Put(bp)
			continue
		}
		bp := saoScratch16.Get().(*[]uint16)
		src := append((*bp)[:0], pl.Pix16...)
		saoPlane(ps, pl, pl.Pix16, src, c, sps, pps, bitDepth, anyNoFilter)
		*bp = src
		saoScratch16.Put(bp)
	}
}

// saoPlane runs the coding tree blocks of one plane.
func saoPlane[T pixel](ps *PictureState, pl *Plane, pix, src []T, c int, sps SPS, pps PPS, bitDepth int, anyNoFilter bool) {
	l := ps.Layout
	subW, subH := int(sps.CodedWidth)/pl.Width, int(sps.CodedHeight)/pl.Height
	ctbW, ctbH := (1<<uint(sps.Log2CtbSize))/subW, (1<<uint(sps.Log2CtbSize))/subH
	for ry := 0; ry < l.HeightCtbs; ry++ {
		for rx := 0; rx < l.WidthCtbs; rx++ {
			p := &ps.Sao[ry*l.WidthCtbs+rx]
			if p.Type[c] == 0 {
				continue
			}
			saoCTB(ps, pl, pix, src, c, p, rx, ry, ctbW, ctbH, subW, subH, bitDepth, anyNoFilter, pps)
		}
	}
}

// saoUsable says whether the samples of the coding tree block rsN may be used for the
// edge offset of a sample of rsCur: the neighbour in another slice counts through the
// slice_loop_filter_across_slices_enabled_flag of the later of the two (8.7.3.2), and
// one in another tile through loop_filter_across_tiles_enabled_flag.
func (ps *PictureState) saoUsable(rsN, rsCur int, pps PPS) bool {
	if rsN == rsCur {
		return true
	}
	l := ps.Layout
	if ps.SliceAddr[rsN] != ps.SliceAddr[rsCur] {
		later := rsCur
		if l.RsToTs[rsN] > l.RsToTs[rsCur] {
			later = rsN
		}
		if !ps.DbAcross[later] {
			return false
		}
	}
	return l.TileID[l.RsToTs[rsN]] == l.TileID[l.RsToTs[rsCur]] || pps.LoopFilterAcrossTiles
}

// errNotBytes is the refusal of a byte plane whose bit depth is not 8: the byte kernels clip at 255.
func errNotBytes(bitDepth int) error {
	return fmt.Errorf("%w: a byte plane of bit depth %d", hevc.ErrRefused, bitDepth)
}

// saoBandKernel runs the band offset of the block at ( x0, y0 ) through the kernels, over the
// samples of the plane whatever their type.
func saoBandKernel[T pixel](pix, src []T, stride, x0, y0, cw, ch, bitDepth, bandPos int, off *[4]int16) error {
	i := y0*stride + x0
	if p, ok := any(pix).([]uint16); ok {
		return hevc.TrySAOBand(p, i, stride, any(src).([]uint16), i, stride, cw, ch, bitDepth, bandPos, off)
	}
	if bitDepth != 8 {
		return errNotBytes(bitDepth)
	}
	return hevc.TrySAOBandBytes(any(pix).([]uint8), i, stride, any(src).([]uint8), i, stride, cw, ch, bandPos, off)
}

// saoEdgeKernel runs the edge offset of the rw x rh block at ( rx0, ry0 ), whose neighbours
// (one sample around it) all lie in the picture, through the kernels.
func saoEdgeKernel[T pixel](pix, src []T, stride, rx0, ry0, rw, rh, bitDepth, class int, off *[4]int16) error {
	i := ry0*stride + rx0
	if p, ok := any(pix).([]uint16); ok {
		return hevc.TrySAOEdge(p, i, stride, any(src).([]uint16), i, stride, rw, rh, bitDepth, class, off)
	}
	if bitDepth != 8 {
		return errNotBytes(bitDepth)
	}
	return hevc.TrySAOEdgeBytes(any(pix).([]uint8), i, stride, any(src).([]uint8), i, stride, rw, rh, class, off)
}

func saoCTB[T pixel](ps *PictureState, pl *Plane, pix, src []T, c int, p *SaoParams, rx, ry, ctbW, ctbH, subW, subH, bitDepth int, anyNoFilter bool, pps PPS) {
	l := ps.Layout
	maxVal := 1<<uint(bitDepth) - 1
	x0, y0 := rx*ctbW, ry*ctbH
	cw, ch := min(ctbW, pl.Width-x0), min(ctbH, pl.Height-y0)
	skip := func(x, y int) bool { return anyNoFilter && ps.NoFilter[ps.cuAt(x*subW, y*subH)] }
	if p.Type[c] == 1 { // band offset
		var delta [32]int // the offset of each band, 0 for the ones with none (8-414)
		for k := 0; k < 4; k++ {
			delta[(k+int(p.BandPos[c]))&31] = int(p.Offset[c][k])
		}
		if off, ok := saoOffsets(&p.Offset[c]); ok && !anyNoFilter &&
			!kernelRefused("SAOBand", saoBandKernel(pix, src, pl.Stride, x0, y0, cw, ch, bitDepth, int(p.BandPos[c]), &off)) {
			return
		}
		shift := uint(bitDepth - 5)
		for j := 0; j < ch; j++ {
			row := src[(y0+j)*pl.Stride+x0 : (y0+j)*pl.Stride+x0+cw]
			out := pix[(y0+j)*pl.Stride+x0 : (y0+j)*pl.Stride+x0+cw]
			for i, cur := range row {
				if d := delta[int(cur)>>shift]; d != 0 && !skip(x0+i, y0+j) {
					out[i] = T(clip3(0, maxVal, int(cur)+d))
				}
			}
		}
		return
	}
	// Edge offset. The offset of an edge index 0 to 4 as 2 + sign + sign gives it (8-411,
	// 8-412): the indices 0 and 1 are the local minimum and the concave edge, 2 is no
	// edge, 3 and 4 the convex edge and the local maximum.
	delta := [5]int{int(p.Offset[c][0]), int(p.Offset[c][1]), 0, int(p.Offset[c][2]), int(p.Offset[c][3])}
	class := p.EoClass[c]
	ha, hb := saoHPos[class][0], saoHPos[class][1]
	va, vb := saoVPos[class][0], saoVPos[class][1]
	// Which neighbouring coding tree blocks (3 x 3 around this one) the neighbours may be in.
	var usable [3][3]bool
	rsCur := ry*l.WidthCtbs + rx
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if nx, ny := rx+dx, ry+dy; nx >= 0 && ny >= 0 && nx < l.WidthCtbs && ny < l.HeightCtbs {
				usable[dy+1][dx+1] = ps.saoUsable(ny*l.WidthCtbs+nx, rsCur, pps)
			}
		}
	}
	// A block whose eight neighbouring blocks all exist and may be used (same slice or one
	// that lets the filter across, same tile or one that lets it across) has both neighbours
	// of every one of its samples usable, in the deblocked picture src, so the whole block
	// goes to the kernels at once. Nothing in it must stay as it is (no pcm or bypass sample
	// anywhere in the picture), and the block is whole (a block with a neighbour on every
	// side is). Any other block takes the path below.
	if off, ok := saoOffsets(&p.Offset[c]); ok && !anyNoFilter && saoAllUsable(&usable) {
		if saoWholeBlockHook != nil {
			saoWholeBlockHook()
		}
		if !kernelRefused("SAOEdge", saoEdgeKernel(pix, src, pl.Stride, x0, y0, cw, ch, bitDepth, int(class), &off)) {
			return
		}
	}
	// The interior of the block, where both neighbours lie inside it, goes to the kernels
	// (bit depth up to 12, no sample that must stay as it is); the border below stays in Go.
	kernelDone := false
	if off, ok := saoOffsets(&p.Offset[c]); ok && !anyNoFilter && cw >= 3 && ch >= 3 {
		kernelDone = !kernelRefused("SAOEdge", saoEdgeKernel(pix, src, pl.Stride, x0+1, y0+1, cw-2, ch-2, bitDepth, int(class), &off))
	}
	// edge is the sample ( x, y ) with its neighbours checked one by one.
	edge := func(x, y int) {
		cur := int(src[y*pl.Stride+x])
		var nb [2]int
		for k, o := range [2][2]int{{ha, va}, {hb, vb}} {
			xn, yn := x+o[0], y+o[1]
			if xn < 0 || yn < 0 || xn >= pl.Width || yn >= pl.Height {
				return
			}
			dx, dy := 1, 1
			if xn < x0 {
				dx = 0
			} else if xn >= x0+ctbW {
				dx = 2
			}
			if yn < y0 {
				dy = 0
			} else if yn >= y0+ctbH {
				dy = 2
			}
			if !usable[dy][dx] {
				return
			}
			nb[k] = int(src[yn*pl.Stride+xn])
		}
		if d := delta[2+sign(cur-nb[0])+sign(cur-nb[1])]; d != 0 {
			pix[y*pl.Stride+x] = T(clip3(0, maxVal, cur+d))
		}
	}
	for j := 0; j < ch; j++ {
		y := y0 + j
		if j == 0 || j == ch-1 || cw < 3 { // a neighbour row may be outside the block
			for i := 0; i < cw; i++ {
				if !skip(x0+i, y) {
					edge(x0+i, y)
				}
			}
			continue
		}
		if !skip(x0, y) {
			edge(x0, y)
		}
		if !skip(x0+cw-1, y) {
			edge(x0+cw-1, y)
		}
		if kernelDone {
			continue
		}
		// the interior: both neighbours are in this block, whose samples are all usable
		oa, ob := va*pl.Stride+ha, vb*pl.Stride+hb
		base := y*pl.Stride + x0
		for i := 1; i < cw-1; i++ {
			cur := int(src[base+i])
			a, b := int(src[base+i+oa]), int(src[base+i+ob])
			e := 2
			if cur > a {
				e++
			} else if cur < a {
				e--
			}
			if cur > b {
				e++
			} else if cur < b {
				e--
			}
			if d := delta[e]; d != 0 && !skip(x0+i, y) {
				pix[base+i] = T(clip3(0, maxVal, cur+d))
			}
		}
	}
}

// saoAllUsable reports whether all nine blocks of the 3 x 3 around a coding tree block exist
// and may be used for its edge offset (a block outside the picture is never marked usable).
func saoAllUsable(u *[3][3]bool) bool {
	for _, row := range u {
		for _, ok := range row {
			if !ok {
				return false
			}
		}
	}
	return true
}

// saoWholeBlockHook, when set (by tests), is called each time a block takes the path of the
// whole-block kernel call.
var saoWholeBlockHook func()

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

// saoOffsets are the four offsets of a block as the kernels take them; false when one does
// not fit 16 bits (it cannot in a valid stream: 31 << log2OffsetScale).
func saoOffsets(o *[4]int32) (out [4]int16, ok bool) {
	for i, v := range o {
		if v < -1<<15 || v >= 1<<15 {
			return out, false
		}
		out[i] = int16(v)
	}
	return out, true
}
