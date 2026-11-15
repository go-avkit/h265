// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"

	"github.com/go-avkit/h265/internal/cabac"
)

// PredMode is CuPredMode (7.4.9.5).
type PredMode uint8

// The prediction modes of a coding unit.
const (
	ModeInter PredMode = iota
	ModeIntra
	ModeSkip
)

// PartMode is PartMode (Table 7-10): how a coding unit divides into prediction
// blocks.
type PartMode uint8

// The partitionings, in the order of part_mode's values.
const (
	Part2Nx2N PartMode = iota
	Part2NxN
	PartNx2N
	PartNxN
	Part2NxnU
	Part2NxnD
	PartnLx2N
	PartnRx2N
)

// InterPredIdc is inter_pred_idc (Table 7-11).
type InterPredIdc uint8

// The reference lists a prediction block uses.
const (
	PredL0 InterPredIdc = iota
	PredL1
	PredBi
)

// PredictionUnit is prediction_unit( ) (7.3.8.6) of one prediction block, in luma
// samples. A merged or skipped block states only its merge index.
type PredictionUnit struct {
	X, Y, W, H int
	Merge      bool
	MergeIdx   uint8
	Idc        InterPredIdc
	RefIdx     [2]uint8
	// Mvd is MvdL0 and MvdL1 ( horizontal, vertical ); MvpFlag mvp_l0_flag and
	// mvp_l1_flag.
	Mvd     [2][2]int32
	MvpFlag [2]uint8
}

// CodingUnit is coding_unit( ) (7.3.8.5) as it was read: the syntax elements, not
// the prediction they lead to.
type CodingUnit struct {
	X, Y, Log2Size   int
	TransquantBypass bool
	Mode             PredMode
	Part             PartMode
	// PCM says the samples are in PcmLuma and PcmChroma (Cb then Cr), as stated,
	// PcmBitDepth bits each.
	PCM                bool
	PcmLuma, PcmChroma []uint16
	// PU holds the NumPU prediction blocks of an inter coding unit.
	NumPU int
	PU    [4]PredictionUnit
	// The intra modes of the one or four prediction blocks, as stated: the
	// derivation of IntraPredModeY (8.4.2) and of the chroma mode (8.4.3) is not
	// made here. IntraChromaPredMode is 0..4, one per block in 4:4:4 and otherwise
	// the first.
	PrevIntraLumaPred    [4]bool
	MpmIdx               [4]uint8
	RemIntraLumaPredMode [4]uint8
	IntraChromaPredMode  [4]uint8
	// RqtRootCbf is rqt_root_cbf (1 when absent); IntraSplit is IntraSplitFlag and
	// MaxTrafoDepth the depth the transform tree may reach.
	RqtRootCbf    bool
	IntraSplit    bool
	MaxTrafoDepth int
}

// TransformTreeFunc decodes transform_tree( ) of a coding unit whose
// rqt_root_cbf is 1.
type TransformTreeFunc func(w *SliceWalker, cu *CodingUnit) error

// CodingUnitDecoder is coding_unit( ) with tree for its transform tree, as the
// CodingUnitFunc that DecodeSliceData takes. When cu is not nil it receives each
// coding unit once its syntax has been read, before the transform tree.
func CodingUnitDecoder(tree TransformTreeFunc, cu func(*CodingUnit)) CodingUnitFunc {
	return func(w *SliceWalker, x0, y0, log2CbSize int) error {
		return w.codingUnit(x0, y0, log2CbSize, tree, cu)
	}
}

func (w *SliceWalker) decision(ctx int) bool { return w.D.Decision(&w.M.Ctx[ctx]) == 1 }

