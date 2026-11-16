// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "fmt"

// The derivation of motion vectors and reference indices (8.5.3.2): merge mode with
// its spatial, temporal, combined bi-predictive and zero candidates, the motion vector
// predictors of AMVP, and temporal motion vector prediction.

// RefEntry is one entry of a reference picture list as the derivation needs it: the
// picture's order count, whether it is a long-term reference picture, and the picture
// itself (whose stored motion temporal prediction reads).
type RefEntry struct {
	POC      int32
	LongTerm bool
	Pic      *DPBPicture
}

// InterSlice is what the motion derivation of a P or B slice takes from the slice: its
// type and order count, the two reference picture lists, and the variables of 8.3.5.
type InterSlice struct {
	Type SliceType
	POC  int32
	List [2][]RefEntry
	// TemporalMVP is slice_temporal_mvp_enabled_flag; Col is ColPic and
	// NoBackwardPred NoBackwardPredFlag (8.3.5); ColFromL0 is collocated_from_l0_flag.
	TemporalMVP    bool
	Col            *DPBPicture
	ColFromL0      bool
	NoBackwardPred bool
	MaxMergeCand   int
	// Log2ParMrgLevel is Log2ParMrgLevel (7-37).
	Log2ParMrgLevel int
	// Weights is pred_weight_table when the slice weighs its prediction explicitly
	// (weightedPredFlag of 8.5.3.3.4.1 is 1), nil otherwise.
	Weights *PredWeights
}

// NewInterSlice derives ColPic and NoBackwardPredFlag (8.3.5) for a slice whose lists are
// l0 and l1.
func NewInterSlice(hdr SliceHeader, pps PPS, poc int32, l0, l1 []RefEntry) (*InterSlice, error) {
	s := &InterSlice{Type: hdr.Type, POC: poc, List: [2][]RefEntry{l0, l1}, TemporalMVP: hdr.Refs.TemporalMVP,
		ColFromL0: hdr.Refs.CollocatedFromL0 || hdr.Type != SliceB, MaxMergeCand: int(hdr.Refs.MaxMergeCand),
		Log2ParMrgLevel: int(pps.Log2ParMrgLevel), NoBackwardPred: true, Weights: hdr.Refs.Weights}
	for _, l := range s.List {
		for _, e := range l {
			if e.POC > poc {
				s.NoBackwardPred = false
			}
		}
	}
	if s.TemporalMVP {
		col := s.List[0]
		if !s.ColFromL0 {
			col = s.List[1]
		}
		if int(hdr.Refs.CollocatedRefIdx) >= len(col) {
			return nil, fmt.Errorf("h265: collocated_ref_idx %d of a list of %d", hdr.Refs.CollocatedRefIdx, len(col))
		}
		s.Col = col[hdr.Refs.CollocatedRefIdx].Pic
	}
	return s, nil
}

// sameMotion says whether two prediction blocks have the same motion vectors and the same
// reference indices.
func sameMotion(a, b *Motion) bool {
	for l := 0; l < 2; l++ {
		if a.Pred[l] != b.Pred[l] {
			return false
		}
		if a.Pred[l] && (a.RefIdx[l] != b.RefIdx[l] || a.MV[l] != b.MV[l]) {
			return false
		}
	}
	return true
}

func (w *SliceWalker) motionAt(x, y int) *Motion {
	return &w.Pic.Motion[(y>>2)*w.Pic.Edge4W+x>>2]
}

// pbAvailable is 6.4.2: whether the prediction block covering ( xN, yN ) is available to
// the prediction block ( xPb, yPb ) of the coding unit cu.
func (w *SliceWalker) pbAvailable(cu *CodingUnit, xPb, yPb, nPbW, nPbH, partIdx, xN, yN int) bool {
	nCbS := 1 << uint(cu.Log2Size)
	var avail bool
	if sameCb := cu.X <= xN && cu.Y <= yN && cu.X+nCbS > xN && cu.Y+nCbS > yN; !sameCb {
		avail = w.Available(xPb, yPb, xN, yN)
	} else {
		avail = !(nPbW<<1 == nCbS && nPbH<<1 == nCbS && partIdx == 1 && cu.Y+nPbH <= yN && cu.X+nPbW > xN)
	}
	return avail && w.Pic.CuMode[w.cell(xN, yN)] != uint8(ModeIntra)
}

