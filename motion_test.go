// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"math/rand/v2"
	"testing"
)

// The reference derivation of motion vectors: 8.5.3.2.1 to 8.5.3.2.9 as the text has
// them, over plain records, with the availability of 6.4.1 and 6.4.2 decided by an
// interleaving of coordinates instead of MinTbAddrZs.

type mcase struct {
	w        *SliceWalker
	sl       *InterSlice
	cu       *CodingUnit
	ctbLog2  int
	width    int
	height   int
	colState *PictureState
}

// zs is the z-scan order address of the 4x4 block holding ( x, y ): the coding tree block
// in raster scan, then the bits of x and y interleaved inside it.
func (c *mcase) zs(x, y int) int {
	ctb := 1 << uint(c.ctbLog2)
	wCtb := (c.width + ctb - 1) / ctb
	addr := (y/ctb)*wCtb + x/ctb
	bx, by := (x%ctb)/4, (y%ctb)/4
	z := 0
	for b := 0; b < 5; b++ {
		z |= (bx>>uint(b)&1)<<uint(2*b) | (by>>uint(b)&1)<<uint(2*b+1)
	}
	return addr<<12 | z
}

func (c *mcase) avail6_4_1(xCurr, yCurr, xN, yN int) bool {
	if xN < 0 || yN < 0 || xN >= c.width || yN >= c.height {
		return false
	}
	return c.zs(xN, yN) <= c.zs(xCurr, yCurr)
}

func (c *mcase) mot(x, y int) Motion { return c.w.Pic.Motion[(y/4)*c.w.Pic.Edge4W+x/4] }

func (c *mcase) intraAt(x, y int) bool {
	return c.w.Pic.CuMode[(y/8)*c.w.Pic.MinCbWidth+x/8] == uint8(ModeIntra)
}

func (c *mcase) avail6_4_2(xPb, yPb, nPbW, nPbH, partIdx, xN, yN int) bool {
	cu := c.cu
	nCbS := 1 << uint(cu.Log2Size)
	sameCb := cu.X <= xN && cu.Y <= yN && cu.X+nCbS > xN && cu.Y+nCbS > yN
	var a bool
	if !sameCb {
		a = c.avail6_4_1(xPb, yPb, xN, yN)
	} else if nPbW<<1 == nCbS && nPbH<<1 == nCbS && partIdx == 1 && cu.Y+nPbH <= yN && cu.X+nPbW > xN {
		a = false
	} else {
		a = true
	}
	if a && c.intraAt(xN, yN) {
		a = false
	}
	return a
}

func (c *mcase) poc(l, idx int) int { return int(c.sl.List[l][idx].POC) }

func sameMv(a, b Motion) bool {
	for l := 0; l < 2; l++ {
		if a.Pred[l] != b.Pred[l] {
			return false
		}
		if a.Pred[l] && (a.MV[l] != b.MV[l] || a.RefIdx[l] != b.RefIdx[l]) {
			return false
		}
	}
	return true
}

func refScale(mv [2]int, td, tb int) [2]int {
	clip := func(lo, hi, v int) int { return min(max(v, lo), hi) }
	td, tb = clip(-128, 127, td), clip(-128, 127, tb)
	tx := (16384 + abs(td)/2) / td
	dsf := clip(-4096, 4095, (tb*tx+32)>>6)
	var out [2]int
	for i := range mv {
		p := dsf * mv[i]
		s := 1
		if p < 0 {
			s = -1
		} else if p == 0 {
			s = 0
		}
		out[i] = clip(-32768, 32767, s*((abs(p)+127)>>8))
	}
	return out
}

