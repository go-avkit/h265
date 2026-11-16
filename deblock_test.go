// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"math/rand/v2"
	"reflect"
	"testing"
)

// Table 8-12 as the text prints it, Q in the three rows of 19, 19 and 14 entries.
var specBetaRows = [][]int{
	{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 6, 7, 8},
	{9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 20, 22, 24, 26, 28, 30, 32, 34, 36},
	{38, 40, 42, 44, 46, 48, 50, 52, 54, 56, 58, 60, 62, 64},
}
var specTcRows = [][]int{
	{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1},
	{1, 1, 1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4},
	{5, 5, 6, 6, 7, 8, 9, 10, 11, 13, 14, 16, 18, 20, 22, 24},
}

func TestThresholdTablesAreTable8_12(t *testing.T) {
	var beta, tc []int
	for _, r := range specBetaRows {
		beta = append(beta, r...)
	}
	for _, r := range specTcRows {
		tc = append(tc, r...)
	}
	if len(beta) != 52 || len(tc) != 54 {
		t.Fatalf("the transcription has %d and %d entries", len(beta), len(tc))
	}
	for q, v := range beta {
		if betaTable[q] != v {
			t.Errorf("beta' at Q %d: %d, the table's %d", q, betaTable[q], v)
		}
	}
	for q, v := range tc {
		if tcTable[q] != v {
			t.Errorf("tc' at Q %d: %d, the table's %d", q, tcTable[q], v)
		}
	}
}

// refMotionBS is the motion conditions of 8.7.2.4 as the bullets state them, with the
// reference pictures compared as sets and the vectors by list.
func refMotionBS(p, q Motion) bool {
	count := func(m Motion) int { return b2n(m.Pred[0]) + b2n(m.Pred[1]) }
	refs := func(m Motion) (r []int32) {
		for l := 0; l < 2; l++ {
			if m.Pred[l] {
				r = append(r, m.Ref[l])
			}
		}
		return
	}
	far := func(a, b [2]int16) bool {
		dx, dy := int(a[0])-int(b[0]), int(a[1])-int(b[1])
		return dx >= 4 || dx <= -4 || dy >= 4 || dy <= -4
	}
	if count(p) != count(q) {
		return true
	}
	rp, rq := refs(p), refs(q)
	same := func(a, b []int32) bool { // the same pictures, whatever the lists
		if len(a) != len(b) {
			return false
		}
		if len(a) == 1 {
			return a[0] == b[0]
		}
		return (a[0] == b[0] && a[1] == b[1]) || (a[0] == b[1] && a[1] == b[0])
	}
	if !same(rp, rq) {
		return true
	}
	if count(p) == 1 {
		var mp, mq [2]int16
		for l := 0; l < 2; l++ {
			if p.Pred[l] {
				mp = p.MV[l]
			}
			if q.Pred[l] {
				mq = q.MV[l]
			}
		}
		return far(mp, mq)
	}
	if p.Ref[0] != p.Ref[1] { // two vectors and two different pictures
		// the vectors for the same picture are compared
		if p.Ref[0] == q.Ref[0] {
			return far(p.MV[0], q.MV[0]) || far(p.MV[1], q.MV[1])
		}
		return far(p.MV[0], q.MV[1]) || far(p.MV[1], q.MV[0])
	}
	// two vectors for the same picture on both sides: both pairings must differ
	return (far(p.MV[0], q.MV[0]) || far(p.MV[1], q.MV[1])) && (far(p.MV[0], q.MV[1]) || far(p.MV[1], q.MV[0]))
}

