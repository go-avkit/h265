// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"

	"github.com/go-avkit/h265/internal/cabac"
)

// TUBlock is one residual_coding( ) call of a transform unit: the location the
// syntax gives it and the levels read. Levels belong to the walker and are valid
// until the callback of the transform unit returns.
type TUBlock struct {
	X, Y  int
	Block ResidualBlock
}

// TransformUnit is transform_unit( ) (7.3.8.10) as it was read.
type TransformUnit struct {
	X0, Y0, XBase, YBase int
	Log2Size, Depth      int
	BlkIdx               int
	// CbfLuma is cbf_luma; CbfCb and CbfCr are the chroma flags this unit
	// uses ([ 1 ] is the second block of 4:2:2), the parent's for a 4x4 luma
	// block of 4:2:0 and 4:2:2.
	CbfLuma      bool
	CbfCb, CbfCr [2]bool
	// QpDelta is CuQpDeltaVal and CuQpOffsetCb and CuQpOffsetCr the chroma
	// offsets in force when the unit was read.
	QpDelta                    int32
	CuQpOffsetCb, CuQpOffsetCr int32
	// QpY is QpY of the coding unit (8.6.1) and QpPrime Qp'Y, Qp'Cb and Qp'Cr, the
	// qP the scaling of the unit's blocks uses; the chroma ones are 0 without chroma.
	QpY     int32
	QpPrime [3]int32
	// ResScaleVal is ResScaleVal[ 1 ] and [ 2 ] of cross-component prediction
	// (7-79, 7-80), 0 when absent.
	ResScaleVal [2]int32
	// Blocks are the residual blocks in the order they were read.
	Blocks []TUBlock
}

// TransformUnitFunc receives each transform unit once it has been read.
type TransformUnitFunc func(w *SliceWalker, cu *CodingUnit, tu *TransformUnit) error

// TransformTreeDecoder is transform_tree( ) (7.3.8.8) with transform_unit( ),
// delta_qp( ), chroma_qp_offset( ) and cross_comp_pred( ), as the
// TransformTreeFunc that CodingUnitDecoder takes. tu may be nil.
func TransformTreeDecoder(tu TransformUnitFunc) TransformTreeFunc {
	return func(w *SliceWalker, cu *CodingUnit) error {
		return w.transformTree(cu, tu, cu.X, cu.Y, cu.X, cu.Y, cu.Log2Size, 0, 0, [2]bool{}, [2]bool{})
	}
}

// residualTools is what residual_coding( ) reads from the parameter sets.
func (w *SliceWalker) residualTools() *ResidualTools {
	s, x := &w.SPS, &w.SPS.Range
	return &ResidualTools{
		ChromaArrayType:          w.chroma,
		SignDataHiding:           w.PPS.SignDataHiding,
		TransformSkip:            w.PPS.TransformSkip,
		Log2MaxTransformSkipSize: int(w.PPS.Range.Log2MaxTransformSkipSize),
		TransformSkipContext:     x.TransformSkipContext,
		ImplicitRDPCM:            x.ImplicitRDPCM,
		ExplicitRDPCM:            x.ExplicitRDPCM,
		PersistentRiceAdaptation: x.PersistentRiceAdaptation,
		ExtendedPrecision:        x.ExtendedPrecisionProcessing,
		BitDepthY:                int(s.BitDepthLuma),
		BitDepthC:                int(s.BitDepthChroma),
	}
}

