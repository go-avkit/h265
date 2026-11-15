// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/go-avkit/h265/internal/cabac"
	"github.com/go-avkit/h265/internal/cabac/cabactest"
)

// The writer of transform_tree( ), transform_unit( ), delta_qp( ),
// chroma_qp_offset( ) and cross_comp_pred( ) (7.3.8.8, 7.3.8.10, 7.3.8.12,
// 7.3.8.14, 7.3.8.15), and of the intra mode derivation (8.4.2, 8.4.3) that the
// scan order of a block depends on. The derivation is written with a sorted
// candidate list and Table 8-2 as a grid, not with the decoder's swaps and rule.

// table82 is Table 8-2: rows intra_chroma_pred_mode 0 to 4, columns
// IntraPredModeY 0, 26, 10, 1 and any other (-1 stands for X, the luma mode).
var table82 = [5][5]int{
	{34, 0, 0, 0, 0},
	{26, 34, 26, 26, 26},
	{10, 10, 34, 10, 10},
	{1, 1, 1, 34, 1},
	{0, 26, 10, 1, -1},
}

// table83Rows are the two rows of Table 8-3: the values for modeIdx 3 to 17 and 18 to 34.
var table83Rows = [2][]int{
	{2, 2, 2, 3, 5, 7, 8, 10, 12, 13, 15, 17, 18, 19, 20},
	{21, 22, 23, 23, 24, 24, 25, 25, 26, 27, 27, 28, 28, 29, 29, 30, 31},
}

func refChromaMode(chromaSyntax, luma, cat int) int {
	col := slices.Index([]int{0, 26, 10, 1}, luma)
	if col < 0 {
		col = 4
	}
	v := table82[chromaSyntax][col]
	if v < 0 {
		v = luma
	}
	if cat == 2 {
		if v <= 2 {
			return v
		}
		all := append(append([]int{}, table83Rows[0]...), table83Rows[1]...)
		return all[v-3]
	}
	return v
}

func (w *walkWriter) setIMode(x0, y0, size int, mode uint8) {
	for y := y0 >> 2; y < min((y0+size+3)>>2, (int(w.sps.CodedHeight)+3)>>2); y++ {
		for x := x0 >> 2; x < min((x0+size+3)>>2, (int(w.sps.CodedWidth)+3)>>2); x++ {
			w.imode[y*((int(w.sps.CodedWidth)+3)>>2)+x] = mode
		}
	}
}

func (w *walkWriter) iModeAt(x, y int) uint8 {
	return w.imode[(y>>2)*((int(w.sps.CodedWidth)+3)>>2)+x>>2]
}

// deriveModes sets IntraPredModeY and IntraPredModeC of the intra coding unit cu.
func (w *walkWriter) deriveModes(cu *CodingUnit) {
	n, pb := 1, 1<<cu.Log2Size
	if cu.Part == PartNxN {
		n, pb = 4, pb/2
	}
	ctb := int(w.sps.Log2CtbSize)
	for i := 0; i < n; i++ {
		x, y := cu.X+(i%2)*pb, cu.Y+(i/2)*pb
		neighbour := func(xn, yn int, above bool) int {
			inside := xn >= cu.X && yn >= cu.Y // an earlier block of this very coding unit
			if !inside && !w.avail([2]int{x, y}, [2]int{xn, yn}) {
				return 1
			}
			if above && yn < (y>>ctb)<<ctb {
				return 1
			}
			return int(w.iModeAt(xn, yn))
		}
		a, b := neighbour(x-1, y, false), neighbour(x, y-1, true)
		var cand []int
		if a == b {
			if a < 2 {
				cand = []int{0, 1, 26}
			} else {
				cand = []int{a, 2 + (a+29)%32, 2 + (a-1)%32}
			}
		} else {
			third := 26
			for _, c := range []int{0, 1, 26} {
				if c != a && c != b {
					third = c
					break
				}
			}
			cand = []int{a, b, third}
		}
		var mode int
		if cu.PrevIntraLumaPred[i] {
			mode = cand[cu.MpmIdx[i]]
		} else {
			slices.Sort(cand)
			mode = int(cu.RemIntraLumaPredMode[i])
			for _, c := range cand {
				if mode >= c {
					mode++
				}
			}
		}
		cu.IntraPredModeY[i] = uint8(mode)
		w.setIMode(x, y, pb, uint8(mode))
	}
	if w.sps.ChromaFormat == 0 {
		return
	}
	for i := 0; i < 4; i++ {
		src := 0
		if w.sps.ChromaFormat == 3 {
			src = i % n
		}
		cu.IntraPredModeC[i] = uint8(refChromaMode(int(cu.IntraChromaPredMode[src]), int(cu.IntraPredModeY[src]), int(w.sps.ChromaFormat)))
	}
}