func TestMotionBoundaryStrengthAgainstTheBullets(t *testing.T) {
	rng := rand.New(rand.NewPCG(151, 152))
	mk := func() Motion {
		var m Motion
		m.Pred = [2]bool{rng.IntN(2) == 0, rng.IntN(2) == 0}
		if !m.Pred[0] && !m.Pred[1] {
			m.Pred[rng.IntN(2)] = true
		}
		for l := 0; l < 2; l++ {
			m.Ref[l] = int32(rng.IntN(3))
			m.MV[l] = [2]int16{int16(rng.IntN(9) - 4), int16(rng.IntN(9) - 4)}
		}
		return m
	}
	var yes, no int
	for i := 0; i < 20000; i++ {
		p, q := mk(), mk()
		if rng.IntN(2) == 0 { // often the same references, so the vector rules decide
			q.Ref = p.Ref
			q.Pred = p.Pred
		}
		if rng.IntN(4) == 0 {
			q.Ref[0], q.Ref[1] = p.Ref[1], p.Ref[0]
		}
		got, want := motionDiffers(&p, &q), refMotionBS(p, q)
		if got != want {
			t.Fatalf("%+v against %+v: %v, the bullets give %v", p, q, got, want)
		}
		if got {
			yes++
		} else {
			no++
		}
	}
	if yes < 1000 || no < 1000 {
		t.Errorf("the cases are one-sided: %d and %d", yes, no)
	}
}

func TestBoundaryStrengthPrecedence(t *testing.T) {
	ps := &PictureState{Cbf4: []bool{false, true}, Motion: []Motion{{Pred: [2]bool{true}}, {Pred: [2]bool{true}, MV: [2][2]int16{{8, 0}}}}}
	if bs := ps.boundaryStrength(true, false, false, 0, 1); bs != 2 {
		t.Errorf("an intra side: %d", bs)
	}
	if bs := ps.boundaryStrength(false, false, true, 0, 1); bs != 1 {
		t.Errorf("coefficients on a transform edge: %d", bs)
	}
	if bs := ps.boundaryStrength(false, false, false, 0, 1); bs != 1 {
		t.Errorf("motion that differs: %d", bs)
	}
	ps.Motion = nil
	if bs := ps.boundaryStrength(false, false, false, 0, 1); bs != 0 {
		t.Errorf("nothing to separate the blocks: %d", bs)
	}
}