// scaleMV is the scaling of (8-179) to (8-183) (and its two siblings): a motion vector of a
// picture td away scaled to one tb away.
func scaleMV(mv [2]int, td, tb int) [2]int {
	td, tb = clip3(-128, 127, td), clip3(-128, 127, tb)
	if td == 0 {
		return mv
	}
	tx := (16384 + abs(td)>>1) / td
	dsf := clip3(-4096, 4095, (tb*tx+32)>>6)
	var out [2]int
	for i := range mv {
		p := dsf * mv[i]
		out[i] = clip3(-32768, 32767, sign(p)*((abs(p)+127)>>8))
	}
	return out
}

// temporalMV is 8.5.3.2.8 and 8.5.3.2.9: the motion vector of list X for reference index
// refIdx predicted from the collocated picture.
func (w *SliceWalker) temporalMV(sl *InterSlice, xPb, yPb, nPbW, nPbH, X, refIdx int) ([2]int, bool) {
	if !sl.TemporalMVP || sl.Col == nil {
		return [2]int{}, false
	}
	s := &w.SPS
	try := func(x, y int) ([2]int, bool) {
		x, y = (x>>4)<<4, (y>>4)<<4
		col := sl.Col.State
		if col == nil || col.Motion == nil {
			return [2]int{}, false // a generated picture is intra everywhere
		}
		cm := &col.Motion[(y>>2)*col.Edge4W+x>>2]
		if !cm.Pred[0] && !cm.Pred[1] { // coded in an intra prediction mode
			return [2]int{}, false
		}
		var list int
		switch {
		case !cm.Pred[0]:
			list = 1
		case !cm.Pred[1]:
			list = 0
		case sl.NoBackwardPred:
			list = X
		default:
			list = bit(sl.ColFromL0) // N is the value of collocated_from_l0_flag
		}
		cur := sl.List[X][refIdx]
		if cur.LongTerm != cm.LongTerm[list] {
			return [2]int{}, false
		}
		mvCol := [2]int{int(cm.MV[list][0]), int(cm.MV[list][1])}
		colPocDiff := int(sl.Col.POC) - int(cm.Ref[list])
		currPocDiff := int(sl.POC) - int(cur.POC)
		if cur.LongTerm || colPocDiff == currPocDiff {
			return mvCol, true
		}
		return scaleMV(mvCol, colPocDiff, currPocDiff), true
	}
	ctb := uint(s.Log2CtbSize)
	xBr, yBr := xPb+nPbW, yPb+nPbH
	if yPb>>ctb == yBr>>ctb && yBr < int(s.CodedHeight) && xBr < int(s.CodedWidth) {
		if mv, ok := try(xBr, yBr); ok {
			return mv, true
		}
	}
	return try(xPb+nPbW>>1, yPb+nPbH>>1)
}

