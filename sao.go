// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "github.com/go-videodsp/kernels/hevc"

// The sample adaptive offset process (8.7.3). It runs on the deblocked picture, one
// coding tree block at a time, reading the deblocked samples and writing the picture.

// saoHPos and saoVPos are Table 8-13: the two neighbours of an edge offset class.
var (
	saoHPos = [4][2]int{{-1, 1}, {0, 0}, {-1, 1}, {1, -1}}
	saoVPos = [4][2]int{{0, 0}, {-1, 1}, {-1, 1}, {-1, 1}}
)

// SAO applies sample adaptive offset to the deblocked picture frame (8.7.3.1) with
// the parameters the walk read for each coding tree block.
func (ps *PictureState) SAO(frame *DecodedPicture, sps SPS, pps PPS) {
	l := ps.Layout
	anyNoFilter := false
	for _, nf := range ps.NoFilter {
		anyNoFilter = anyNoFilter || nf
	}
	var src []uint16
	for c := 0; c < frame.Components; c++ {
		used := false
		for i := range ps.Sao {
			used = used || ps.Sao[i].Type[c] != 0
		}
		if !used {
			continue
		}
		pl := &frame.Planes[c]
		src = append(src[:0], pl.Pix...) // recPicture: the deblocked samples
		subW, subH := int(sps.CodedWidth)/pl.Width, int(sps.CodedHeight)/pl.Height
		bitDepth := int(sps.BitDepthLuma)
		if c > 0 {
			bitDepth = int(sps.BitDepthChroma)
		}
		ctbW, ctbH := (1<<uint(sps.Log2CtbSize))/subW, (1<<uint(sps.Log2CtbSize))/subH
		for ry := 0; ry < l.HeightCtbs; ry++ {
			for rx := 0; rx < l.WidthCtbs; rx++ {
				p := &ps.Sao[ry*l.WidthCtbs+rx]
				if p.Type[c] == 0 {
					continue
				}
				ps.saoCTB(pl, src, c, p, rx, ry, ctbW, ctbH, subW, subH, bitDepth, anyNoFilter, pps)
			}
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

func (ps *PictureState) saoCTB(pl *Plane, src []uint16, c int, p *SaoParams, rx, ry, ctbW, ctbH, subW, subH, bitDepth int, anyNoFilter bool, pps PPS) {
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
			hevc.TrySAOBand(pl.Pix, y0*pl.Stride+x0, pl.Stride, src, y0*pl.Stride+x0, pl.Stride, cw, ch, bitDepth, int(p.BandPos[c]), &off) == nil {
			return
		}
		shift := uint(bitDepth - 5)
		for j := 0; j < ch; j++ {
			row := src[(y0+j)*pl.Stride+x0 : (y0+j)*pl.Stride+x0+cw]
			out := pl.Pix[(y0+j)*pl.Stride+x0 : (y0+j)*pl.Stride+x0+cw]
			for i, cur := range row {
				if d := delta[int(cur)>>shift]; d != 0 && !skip(x0+i, y0+j) {
					out[i] = uint16(clip3(0, maxVal, int(cur)+d))
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
	// The interior of the block, where both neighbours lie inside it, goes to the kernels
	// (bit depth up to 12, no sample that must stay as it is); the border below stays in Go.
	kernelDone := false
	if off, ok := saoOffsets(&p.Offset[c]); ok && !anyNoFilter && cw >= 3 && ch >= 3 {
		i := (y0+1)*pl.Stride + x0 + 1
		kernelDone = hevc.TrySAOEdge(pl.Pix, i, pl.Stride, src, i, pl.Stride, cw-2, ch-2, bitDepth, int(class), &off) == nil
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
			pl.Pix[y*pl.Stride+x] = uint16(clip3(0, maxVal, cur+d))
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
				pl.Pix[base+i] = uint16(clip3(0, maxVal, cur+d))
			}
		}
	}
}

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