// transformTree is transform_tree( x0, y0, xBase, yBase, log2TrafoSize,
// trafoDepth, blkIdx ); parentCb and parentCr are the parent's cbf_cb and cbf_cr.
func (w *SliceWalker) transformTree(cu *CodingUnit, fn TransformUnitFunc, x0, y0, xBase, yBase, log2, depth, blkIdx int, parentCb, parentCr [2]bool) error {
	s := &w.SPS
	cat := w.chroma
	var split bool
	if log2 <= int(s.Log2MaxTbSize) && log2 > int(s.Log2MinTbSize) && depth < cu.MaxTrafoDepth && !(cu.IntraSplit && depth == 0) {
		split = w.decision(cabac.CtxSplitTransform + 5 - log2)
	} else {
		// 7.4.9.8: inferred
		interSplit := s.MaxTransformHierarchyDepthInter == 0 && cu.Mode == ModeInter && cu.Part != Part2Nx2N && depth == 0
		split = log2 > int(s.Log2MaxTbSize) || (cu.IntraSplit && depth == 0) || interSplit
	}
	var cb, cr [2]bool
	if (log2 > 2 && cat != 0) || cat == 3 {
		second := cat == 2 && (!split || log2 == 3)
		if depth == 0 || parentCb[0] {
			cb[0] = w.decision(cabac.CtxCbfChroma + depth)
			if second {
				cb[1] = w.decision(cabac.CtxCbfChroma + depth)
			}
		}
		if depth == 0 || parentCr[0] {
			cr[0] = w.decision(cabac.CtxCbfChroma + depth)
			if second {
				cr[1] = w.decision(cabac.CtxCbfChroma + depth)
			}
		}
	}
	if split {
		h := 1 << (log2 - 1)
		x1, y1 := x0+h, y0+h
		for i, p := range [4][2]int{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}} {
			if err := w.transformTree(cu, fn, p[0], p[1], x0, y0, log2-1, depth+1, i, cb, cr); err != nil {
				return err
			}
		}
		return nil
	}
	cbfLuma := true
	if cu.Mode == ModeIntra || depth != 0 || cb[0] || cr[0] || (cat == 2 && (cb[1] || cr[1])) {
		inc := 0
		if depth == 0 {
			inc = 1
		}
		cbfLuma = w.decision(cabac.CtxCbfLuma + inc)
	}
	return w.transformUnit(cu, fn, x0, y0, xBase, yBase, log2, depth, blkIdx, cbfLuma, cb, cr, parentCb, parentCr)
}

// partOf is the index of the prediction block of an intra coding unit that
// covers ( x, y ): 0 unless the unit is split into four.
func (cu *CodingUnit) partOf(x, y int) int {
	if !cu.IntraSplit {
		return 0
	}
	h := 1 << (cu.Log2Size - 1)
	p := 0
	if x-cu.X >= h {
		p++
	}
	if y-cu.Y >= h {
		p += 2
	}
	return p
}