// mergeMotion is 8.5.3.2.2 to 8.5.3.2.5: the motion of the prediction block picked by
// merge_idx from the merging candidate list.
func (w *SliceWalker) mergeMotion(sl *InterSlice, cu *CodingUnit, xPb, yPb, nPbW, nPbH, partIdx, mergeIdx int) Motion {
	origW, origH := nPbW, nPbH
	nCbS := 1 << uint(cu.Log2Size)
	if sl.Log2ParMrgLevel > 2 && nCbS == 8 {
		xPb, yPb, nPbW, nPbH, partIdx = cu.X, cu.Y, nCbS, nCbS, 0
	}
	pml := uint(sl.Log2ParMrgLevel)
	sameMerge := func(xN, yN int) bool { return xPb>>pml == xN>>pml && yPb>>pml == yN>>pml }
	xA1, yA1 := xPb-1, yPb+nPbH-1
	xB1, yB1 := xPb+nPbW-1, yPb-1
	xB0, yB0 := xPb+nPbW, yPb-1
	xA0, yA0 := xPb-1, yPb+nPbH
	xB2, yB2 := xPb-1, yPb-1

	availA1 := w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xA1, yA1) && !sameMerge(xA1, yA1) &&
		!((cu.Part == PartNx2N || cu.Part == PartnLx2N || cu.Part == PartnRx2N) && partIdx == 1)
	availB1 := w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xB1, yB1) && !sameMerge(xB1, yB1) &&
		!((cu.Part == Part2NxN || cu.Part == Part2NxnU || cu.Part == Part2NxnD) && partIdx == 1)
	availB0 := w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xB0, yB0) && !sameMerge(xB0, yB0)
	availA0 := w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xA0, yA0) && !sameMerge(xA0, yA0)
	availB2 := w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xB2, yB2) && !sameMerge(xB2, yB2)

	var list []Motion
	flags := 0
	var mA1, mB1 *Motion
	if availA1 {
		mA1 = w.motionAt(xA1, yA1)
		list = append(list, *mA1)
		flags++
	}
	if availB1 {
		mB1 = w.motionAt(xB1, yB1)
		if !(availA1 && sameMotion(mA1, mB1)) {
			list = append(list, *mB1)
			flags++
		}
	}
	if availB0 {
		if m := w.motionAt(xB0, yB0); !(availB1 && sameMotion(mB1, m)) {
			list = append(list, *m)
			flags++
		}
	}
	if availA0 {
		if m := w.motionAt(xA0, yA0); !(availA1 && sameMotion(mA1, m)) {
			list = append(list, *m)
			flags++
		}
	}
	if availB2 {
		m := w.motionAt(xB2, yB2)
		if !(availA1 && sameMotion(mA1, m)) && !(availB1 && sameMotion(mB1, m)) && flags != 4 {
			list = append(list, *m)
		}
	}
	// The temporal candidate: refIdx 0 in each list used.
	var col Motion
	if mv, ok := w.temporalMV(sl, xPb, yPb, nPbW, nPbH, 0, 0); ok {
		col.Pred[0], col.MV[0] = true, [2]int16{int16(mv[0]), int16(mv[1])}
	}
	if sl.Type == SliceB {
		if mv, ok := w.temporalMV(sl, xPb, yPb, nPbW, nPbH, 1, 0); ok {
			col.Pred[1], col.MV[1] = true, [2]int16{int16(mv[0]), int16(mv[1])}
		}
	}
	if col.Pred[0] || col.Pred[1] {
		col.RefIdx = [2]int8{int8(bit(col.Pred[0])) - 1, int8(bit(col.Pred[1])) - 1}
		list = append(list, col)
	}
	num := len(list)
	// Combined bi-predictive candidates (8.5.3.2.4).
	if sl.Type == SliceB && num > 1 && num < sl.MaxMergeCand {
		l0Idx := [12]int{0, 1, 0, 2, 1, 2, 0, 3, 1, 3, 2, 3}
		l1Idx := [12]int{1, 0, 2, 0, 2, 1, 3, 0, 3, 1, 3, 2}
		for combIdx := 0; ; {
			c0, c1 := list[l0Idx[combIdx]], list[l1Idx[combIdx]]
			if c0.Pred[0] && c1.Pred[1] &&
				(sl.List[0][c0.RefIdx[0]].POC != sl.List[1][c1.RefIdx[1]].POC || c0.MV[0] != c1.MV[1]) {
				list = append(list, Motion{Pred: [2]bool{true, true}, RefIdx: [2]int8{c0.RefIdx[0], c1.RefIdx[1]}, MV: [2][2]int16{c0.MV[0], c1.MV[1]}})
			}
			combIdx++
			if combIdx == num*(num-1) || len(list) == sl.MaxMergeCand {
				break
			}
		}
	}
	// Zero candidates (8.5.3.2.5).
	numRefIdx := len(sl.List[0])
	if sl.Type == SliceB {
		numRefIdx = min(len(sl.List[0]), len(sl.List[1]))
	}
	for zeroIdx := 0; len(list) < sl.MaxMergeCand; zeroIdx++ {
		ri := int8(0)
		if zeroIdx < numRefIdx {
			ri = int8(zeroIdx)
		}
		if sl.Type == SliceP {
			list = append(list, Motion{Pred: [2]bool{true, false}, RefIdx: [2]int8{ri, -1}})
		} else {
			list = append(list, Motion{Pred: [2]bool{true, true}, RefIdx: [2]int8{ri, ri}})
		}
	}
	m := list[mergeIdx]
	if m.Pred[0] && m.Pred[1] && origW+origH == 12 {
		m.Pred[1], m.RefIdx[1], m.MV[1] = false, -1, [2]int16{}
	}
	return m
}