// codingUnit is coding_unit( x0, y0, log2CbSize ) (7.3.8.5) without palette mode,
// which an SPS stating it is refused for.
func (w *SliceWalker) codingUnit(x0, y0, log2CbSize int, tree TransformTreeFunc, report func(*CodingUnit)) error {
	s := &w.SPS
	cu := &CodingUnit{X: x0, Y: y0, Log2Size: log2CbSize}
	if w.PPS.TransquantBypass {
		cu.TransquantBypass = w.decision(cabac.CtxCUTransquantBypass)
	}
	nCbS := 1 << log2CbSize
	skip := false
	if w.Hdr.Type != SliceI {
		inc := 0
		if w.Available(x0, y0, x0-1, y0) && w.Pic.CuSkip[w.cell(x0-1, y0)] {
			inc++
		}
		if w.Available(x0, y0, x0, y0-1) && w.Pic.CuSkip[w.cell(x0, y0-1)] {
			inc++
		}
		skip = w.decision(cabac.CtxCUSkip + inc)
	}
	w.setSkip(x0, y0, nCbS, skip)
	if skip {
		cu.Mode, cu.Part, cu.NumPU = ModeSkip, Part2Nx2N, 1
		// prediction_unit( ) of a skipped coding unit states only merge_idx.
		cu.PU[0] = PredictionUnit{X: x0, Y: y0, W: nCbS, H: nCbS, Merge: true}
		w.mergeIndex(&cu.PU[0])
		if report != nil {
			report(cu)
		}
		return nil
	}
	cu.Mode = ModeIntra
	if w.Hdr.Type != SliceI && !w.decision(cabac.CtxPredMode) {
		cu.Mode = ModeInter
	}
	if cu.Mode != ModeIntra || log2CbSize == int(s.Log2MinCbSize) {
		cu.Part = w.partMode(cu.Mode == ModeIntra, log2CbSize)
	}
	if cu.Mode == ModeIntra {
		cu.IntraSplit = cu.Part == PartNxN
		if cu.Part == Part2Nx2N && s.PCM.Enabled && log2CbSize >= int(s.PCM.Log2MinCbSize) && log2CbSize <= int(s.PCM.Log2MaxCbSize) {
			cu.PCM = w.D.Terminate() == 1
		}
		if cu.PCM {
			if err := w.pcmSamples(cu); err != nil {
				return err
			}
		} else {
			w.intraModes(cu)
		}
	} else {
		if err := w.interUnits(cu, x0, y0, nCbS); err != nil {
			return err
		}
	}
	cu.RqtRootCbf = true
	if !cu.PCM {
		if cu.Mode != ModeIntra && !(cu.Part == Part2Nx2N && cu.PU[0].Merge) {
			cu.RqtRootCbf = w.decision(cabac.CtxRqtRootCbf)
		}
		if cu.RqtRootCbf {
			if cu.Mode == ModeIntra {
				cu.MaxTrafoDepth = int(s.MaxTransformHierarchyDepthIntra)
				if cu.IntraSplit {
					cu.MaxTrafoDepth++
				}
			} else {
				cu.MaxTrafoDepth = int(s.MaxTransformHierarchyDepthInter)
			}
		}
	}
	if report != nil {
		report(cu)
	}
	if !cu.PCM && cu.RqtRootCbf {
		if tree == nil {
			return fmt.Errorf("%w: no transform tree decoder", ErrSliceData)
		}
		return tree(w, cu)
	}
	return nil
}

func (w *SliceWalker) cell(x, y int) int {
	m := uint(w.SPS.Log2MinCbSize)
	return (y>>m)*w.Pic.MinCbWidth + x>>m
}

func (w *SliceWalker) setSkip(x0, y0, size int, skip bool) {
	s := &w.SPS
	m := uint(s.Log2MinCbSize)
	for y := y0 >> m; y < min((y0+size)>>m, (int(s.CodedHeight)+1<<m-1)>>m); y++ {
		for x := x0 >> m; x < min((x0+size)>>m, w.Pic.MinCbWidth); x++ {
			w.Pic.CuSkip[y*w.Pic.MinCbWidth+x] = skip
		}
	}
}

// partMode reads part_mode (Table 9-45; context increments of Table 9-48).
func (w *SliceWalker) partMode(intra bool, log2CbSize int) PartMode {
	if w.decision(cabac.CtxPartMode) {
		return Part2Nx2N
	}
	if intra {
		return PartNxN
	}
	if log2CbSize == int(w.SPS.Log2MinCbSize) {
		if w.decision(cabac.CtxPartMode + 1) {
			return Part2NxN
		}
		if log2CbSize == 3 {
			return PartNx2N
		}
		if w.decision(cabac.CtxPartMode + 2) {
			return PartNx2N
		}
		return PartNxN
	}
	horizontal := w.decision(cabac.CtxPartMode + 1)
	if !w.SPS.AMPEnabled {
		if horizontal {
			return Part2NxN
		}
		return PartNx2N
	}
	if w.decision(cabac.CtxPartMode + 3) {
		if horizontal {
			return Part2NxN
		}
		return PartNx2N
	}
	second := w.D.Bypass() == 1
	switch {
	case horizontal && second:
		return Part2NxnD
	case horizontal:
		return Part2NxnU
	case second:
		return PartnRx2N
	}
	return PartnLx2N
}