func (w *walkWriter) tools() *ResidualTools {
	s, p := &w.sps, &w.pps
	return &ResidualTools{
		ChromaArrayType: int(s.ChromaFormat), SignDataHiding: p.SignDataHiding, TransformSkip: p.TransformSkip,
		Log2MaxTransformSkipSize: int(p.Range.Log2MaxTransformSkipSize), TransformSkipContext: s.Range.TransformSkipContext,
		ImplicitRDPCM: s.Range.ImplicitRDPCM, ExplicitRDPCM: s.Range.ExplicitRDPCM,
		PersistentRiceAdaptation: s.Range.PersistentRiceAdaptation, ExtendedPrecision: s.Range.ExtendedPrecisionProcessing,
		BitDepthY: int(s.BitDepthLuma), BitDepthC: int(s.BitDepthChroma),
	}
}

func (w *walkWriter) writeDeltaQP() {
	if !w.pps.CUQPDeltaEnabled || w.qpDone {
		return
	}
	w.qpDone = true
	qpBd := 6 * (int(w.sps.BitDepthLuma) - 8)
	v := w.rng.IntN(60) - 30
	if w.rng.IntN(3) == 0 {
		v = 0
	}
	v = max(-(26 + qpBd/2), min(25+qpBd/2, v))
	abs := v
	if abs < 0 {
		abs = -abs
	}
	for i := 0; i < min(abs, 5); i++ { // prefix: TU, cMax 5; first bin context 0, the rest context 1
		w.bin(cabac.CtxCuQpDeltaAbs+min(i, 1), 1)
	}
	if abs < 5 {
		w.bin(cabac.CtxCuQpDeltaAbs+min(abs, 1), 0)
	} else {
		for _, b := range refEncEGk(uint64(abs-5), 0) {
			w.e.Bypass(b)
		}
	}
	if abs > 0 {
		w.e.Bypass(b2n(v < 0))
	}
	w.cuQpDeltaVal = int32(v)
}

func (w *walkWriter) writeChromaQPOffset() {
	if !w.hdr.CuChromaQPOffsetEnabled || w.cqDone {
		return
	}
	list := w.pps.Range.CbQPOffsetList
	flag := w.rng.IntN(3) != 0
	w.bin(cabac.CtxCuChromaQpOffsetFlag, b2n(flag))
	idx := 0
	if flag && len(list) > 1 {
		idx = w.rng.IntN(len(list))
		for i := 0; i < idx; i++ {
			w.bin(cabac.CtxCuChromaQpOffsetIdx, 1)
		}
		if idx < len(list)-1 {
			w.bin(cabac.CtxCuChromaQpOffsetIdx, 0)
		}
	}
	w.cqDone = true
	w.cbOff, w.crOff = 0, 0
	if flag {
		w.cbOff, w.crOff = list[idx], w.pps.Range.CrQPOffsetList[idx]
	}
}

func (w *walkWriter) writeCrossComp(c int) int32 {
	v := w.rng.IntN(5)
	for i := 0; i < v; i++ {
		w.bin(cabac.CtxLog2ResScaleAbsPlus1+4*c+i, 1)
	}
	if v < 4 {
		w.bin(cabac.CtxLog2ResScaleAbsPlus1+4*c+v, 0)
	}
	if v == 0 {
		return 0
	}
	neg := w.rng.IntN(2) == 0
	w.bin(cabac.CtxResScaleSign+c, b2n(neg))
	val := int32(1) << uint(v-1)
	if neg {
		val = -val
	}
	return val
}