// refLuma is 8.7.2.5.3, 8.7.2.5.4, 8.7.2.5.6 and 8.7.2.5.7 for one edge segment, with the
// samples p[ i ][ k ] and q[ i ][ k ] as the text names them.
func refLuma(pl *Plane, xq, yq int, vertical bool, bS, qpP, qpQ, betaOff, tcOff, bd int, noP, noQ bool) {
	get := func(i, k int, isP bool) *uint16 {
		var x, y int
		switch {
		case vertical && isP:
			x, y = xq-i-1, yq+k
		case vertical:
			x, y = xq+i, yq+k
		case isP:
			x, y = xq+k, yq-i-1
		default:
			x, y = xq+k, yq+i
		}
		return &pl.Pix[y*pl.Stride+x]
	}
	P := func(i, k int) int { return int(*get(i, k, true)) }
	Q := func(i, k int) int { return int(*get(i, k, false)) }
	qPL := (qpQ + qpP + 1) >> 1
	Qb := min(max(qPL+(betaOff<<1), 0), 51)
	beta := betaTable[Qb] * (1 << uint(bd-8))
	Qt := min(max(qPL+2*(bS-1)+(tcOff<<1), 0), 53)
	tC := tcTable[Qt] * (1 << uint(bd-8))
	dp0 := abs(P(2, 0) - 2*P(1, 0) + P(0, 0))
	dp3 := abs(P(2, 3) - 2*P(1, 3) + P(0, 3))
	dq0 := abs(Q(2, 0) - 2*Q(1, 0) + Q(0, 0))
	dq3 := abs(Q(2, 3) - 2*Q(1, 3) + Q(0, 3))
	dpq0, dpq3, dp, dq := dp0+dq0, dp3+dq3, dp0+dp3, dq0+dq3
	d := dpq0 + dpq3
	dE, dEp, dEq := 0, 0, 0
	if d < beta {
		dSam := func(dpq, k int) int {
			if dpq < beta>>2 && abs(P(3, k)-P(0, k))+abs(Q(0, k)-Q(3, k)) < beta>>3 && abs(P(0, k)-Q(0, k)) < (5*tC+1)>>1 {
				return 1
			}
			return 0
		}
		dE = 1
		if dSam(2*dpq0, 0) == 1 && dSam(2*dpq3, 3) == 1 {
			dE = 2
		}
		if dp < (beta+(beta>>1))>>3 {
			dEp = 1
		}
		if dq < (beta+(beta>>1))>>3 {
			dEq = 1
		}
	}
	if dE == 0 {
		return
	}
	clip1 := func(v int) int { return min(max(v, 0), 1<<uint(bd)-1) }
	for k := 0; k < 4; k++ {
		p0, p1, p2, p3 := P(0, k), P(1, k), P(2, k), P(3, k)
		q0, q1, q2, q3 := Q(0, k), Q(1, k), Q(2, k), Q(3, k)
		var pp, qq [3]int
		nDp, nDq := 0, 0
		if dE == 2 {
			nDp, nDq = 3, 3
			pp[0] = min(max((p2+2*p1+2*p0+2*q0+q1+4)>>3, p0-2*tC), p0+2*tC)
			pp[1] = min(max((p2+p1+p0+q0+2)>>2, p1-2*tC), p1+2*tC)
			pp[2] = min(max((2*p3+3*p2+p1+p0+q0+4)>>3, p2-2*tC), p2+2*tC)
			qq[0] = min(max((p1+2*p0+2*q0+2*q1+q2+4)>>3, q0-2*tC), q0+2*tC)
			qq[1] = min(max((p0+q0+q1+q2+2)>>2, q1-2*tC), q1+2*tC)
			qq[2] = min(max((p0+q0+q1+3*q2+2*q3+4)>>3, q2-2*tC), q2+2*tC)
		} else {
			delta := (9*(q0-p0) - 3*(q1-p1) + 8) >> 4
			if abs(delta) < tC*10 {
				delta = min(max(delta, -tC), tC)
				pp[0], qq[0] = clip1(p0+delta), clip1(q0-delta)
				if dEp == 1 {
					dP := min(max((((p2+p0+1)>>1)-p1+delta)>>1, -(tC>>1)), tC>>1)
					pp[1] = clip1(p1 + dP)
				}
				if dEq == 1 {
					dQ := min(max((((q2+q0+1)>>1)-q1-delta)>>1, -(tC>>1)), tC>>1)
					qq[1] = clip1(q1 + dQ)
				}
				nDp, nDq = dEp+1, dEq+1
			}
		}
		if noP {
			nDp = 0
		}
		if noQ {
			nDq = 0
		}
		for i := 0; i < nDp; i++ {
			*get(i, k, true) = uint16(pp[i])
		}
		for i := 0; i < nDq; i++ {
			*get(i, k, false) = uint16(qq[i])
		}
	}
}

// refChroma is 8.7.2.5.5 and 8.7.2.5.8 for one segment of four chroma lines.
func refChroma(pl *Plane, xq, yq int, vertical bool, tC, bd int, noP, noQ []bool) {
	for k := 0; k < 4; k++ {
		var p0x, p0y, p1x, p1y, q0x, q0y, q1x, q1y int
		if vertical {
			p0x, p0y, p1x, p1y = xq-1, yq+k, xq-2, yq+k
			q0x, q0y, q1x, q1y = xq, yq+k, xq+1, yq+k
		} else {
			p0x, p0y, p1x, p1y = xq+k, yq-1, xq+k, yq-2
			q0x, q0y, q1x, q1y = xq+k, yq, xq+k, yq+1
		}
		at := func(x, y int) int { return int(pl.Pix[y*pl.Stride+x]) }
		p0, p1, q0, q1 := at(p0x, p0y), at(p1x, p1y), at(q0x, q0y), at(q1x, q1y)
		delta := min(max((((q0-p0)<<2)+p1-q1+4)>>3, -tC), tC)
		clip1 := func(v int) int { return min(max(v, 0), 1<<uint(bd)-1) }
		if !noP[k] {
			pl.Pix[p0y*pl.Stride+p0x] = uint16(clip1(p0 + delta))
		}
		if !noQ[k] {
			pl.Pix[q0y*pl.Stride+q0x] = uint16(clip1(q0 - delta))
		}
	}
}

