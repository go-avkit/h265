// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// The deblocking filter process (8.7.2). The walk marks the edges it finds, on a 4x4
// grid of luma samples, and the filter runs over the picture once every slice is
// decoded: all vertical edges first, then all horizontal ones (8.7.2.1).

// The bits of an edge flag.
const (
	edgeFiltered  = 1 // edgeFlags is 1: the edge is a transform or prediction block edge to filter
	edgeTransform = 2 // and it is also a transform block edge (8.7.2.4's bS 1 rule)
)

// Motion is the motion of a prediction block as the boundary strength compares it
// (8.7.2.4): which lists it predicts from, the picture each list's reference is
// (an identifier of the picture, not an index into a list) and the vectors in
// quarter luma samples.
type Motion struct {
	Pred [2]bool
	// Ref is the order count of the reference picture of each list used, which names
	// the picture; RefIdx its index in the slice's list and LongTerm whether it was a
	// long-term reference picture, which motion derivation reads (-1 for a list not used).
	Ref      [2]int32
	MV       [2][2]int16
	RefIdx   [2]int8
	LongTerm [2]bool
}

// initDeblocking allocates the maps the filter needs.
func (ps *PictureState) initDeblocking(sps SPS) {
	ps.Edge4W = (int(sps.CodedWidth) + 3) >> 2
	h4 := (int(sps.CodedHeight) + 3) >> 2
	ps.EdgeV = make([]uint8, ps.Edge4W*h4)
	ps.EdgeH = make([]uint8, ps.Edge4W*h4)
	ps.Cbf4 = make([]bool, ps.Edge4W*h4)
	ps.NoFilter = make([]bool, len(ps.CtDepth))
	ps.DbBeta = make([]int8, len(ps.Sao))
	ps.DbTc = make([]int8, len(ps.Sao))
	ps.DbAcross = make([]bool, len(ps.Sao))
}

// beginDeblockCU derives filterLeftCbEdgeFlag and filterTopCbEdgeFlag of the
// coding unit at ( x0, y0 ) (8.7.2.1): the edge is not filtered at the picture's
// boundary, at a tile boundary when loop_filter_across_tiles_enabled_flag is 0, or
// at the left or upper boundary of a slice with
// slice_loop_filter_across_slices_enabled_flag 0.
func (w *SliceWalker) beginDeblockCU(x0, y0 int) {
	s := &w.SPS
	l := w.Pic.Layout
	ctb := uint(s.Log2CtbSize)
	rsC := (y0>>ctb)*l.WidthCtbs + x0>>ctb
	across := func(xN, yN int) bool {
		if xN < 0 || yN < 0 {
			return false
		}
		rsN := (yN>>ctb)*l.WidthCtbs + xN>>ctb
		if rsN == rsC {
			return true
		}
		if l.TileID[l.RsToTs[rsN]] != l.TileID[l.RsToTs[rsC]] && !w.PPS.LoopFilterAcrossTiles {
			return false
		}
		if w.Pic.SliceAddr[rsN] != w.Pic.SliceAddr[rsC] && !w.Hdr.LoopFilterAcrossSlices {
			return false
		}
		return true
	}
	w.dbLeft, w.dbTop = across(x0-1, y0), across(x0, y0-1)
}

func (w *SliceWalker) setEdge(arr []uint8, x4, y4 int, v uint8) {
	arr[y4*w.Pic.Edge4W+x4] |= v
}

// markTB records the edges of the transform block at ( x0, y0 ) of the coding unit
// cu (8.7.2.2): its left and top edges, filtered unconditionally inside the coding
// unit and by the flags of beginDeblockCU on its boundary.
func (w *SliceWalker) markTB(cu *CodingUnit, x0, y0, log2 int) {
	if w.Hdr.DeblockingDisabled {
		return
	}
	n4 := 1 << uint(log2-2)
	if x0 != cu.X || w.dbLeft {
		for k := 0; k < n4; k++ {
			w.setEdge(w.Pic.EdgeV, x0>>2, y0>>2+k, edgeFiltered|edgeTransform)
		}
	}
	if y0 != cu.Y || w.dbTop {
		for k := 0; k < n4; k++ {
			w.setEdge(w.Pic.EdgeH, x0>>2+k, y0>>2, edgeFiltered|edgeTransform)
		}
	}
}