// refTemporal is 8.5.3.2.8 and 8.5.3.2.9.
func (c *mcase) refTemporal(xPb, yPb, nPbW, nPbH, X, refIdx int) ([2]int, bool) {
	sl := c.sl
	if !sl.TemporalMVP {
		return [2]int{}, false
	}
	colMv := func(x, y int) ([2]int, bool) {
		x, y = (x>>4)<<4, (y>>4)<<4
		cm := c.colState.Motion[(y/4)*c.colState.Edge4W+x/4]
		if !cm.Pred[0] && !cm.Pred[1] {
			return [2]int{}, false
		}
		var mvCol [2]int16
		var refPoc int32
		var lt bool
		useL := func(l int) { mvCol, refPoc, lt = cm.MV[l], cm.Ref[l], cm.LongTerm[l] }
		switch {
		case !cm.Pred[0]:
			useL(1)
		case cm.Pred[0] && !cm.Pred[1]:
			useL(0)
		default:
			if sl.NoBackwardPred {
				useL(X)
			} else if sl.ColFromL0 {
				useL(1)
			} else {
				useL(0)
			}
		}
		if sl.List[X][refIdx].LongTerm != lt {
			return [2]int{}, false
		}
		colPocDiff := int(sl.Col.POC) - int(refPoc)
		currPocDiff := int(sl.POC) - c.poc(X, refIdx)
		if sl.List[X][refIdx].LongTerm || colPocDiff == currPocDiff {
			return [2]int{int(mvCol[0]), int(mvCol[1])}, true
		}
		return refScale([2]int{int(mvCol[0]), int(mvCol[1])}, colPocDiff, currPocDiff), true
	}
	xBr, yBr := xPb+nPbW, yPb+nPbH
	if yPb>>uint(c.ctbLog2) == yBr>>uint(c.ctbLog2) && yBr < c.height && xBr < c.width {
		if mv, ok := colMv(xBr, yBr); ok {
			return mv, true
		}
	}
	return colMv(xPb+(nPbW>>1), yPb+(nPbH>>1))
}

