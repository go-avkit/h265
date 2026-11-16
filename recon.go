// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// ErrNotReconstructed means a coding unit needs what the reconstruction was not given:
// an inter coding unit without the reference lists of its slice (Reconstructor.Inter).
var ErrNotReconstructed = errors.New("h265: the coding unit needs a process that is not implemented")

// NewDecodedPicture allocates the picture the sequence parameter set describes, a
// coded picture of pic_width_in_luma_samples by pic_height_in_luma_samples.
func NewDecodedPicture(sps SPS) *DecodedPicture {
	p := &DecodedPicture{Components: 3}
	subW, subH := 1, 1
	switch sps.ChromaFormat {
	case 0:
		p.Components = 1
	case 1:
		subW, subH = 2, 2
	case 2:
		subW = 2
	}
	w, h := int(sps.CodedWidth), int(sps.CodedHeight)
	for c := 0; c < p.Components; c++ {
		cw, ch := w, h
		p.BitDepth[c] = int(sps.BitDepthLuma)
		if c > 0 {
			cw, ch = w/subW, h/subH
			p.BitDepth[c] = int(sps.BitDepthChroma)
		}
		p.Planes[c] = Plane{Width: cw, Height: ch, Stride: cw, Pix: make([]uint16, cw*ch)}
	}
	return p
}

// Reconstructor builds a picture, before the in-loop filters, from the coding
// units and transform units a slice walk reports (8.4 and 8.6.7).
type Reconstructor struct {
	Frame *DecodedPicture
	sps   SPS
	pps   PPS
	// factors are the scaling factors of the picture (nil: flat).
	factors *ScalingFactors
	// Inter is the slice being decoded when it is a P or B slice: the lists the inter
	// prediction reads, the same value as PictureState.Inter.
	Inter *InterSlice
	err   error
	// sc is the working memory of the inter prediction of a block.
	sc interpScratch
}

// Err is the first error the prediction of an inter coding unit met. Such a unit is
// predicted when it is reported, which has no way to return one, so the caller
// checks it after the slice.
func (r *Reconstructor) Err() error { return r.err }

// NewReconstructor starts the reconstruction of a picture into frame; the picture
// parameter set is the one its slices use.
func NewReconstructor(frame *DecodedPicture, sps SPS, pps PPS) *Reconstructor {
	return &Reconstructor{Frame: frame, sps: sps, pps: pps, factors: ScalingFactorsOf(sps, pps)}
}

// Decoder is the coding unit decoder of a slice that reconstructs what it reads.
func (r *Reconstructor) Decoder() CodingUnitFunc {
	return CodingUnitDecoder(TransformTreeDecoder(r.Unit), r.CodingUnit)
}

// CodingUnit is called with each coding unit once its syntax is read: a PCM block
// is the reconstruction itself (8-12, 8-15, 8-16).
func (r *Reconstructor) CodingUnit(cu *CodingUnit) {
	if cu.Mode != ModeIntra { // inter and skipped: the prediction (8.5.3.3), the residual follows
		if err := r.predictInter(cu); err != nil && r.err == nil {
			r.err = err
		}
		return
	}
	if !cu.PCM {
		return
	}
	s := &r.sps
	n := 1 << uint(cu.Log2Size)
	luma := &r.Frame.Planes[0]
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			luma.Pix[(cu.Y+j)*luma.Stride+cu.X+i] = cu.PcmLuma[n*j+i] << uint(int(s.BitDepthLuma)-int(s.PCM.BitDepthLuma))
		}
	}
	for c := 1; c < r.Frame.Components; c++ {
		pl := &r.Frame.Planes[c]
		sw, sh := int(s.CodedWidth)/pl.Width, int(s.CodedHeight)/pl.Height
		cw, ch := n/sw, n/sh
		for j := 0; j < ch; j++ {
			for i := 0; i < cw; i++ {
				v := cu.PcmChroma[cw*(j+(c-1)*ch)+i]
				pl.Pix[(cu.Y/sh+j)*pl.Stride+cu.X/sw+i] = v << uint(int(s.BitDepthChroma)-int(s.PCM.BitDepthChroma))
			}
		}
	}
}