// markTBTree records the transform blocks of a coding unit that has no transform
// tree in the stream, which are those split_transform_flag is inferred to give
// (7.4.9.8).
func (w *SliceWalker) markTBTree(cu *CodingUnit, x0, y0, log2, depth int) {
	s := &w.SPS
	interSplit := s.MaxTransformHierarchyDepthInter == 0 && cu.Mode == ModeInter && cu.Part != Part2Nx2N && depth == 0
	if log2 > int(s.Log2MaxTbSize) || interSplit {
		h := 1 << uint(log2-1)
		for _, d := range [4][2]int{{0, 0}, {h, 0}, {0, h}, {h, h}} {
			w.markTBTree(cu, x0+d[0], y0+d[1], log2-1, depth+1)
		}
		return
	}
	w.markTB(cu, x0, y0, log2)
}

// markPUEdges records the prediction block edges inside a coding unit (8.7.2.3).
func (w *SliceWalker) markPUEdges(cu *CodingUnit) {
	if w.Hdr.DeblockingDisabled {
		return
	}
	n := 1 << uint(cu.Log2Size)
	vertical := func(off int) {
		for k := 0; k < n>>2; k++ {
			w.setEdge(w.Pic.EdgeV, (cu.X+off)>>2, cu.Y>>2+k, edgeFiltered)
		}
	}
	horizontal := func(off int) {
		for k := 0; k < n>>2; k++ {
			w.setEdge(w.Pic.EdgeH, cu.X>>2+k, (cu.Y+off)>>2, edgeFiltered)
		}
	}
	switch cu.Part {
	case PartNx2N:
		vertical(n / 2)
	case PartNxN:
		vertical(n / 2)
		horizontal(n / 2)
	case PartnLx2N:
		vertical(n / 4)
	case PartnRx2N:
		vertical(3 * n / 4)
	case Part2NxN:
		horizontal(n / 2)
	case Part2NxnU:
		horizontal(n / 4)
	case Part2NxnD:
		horizontal(3 * n / 4)
	}
}

// boundaryStrength is 8.7.2.4 for the edge between the 4x4 blocks p and q: bS 2 for
// an intra side, 1 for a coded luma transform block on a transform block edge, and
// 1 or 0 from the motion otherwise. p and q index the 4x4 grids; the motion is nil
// for a picture without inter prediction.
func (ps *PictureState) boundaryStrength(pIntra, qIntra, transformEdge bool, p4, q4 int) int {
	switch {
	case pIntra || qIntra:
		return 2
	case transformEdge && (ps.Cbf4[p4] || ps.Cbf4[q4]):
		return 1
	case ps.Motion != nil && motionDiffers(&ps.Motion[p4], &ps.Motion[q4]):
		return 1
	}
	return 0
}

// motionDiffers is the motion conditions of 8.7.2.4: different reference pictures
// or numbers of vectors, or vectors four or more quarter samples apart.
func motionDiffers(p, q *Motion) bool {
	np, nq := bit(p.Pred[0])+bit(p.Pred[1]), bit(q.Pred[0])+bit(q.Pred[1])
	if np != nq {
		return true
	}
	far := func(a, b [2]int16) bool { return abs(int(a[0])-int(b[0])) >= 4 || abs(int(a[1])-int(b[1])) >= 4 }
	if np == 1 {
		lp, lq := 0, 0
		if !p.Pred[0] {
			lp = 1
		}
		if !q.Pred[0] {
			lq = 1
		}
		return p.Ref[lp] != q.Ref[lq] || far(p.MV[lp], q.MV[lq])
	}
	// Two vectors each.
	sameSet := (p.Ref[0] == q.Ref[0] && p.Ref[1] == q.Ref[1]) || (p.Ref[0] == q.Ref[1] && p.Ref[1] == q.Ref[0])
	if !sameSet {
		return true
	}
	if p.Ref[0] != p.Ref[1] { // two different reference pictures: compare vector to vector by picture
		if p.Ref[0] == q.Ref[0] {
			return far(p.MV[0], q.MV[0]) || far(p.MV[1], q.MV[1])
		}
		return far(p.MV[0], q.MV[1]) || far(p.MV[1], q.MV[0])
	}
	// Both vectors of each block use the same picture.
	return (far(p.MV[0], q.MV[0]) || far(p.MV[1], q.MV[1])) && (far(p.MV[0], q.MV[1]) || far(p.MV[1], q.MV[0]))
}