func (c *mcase) refMerge(xPb, yPb, nPbW, nPbH, partIdx, mergeIdx int) Motion {
	sl, cu := c.sl, c.cu
	nCbS := 1 << uint(cu.Log2Size)
	oW, oH := nPbW, nPbH
	if sl.Log2ParMrgLevel > 2 && nCbS == 8 {
		xPb, yPb, nPbW, nPbH, partIdx = cu.X, cu.Y, nCbS, nCbS, 0
	}
	pm := uint(sl.Log2ParMrgLevel)
	inSameRegion := func(xN, yN int) bool { return xPb>>pm == xN>>pm && yPb>>pm == yN>>pm }
	type nb struct {
		x, y   int
		avail  bool
		flag   bool
		motion Motion
	}
	A1 := &nb{x: xPb - 1, y: yPb + nPbH - 1}
	B1 := &nb{x: xPb + nPbW - 1, y: yPb - 1}
	B0 := &nb{x: xPb + nPbW, y: yPb - 1}
	A0 := &nb{x: xPb - 1, y: yPb + nPbH}
	B2 := &nb{x: xPb - 1, y: yPb - 1}
	for _, n := range []*nb{A1, B1, B0, A0, B2} {
		n.avail = c.avail6_4_2(xPb, yPb, nPbW, nPbH, partIdx, n.x, n.y) && !inSameRegion(n.x, n.y)
		if n.avail {
			n.motion = c.mot(n.x, n.y)
		}
	}
	if (cu.Part == PartNx2N || cu.Part == PartnLx2N || cu.Part == PartnRx2N) && partIdx == 1 {
		A1.avail = false
	}
	if (cu.Part == Part2NxN || cu.Part == Part2NxnU || cu.Part == Part2NxnD) && partIdx == 1 {
		B1.avail = false
	}
	A1.flag = A1.avail
	B1.flag = B1.avail && !(A1.avail && sameMv(A1.motion, B1.motion))
	B0.flag = B0.avail && !(B1.avail && sameMv(B1.motion, B0.motion))
	A0.flag = A0.avail && !(A1.avail && sameMv(A1.motion, A0.motion))
	count := b2n(A0.flag) + b2n(A1.flag) + b2n(B0.flag) + b2n(B1.flag)
	B2.flag = B2.avail && !(A1.avail && sameMv(A1.motion, B2.motion)) && !(B1.avail && sameMv(B1.motion, B2.motion)) && count != 4
	var cands []Motion
	for _, n := range []*nb{A1, B1, B0, A0, B2} {
		if n.flag {
			cands = append(cands, n.motion)
		}
	}
	var col Motion
	col.RefIdx = [2]int8{-1, -1}
	mv0, ok0 := c.refTemporal(xPb, yPb, nPbW, nPbH, 0, 0)
	var mv1 [2]int
	var ok1 bool
	if sl.Type == SliceB {
		mv1, ok1 = c.refTemporal(xPb, yPb, nPbW, nPbH, 1, 0)
	}
	if ok0 || ok1 {
		if ok0 {
			col.Pred[0], col.RefIdx[0], col.MV[0] = true, 0, [2]int16{int16(mv0[0]), int16(mv0[1])}
		}
		if ok1 {
			col.Pred[1], col.RefIdx[1], col.MV[1] = true, 0, [2]int16{int16(mv1[0]), int16(mv1[1])}
		}
		cands = append(cands, col)
	}
	numOrig := len(cands)
	if sl.Type == SliceB && numOrig > 1 && numOrig < sl.MaxMergeCand {
		l0 := []int{0, 1, 0, 2, 1, 2, 0, 3, 1, 3, 2, 3}
		l1 := []int{1, 0, 2, 0, 2, 1, 3, 0, 3, 1, 3, 2}
		for comb, stop := 0, false; !stop; {
			a, b := cands[l0[comb]], cands[l1[comb]]
			if a.Pred[0] && b.Pred[1] && (c.poc(0, int(a.RefIdx[0])) != c.poc(1, int(b.RefIdx[1])) || a.MV[0] != b.MV[1]) {
				cands = append(cands, Motion{Pred: [2]bool{true, true}, RefIdx: [2]int8{a.RefIdx[0], b.RefIdx[1]}, MV: [2][2]int16{a.MV[0], b.MV[1]}})
			}
			comb++
			if comb == numOrig*(numOrig-1) || len(cands) == sl.MaxMergeCand {
				stop = true
			}
		}
	}
	numRef := len(sl.List[0])
	if sl.Type == SliceB {
		numRef = min(len(sl.List[0]), len(sl.List[1]))
	}
	for zero := 0; len(cands) < sl.MaxMergeCand; zero++ {
		r := 0
		if zero < numRef {
			r = zero
		}
		if sl.Type == SliceP {
			cands = append(cands, Motion{Pred: [2]bool{true, false}, RefIdx: [2]int8{int8(r), -1}})
		} else {
			cands = append(cands, Motion{Pred: [2]bool{true, true}, RefIdx: [2]int8{int8(r), int8(r)}})
		}
	}
	m := cands[mergeIdx]
	if m.Pred[0] && m.Pred[1] && oW+oH == 12 {
		m.RefIdx[1], m.Pred[1], m.MV[1] = -1, false, [2]int16{}
	}
	return m
}