// transformUnit is transform_unit( ) (7.3.8.10) without the adaptive colour
// transform of the screen content extensions.
func (w *SliceWalker) transformUnit(cu *CodingUnit, fn TransformUnitFunc, x0, y0, xBase, yBase, log2, depth, blkIdx int, cbfLuma bool, cb, cr, parentCb, parentCr [2]bool) error {
	cat := w.chroma
	log2C := max(2, log2-bit(cat != 3))
	small := cat != 3 && log2 == 2 // 4x4 luma blocks share the chroma blocks of their parent
	if small {
		cb, cr = parentCb, parentCr
	}
	w.markTB(cu, x0, y0, log2)
	if cbfLuma {
		w.setCbf(x0, y0, log2)
	}
	tu := TransformUnit{X0: x0, Y0: y0, XBase: xBase, YBase: yBase, Log2Size: log2, Depth: depth, BlkIdx: blkIdx,
		CbfLuma: cbfLuma, CbfCb: cb, CbfCr: cr, Blocks: w.tuBlocks[:0]}
	cbfChroma := cb[0] || cr[0] || (cat == 2 && (cb[1] || cr[1]))
	if cat == 0 {
		cbfChroma = false
	}
	if cbfLuma || cbfChroma {
		if err := w.deltaQP(); err != nil {
			return err
		}
		if cbfChroma && !cu.TransquantBypass {
			if err := w.chromaQPOffset(); err != nil {
				return err
			}
		}
		tools := w.residualTools()
		part := cu.partOf(x0, y0)
		intra := cu.Mode == ModeIntra
		used := 0
		read := func(x, y, size, cIdx int, mode int) error {
			blk := TUBlock{X: x, Y: y}
			blk.Block.Levels = w.levels[used][:1<<(2*size)]
			in := ResidualInput{Log2Size: size, CIdx: cIdx, Intra: intra, PredModeIntra: mode, CuTransquantBypass: cu.TransquantBypass}
			if err := DecodeResidual(&w.D, &w.M, tools, in, &blk.Block); err != nil {
				return err
			}
			used++
			tu.Blocks = append(tu.Blocks, blk)
			return nil
		}
		if cbfLuma {
			if err := read(x0, y0, log2, 0, int(cu.IntraPredModeY[part])); err != nil {
				return err
			}
		}
		nBlk := 1
		if cat == 2 {
			nBlk = 2
		}
		if log2 > 2 || cat == 3 {
			cross := w.PPS.Range.CrossComponentPrediction && cbfLuma && (cu.Mode != ModeIntra || cu.IntraChromaPredMode[part] == 4)
			for c := 1; c <= 2; c++ {
				if cross {
					tu.ResScaleVal[c-1] = w.crossCompPred(c - 1)
				}
				flags := cb
				if c == 2 {
					flags = cr
				}
				for t := 0; t < nBlk; t++ {
					if flags[t] {
						if err := read(x0, y0+(t<<log2C), log2C, c, int(cu.IntraPredModeC[part])); err != nil {
							return err
						}
					}
				}
			}
		} else if blkIdx == 3 {
			for c := 1; c <= 2; c++ {
				flags := cb
				if c == 2 {
					flags = cr
				}
				for t := 0; t < nBlk; t++ {
					if flags[t] {
						if err := read(xBase, yBase+(t<<log2C), log2, c, int(cu.IntraPredModeC[0])); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	tu.QpDelta, tu.CuQpOffsetCb, tu.CuQpOffsetCr = w.CuQpDeltaVal, w.CuQpOffsetCb, w.CuQpOffsetCr
	tu.QpY = w.qpY()
	tu.QpPrime[0] = tu.QpY + w.qpBdOffsetY()
	if cat != 0 {
		tu.QpPrime[1], tu.QpPrime[2] = w.chromaQP(tu.QpY, 1), w.chromaQP(tu.QpY, 2)
	}
	if fn != nil {
		return fn(w, cu, &tu)
	}
	return nil
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

// deltaQP is delta_qp( ) (7.3.8.14); CuQpDeltaVal is limited by 7.4.9.14.
func (w *SliceWalker) deltaQP() error {
	if !w.PPS.CUQPDeltaEnabled || w.IsCuQpDeltaCoded {
		return nil
	}
	w.IsCuQpDeltaCoded = true
	abs, err := w.D.CuQpDeltaAbs(&w.M)
	if err != nil {
		return fmt.Errorf("%w: cu_qp_delta_abs: %w", ErrSliceData, err)
	}
	v := int64(abs)
	if abs != 0 && w.D.Bypass() == 1 {
		v = -v
	}
	qpBd := int64(6 * (int(w.SPS.BitDepthLuma) - 8))
	if v < -(26+qpBd/2) || v > 25+qpBd/2 {
		return fmt.Errorf("%w: CuQpDeltaVal %d", ErrSliceData, v)
	}
	w.CuQpDeltaVal = int32(v)
	return nil
}

// chromaQPOffset is chroma_qp_offset( ) (7.3.8.15) with the semantics of 7.4.9.15.
func (w *SliceWalker) chromaQPOffset() error {
	if !w.Hdr.CuChromaQPOffsetEnabled || w.IsCuChromaQpOffsetCoded {
		return nil
	}
	n := len(w.PPS.Range.CbQPOffsetList)
	if n == 0 {
		return fmt.Errorf("%w: cu_chroma_qp_offset_flag with no chroma QP offset list", ErrSliceData)
	}
	flag := w.decision(cabac.CtxCuChromaQpOffsetFlag)
	idx := 0
	if flag && n > 1 {
		// TR, cMax = chroma_qp_offset_list_len_minus1, every bin on one context.
		for idx < n-1 && w.decision(cabac.CtxCuChromaQpOffsetIdx) {
			idx++
		}
	}
	w.IsCuChromaQpOffsetCoded = true
	w.CuQpOffsetCb, w.CuQpOffsetCr = 0, 0
	if flag {
		w.CuQpOffsetCb, w.CuQpOffsetCr = w.PPS.Range.CbQPOffsetList[idx], w.PPS.Range.CrQPOffsetList[idx]
	}
	return nil
}

// crossCompPred is cross_comp_pred( x0, y0, c ) (7.3.8.12) and returns ResScaleVal
// of 7.4.9.12.
func (w *SliceWalker) crossCompPred(c int) int32 {
	// log2_res_scale_abs_plus1: TR, cMax 4, bin i on context 4 * c + i.
	v := 0
	for v < 4 && w.decision(cabac.CtxLog2ResScaleAbsPlus1+4*c+v) {
		v++
	}
	if v == 0 {
		return 0
	}
	val := int32(1) << uint(v-1)
	if w.decision(cabac.CtxResScaleSign + c) {
		val = -val
	}
	return val
}