// refDeblock is 8.7.2 over the coding units the writer wrote: for each the transform
// and prediction block edges (8.7.2.2, 8.7.2.3), the strengths of the edges on the
// 8x8 grid (all intra here, so 2), and the luma and chroma edge filtering; every
// vertical edge of the picture first, then every horizontal one.
func refDeblock(ww *walkWriter, frame *DecodedPicture, plans []segPlan) {
	s := &ww.sps
	hdrOf := func(x, y int) SliceHeader {
		ctb := uint(s.Log2CtbSize)
		sa := ww.sliceOf[(y>>ctb)*ww.l.WidthCtbs+x>>ctb]
		for _, p := range plans {
			if int32(p.hdr.SliceAddrRs) == sa {
				return p.hdr
			}
		}
		panic("no slice")
	}
	ctbOf := func(x, y int) int { return (y>>uint(s.Log2CtbSize))*ww.l.WidthCtbs + x>>uint(s.Log2CtbSize) }
	cuAt := make(map[int]*CodingUnit)
	for i := range ww.expect {
		cu := &ww.expect[i]
		for y := cu.Y; y < cu.Y+1<<uint(cu.Log2Size); y += 1 << s.Log2MinCbSize {
			for x := cu.X; x < cu.X+1<<uint(cu.Log2Size); x += 1 << s.Log2MinCbSize {
				cuAt[ww.cell(x, y)] = cu
			}
		}
	}
	noFilter := func(x, y int) bool {
		cu := cuAt[ww.cell(x, y)]
		return (cu.PCM && s.PCM.LoopFilterDisabled) || cu.TransquantBypass
	}
	subW, subH := 1, 1
	if frame.Components > 1 {
		subW, subH = int(s.CodedWidth)/frame.Planes[1].Width, int(s.CodedHeight)/frame.Planes[1].Height
	}
	for _, vertical := range []bool{true, false} {
		for ci := range ww.expect {
			cu := &ww.expect[ci]
			hdr := hdrOf(cu.X, cu.Y)
			if hdr.DeblockingDisabled {
				continue
			}
			n := 1 << uint(cu.Log2Size)
			// filterLeftCbEdgeFlag / filterTopCbEdgeFlag
			nbX, nbY := cu.X-1, cu.Y
			onPicture := cu.X > 0
			if !vertical {
				nbX, nbY = cu.X, cu.Y-1
				onPicture = cu.Y > 0
			}
			filter := onPicture
			if onPicture {
				if ww.l.TileID[ww.l.RsToTs[ctbOf(nbX, nbY)]] != ww.l.TileID[ww.l.RsToTs[ctbOf(cu.X, cu.Y)]] && !ww.pps.LoopFilterAcrossTiles {
					filter = false
				}
				if ww.sliceOf[ctbOf(nbX, nbY)] != ww.sliceOf[ctbOf(cu.X, cu.Y)] && !hdr.LoopFilterAcrossSlices {
					filter = false
				}
			}
			flags := make([][]bool, n) // [ x ][ y ] inside the coding unit
			for i := range flags {
				flags[i] = make([]bool, n)
			}
			markTU := func(xB, yB, log2 int) {
				for k := 0; k < 1<<uint(log2); k++ {
					if vertical {
						flags[xB][yB+k] = xB != 0 || filter
					} else {
						flags[xB+k][yB] = yB != 0 || filter
					}
				}
			}
			found := false
			for ti := range ww.expectTU {
				if ww.tuCU[ti] == ci {
					tu := &ww.expectTU[ti]
					markTU(tu.X0-cu.X, tu.Y0-cu.Y, tu.Log2Size)
					found = true
				}
			}
			if !found { // no transform tree: the blocks split_transform_flag is inferred to give
				var rec func(x, y, log2 int)
				rec = func(x, y, log2 int) {
					if log2 > int(s.Log2MaxTbSize) {
						h := 1 << uint(log2-1)
						rec(x, y, log2-1)
						rec(x+h, y, log2-1)
						rec(x, y+h, log2-1)
						rec(x+h, y+h, log2-1)
						return
					}
					markTU(x, y, log2)
				}
				rec(0, 0, cu.Log2Size)
			}
			if cu.Part == PartNxN { // 8.7.2.3
				for k := 0; k < n; k++ {
					if vertical {
						flags[n/2][k] = true
					} else {
						flags[k][n/2] = true
					}
				}
			}
			// luma: the edges on the 8x8 grid, in segments of four
			for a := 0; a < n; a += 8 {
				for b := 0; b < n; b += 4 {
					xd, yd := a, b
					if !vertical {
						xd, yd = b, a
					}
					if !flags[xd][yd] {
						continue
					}
					xq, yq := cu.X+xd, cu.Y+yd
					px, py := xq-1, yq
					if !vertical {
						px, py = xq, yq-1
					}
					refLuma(&frame.Planes[0], xq, yq, vertical, 2, int(ww.qpMap[ww.cell(px, py)]), int(ww.qpMap[ww.cell(xq, yq)]),
						int(hdr.BetaOffsetDiv2), int(hdr.TCOffsetDiv2), int(s.BitDepthLuma), noFilter(px, py), noFilter(xq, yq))
				}
			}
			if frame.Components == 1 {
				continue
			}
			// chroma: the edges on the 8x8 grid of the chroma samples, in segments of four
			xcb, ycb := cu.X/subW, cu.Y/subH
			ncw, nch := n/subW, n/subH
			for _, c := range []int{1, 2} {
				pl := &frame.Planes[c]
				off := int(ww.pps.CbQPOffset)
				if c == 2 {
					off = int(ww.pps.CrQPOffset)
				}
				for a := 0; a < map[bool]int{true: ncw, false: nch}[vertical]; a++ {
					if (map[bool]int{true: xcb, false: ycb}[vertical]+a)%8 != 0 {
						continue
					}
					for b := 0; b < map[bool]int{true: nch, false: ncw}[vertical]; b += 4 {
						xd, yd := a, b
						if !vertical {
							xd, yd = b, a
						}
						lx, ly := xd*subW, yd*subH
						if lx >= n || ly >= n || !flags[lx][ly] {
							continue
						}
						xq, yq := xcb+xd, ycb+yd
						pxl, pyl := (xq-b2n(vertical))*subW, yq*subH
						if !vertical {
							pxl, pyl = xq*subW, (yq-1)*subH
						}
						qpi := ((int(ww.qpMap[ww.cell(xq*subW, yq*subH)]) + int(ww.qpMap[ww.cell(pxl, pyl)]) + 1) >> 1) + off
						qpc := min(qpi, 51)
						if s.ChromaFormat == 1 {
							switch {
							case qpi < 30:
								qpc = qpi
							case qpi >= 44:
								qpc = qpi - 6
							default:
								qpc = []int{29, 30, 31, 32, 33, 33, 34, 34, 35, 35, 36, 36, 37, 37}[qpi-30]
							}
						}
						tc := tcTable[min(max(qpc+2+(int(hdr.TCOffsetDiv2)<<1), 0), 53)] * (1 << uint(s.BitDepthChroma-8))
						noP, noQ := make([]bool, 4), make([]bool, 4)
						for k := 0; k < 4; k++ {
							bx, by := xq, yq+k
							if !vertical {
								bx, by = xq+k, yq
							}
							ppx, ppy := (bx-b2n(vertical))*subW, by*subH
							if !vertical {
								ppx, ppy = bx*subW, (by-1)*subH
							}
							noP[k], noQ[k] = noFilter(ppx, ppy), noFilter(bx*subW, by*subH)
						}
						refChroma(pl, xq, yq, vertical, tc, int(s.BitDepthChroma), noP, noQ)
					}
				}
			}
		}
	}
}