// tcTable and betaTable are Table 8-12.
var betaTable = [52]int{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 20, 22, 24,
	26, 28, 30, 32, 34, 36, 38, 40, 42, 44, 46, 48, 50, 52, 54, 56, 58, 60, 62, 64,
}
var tcTable = [54]int{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 3,
	3, 3, 3, 4, 4, 4, 5, 5, 6, 6, 7, 8, 9, 10, 11, 13, 14, 16, 18, 20, 22, 24,
}

func clip3(lo, hi, v int) int { return min(max(v, lo), hi) }

// Deblock applies the deblocking filter to the reconstructed picture (8.7.2): the
// vertical edges of every coding unit, then the horizontal ones.
func (ps *PictureState) Deblock(frame *DecodedPicture, sps SPS, pps PPS) {
	for _, vertical := range []bool{true, false} {
		ps.deblockLuma(frame, sps, vertical)
		if frame.Components > 1 {
			ps.deblockChroma(frame, sps, pps, vertical)
		}
	}
}

func (ps *PictureState) cuAt(x, y int) int {
	m := uint(ps.SPS.Log2MinCbSize)
	return (y>>m)*ps.MinCbWidth + x>>m
}

func (ps *PictureState) ctbAt(x, y int) int {
	c := uint(ps.SPS.Log2CtbSize)
	return (y>>c)*ps.Layout.WidthCtbs + x>>c
}

// edgeAt returns the edge flags and boundary strength of the edge whose q side is
// the luma sample ( x, y ), and the 4x4 grid indices of its sides.
func (ps *PictureState) edgeAt(x, y int, vertical bool) (flags uint8, bs int) {
	idx := (y>>2)*ps.Edge4W + x>>2
	px, py := x-1, y
	arr := ps.EdgeV
	if !vertical {
		px, py = x, y-1
		arr = ps.EdgeH
	}
	flags = arr[idx]
	if flags&edgeFiltered == 0 {
		return flags, 0
	}
	pIdx := (py>>2)*ps.Edge4W + px>>2
	pIntra := ps.CuMode[ps.cuAt(px, py)] == uint8(ModeIntra)
	qIntra := ps.CuMode[ps.cuAt(x, y)] == uint8(ModeIntra)
	return flags, ps.boundaryStrength(pIntra, qIntra, flags&edgeTransform != 0, pIdx, idx)
}

func (ps *PictureState) deblockLuma(frame *DecodedPicture, sps SPS, vertical bool) {
	pl := &frame.Planes[0]
	w, h := pl.Width, pl.Height
	bd := int(sps.BitDepthLuma)
	for y := 0; y < h; y += 4 {
		for x := 0; x < w; x += 4 {
			if (vertical && (x%8 != 0 || x == 0)) || (!vertical && (y%8 != 0 || y == 0)) {
				continue
			}
			_, bs := ps.edgeAt(x, y, vertical)
			if bs == 0 {
				continue
			}
			px, py := x-1, y
			along, across := pl.Stride, 1
			if !vertical {
				px, py = x, y-1
				along, across = 1, pl.Stride
			}
			qpL := (int(ps.QpY[ps.cuAt(x, y)]) + int(ps.QpY[ps.cuAt(px, py)]) + 1) >> 1
			ctb := ps.ctbAt(x, y)
			beta := betaTable[clip3(0, 51, qpL+int(ps.DbBeta[ctb])*2)] << uint(bd-8)
			tc := tcTable[clip3(0, 53, qpL+2*(bs-1)+int(ps.DbTc[ctb])*2)] << uint(bd-8)
			noP, noQ := ps.NoFilter[ps.cuAt(px, py)], ps.NoFilter[ps.cuAt(x, y)]
			filterLumaEdge(pl.Pix, y*pl.Stride+x, along, across, beta, tc, bd, noP, noQ)
		}
	}
}