// writeBlock writes residual_coding( ) of one block and returns what the decoder must read.
func (w *walkWriter) writeBlock(cu *CodingUnit, tools *ResidualTools, x, y, log2, cIdx, mode int) TUBlock {
	in := ResidualInput{Log2Size: log2, CIdx: cIdx, Intra: cu.Mode == ModeIntra, PredModeIntra: mode, CuTransquantBypass: cu.TransquantBypass}
	b := randomBlock(w.rng, 1<<log2, tools, cIdx)
	rw := &resWriter{e: w.e, m: w.m, t: tools}
	rw.write(in, b)
	w.m = rw.m
	return TUBlock{X: x, Y: y, Block: ResidualBlock{Log2Size: log2, CIdx: cIdx, TransformSkip: b.ts,
		ExplicitRDPCM: b.rdpcm, ExplicitRDPCMDir: b.rdpcmD, Levels: b.levels}}
}

func (w *walkWriter) writeTree(cu *CodingUnit, x0, y0, xBase, yBase, log2, depth, blkIdx int, parCb, parCr [2]bool) {
	s := &w.sps
	cat := int(s.ChromaFormat)
	split := false
	canCode := log2 <= int(s.Log2MaxTbSize) && log2 > int(s.Log2MinTbSize) && depth < cu.MaxTrafoDepth && !(cu.IntraSplit && depth == 0)
	if canCode {
		split = w.rng.IntN(2) == 0
		w.bin(cabac.CtxSplitTransform+5-log2, b2n(split))
	} else {
		interSplit := s.MaxTransformHierarchyDepthInter == 0 && cu.Mode == ModeInter && cu.Part != Part2Nx2N && depth == 0
		split = log2 > int(s.Log2MaxTbSize) || (cu.IntraSplit && depth == 0) || interSplit
	}
	var cb, cr [2]bool
	if (log2 > 2 && cat != 0) || cat == 3 {
		for c, par := range [2][2]bool{parCb, parCr} {
			f := &cb
			if c == 1 {
				f = &cr
			}
			if depth == 0 || par[0] {
				f[0] = w.rng.IntN(2) == 0
				w.bin(cabac.CtxCbfChroma+depth, b2n(f[0]))
				if cat == 2 && (!split || log2 == 3) {
					f[1] = w.rng.IntN(2) == 0
					w.bin(cabac.CtxCbfChroma+depth, b2n(f[1]))
				}
			}
		}
	}
	if split {
		h := 1 << (log2 - 1)
		w.writeTree(cu, x0, y0, x0, y0, log2-1, depth+1, 0, cb, cr)
		w.writeTree(cu, x0+h, y0, x0, y0, log2-1, depth+1, 1, cb, cr)
		w.writeTree(cu, x0, y0+h, x0, y0, log2-1, depth+1, 2, cb, cr)
		w.writeTree(cu, x0+h, y0+h, x0, y0, log2-1, depth+1, 3, cb, cr)
		return
	}
	cbfLuma := true
	if cu.Mode == ModeIntra || depth != 0 || cb[0] || cr[0] || (cat == 2 && (cb[1] || cr[1])) {
		cbfLuma = w.rng.IntN(3) != 0
		ctx := 0
		if depth == 0 {
			ctx = 1
		}
		w.bin(cabac.CtxCbfLuma+ctx, b2n(cbfLuma))
	}
	tu := TransformUnit{X0: x0, Y0: y0, XBase: xBase, YBase: yBase, Log2Size: log2, Depth: depth, BlkIdx: blkIdx, CbfLuma: cbfLuma}
	ucb, ucr := cb, cr
	if cat != 3 && log2 == 2 {
		ucb, ucr = parCb, parCr
	}
	if cat == 0 {
		ucb, ucr = [2]bool{}, [2]bool{}
	}
	tu.CbfCb, tu.CbfCr = ucb, ucr
	cbfChroma := ucb[0] || ucr[0] || ucb[1] || ucr[1]
	if cbfLuma || cbfChroma {
		w.writeDeltaQP()
		if cbfChroma && !cu.TransquantBypass {
			w.writeChromaQPOffset()
		}
		tools := w.tools()
		part := 0
		if cu.IntraSplit {
			part = b2n(x0-cu.X >= 1<<(cu.Log2Size-1)) + 2*b2n(y0-cu.Y >= 1<<(cu.Log2Size-1))
		}
		log2C := max(2, log2-b2n(cat != 3))
		tIdxs := 1
		if cat == 2 {
			tIdxs = 2
		}
		if cbfLuma {
			tu.Blocks = append(tu.Blocks, w.writeBlock(cu, tools, x0, y0, log2, 0, int(cu.IntraPredModeY[part])))
		}
		if log2 > 2 || cat == 3 {
			for c := 1; c <= 2; c++ {
				if w.pps.Range.CrossComponentPrediction && cbfLuma && (cu.Mode != ModeIntra || cu.IntraChromaPredMode[part] == 4) {
					tu.ResScaleVal[c-1] = w.writeCrossComp(c - 1)
				}
				flags := ucb
				if c == 2 {
					flags = ucr
				}
				for t := 0; t < tIdxs; t++ {
					if flags[t] {
						tu.Blocks = append(tu.Blocks, w.writeBlock(cu, tools, x0, y0+(t<<log2C), log2C, c, int(cu.IntraPredModeC[part])))
					}
				}
			}
		} else if blkIdx == 3 {
			for c := 1; c <= 2; c++ {
				flags := parCb
				if c == 2 {
					flags = parCr
				}
				for t := 0; t < tIdxs; t++ {
					if flags[t] {
						tu.Blocks = append(tu.Blocks, w.writeBlock(cu, tools, xBase, yBase+(t<<log2C), log2, c, int(cu.IntraPredModeC[0])))
					}
				}
			}
		}
	}
	tu.QpDelta, tu.CuQpOffsetCb, tu.CuQpOffsetCr = w.cuQpDeltaVal, w.cbOff, w.crOff
	tu.QpY = w.qpYw()
	tu.QpPrime[0] = tu.QpY + 6*(int32(w.sps.BitDepthLuma)-8)
	if cat != 0 {
		tu.QpPrime[1], tu.QpPrime[2] = w.qpChroma(1, tu.QpY), w.qpChroma(2, tu.QpY)
	}
	w.expectTU = append(w.expectTU, tu)
	w.tuCU = append(w.tuCU, len(w.expect)-1)
}