func (c *mcase) refAMVP(xPb, yPb, nPbW, nPbH, partIdx, X, refIdx, flag int) [2]int {
	sl := c.sl
	Y := 1 - X
	lt := func(l, idx int) bool { return sl.List[l][idx].LongTerm }
	mvl := func(m Motion, l int) [2]int { return [2]int{int(m.MV[l][0]), int(m.MV[l][1])} }
	xA0, yA0 := xPb-1, yPb+nPbH
	xA1, yA1 := xA0, yA0-1
	avA := [2]bool{c.avail6_4_2(xPb, yPb, nPbW, nPbH, partIdx, xA0, yA0), c.avail6_4_2(xPb, yPb, nPbW, nPbH, partIdx, xA1, yA1)}
	pos := [2][2]int{{xA0, yA0}, {xA1, yA1}}
	isScaled := avA[0] || avA[1]
	var mvA, mvB [2]int
	var fA, fB bool
	for k := 0; k < 2; k++ {
		if avA[k] && !fA {
			m := c.mot(pos[k][0], pos[k][1])
			if m.Pred[X] && c.poc(X, int(m.RefIdx[X])) == c.poc(X, refIdx) {
				fA, mvA = true, mvl(m, X)
			} else if m.Pred[Y] && c.poc(Y, int(m.RefIdx[Y])) == c.poc(X, refIdx) {
				fA, mvA = true, mvl(m, Y)
			}
		}
	}
	for k := 0; k < 2 && !fA; k++ {
		if avA[k] && !fA {
			m := c.mot(pos[k][0], pos[k][1])
			var refPoc int
			var refLt bool
			if m.Pred[X] && lt(X, refIdx) == lt(X, int(m.RefIdx[X])) {
				fA, mvA, refPoc, refLt = true, mvl(m, X), c.poc(X, int(m.RefIdx[X])), lt(X, int(m.RefIdx[X]))
			} else if m.Pred[Y] && lt(X, refIdx) == lt(Y, int(m.RefIdx[Y])) {
				fA, mvA, refPoc, refLt = true, mvl(m, Y), c.poc(Y, int(m.RefIdx[Y])), lt(Y, int(m.RefIdx[Y]))
			}
			if fA && refPoc != c.poc(X, refIdx) && !refLt && !lt(X, refIdx) {
				mvA = refScale(mvA, int(sl.POC)-refPoc, int(sl.POC)-c.poc(X, refIdx))
			}
		}
	}
	posB := [3][2]int{{xPb + nPbW, yPb - 1}, {xPb + nPbW - 1, yPb - 1}, {xPb - 1, yPb - 1}}
	var avB [3]bool
	for k := 0; k < 3; k++ {
		avB[k] = c.avail6_4_2(xPb, yPb, nPbW, nPbH, partIdx, posB[k][0], posB[k][1])
		if avB[k] && !fB {
			m := c.mot(posB[k][0], posB[k][1])
			if m.Pred[X] && c.poc(X, int(m.RefIdx[X])) == c.poc(X, refIdx) {
				fB, mvB = true, mvl(m, X)
			} else if m.Pred[Y] && c.poc(Y, int(m.RefIdx[Y])) == c.poc(X, refIdx) {
				fB, mvB = true, mvl(m, Y)
			}
		}
	}
	if !isScaled && fB {
		fA, mvA = true, mvB
	}
	if !isScaled {
		fB = false
		for k := 0; k < 3 && !fB; k++ {
			if avB[k] {
				m := c.mot(posB[k][0], posB[k][1])
				var refPoc int
				var refLt bool
				if m.Pred[X] && lt(X, refIdx) == lt(X, int(m.RefIdx[X])) {
					fB, mvB, refPoc, refLt = true, mvl(m, X), c.poc(X, int(m.RefIdx[X])), lt(X, int(m.RefIdx[X]))
				} else if m.Pred[Y] && lt(X, refIdx) == lt(Y, int(m.RefIdx[Y])) {
					fB, mvB, refPoc, refLt = true, mvl(m, Y), c.poc(Y, int(m.RefIdx[Y])), lt(Y, int(m.RefIdx[Y]))
				}
				if fB && refPoc != c.poc(X, refIdx) && !refLt && !lt(X, refIdx) {
					mvB = refScale(mvB, int(sl.POC)-refPoc, int(sl.POC)-c.poc(X, refIdx))
				}
			}
		}
	}
	var fCol bool
	var mvCol [2]int
	if !(fA && fB && mvA != mvB) {
		mvCol, fCol = c.refTemporal(xPb, yPb, nPbW, nPbH, X, refIdx)
	}
	var list [][2]int
	if fA {
		list = append(list, mvA)
		if fB && mvA != mvB {
			list = append(list, mvB)
		}
	} else if fB {
		list = append(list, mvB)
	}
	if len(list) < 2 && fCol {
		list = append(list, mvCol)
	}
	for len(list) < 2 {
		list = append(list, [2]int{})
	}
	return list[flag]
}