// intraModes reads the intra prediction mode syntax of a coding unit that is not
// PCM (7.3.8.5).
func (w *SliceWalker) intraModes(cu *CodingUnit) {
	n := 1
	if cu.Part == PartNxN {
		n = 4
	}
	for i := 0; i < n; i++ {
		cu.PrevIntraLumaPred[i] = w.decision(cabac.CtxPrevIntraLumaPred)
	}
	for i := 0; i < n; i++ {
		if cu.PrevIntraLumaPred[i] {
			cu.MpmIdx[i] = uint8(w.D.TruncatedUnaryBypass(2))
		} else {
			cu.RemIntraLumaPredMode[i] = uint8(w.D.FixedLengthBypass(5))
		}
	}
	switch w.chroma {
	case 0:
	case 3:
		for i := 0; i < n; i++ {
			cu.IntraChromaPredMode[i] = w.intraChromaPredMode()
		}
	default:
		cu.IntraChromaPredMode[0] = w.intraChromaPredMode()
	}
}

// intraChromaPredMode reads intra_chroma_pred_mode (Table 9-46).
func (w *SliceWalker) intraChromaPredMode() uint8 {
	if !w.decision(cabac.CtxIntraChromaPredMode) {
		return 4
	}
	return uint8(w.D.FixedLengthBypass(2))
}

// pcmSamples reads pcm_alignment_zero_bit and pcm_sample( ) (7.3.8.7), then
// restarts the engine after them (9.3.2.5).
func (w *SliceWalker) pcmSamples(cu *CodingUnit) error {
	s := &w.SPS
	// 1 << (log2 << 1) is the sample count too, but on ppc64le it came out as 0
	// here (CI, qemu), so the size is squared instead.
	side := 1 << uint(cu.Log2Size)
	lumaN := side * side
	chromaN := 0
	if w.chroma != 0 {
		subW, subH := 2, 2
		switch w.chroma {
		case 2:
			subH = 1
		case 3:
			subW, subH = 1, 1
		}
		chromaN = 2 * lumaN / (subW * subH)
	}
	bdL, bdC := int(s.PCM.BitDepthLuma), int(s.PCM.BitDepthChroma)
	start := w.pos + int((w.D.BitPos()+7)/8)
	bits := lumaN*bdL + chromaN*bdC
	end := start + (bits+7)/8
	if end > len(w.data) {
		return fmt.Errorf("%w: PCM samples run past the slice data", ErrSliceData)
	}
	bit := start * 8
	read := func(n int) uint16 {
		var v uint16
		for i := 0; i < n; i++ {
			v = v<<1 | uint16(w.data[bit>>3]>>(7-uint(bit&7))&1)
			bit++
		}
		return v
	}
	cu.PcmLuma = make([]uint16, lumaN)
	for i := range cu.PcmLuma {
		cu.PcmLuma[i] = read(bdL)
	}
	cu.PcmChroma = make([]uint16, chromaN)
	for i := range cu.PcmChroma {
		cu.PcmChroma[i] = read(bdC)
	}
	w.pos = end
	if err := w.D.Start(w.data[w.pos:], cabac.Config{}); err != nil {
		return fmt.Errorf("%w: after the PCM samples: %w", ErrSliceData, err)
	}
	return nil
}

// interUnits reads the prediction blocks of an inter coding unit (7.3.8.5).
func (w *SliceWalker) interUnits(cu *CodingUnit, x0, y0, n int) error {
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
		bs = []blk{{0, 0, n, n / 4}, {0, n / 4, n, n * 3 / 4}}
	case Part2NxnD:
		bs = []blk{{0, 0, n, n * 3 / 4}, {0, n * 3 / 4, n, n / 4}}
	case PartnLx2N:
		bs = []blk{{0, 0, n / 4, n}, {n / 4, 0, n * 3 / 4, n}}
	case PartnRx2N:
		bs = []blk{{0, 0, n * 3 / 4, n}, {n * 3 / 4, 0, n / 4, n}}
	default:
		bs = []blk{{0, 0, n / 2, n / 2}, {n / 2, 0, n / 2, n / 2}, {0, n / 2, n / 2, n / 2}, {n / 2, n / 2, n / 2, n / 2}}
	}
	cu.NumPU = len(bs)
	for i, b := range bs {
		if err := w.predictionUnit(cu, &cu.PU[i], x0+b.x, y0+b.y, b.w, b.h); err != nil {
			return err
		}
	}
	return nil
}