// ttCase is a cuCase whose coding units carry a real transform tree.
func ttCase(t *testing.T, rng *rand.Rand) (*walkWriter, *PictureState, []segPlan) {
	t.Helper()
	ww, ps, plans := cuCase(t, rng)
	s, p := &ww.sps, &ww.pps
	span := int(s.Log2CtbSize) - int(s.Log2MinTbSize)
	s.MaxTransformHierarchyDepthInter, s.MaxTransformHierarchyDepthIntra = uint8(rng.IntN(span+1)), uint8(rng.IntN(span+1))
	s.Range.TransformSkipContext, s.Range.ImplicitRDPCM, s.Range.ExplicitRDPCM = rng.IntN(2) == 0, rng.IntN(2) == 0, rng.IntN(2) == 0
	s.Range.PersistentRiceAdaptation, s.Range.ExtendedPrecisionProcessing = rng.IntN(2) == 0, rng.IntN(4) == 0
	p.SignDataHiding, p.TransformSkip = rng.IntN(2) == 0, rng.IntN(2) == 0
	p.Range.Log2MaxTransformSkipSize = uint8(2 + rng.IntN(int(s.Log2MaxTbSize)-1))
	p.Range.CrossComponentPrediction = s.ChromaFormat == 3 && rng.IntN(2) == 0
	if s.ChromaFormat != 0 && rng.IntN(2) == 0 {
		n := 1 + rng.IntN(6)
		p.Range.ChromaQPOffsetListEnabled = true
		p.Range.CbQPOffsetList, p.Range.CrQPOffsetList = nil, nil
		for i := 0; i < n; i++ {
			p.Range.CbQPOffsetList = append(p.Range.CbQPOffsetList, int32(rng.IntN(25)-12))
			p.Range.CrQPOffsetList = append(p.Range.CrQPOffsetList, int32(rng.IntN(25)-12))
		}
	}
	if len(p.Range.CbQPOffsetList) == 0 { // a slice may not enable the offsets without a list
		for i := range plans {
			plans[i].hdr.CuChromaQPOffsetEnabled = false
		}
	}
	ps.SPS = ww.sps
	ww.fullTree = true
	return ww, ps, plans
}