// deblockCase is a picture of intra slices with the deblocking controls at random.
func deblockCase(t *testing.T, rng *rand.Rand) (*walkWriter, *PictureState, []segPlan) {
	t.Helper()
	ww, ps, plans := reconCase(t, rng)
	p := &ww.pps
	p.LoopFilterAcrossTiles = rng.IntN(2) == 0
	p.CbQPOffset, p.CrQPOffset = int32(rng.IntN(25)-12), int32(rng.IntN(25)-12)
	ww.sps.PCM.LoopFilterDisabled = rng.IntN(2) == 0
	for i := range plans {
		h := &plans[i].hdr
		if plans[i].dependent {
			prev := plans[i-1].hdr
			h.DeblockingDisabled, h.BetaOffsetDiv2, h.TCOffsetDiv2, h.LoopFilterAcrossSlices =
				prev.DeblockingDisabled, prev.BetaOffsetDiv2, prev.TCOffsetDiv2, prev.LoopFilterAcrossSlices
			continue
		}
		h.DeblockingDisabled = rng.IntN(4) == 0
		h.BetaOffsetDiv2, h.TCOffsetDiv2 = int32(rng.IntN(13)-6), int32(rng.IntN(13)-6)
		h.LoopFilterAcrossSlices = rng.IntN(2) == 0
	}
	ps.SPS = ww.sps
	return ww, ps, plans
}

func TestDeblockingAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(161, 162))
	var changed, total int
	for iter := 0; iter < 400; iter++ {
		ww, ps, plans := deblockCase(t, rng)
		frame := NewDecodedPicture(ww.sps)
		rec := NewReconstructor(frame, ww.sps, ww.pps)
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, rec.Decoder()); err != nil {
				t.Fatalf("iter %d segment %d: %v", iter, si, err)
			}
		}
		ref := newRefRecon(ww)
		for ci := range ww.expect {
			cu := &ww.expect[ci]
			if cu.PCM {
				ref.pcm(cu)
			}
			for ti := range ww.expectTU {
				if ww.tuCU[ti] == ci {
					ref.unit(cu, &ww.expectTU[ti])
				}
			}
		}
		before := make([][]uint16, frame.Components)
		for c := range before {
			before[c] = append([]uint16(nil), frame.Planes[c].Pix...)
		}
		ps.Deblock(frame, ww.sps, ww.pps)
		refDeblock(ww, ref.frame, plans)
		for c := 0; c < frame.Components; c++ {
			total++
			if !reflect.DeepEqual(frame.Planes[c].Pix, before[c]) {
				changed++
			}
			if !reflect.DeepEqual(frame.Planes[c].Pix, ref.frame.Planes[c].Pix) {
				w := frame.Planes[c].Width
				for i := range frame.Planes[c].Pix {
					if frame.Planes[c].Pix[i] != ref.frame.Planes[c].Pix[i] {
						t.Fatalf("iter %d: component %d differs at (%d,%d): %d, the text's %d (chroma format %d, size %dx%d)", iter, c, i%w, i/w,
							frame.Planes[c].Pix[i], ref.frame.Planes[c].Pix[i], ww.sps.ChromaFormat, ww.sps.CodedWidth, ww.sps.CodedHeight)
					}
				}
			}
		}
	}
	t.Logf("%d of %d planes were changed by the filter", changed, total)
	if changed < total/4 {
		t.Error("the filter changed too little to be tested")
	}
}

// 8.7.2.3 on a 32x32 coding unit: where each partitioning puts its prediction block
// edges, and nothing when the slice does not deblock.
func TestPredictionBlockEdges(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 64, height: 64, sizes: []uint32{0, 3, 0, 3, 0, 0}})
	pps := mustPPS(t, pset{})
	l, err := NewCtbLayout(sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	// vertical and horizontal edge offsets, in samples, inside a coding unit of 32.
	want := map[PartMode][2][]int{
		Part2Nx2N: {nil, nil}, Part2NxN: {nil, {16}}, PartNx2N: {{16}, nil}, PartNxN: {{16}, {16}},
		Part2NxnU: {nil, {8}}, Part2NxnD: {nil, {24}}, PartnLx2N: {{8}, nil}, PartnRx2N: {{24}, nil},
	}
	for part, w := range want {
		for _, disabled := range []bool{false, true} {
			ps := NewPictureState(sps, l)
			wk := &SliceWalker{Pic: ps, SPS: sps, PPS: pps}
			wk.Hdr.DeblockingDisabled = disabled
			wk.markPUEdges(&CodingUnit{X: 32, Y: 32, Log2Size: 5, Part: part})
			var gotV, gotH []int
			for x := 0; x < 32; x += 4 {
				if ps.EdgeV[(32>>2)*ps.Edge4W+(32+x)>>2] != 0 && x != 0 {
					gotV = append(gotV, x)
				}
				if ps.EdgeH[((32+x)>>2)*ps.Edge4W+32>>2] != 0 && x != 0 {
					gotH = append(gotH, x)
				}
			}
			if disabled {
				if gotV != nil || gotH != nil {
					t.Errorf("part %d: edges marked in a slice that does not deblock", part)
				}
				continue
			}
			if !reflect.DeepEqual(gotV, w[0]) || !reflect.DeepEqual(gotH, w[1]) {
				t.Errorf("part %d: vertical %v horizontal %v, want %v and %v", part, gotV, gotH, w[0], w[1])
			}
		}
	}
}