// Unit reconstructs the blocks of a transform unit: of an intra coding unit per block
// the intra prediction, the residual, and their sum (8.4.4.1); of an inter one the
// residual (8.5.4) added to the prediction CodingUnit already wrote.
func (r *Reconstructor) Unit(w *SliceWalker, cu *CodingUnit, tu *TransformUnit) error {
	if r.err != nil {
		return r.err
	}
	cat := w.chroma
	intra := cu.Mode == ModeIntra
	var modeY, modeC int
	if intra {
		part := cu.partOf(tu.X0, tu.Y0)
		modeY, modeC = int(cu.IntraPredModeY[part]), int(cu.IntraPredModeC[part])
	}
	var rY []int32 // the luma residual of this unit, for cross-component prediction
	n := 1 << uint(tu.Log2Size)
	res := make([]int32, n*n)
	if tu.CbfLuma {
		if err := r.residual(w, cu, tu, tu.lumaBlock(), 0, modeY, res); err != nil {
			return err
		}
	}
	rY = res
	r.reconstruct(w, cu, 0, tu.X0, tu.Y0, tu.Log2Size, modeY, rY)
	if cat == 0 {
		return nil
	}
	subW, subH := 1, 1
	if cat != 3 {
		subW = 2
		if cat == 1 {
			subH = 2
		}
	}
	var xC, yC, log2C int
	switch {
	case tu.Log2Size > 2 || cat == 3:
		xC, yC, log2C = tu.X0/subW, tu.Y0/subH, max(2, tu.Log2Size-b2i32(cat != 3))
	case tu.BlkIdx == 3:
		xC, yC, log2C = tu.XBase/subW, tu.YBase/subH, 2
	default:
		return nil
	}
	nC := 1 << uint(log2C)
	blocks := 1
	if cat == 2 {
		blocks = 2
	}
	for c := 1; c <= 2; c++ {
		for t := 0; t < blocks; t++ {
			rc := make([]int32, nC*nC)
			if b := tu.chromaBlock(c, t, log2C, cat); b != nil {
				if err := r.residual(w, cu, tu, b, c, modeC, rc); err != nil {
					return err
				}
			}
			if cat == 3 && r.pps.Range.CrossComponentPrediction && tu.ResScaleVal[c-1] != 0 {
				CrossComponent(rc, rY, tu.ResScaleVal[c-1], int(r.sps.BitDepthLuma), int(r.sps.BitDepthChroma))
			}
			r.reconstruct(w, cu, c, xC, yC+t*nC, log2C, modeC, rc)
		}
	}
	return nil
}

func b2i32(b bool) int {
	if b {
		return 1
	}
	return 0
}

// lumaBlock returns the luma block of the unit, which has at most one, or nil.
func (tu *TransformUnit) lumaBlock() *TUBlock {
	for i := range tu.Blocks {
		if tu.Blocks[i].Block.CIdx == 0 {
			return &tu.Blocks[i]
		}
	}
	return nil
}

// chromaBlock returns the t-th ( 0 or 1 ) block of colour component cIdx read in the unit.
func (tu *TransformUnit) chromaBlock(cIdx, t, log2C, chromaArrayType int) *TUBlock {
	base := tu.Y0
	if tu.Log2Size == 2 && chromaArrayType != 3 { // the blocks of the parent's 8x8 area, read with the fourth unit
		base = tu.YBase
	}
	for i := range tu.Blocks {
		b := &tu.Blocks[i]
		if b.Block.CIdx == cIdx && (b.Y-base)>>uint(log2C) == t {
			return b
		}
	}
	return nil
}