func copyTU(tu *TransformUnit) TransformUnit {
	out := *tu
	out.Blocks = nil
	for _, b := range tu.Blocks {
		b.Block.Levels = slices.Clone(b.Block.Levels)
		out.Blocks = append(out.Blocks, b)
	}
	return out
}

func TestTransformTree(t *testing.T) {
	rng := rand.New(rand.NewPCG(81, 82))
	var tus, withChroma, cross, qpd, cqo, small, c422 int
	for iter := 0; iter < 500; iter++ {
		ww, ps, plans := ttCase(t, rng)
		var got []CodingUnit
		var gotTU []TransformUnit
		dec := CodingUnitDecoder(TransformTreeDecoder(func(w *SliceWalker, cu *CodingUnit, tu *TransformUnit) error {
			gotTU = append(gotTU, copyTU(tu))
			return nil
		}), func(cu *CodingUnit) { got = append(got, *cu) })
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, dec); err != nil {
				t.Fatalf("iter %d segment %d of %d: %v", iter, si, len(plans), err)
			}
		}
		if !reflect.DeepEqual(got, ww.expect) {
			for i := range got {
				if i >= len(ww.expect) || !reflect.DeepEqual(got[i], ww.expect[i]) {
					t.Fatalf("iter %d: coding unit %d is\n%+v\nwritten\n%+v", iter, i, got[i], ww.expect[min(i, len(ww.expect)-1)])
				}
			}
			t.Fatalf("iter %d: %d coding units, written %d", iter, len(got), len(ww.expect))
		}
		if len(gotTU) != len(ww.expectTU) {
			t.Fatalf("iter %d: %d transform units, written %d", iter, len(gotTU), len(ww.expectTU))
		}
		for i := range gotTU {
			if !reflect.DeepEqual(gotTU[i], ww.expectTU[i]) {
				t.Fatalf("iter %d: transform unit %d is\n%+v\nwritten\n%+v", iter, i, gotTU[i], ww.expectTU[i])
			}
			u := gotTU[i]
			tus++
			for _, b := range u.Blocks {
				if b.Block.CIdx > 0 {
					withChroma++
					break
				}
			}
			if u.ResScaleVal != [2]int32{} {
				cross++
			}
			if u.QpDelta != 0 {
				qpd++
			}
			if u.CuQpOffsetCb != 0 || u.CuQpOffsetCr != 0 {
				cqo++
			}
			if u.Log2Size == 2 && ww.sps.ChromaFormat != 3 && u.BlkIdx == 3 {
				small++
			}
			if ww.sps.ChromaFormat == 2 && (u.CbfCb[1] || u.CbfCr[1]) {
				c422++
			}
		}
		if ps.Layout == nil || len(ww.treeBits) != 0 {
			t.Fatalf("iter %d: stub transform tree bins written", iter)
		}
	}
	t.Logf("transform units %d, with chroma blocks %d, cross-component %d, qp delta %d, chroma qp offset %d, 4x4 chroma %d, 4:2:2 second blocks %d",
		tus, withChroma, cross, qpd, cqo, small, c422)
	if withChroma == 0 || cross == 0 || qpd == 0 || cqo == 0 || small == 0 || c422 == 0 {
		t.Error("a transform unit case was never exercised")
	}
}