// randomMotion is the motion of a prediction block of the slice: lists used with
// reference indices inside them, and the unused ones normalised.
func randomMotion(rng *rand.Rand, sl *InterSlice, mvRange int) Motion {
	var m Motion
	m.RefIdx = [2]int8{-1, -1}
	m.Pred[0] = true
	if sl.Type == SliceB {
		m.Pred = [2]bool{rng.IntN(3) != 0, rng.IntN(3) != 0}
		if !m.Pred[0] && !m.Pred[1] {
			m.Pred[rng.IntN(2)] = true
		}
	}
	for l := 0; l < 2; l++ {
		if m.Pred[l] {
			m.RefIdx[l] = int8(rng.IntN(len(sl.List[l])))
			m.MV[l] = [2]int16{int16(rng.IntN(2*mvRange+1) - mvRange), int16(rng.IntN(2*mvRange+1) - mvRange)}
			e := sl.List[l][m.RefIdx[l]]
			m.Ref[l], m.LongTerm[l] = e.POC, e.LongTerm
		}
	}
	return m
}

func newMotionCase(t *testing.T, rng *rand.Rand) *mcase {
	t.Helper()
	diff := rng.IntN(3)
	ctbLog2 := 4 + diff
	wd, ht := 64+16*rng.IntN(3), 64+16*rng.IntN(2)
	sps := mustSPS(t, set{chroma: 1, width: uint32(wd), height: uint32(ht), sizes: []uint32{0, uint32(1 + diff), 0, uint32(min(1+diff, 3)), 0, 0}})
	pps := mustPPS(t, pset{})
	l, err := NewCtbLayout(sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	ps := NewPictureState(sps, l)
	for i := range ps.SliceAddr {
		ps.SliceAddr[i] = 0
	}
	sl := &InterSlice{Type: []SliceType{SliceP, SliceB}[rng.IntN(2)], POC: int32(20 + rng.IntN(10)),
		MaxMergeCand: 1 + rng.IntN(5), Log2ParMrgLevel: 2 + rng.IntN(ctbLog2-1), TemporalMVP: rng.IntN(3) != 0, ColFromL0: rng.IntN(2) == 0}
	if sl.Type == SliceP {
		sl.ColFromL0 = true
	}
	for list := 0; list < 2; list++ {
		if list == 1 && sl.Type == SliceP {
			break
		}
		for i := 0; i < 1+rng.IntN(4); i++ {
			// a picture never refers to its own picture order count
			poc := sl.POC - 6 + int32(rng.IntN(13))
			if poc == sl.POC {
				poc += 7
			}
			sl.List[list] = append(sl.List[list], RefEntry{POC: poc, LongTerm: rng.IntN(5) == 0})
		}
	}
	sl.NoBackwardPred = true
	for _, lst := range sl.List {
		for _, e := range lst {
			if e.POC > sl.POC {
				sl.NoBackwardPred = false
			}
		}
	}
	ps.Motion = make([]Motion, len(ps.EdgeV))
	for i := range ps.Motion {
		if rng.IntN(8) != 0 {
			ps.Motion[i] = randomMotion(rng, sl, 40)
		}
	}
	for i := range ps.CuMode {
		ps.CuMode[i] = uint8(ModeInter)
		if rng.IntN(5) == 0 {
			ps.CuMode[i] = uint8(ModeIntra)
		}
	}
	// intra blocks carry no motion
	for y := 0; y < len(ps.Motion)/ps.Edge4W; y++ {
		for x := 0; x < ps.Edge4W; x++ {
			if ps.CuMode[(y/2)*ps.MinCbWidth+x/2] == uint8(ModeIntra) {
				ps.Motion[y*ps.Edge4W+x] = Motion{RefIdx: [2]int8{-1, -1}}
			}
		}
	}
	// the collocated picture
	colPOC := sl.POC - 4 + int32(rng.IntN(9))
	colState := NewPictureState(sps, l)
	colState.Motion = make([]Motion, len(ps.EdgeV))
	for i := range colState.Motion {
		if rng.IntN(4) != 0 {
			cm := randomMotion(rng, sl, 60)
			// the collocated picture's own reference pictures, not this slice's
			for l := 0; l < 2; l++ {
				if cm.Pred[l] {
					cm.Ref[l] = colPOC - 12 + int32(rng.IntN(25))
					if cm.Ref[l] == colPOC {
						cm.Ref[l]++
					}
					cm.LongTerm[l] = rng.IntN(6) == 0
				}
			}
			colState.Motion[i] = cm
		}
	}
	if sl.TemporalMVP {
		sl.Col = &DPBPicture{POC: colPOC, State: colState}
	}
	w := &SliceWalker{Pic: ps, SPS: sps, PPS: pps, chroma: 1}
	return &mcase{w: w, sl: sl, ctbLog2: ctbLog2, width: wd, height: ht, colState: colState}
}

func TestMotionDerivationAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(191, 192))
	var merges, amvps, temporal, combined, scaledCount int
	for iter := 0; iter < 20000; iter++ {
		c := newMotionCase(t, rng)
		// a coding unit and one of its prediction blocks
		log2 := 3 + rng.IntN(min(3, c.ctbLog2-2))
		n := 1 << uint(log2)
		cu := &CodingUnit{X: rng.IntN(c.width/n) * n, Y: rng.IntN(c.height/n) * n, Log2Size: log2, Mode: ModeInter}
		parts := []PartMode{Part2Nx2N, Part2NxN, PartNx2N}
		if log2 > 3 {
			parts = append(parts, PartNxN, Part2NxnU, Part2NxnD, PartnLx2N, PartnRx2N)
		}
		cu.Part = parts[rng.IntN(len(parts))]
		type blk struct{ x, y, w, h int }
		var bs []blk
		switch cu.Part {
		case Part2Nx2N:
			bs = []blk{{0, 0, n, n}}
		case Part2NxN:
			bs = []blk{{0, 0, n, n / 2}, {0, n / 2, n, n / 2}}
		case PartNx2N:
			bs = []blk{{0, 0, n / 2, n}, {n / 2, 0, n / 2, n}}
		case Part2NxnU:
			bs = []blk{{0, 0, n, n / 4}, {0, n / 4, n, 3 * n / 4}}
		case Part2NxnD:
			bs = []blk{{0, 0, n, 3 * n / 4}, {0, 3 * n / 4, n, n / 4}}
		case PartnLx2N:
			bs = []blk{{0, 0, n / 4, n}, {n / 4, 0, 3 * n / 4, n}}
		case PartnRx2N:
			bs = []blk{{0, 0, 3 * n / 4, n}, {3 * n / 4, 0, n / 4, n}}
		default:
			bs = []blk{{0, 0, n / 2, n / 2}, {n / 2, 0, n / 2, n / 2}, {0, n / 2, n / 2, n / 2}, {n / 2, n / 2, n / 2, n / 2}}
		}
		partIdx := rng.IntN(len(bs))
		b := bs[partIdx]
		c.cu = cu
		c.w.Pic.CuMode[c.w.cell(cu.X, cu.Y)] = uint8(ModeInter)
		xPb, yPb := cu.X+b.x, cu.Y+b.y
		// the coding unit's own region is inter
		for y := cu.Y; y < cu.Y+n; y += 8 {
			for x := cu.X; x < cu.X+n; x += 8 {
				c.w.Pic.CuMode[c.w.cell(x, y)] = uint8(ModeInter)
			}
		}
		if rng.IntN(2) == 0 {
			mi := rng.IntN(c.sl.MaxMergeCand)
			got := c.w.mergeMotion(c.sl, cu, xPb, yPb, b.w, b.h, partIdx, mi)
			want := c.refMerge(xPb, yPb, b.w, b.h, partIdx, mi)
			// Ref and LongTerm are filled in by deriveMotion from the lists, not here.
			got.Ref, got.LongTerm, want.Ref, want.LongTerm = [2]int32{}, [2]bool{}, [2]int32{}, [2]bool{}
			if got != want {
				t.Fatalf("iter %d: merge %d of block %dx%d at (%d,%d) part %d/%d: %+v, the text gives %+v", iter, mi, b.w, b.h, xPb, yPb, cu.Part, partIdx, got, want)
			}
			merges++
			if want.Pred[0] && want.Pred[1] && want.RefIdx[0] != want.RefIdx[1] {
				combined++
			}
		} else {
			X := 0
			if c.sl.Type == SliceB {
				X = rng.IntN(2)
			}
			refIdx, flag := rng.IntN(len(c.sl.List[X])), rng.IntN(2)
			got := c.w.amvp(c.sl, cu, xPb, yPb, b.w, b.h, partIdx, X, refIdx, flag)
			want := c.refAMVP(xPb, yPb, b.w, b.h, partIdx, X, refIdx, flag)
			if got != want {
				t.Fatalf("iter %d: AMVP list %d index %d flag %d of block %dx%d at (%d,%d) part %d/%d: %v, the text gives %v", iter, X, refIdx, flag, b.w, b.h, xPb, yPb, cu.Part, partIdx, got, want)
			}
			amvps++
			if c.sl.TemporalMVP {
				temporal++
			}
			if got != [2]int{} {
				scaledCount++
			}
		}
	}
	t.Logf("merges %d (bi-predicted with two indices %d), AMVPs %d (non-zero %d, with temporal prediction on %d)", merges, combined, amvps, scaledCount, temporal)
	if merges == 0 || amvps == 0 || combined == 0 || scaledCount == 0 {
		t.Error("a case was never exercised")
	}
}