// filterLumaEdge is the decision process of 8.7.2.5.3 and the filtering of
// 8.7.2.5.4 and 8.7.2.5.7 for four lines of samples across an edge: line k starts at
// pix[ base + k * along ], q_i is i steps of across ahead and p_i behind.
func filterLumaEdge(pix []uint16, base, along, across, beta, tc, bitDepth int, noP, noQ bool) {
	at := func(k, i int) int { return int(pix[base+k*along+i*across]) } // i >= 0: q_i; i < 0: p_(-i-1)
	p := func(i, k int) int { return at(k, -i-1) }
	q := func(i, k int) int { return at(k, i) }
	dp0 := abs(p(2, 0) - 2*p(1, 0) + p(0, 0))
	dp3 := abs(p(2, 3) - 2*p(1, 3) + p(0, 3))
	dq0 := abs(q(2, 0) - 2*q(1, 0) + q(0, 0))
	dq3 := abs(q(2, 3) - 2*q(1, 3) + q(0, 3))
	dpq0, dpq3 := dp0+dq0, dp3+dq3
	dp, dq := dp0+dp3, dq0+dq3
	if dpq0+dpq3 >= beta {
		return
	}
	dSam := func(dpq, k int) bool {
		return dpq < beta>>2 && abs(p(3, k)-p(0, k))+abs(q(0, k)-q(3, k)) < beta>>3 && abs(p(0, k)-q(0, k)) < (5*tc+1)>>1
	}
	strong := dSam(2*dpq0, 0) && dSam(2*dpq3, 3)
	dEp, dEq := dp < (beta+beta>>1)>>3, dq < (beta+beta>>1)>>3
	maxVal := 1<<uint(bitDepth) - 1
	for k := 0; k < 4; k++ {
		p0, p1, p2, p3 := p(0, k), p(1, k), p(2, k), p(3, k)
		q0, q1, q2, q3 := q(0, k), q(1, k), q(2, k), q(3, k)
		var np, nq int // samples to replace on each side
		var pf, qf [3]int
		if strong {
			np, nq = 3, 3
			pf[0] = clip3(p0-2*tc, p0+2*tc, (p2+2*p1+2*p0+2*q0+q1+4)>>3)
			pf[1] = clip3(p1-2*tc, p1+2*tc, (p2+p1+p0+q0+2)>>2)
			pf[2] = clip3(p2-2*tc, p2+2*tc, (2*p3+3*p2+p1+p0+q0+4)>>3)
			qf[0] = clip3(q0-2*tc, q0+2*tc, (p1+2*p0+2*q0+2*q1+q2+4)>>3)
			qf[1] = clip3(q1-2*tc, q1+2*tc, (p0+q0+q1+q2+2)>>2)
			qf[2] = clip3(q2-2*tc, q2+2*tc, (p0+q0+q1+3*q2+2*q3+4)>>3)
		} else {
			delta := (9*(q0-p0) - 3*(q1-p1) + 8) >> 4
			if abs(delta) < tc*10 {
				delta = clip3(-tc, tc, delta)
				pf[0] = clip3(0, maxVal, p0+delta)
				qf[0] = clip3(0, maxVal, q0-delta)
				np, nq = 1, 1
				if dEp {
					dP := clip3(-(tc >> 1), tc>>1, (((p2+p0+1)>>1)-p1+delta)>>1)
					pf[1] = clip3(0, maxVal, p1+dP)
					np = 2
				}
				if dEq {
					dQ := clip3(-(tc >> 1), tc>>1, (((q2+q0+1)>>1)-q1-delta)>>1)
					qf[1] = clip3(0, maxVal, q1+dQ)
					nq = 2
				}
			}
		}
		if noP {
			np = 0
		}
		if noQ {
			nq = 0
		}
		for i := 0; i < np; i++ {
			pix[base+k*along-(i+1)*across] = uint16(pf[i])
		}
		for i := 0; i < nq; i++ {
			pix[base+k*along+i*across] = uint16(qf[i])
		}
	}
}