func TestChromaModeTables(t *testing.T) {
	// Table 8-3 as the decoder holds it against the two rows as the test holds them.
	for idx := 0; idx < 35; idx++ {
		if got, want := int(mode422[idx]), refChromaMode(4, idx, 2); got != want {
			t.Errorf("modeIdx %d: %d, want %d", idx, got, want)
		}
	}
	for chroma := uint8(0); chroma < 5; chroma++ {
		for luma := uint8(0); luma < 35; luma++ {
			if got, want := int(chromaModeIdx(chroma, luma)), refChromaMode(int(chroma), int(luma), 1); got != want {
				t.Errorf("chroma %d luma %d: %d, want %d", chroma, luma, got, want)
			}
		}
	}
}

func TestTransformTreeRefusals(t *testing.T) {
	boom := errors.New("boom")
	var ww *walkWriter
	var ps *PictureState
	var payload []byte
	var hdr SliceHeader
	for seed := uint64(1); ; seed++ {
		var plans []segPlan
		ww, ps, plans = ttCase(t, rand.New(rand.NewPCG(seed, seed+3)))
		payload, hdr = ww.writeSegment(plans[0])
		if len(ww.expectTU) > 0 {
			break
		}
	}
	fresh := func() *PictureState { return NewPictureState(ps.SPS, ps.Layout) }
	dec := CodingUnitDecoder(TransformTreeDecoder(func(*SliceWalker, *CodingUnit, *TransformUnit) error { return boom }), nil)
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, hdr, ww.pps, dec); !errors.Is(err, boom) {
		t.Errorf("a failing transform unit callback: %v", err)
	}
	// With nil callbacks the stream still decodes.
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, hdr, ww.pps, CodingUnitDecoder(TransformTreeDecoder(nil), nil)); err != nil {
		t.Errorf("no callback: %v", err)
	}
}

// Flipping bits in a valid stream reaches the refusals of the transform tree: a
// residual that does not decode, an impossible cu_qp_delta, and none of it may
// panic or end other than in an error of this package or in success.
func TestTransformTreeCorruptStreams(t *testing.T) {
	rng := rand.New(rand.NewPCG(91, 92))
	msgs := map[string]int{}
	for iter := 0; iter < 400; iter++ {
		ww, ps, plans := ttCase(t, rng)
		payload, hdr := ww.writeSegment(plans[0])
		for k := 0; k < 40; k++ {
			bad := slices.Clone(payload)
			for f := 0; f < 1+rng.IntN(3); f++ {
				bad[rng.IntN(len(bad))] ^= 1 << uint(rng.IntN(8))
			}
			dec := CodingUnitDecoder(TransformTreeDecoder(nil), nil)
			err := DecodeSliceData(NewPictureState(ps.SPS, ps.Layout), Unit{Payload: bad}, hdr, ww.pps, dec)
			switch {
			case err == nil:
			case errors.Is(err, ErrSliceData), errors.Is(err, ErrResidual):
				msgs[err.Error()[:min(len(err.Error()), 60)]]++
			default:
				t.Fatalf("iter %d: %v", iter, err)
			}
		}
	}
	t.Logf("%d kinds of refusal", len(msgs))
}

func TestChromaQPOffsetWithoutList(t *testing.T) {
	for seed := uint64(1); seed < 300; seed++ {
		ww, ps, plans := ttCase(t, rand.New(rand.NewPCG(seed, seed+5)))
		if len(ww.pps.Range.CbQPOffsetList) == 0 {
			continue
		}
		payload, hdr := ww.writeSegment(plans[0])
		if !hdr.CuChromaQPOffsetEnabled {
			continue
		}
		pps := ww.pps
		pps.Range.CbQPOffsetList, pps.Range.CrQPOffsetList = nil, nil
		err := DecodeSliceData(NewPictureState(ps.SPS, ps.Layout), Unit{Payload: payload}, hdr, pps, CodingUnitDecoder(TransformTreeDecoder(nil), nil))
		if sliceErrHas(err, "no chroma QP offset list") {
			return
		}
	}
	t.Error("a chroma QP offset was never read")
}