// A coding unit decoded in an inter slice leaves its motion in the picture and in its
// prediction units, with the reference picture order counts of the lists.
func TestCodingUnitsRecordTheirMotion(t *testing.T) {
	rng := rand.New(rand.NewPCG(301, 302))
	var merged, explicit, skipped int
	for iter := 0; iter < 300; iter++ {
		ww, ps, plans := cuCase(t, rng)
		poc := int32(30)
		colState := NewPictureState(ps.SPS, ps.Layout)
		colState.Motion = make([]Motion, len(ps.EdgeV))
		for i := range colState.Motion {
			if rng.IntN(3) != 0 {
				colState.Motion[i] = Motion{Pred: [2]bool{true, rng.IntN(2) == 0}, RefIdx: [2]int8{0, 0}, Ref: [2]int32{int32(20 + rng.IntN(8)), 21}, MV: [2][2]int16{{int16(rng.IntN(64)), -3}, {5, int16(rng.IntN(64))}}}
			}
		}
		generated := &DPBPicture{POC: 28}
		real := &DPBPicture{POC: 27, State: colState}
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if hdr.Type != SliceI {
				var lists [2][]RefEntry
				for l, n := range []uint32{hdr.Refs.NumRefIdxL0Active, hdr.Refs.NumRefIdxL1Active} {
					for i := uint32(0); i < n; i++ {
						lists[l] = append(lists[l], RefEntry{POC: poc - 1 - int32(rng.IntN(5)) + 10*int32(rng.IntN(2)), LongTerm: rng.IntN(6) == 0, Pic: []*DPBPicture{generated, real}[rng.IntN(2)]})
					}
				}
				hdr.Refs.TemporalMVP = rng.IntN(2) == 0
				hdr.Refs.CollocatedFromL0 = rng.IntN(2) == 0
				hdr.Refs.CollocatedRefIdx = 0
				if hdr.Type == SliceP {
					lists[1] = nil
				}
				sl, err := NewInterSlice(hdr, ww.pps, poc, lists[0], lists[1])
				if err != nil {
					t.Fatalf("iter %d segment %d: %v", iter, si, err)
				}
				ps.Inter = sl
			} else {
				ps.Inter = nil
			}
			check := func(cu *CodingUnit) {
				for k := 0; k < cu.NumPU; k++ {
					pu := &cu.PU[k]
					if ps.Inter == nil {
						continue
					}
					if cu.Mode == ModeSkip {
						skipped++
					} else if pu.Merge {
						merged++
					} else {
						explicit++
						for X := 0; X < 2; X++ {
							want := !((X == 0 && pu.Idc == PredL1) || (X == 1 && pu.Idc == PredL0))
							if pu.Motion.Pred[X] != want {
								t.Fatalf("iter %d: list %d used %v for %v", iter, X, pu.Motion.Pred[X], pu.Idc)
							}
						}
					}
					for l := 0; l < 2; l++ {
						if pu.Motion.Pred[l] != (pu.Motion.RefIdx[l] >= 0) {
							t.Fatalf("iter %d: list %d flag %v index %d", iter, l, pu.Motion.Pred[l], pu.Motion.RefIdx[l])
						}
						if pu.Motion.Pred[l] {
							e := ps.Inter.List[l][pu.Motion.RefIdx[l]]
							if pu.Motion.Ref[l] != e.POC || pu.Motion.LongTerm[l] != e.LongTerm {
								t.Fatalf("iter %d: reference of list %d not taken from the list", iter, l)
							}
						}
					}
					for y := pu.Y >> 2; y < (pu.Y+pu.H)>>2; y++ {
						for x := pu.X >> 2; x < (pu.X+pu.W)>>2; x++ {
							if ps.Motion[y*ps.Edge4W+x] != pu.Motion {
								t.Fatalf("iter %d: motion at (%d,%d) differs from its prediction unit", iter, 4*x, 4*y)
							}
						}
					}
				}
			}
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, CodingUnitDecoder(func(w *SliceWalker, _ *CodingUnit) error { w.D.Bypass(); return nil }, check)); err != nil {
				t.Fatalf("iter %d segment %d: %v", iter, si, err)
			}
		}
	}
	t.Logf("skipped %d merged %d explicit %d", skipped, merged, explicit)
	if skipped == 0 || merged == 0 || explicit == 0 {
		t.Error("a prediction unit kind was never exercised")
	}
}