// residual is the scaling and transformation process (8.6.2) with the implicit
// RDPCM of 8.4.4.1 for one block of the unit.
func (r *Reconstructor) residual(w *SliceWalker, cu *CodingUnit, tu *TransformUnit, b *TUBlock, cIdx, mode int, out []int32) error {
	if b == nil {
		return fmt.Errorf("h265: a coded block missing from its transform unit")
	}
	log2 := b.Block.Log2Size
	bp := BlockParams{
		Log2Size: log2, CIdx: cIdx, QP: int(tu.QpPrime[cIdx]), BitDepth: int(r.sps.BitDepthLuma),
		ExtendedPrecision: r.sps.Range.ExtendedPrecisionProcessing, TransquantBypass: cu.TransquantBypass,
		TransformSkip: b.Block.TransformSkip, Intra: cu.Mode == ModeIntra, RotateEnabled: r.sps.Range.TransformSkipRotation,
	}
	if cIdx > 0 {
		bp.BitDepth = int(r.sps.BitDepthChroma)
	}
	if r.factors != nil {
		bp.Factor = r.factors[log2-2][cIdx+3*(1-b2i32(bp.Intra))] // matrixId: 3 more for inter (Table 7-4)
	}
	if err := Residual(b.Block.Levels, bp, out); err != nil {
		return err
	}
	switch {
	case bp.Intra && r.sps.Range.ImplicitRDPCM && (b.Block.TransformSkip || cu.TransquantBypass) && (mode == 10 || mode == 26):
		RDPCM(out, 1<<uint(log2), mode/26)
	case !bp.Intra && b.Block.ExplicitRDPCM: // explicit_rdpcm_dir_flag 0 is horizontal
		RDPCM(out, 1<<uint(log2), bit(b.Block.ExplicitRDPCMDir))
	}
	return nil
}

// reconstruct finishes one block of a transform unit: an intra block is predicted and
// gets its residual, an inter block gets it added to the prediction already in the
// picture.
func (r *Reconstructor) reconstruct(w *SliceWalker, cu *CodingUnit, c, x, y, log2, mode int, res []int32) {
	if cu.Mode == ModeIntra {
		r.block(w, cu, c, x, y, log2, mode, res)
		return
	}
	n := 1 << uint(log2)
	pl := &r.Frame.Planes[c]
	bitDepth := int(r.sps.BitDepthLuma)
	if c > 0 {
		bitDepth = int(r.sps.BitDepthChroma)
	}
	maxVal := int32(1)<<uint(bitDepth) - 1
	for j := 0; j < n; j++ {
		row, rr := pl.Pix[(y+j)*pl.Stride+x:(y+j)*pl.Stride+x+n], res[j*n:j*n+n]
		for i := range row {
			row[i] = uint16(min(max(int32(row[i])+rr[i], 0), maxVal))
		}
	}
}

// block predicts the block of component c at ( x, y ) in that component's samples
// (8.4.4.2) and adds the residual (8.6.7).
func (r *Reconstructor) block(w *SliceWalker, cu *CodingUnit, c, x, y, log2, mode int, res []int32) {
	n := 1 << uint(log2)
	pl := &r.Frame.Planes[c]
	subW, subH := 1, 1
	if c > 0 {
		subW, subH = int(r.sps.CodedWidth)/pl.Width, int(r.sps.CodedHeight)/pl.Height
	}
	refs := make([]int32, 4*n+1)
	avail := make([]bool, 4*n+1)
	xTbY, yTbY := x*subW, y*subH
	for i := range refs {
		xn, yn := x-1, y+2*n-1-i
		if i > 2*n {
			xn, yn = x+i-2*n-1, y-1
		}
		xNbY, yNbY := xn*subW, yn*subH
		if !w.Available(xTbY, yTbY, xNbY, yNbY) {
			continue
		}
		if r.pps.ConstrainedIntraPred && w.Pic.CuMode[w.cell(xNbY, yNbY)] != uint8(ModeIntra) {
			continue
		}
		avail[i] = true
		refs[i] = int32(pl.Pix[yn*pl.Stride+xn])
	}
	bitDepth := int(r.sps.BitDepthLuma)
	if c > 0 {
		bitDepth = int(r.sps.BitDepthChroma)
	}
	SubstituteReferences(refs, avail, bitDepth)
	pred := make([]int32, n*n)
	PredictIntra(refs, IntraParams{
		Log2Size: log2, CIdx: c, Mode: mode, BitDepth: bitDepth,
		FilterRefs:       !r.sps.Range.IntraSmoothingDisabled && (c == 0 || w.chroma == 3),
		Strong:           r.sps.StrongIntraSmoothing,
		NoBoundaryFilter: r.sps.Range.ImplicitRDPCM && cu.TransquantBypass,
	}, pred)
	maxVal := int32(1)<<uint(bitDepth) - 1
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			pl.Pix[(y+j)*pl.Stride+x+i] = uint16(min(max(pred[j*n+i]+res[j*n+i], 0), maxVal))
		}
	}
}