// A transform unit callback that fails at the k-th unit fails the slice, whichever
// level of the tree the unit is at.
func TestTransformTreeFailsAtEveryUnit(t *testing.T) {
	boom := errors.New("boom")
	for seed := uint64(1); seed < 15; seed++ {
		ww, ps, plans := ttCase(t, rand.New(rand.NewPCG(seed, seed+7)))
		payload, hdr := ww.writeSegment(plans[0])
		for k := 0; ; k++ {
			n := 0
			fn := func(*SliceWalker, *CodingUnit, *TransformUnit) error {
				if n == k {
					return boom
				}
				n++
				return nil
			}
			err := DecodeSliceData(NewPictureState(ps.SPS, ps.Layout), Unit{Payload: payload}, hdr, ww.pps, CodingUnitDecoder(TransformTreeDecoder(fn), nil))
			if err == nil {
				break
			}
			if !errors.Is(err, boom) {
				t.Fatalf("seed %d unit %d: %v", seed, k, err)
			}
		}
	}
}

// cu_qp_delta_abs whose Exp-Golomb suffix never ends is refused.
func TestDeltaQPSuffixRefused(t *testing.T) {
	var m cabac.Model
	m.Init(0, 26)
	enc := cabactest.New()
	em := m
	for i := 0; i < 5; i++ {
		enc.Decision(&em.Ctx[cabac.CtxCuQpDeltaAbs+min(i, 1)], 1)
	}
	for i := 0; i < 40; i++ {
		enc.Bypass(1)
	}
	enc.Terminate(1)
	w := &SliceWalker{M: m}
	w.PPS.CUQPDeltaEnabled = true
	if err := w.D.Init(enc.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := w.deltaQP(); !errors.Is(err, ErrSliceData) {
		t.Errorf("an endless cu_qp_delta_abs: %v", err)
	}
}

// The writer's quantization parameters (8.6.1). qPY_PRED is taken from the groups
// to the left and above only when they are in the same coding tree block, which is
// decided here by comparing block coordinates, not CTB addresses.
func (w *walkWriter) startQG(xQg, yQg int) {
	ctb := uint(w.sps.Log2CtbSize)
	nb := func(xn, yn int) int32 {
		sameCTB := xn>>ctb == xQg>>ctb && yn>>ctb == yQg>>ctb
		if !sameCTB || !w.avail([2]int{xQg, yQg}, [2]int{xn, yn}) {
			return w.lastQp
		}
		return w.qpMap[w.cell(xn, yn)]
	}
	w.qpPred = (nb(xQg-1, yQg) + nb(xQg, yQg-1) + 1) >> 1 // an arithmetic shift: the sum may be negative
}

func (w *walkWriter) qpYw() int32 {
	off := 6 * (int32(w.sps.BitDepthLuma) - 8)
	v := w.qpPred + w.cuQpDeltaVal + 52 + 2*off
	return v%(52+off) - off
}

// qpChroma is Qp'Cb or Qp'Cr for the component c (1 or 2), Table 8-10 as ranges.
func (w *walkWriter) qpChroma(c int, qpY int32) int32 {
	off := 6 * (int32(w.sps.BitDepthChroma) - 8)
	delta := w.pps.CbQPOffset + w.hdr.CbQPOffset + w.cbOff
	if c == 2 {
		delta = w.pps.CrQPOffset + w.hdr.CrQPOffset + w.crOff
	}
	qPi := qpY + delta
	if qPi < -off {
		qPi = -off
	}
	if qPi > 57 {
		qPi = 57
	}
	qPc := qPi
	if w.sps.ChromaFormat == 1 {
		switch {
		case qPi < 30:
		case qPi >= 44:
			qPc = qPi - 6
		default:
			qPc = []int32{29, 30, 31, 32, 33, 33, 34, 34, 35, 35, 36, 36, 37, 37}[qPi-30]
		}
	} else if qPc > 51 {
		qPc = 51
	}
	return qPc + off
}

func (w *walkWriter) endCU(x0, y0, log2 int) {
	q := w.qpYw()
	w.lastQp = q
	n := 1 << log2
	for y := y0; y < min(y0+n, int(w.sps.CodedHeight)); y += 1 << w.sps.Log2MinCbSize {
		for x := x0; x < min(x0+n, int(w.sps.CodedWidth)); x += 1 << w.sps.Log2MinCbSize {
			w.qpMap[w.cell(x, y)] = q
		}
	}
}