// deblockChroma filters the chroma edges (8.7.2.5.1 and 8.7.2.5.2): those on the 8x8
// grid of the chroma samples whose luma boundary strength is 2.
func (ps *PictureState) deblockChroma(frame *DecodedPicture, sps SPS, pps PPS, vertical bool) {
	subW, subH := int(sps.CodedWidth)/frame.Planes[1].Width, int(sps.CodedHeight)/frame.Planes[1].Height
	bd := int(sps.BitDepthChroma)
	for c := 1; c <= 2; c++ {
		pl := &frame.Planes[c]
		cQpPicOffset := int(pps.CbQPOffset)
		if c == 2 {
			cQpPicOffset = int(pps.CrQPOffset)
		}
		for yc := 0; yc < pl.Height; yc += 4 {
			for xc := 0; xc < pl.Width; xc += 4 {
				if (vertical && (xc%8 != 0 || xc == 0)) || (!vertical && (yc%8 != 0 || yc == 0)) {
					continue
				}
				x, y := xc*subW, yc*subH // the luma location whose bS is taken
				if _, bs := ps.edgeAt(x, y, vertical); bs != 2 {
					continue
				}
				pxc, pyc := xc-1, yc
				along, across := pl.Stride, 1
				if !vertical {
					pxc, pyc = xc, yc-1
					along, across = 1, pl.Stride
				}
				qpi := ((int(ps.QpY[ps.cuAt(x, y)]) + int(ps.QpY[ps.cuAt(pxc*subW, pyc*subH)]) + 1) >> 1) + cQpPicOffset
				qpc := min(qpi, 51)
				if sps.ChromaFormat == 1 {
					qpc = ChromaQPTable(qpi)
				}
				tc := tcTable[clip3(0, 53, qpc+2+int(ps.DbTc[ps.ctbAt(x, y)])*2)] << uint(bd-8)
				maxVal := 1<<uint(bd) - 1
				for k := 0; k < 4; k++ {
					bx, by := xc, yc+k // sample q0 of line k
					if !vertical {
						bx, by = xc+k, yc
					}
					base := by*pl.Stride + bx
					p0, p1 := int(pl.Pix[base-across]), int(pl.Pix[base-2*across])
					q0, q1 := int(pl.Pix[base]), int(pl.Pix[base+across])
					delta := clip3(-tc, tc, (((q0-p0)<<2)+p1-q1+4)>>3)
					pxl, pyl := (bx-bit(vertical))*subW, by*subH
					if !vertical {
						pxl, pyl = bx*subW, (by-1)*subH
					}
					if !ps.NoFilter[ps.cuAt(pxl, pyl)] {
						pl.Pix[base-across] = uint16(clip3(0, maxVal, p0+delta))
					}
					if !ps.NoFilter[ps.cuAt(bx*subW, by*subH)] {
						pl.Pix[base] = uint16(clip3(0, maxVal, q0-delta))
					}
				}
				_ = along
			}
		}
	}
}

func (w *SliceWalker) setCbf(x0, y0, log2 int) {
	n4 := 1 << uint(log2-2)
	for j := 0; j < n4; j++ {
		for i := 0; i < n4; i++ {
			w.Pic.Cbf4[(y0>>2+j)*w.Pic.Edge4W+x0>>2+i] = true
		}
	}
}

func (w *SliceWalker) setNoFilter(x0, y0, log2 int) {
	s := &w.SPS
	m := uint(s.Log2MinCbSize)
	size := 1 << uint(log2)
	for y := y0 >> m; y < min((y0+size)>>m, (int(s.CodedHeight)+1<<m-1)>>m); y++ {
		for x := x0 >> m; x < min((x0+size)>>m, w.Pic.MinCbWidth); x++ {
			w.Pic.NoFilter[y*w.Pic.MinCbWidth+x] = true
		}
	}
}