// amvp is 8.5.3.2.6 and 8.5.3.2.7: the motion vector predictor of list X, reference index
// refIdx, picked by mvp_lX_flag.
func (w *SliceWalker) amvp(sl *InterSlice, cu *CodingUnit, xPb, yPb, nPbW, nPbH, partIdx, X, refIdx, mvpFlag int) [2]int {
	cur := sl.List[X][refIdx]
	mvOf := func(m *Motion, l int) [2]int { return [2]int{int(m.MV[l][0]), int(m.MV[l][1])} }

	// A candidate of the same reference picture, from list X first and then list Y.
	noScale := func(m *Motion) ([2]int, bool) {
		if m.Pred[X] && sl.List[X][m.RefIdx[X]].POC == cur.POC {
			return mvOf(m, X), true
		}
		if Y := 1 - X; m.Pred[Y] && sl.List[Y][m.RefIdx[Y]].POC == cur.POC {
			return mvOf(m, Y), true
		}
		return [2]int{}, false
	}
	// A candidate of any reference picture of the same kind (both long-term or both short-term),
	// scaled by distance when both are short-term.
	scaled := func(m *Motion) ([2]int, bool) {
		for _, l := range [2]int{X, 1 - X} {
			if !m.Pred[l] {
				continue
			}
			ref := sl.List[l][m.RefIdx[l]]
			if cur.LongTerm != ref.LongTerm {
				continue
			}
			mv := mvOf(m, l)
			if ref.POC != cur.POC && !ref.LongTerm && !cur.LongTerm {
				mv = scaleMV(mv, int(sl.POC)-int(ref.POC), int(sl.POC)-int(cur.POC))
			}
			return mv, true
		}
		return [2]int{}, false
	}

	xA0, yA0 := xPb-1, yPb+nPbH
	xA1, yA1 := xA0, yA0-1
	availA := [2]bool{w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xA0, yA0), w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, xA1, yA1)}
	posA := [2][2]int{{xA0, yA0}, {xA1, yA1}}
	isScaled := availA[0] || availA[1]
	var mvA, mvB [2]int
	foundA, foundB := false, false
	for k := 0; k < 2 && !foundA; k++ {
		if availA[k] {
			mvA, foundA = noScale(w.motionAt(posA[k][0], posA[k][1]))
		}
	}
	for k := 0; k < 2 && !foundA; k++ {
		if availA[k] {
			mvA, foundA = scaled(w.motionAt(posA[k][0], posA[k][1]))
		}
	}

	posB := [3][2]int{{xPb + nPbW, yPb - 1}, {xPb + nPbW - 1, yPb - 1}, {xPb - 1, yPb - 1}}
	var availB [3]bool
	for k := range posB {
		availB[k] = w.pbAvailable(cu, xPb, yPb, nPbW, nPbH, partIdx, posB[k][0], posB[k][1])
		if availB[k] && !foundB {
			mvB, foundB = noScale(w.motionAt(posB[k][0], posB[k][1]))
		}
	}
	if !isScaled && foundB {
		foundA, mvA = true, mvB
	}
	if !isScaled {
		foundB = false
		for k := range posB {
			if availB[k] && !foundB {
				mvB, foundB = scaled(w.motionAt(posB[k][0], posB[k][1]))
			}
		}
	}

	var mvCol [2]int
	foundCol := false
	if !(foundA && foundB && mvA != mvB) {
		mvCol, foundCol = w.temporalMV(sl, xPb, yPb, nPbW, nPbH, X, refIdx)
	}
	var cand [][2]int
	if foundA {
		cand = append(cand, mvA)
		if foundB && mvA != mvB {
			cand = append(cand, mvB)
		}
	} else if foundB {
		cand = append(cand, mvB)
	}
	if len(cand) < 2 && foundCol {
		cand = append(cand, mvCol)
	}
	for len(cand) < 2 {
		cand = append(cand, [2]int{})
	}
	return cand[mvpFlag]
}

// deriveMotion derives the motion of prediction unit pu (partition partIdx of cu, 8.5.3.2.1)
// and records it for the 4x4 blocks it covers.
func (w *SliceWalker) deriveMotion(sl *InterSlice, cu *CodingUnit, pu *PredictionUnit, partIdx int) {
	var m Motion
	if pu.Merge {
		m = w.mergeMotion(sl, cu, pu.X, pu.Y, pu.W, pu.H, partIdx, int(pu.MergeIdx))
	} else {
		for X := 0; X < 2; X++ {
			m.RefIdx[X] = -1
			if (X == 0 && pu.Idc == PredL1) || (X == 1 && pu.Idc == PredL0) {
				continue
			}
			m.Pred[X], m.RefIdx[X] = true, int8(pu.RefIdx[X])
			mvp := w.amvp(sl, cu, pu.X, pu.Y, pu.W, pu.H, partIdx, X, int(pu.RefIdx[X]), int(pu.MvpFlag[X]))
			for c := 0; c < 2; c++ { // (8-94) to (8-97): modulo 2^16
				m.MV[X][c] = int16(mvp[c] + int(pu.Mvd[X][c]))
			}
		}
	}
	for l := 0; l < 2; l++ {
		if m.Pred[l] {
			e := sl.List[l][m.RefIdx[l]]
			m.Ref[l], m.LongTerm[l] = e.POC, e.LongTerm
		}
	}
	pu.Motion = m
	for y := pu.Y >> 2; y < (pu.Y+pu.H)>>2; y++ {
		for x := pu.X >> 2; x < (pu.X+pu.W)>>2; x++ {
			w.Pic.Motion[y*w.Pic.Edge4W+x] = m
		}
	}
}