func TestInterSliceSetup(t *testing.T) {
	past := &DPBPicture{POC: 3}
	future := &DPBPicture{POC: 9}
	l0 := []RefEntry{{POC: 3, Pic: past}}
	l1 := []RefEntry{{POC: 9, Pic: future}}
	hdr := SliceHeader{Type: SliceB}
	hdr.Refs.TemporalMVP, hdr.Refs.MaxMergeCand = true, 4
	sl, err := NewInterSlice(hdr, PPS{Log2ParMrgLevel: 3}, 5, l0, l1)
	if err != nil {
		t.Fatal(err)
	}
	// collocated_from_l0_flag 0 in a B slice picks list 1; a future reference clears NoBackwardPredFlag
	if sl.Col != future || sl.ColFromL0 || sl.NoBackwardPred || sl.MaxMergeCand != 4 || sl.Log2ParMrgLevel != 3 {
		t.Errorf("%+v", sl)
	}
	hdr.Refs.CollocatedFromL0 = true
	if sl, _ = NewInterSlice(hdr, PPS{}, 5, l0, l1); sl.Col != past {
		t.Error("collocated_from_l0_flag 1 must pick list 0")
	}
	if sl, _ = NewInterSlice(hdr, PPS{}, 10, l0, l1); !sl.NoBackwardPred {
		t.Error("no reference after the picture: NoBackwardPredFlag is 1")
	}
	// a P slice has no collocated_from_l0_flag: it is inferred 1
	hdr.Type, hdr.Refs.CollocatedFromL0 = SliceP, false
	if sl, _ = NewInterSlice(hdr, PPS{}, 5, l0, nil); sl.Col != past || !sl.ColFromL0 {
		t.Error("a P slice collocates with list 0")
	}
	hdr.Refs.CollocatedRefIdx = 1
	if _, err = NewInterSlice(hdr, PPS{}, 5, l0, nil); err == nil {
		t.Error("collocated_ref_idx beyond the list was accepted")
	}
	hdr.Refs.TemporalMVP = false
	if sl, err = NewInterSlice(hdr, PPS{}, 5, l0, nil); err != nil || sl.Col != nil {
		t.Error("without temporal prediction the index is not looked at")
	}
}

func TestScaleMVOfAZeroDistanceIsTheVector(t *testing.T) {
	if got := scaleMV([2]int{7, -9}, 0, 5); got != [2]int{7, -9} {
		t.Errorf("%v", got)
	}
	// Twice the distance halves it, the rounding going away from zero at the half.
	if got := scaleMV([2]int{8, -8}, 2, 1); got != [2]int{4, -4} {
		t.Errorf("%v", got)
	}
}