// refSAO is 8.7.3 as the text has it, over the picture refDeblock left: every
// coding tree block, component and sample, with the conditions of 8.7.3.2 on the
// neighbours stated by the comparison of MinTbAddrZs and the slice's flag.
func refSAO(ww *walkWriter, frame *DecodedPicture, plans []segPlan) {
	s := &ww.sps
	l := ww.l
	hdrAt := func(x, y int) SliceHeader {
		ctb := uint(s.Log2CtbSize)
		sa := ww.sliceOf[(y>>ctb)*l.WidthCtbs+x>>ctb]
		for _, p := range plans {
			if int32(p.hdr.SliceAddrRs) == sa {
				return p.hdr
			}
		}
		panic("no slice")
	}
	sliceAt := func(x, y int) int32 {
		ctb := uint(s.Log2CtbSize)
		return ww.sliceOf[(y>>ctb)*l.WidthCtbs+x>>ctb]
	}
	tileAt := func(x, y int) int32 {
		ctb := uint(s.Log2CtbSize)
		return l.TileID[l.RsToTs[(y>>ctb)*l.WidthCtbs+x>>ctb]]
	}
	cuAt := make(map[int]*CodingUnit)
	for i := range ww.expect {
		cu := &ww.expect[i]
		for y := cu.Y; y < cu.Y+1<<uint(cu.Log2Size); y += 1 << s.Log2MinCbSize {
			for x := cu.X; x < cu.X+1<<uint(cu.Log2Size); x += 1 << s.Log2MinCbSize {
				cuAt[ww.cell(x, y)] = cu
			}
		}
	}
	hPos := [4][2]int{{-1, 1}, {0, 0}, {-1, 1}, {1, -1}}
	vPos := [4][2]int{{0, 0}, {-1, 1}, {-1, 1}, {-1, 1}}
	zs := func(x, y int) int { return l.MinTbAddrZs(x>>uint(s.Log2MinTbSize), y>>uint(s.Log2MinTbSize)) }
	for c := 0; c < frame.Components; c++ {
		pl := &frame.Planes[c]
		rec := append([]uint16(nil), pl.Pix...)
		subW, subH := int(s.CodedWidth)/pl.Width, int(s.CodedHeight)/pl.Height
		bd := int(s.BitDepthLuma)
		if c > 0 {
			bd = int(s.BitDepthChroma)
		}
		nW, nH := (1<<uint(s.Log2CtbSize))/subW, (1<<uint(s.Log2CtbSize))/subH
		for ry := 0; ry < l.HeightCtbs; ry++ {
			for rx := 0; rx < l.WidthCtbs; rx++ {
				p := ww.sao[ry*l.WidthCtbs+rx]
				for j := 0; j < nH; j++ {
					for i := 0; i < nW; i++ {
						xs, ys := rx*nW+i, ry*nH+j
						if xs >= pl.Width || ys >= pl.Height {
							continue
						}
						xy, yy := xs*subW, ys*subH
						cu := cuAt[ww.cell(xy, yy)]
						if (cu.PCM && s.PCM.LoopFilterDisabled) || cu.TransquantBypass || p.Type[c] == 0 {
							continue
						}
						cur := int(rec[ys*pl.Stride+xs])
						var offs int
						if p.Type[c] == 2 {
							edgeIdx := 0
							skip := false
							for k := 0; k < 2; k++ {
								xk, yk := xs+hPos[p.EoClass[c]][k], ys+vPos[p.EoClass[c]][k]
								xyk, yyk := xk*subW, yk*subH
								if xk < 0 || yk < 0 || xk >= pl.Width || yk >= pl.Height {
									skip = true
									break
								}
								if sliceAt(xyk, yyk) != sliceAt(xy, yy) {
									if zs(xyk, yyk) < zs(xy, yy) && !hdrAt(xy, yy).LoopFilterAcrossSlices {
										skip = true
										break
									}
									if zs(xy, yy) < zs(xyk, yyk) && !hdrAt(xyk, yyk).LoopFilterAcrossSlices {
										skip = true
										break
									}
								}
								if !ww.pps.LoopFilterAcrossTiles && tileAt(xyk, yyk) != tileAt(xy, yy) {
									skip = true
									break
								}
							}
							if !skip {
								sgn := func(v int) int {
									if v > 0 {
										return 1
									} else if v < 0 {
										return -1
									}
									return 0
								}
								a := int(rec[(ys+vPos[p.EoClass[c]][0])*pl.Stride+xs+hPos[p.EoClass[c]][0]])
								b := int(rec[(ys+vPos[p.EoClass[c]][1])*pl.Stride+xs+hPos[p.EoClass[c]][1]])
								edgeIdx = 2 + sgn(cur-a) + sgn(cur-b)
								if edgeIdx == 0 || edgeIdx == 1 || edgeIdx == 2 {
									if edgeIdx == 2 {
										edgeIdx = 0
									} else {
										edgeIdx++
									}
								}
							}
							if edgeIdx != 0 {
								offs = int(p.Offset[c][edgeIdx-1])
							}
						} else {
							bandTable := make([]int, 32)
							for k := 0; k < 4; k++ {
								bandTable[(k+int(p.BandPos[c]))&31] = k + 1
							}
							if bandIdx := bandTable[cur>>uint(bd-5)]; bandIdx != 0 {
								offs = int(p.Offset[c][bandIdx-1])
							}
						}
						pl.Pix[ys*pl.Stride+xs] = uint16(min(max(cur+offs, 0), 1<<uint(bd)-1))
					}
				}
			}
		}
	}
}

func TestSampleAdaptiveOffsetAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(171, 172))
	var changed, total int
	for iter := 0; iter < 400; iter++ {
		ww, ps, plans := deblockCase(t, rng)
		frame := NewDecodedPicture(ww.sps)
		rec := NewReconstructor(frame, ww.sps, ww.pps)
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, rec.Decoder()); err != nil {
				t.Fatalf("iter %d segment %d: %v", iter, si, err)
			}
		}
		ref := newRefRecon(ww)
		for ci := range ww.expect {
			cu := &ww.expect[ci]
			if cu.PCM {
				ref.pcm(cu)
			}
			for ti := range ww.expectTU {
				if ww.tuCU[ti] == ci {
					ref.unit(cu, &ww.expectTU[ti])
				}
			}
		}
		ps.Deblock(frame, ww.sps, ww.pps)
		refDeblock(ww, ref.frame, plans)
		deblocked := make([][]uint16, frame.Components)
		for c := range deblocked {
			deblocked[c] = append([]uint16(nil), frame.Planes[c].Pix...)
		}
		ps.SAO(frame, ww.sps, ww.pps)
		refSAO(ww, ref.frame, plans)
		for c := 0; c < frame.Components; c++ {
			total++
			if !reflect.DeepEqual(frame.Planes[c].Pix, deblocked[c]) {
				changed++
			}
			if !reflect.DeepEqual(frame.Planes[c].Pix, ref.frame.Planes[c].Pix) {
				w := frame.Planes[c].Width
				for i := range frame.Planes[c].Pix {
					if frame.Planes[c].Pix[i] != ref.frame.Planes[c].Pix[i] {
						t.Fatalf("iter %d: component %d differs at (%d,%d): %d, the text's %d", iter, c, i%w, i/w, frame.Planes[c].Pix[i], ref.frame.Planes[c].Pix[i])
					}
				}
			}
		}
	}
	t.Logf("%d of %d planes were changed by SAO", changed, total)
	if changed < total/4 {
		t.Error("SAO changed too little to be tested")
	}
}