// predictionUnit is prediction_unit( x0, y0, nPbW, nPbH ) (7.3.8.6).
func (w *SliceWalker) predictionUnit(cu *CodingUnit, pu *PredictionUnit, x0, y0, nPbW, nPbH int) error {
	*pu = PredictionUnit{X: x0, Y: y0, W: nPbW, H: nPbH}
	if pu.Merge = w.decision(cabac.CtxMergeFlag); pu.Merge {
		w.mergeIndex(pu)
		return nil
	}
	pu.Idc = PredL0
	if w.Hdr.Type == SliceB {
		pu.Idc = w.interPredIdc(x0, y0, nPbW, nPbH)
	}
	for list := 0; list < 2; list++ {
		if (list == 0 && pu.Idc == PredL1) || (list == 1 && pu.Idc == PredL0) {
			continue
		}
		active := w.Hdr.Refs.NumRefIdxL0Active
		if list == 1 {
			active = w.Hdr.Refs.NumRefIdxL1Active
		}
		if active > 1 {
			pu.RefIdx[list] = w.refIdx(int(active) - 1)
		}
		if list == 1 && w.Hdr.Refs.MvdL1Zero && pu.Idc == PredBi {
			pu.Mvd[1] = [2]int32{}
		} else if err := w.mvd(&pu.Mvd[list]); err != nil {
			return err
		}
		if w.decision(cabac.CtxMvpFlag) {
			pu.MvpFlag[list] = 1
		}
	}
	return nil
}

// mergeIndex reads merge_idx when MaxNumMergeCand allows more than one: TR with
// cMax = MaxNumMergeCand - 1, the first bin coded and the others bypass.
func (w *SliceWalker) mergeIndex(pu *PredictionUnit) {
	cMax := int(w.Hdr.Refs.MaxMergeCand) - 1
	if cMax < 1 || !w.decision(cabac.CtxMergeIdx) {
		return
	}
	i := 1
	for i < cMax && w.D.Bypass() == 1 {
		i++
	}
	pu.MergeIdx = uint8(i)
}

// interPredIdc reads inter_pred_idc (Table 9-47, ctxInc of Table 9-48).
func (w *SliceWalker) interPredIdc(x0, y0, nPbW, nPbH int) InterPredIdc {
	if nPbW+nPbH != 12 && w.decision(cabac.CtxInterPredIdc+int(w.ctDepth(x0, y0))) {
		return PredBi
	}
	if w.decision(cabac.CtxInterPredIdc + 4) {
		return PredL1
	}
	return PredL0
}

// refIdx reads ref_idx_l0 or ref_idx_l1: TR with cMax, the first two bins coded
// with contexts 0 and 1, the rest bypass.
func (w *SliceWalker) refIdx(cMax int) uint8 {
	i := 0
	for i < cMax {
		var one bool
		if i < 2 {
			one = w.decision(cabac.CtxRefIdx + i)
		} else {
			one = w.D.Bypass() == 1
		}
		if !one {
			break
		}
		i++
	}
	return uint8(i)
}

// mvd is mvd_coding( ) (7.3.8.9). The values are limited to -2^15..2^15-1 (7.4.9.9).
func (w *SliceWalker) mvd(out *[2]int32) error {
	var g0, g1 [2]bool
	g0[0] = w.decision(cabac.CtxAbsMvdGreater0)
	g0[1] = w.decision(cabac.CtxAbsMvdGreater0)
	for c := 0; c < 2; c++ {
		if g0[c] {
			g1[c] = w.decision(cabac.CtxAbsMvdGreater1)
		}
	}
	for c := 0; c < 2; c++ {
		if !g0[c] {
			out[c] = 0
			continue
		}
		abs := uint32(1)
		if g1[c] {
			v, err := w.D.ExpGolombBypass(1)
			if err != nil {
				return fmt.Errorf("%w: abs_mvd_minus2: %w", ErrSliceData, err)
			}
			if v > 1<<15-2 {
				return fmt.Errorf("%w: a motion vector difference of %d", ErrSliceData, uint64(v)+2)
			}
			abs = v + 2
		}
		neg := w.D.Bypass() == 1
		if neg {
			out[c] = -int32(abs)
		} else {
			if abs > 1<<15-1 {
				return fmt.Errorf("%w: a motion vector difference of %d", ErrSliceData, abs)
			}
			out[c] = int32(abs)
		}
	}
	return nil
}
