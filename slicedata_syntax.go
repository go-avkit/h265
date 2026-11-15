// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"

	"github.com/go-avkit/h265/internal/cabac"
)

// sao is sao( rx, ry ) (7.3.8.3) of the coding tree block rs, with the
// semantics of 7.4.9.3: a merged block copies its neighbour's parameters, the
// offsets are signed and scaled (7-72), and chroma shares one type and one edge
// class.
func (w *SliceWalker) sao(rs int) {
	l := w.Pic.Layout
	rx, ry := rs%l.WidthCtbs, rs/l.WidthCtbs
	p := &w.Pic.Sao[rs]
	*p = SaoParams{}
	sliceAddr := int(w.Hdr.SliceAddrRs)
	tile := l.TileID[l.RsToTs[rs]]
	mergeLeft, mergeUp := false, false
	if rx > 0 && rs > sliceAddr && tile == l.TileID[l.RsToTs[rs-1]] {
		mergeLeft = w.D.Decision(&w.M.Ctx[cabac.CtxSAOMerge]) == 1
	}
	if ry > 0 && !mergeLeft && rs-l.WidthCtbs >= sliceAddr && tile == l.TileID[l.RsToTs[rs-l.WidthCtbs]] {
		mergeUp = w.D.Decision(&w.M.Ctx[cabac.CtxSAOMerge]) == 1
	}
	switch {
	case mergeLeft:
		*p = w.Pic.Sao[rs-1]
		return
	case mergeUp:
		*p = w.Pic.Sao[rs-l.WidthCtbs]
		return
	}
	comps := 1
	if w.chroma != 0 {
		comps = 3
	}
	for cIdx := 0; cIdx < comps; cIdx++ {
		if !((w.Hdr.Refs.SAOLuma && cIdx == 0) || (w.Hdr.Refs.SAOChroma && cIdx > 0)) {
			continue
		}
		switch cIdx {
		case 0, 1:
			// sao_type_idx_luma / _chroma: TR, cMax 2; the first bin is coded,
			// the second bypass (Table 9-48).
			switch {
			case w.D.Decision(&w.M.Ctx[cabac.CtxSAOTypeIdx]) == 0:
				p.Type[cIdx] = 0
			case w.D.Bypass() == 0:
				p.Type[cIdx] = 1
			default:
				p.Type[cIdx] = 2
			}
		default:
			p.Type[2] = p.Type[1]
		}
		if p.Type[cIdx] == 0 {
			continue
		}
		bitDepth, scale := int(w.SPS.BitDepthLuma), uint(w.PPS.Range.Log2SAOOffsetScaleLuma)
		if cIdx > 0 {
			bitDepth, scale = int(w.SPS.BitDepthChroma), uint(w.PPS.Range.Log2SAOOffsetScaleChroma)
		}
		cMax := uint32(1)<<(min(bitDepth, 10)-5) - 1
		var abs [4]uint32
		for i := range abs {
			abs[i] = w.D.TruncatedUnaryBypass(cMax) // sao_offset_abs
		}
		var neg [4]bool
		if p.Type[cIdx] == 1 { // band offset
			for i := range abs {
				if abs[i] != 0 {
					neg[i] = w.D.Bypass() == 1 // sao_offset_sign
				}
			}
			p.BandPos[cIdx] = uint8(w.D.FixedLengthBypass(5)) // sao_band_position
		} else { // edge offset: the first two offsets raise, the last two lower (7.4.9.3)
			neg = [4]bool{false, false, true, true}
			switch cIdx {
			case 0, 1:
				p.EoClass[cIdx] = uint8(w.D.FixedLengthBypass(2)) // sao_eo_class_luma / _chroma
			default:
				p.EoClass[2] = p.EoClass[1]
			}
		}
		for i := range abs {
			v := int32(abs[i]) << scale
			if neg[i] {
				v = -v
			}
			p.Offset[cIdx][i] = v
		}
	}
}

// quadtree is coding_quadtree( x0, y0, log2CbSize, cqtDepth ) (7.3.8.4).
func (w *SliceWalker) quadtree(x0, y0, log2CbSize, cqtDepth int) error {
	s := &w.SPS
	width, height := int(s.CodedWidth), int(s.CodedHeight)
	size := 1 << log2CbSize
	split := log2CbSize > int(s.Log2MinCbSize) // inferred when absent (7.4.9.4)
	if x0+size <= width && y0+size <= height && log2CbSize > int(s.Log2MinCbSize) {
		// split_cu_flag: ctxInc of Table 9-49 from the left and above coding tree depths.
		inc := 0
		if w.Available(x0, y0, x0-1, y0) && int(w.ctDepth(x0-1, y0)) > cqtDepth {
			inc++
		}
		if w.Available(x0, y0, x0, y0-1) && int(w.ctDepth(x0, y0-1)) > cqtDepth {
			inc++
		}
		split = w.D.Decision(&w.M.Ctx[cabac.CtxSplitCU+inc]) == 1
	}
	if log2CbSize >= int(s.Log2CtbSize)-int(w.PPS.DiffCuQPDeltaDepth) {
		w.startQuantizationGroup(x0, y0)
		if w.PPS.CUQPDeltaEnabled {
			w.IsCuQpDeltaCoded, w.CuQpDeltaVal = false, 0
		}
	}
	if w.Hdr.CuChromaQPOffsetEnabled && log2CbSize >= int(s.Log2CtbSize)-int(w.PPS.Range.DiffCuChromaQPOffsetDepth) {
		w.IsCuChromaQpOffsetCoded = false
	}
	if split {
		x1, y1 := x0+size/2, y0+size/2
		if err := w.quadtree(x0, y0, log2CbSize-1, cqtDepth+1); err != nil {
			return err
		}
		if x1 < width {
			if err := w.quadtree(x1, y0, log2CbSize-1, cqtDepth+1); err != nil {
				return err
			}
		}
		if y1 < height {
			if err := w.quadtree(x0, y1, log2CbSize-1, cqtDepth+1); err != nil {
				return err
			}
		}
		if x1 < width && y1 < height {
			return w.quadtree(x1, y1, log2CbSize-1, cqtDepth+1)
		}
		return nil
	}
	// CtDepth of the coding block's area is cqtDepth (7.4.9.4).
	minCb := uint(s.Log2MinCbSize)
	for y := y0 >> minCb; y < min((y0+size)>>minCb, (height+1<<minCb-1)>>minCb); y++ {
		for x := x0 >> minCb; x < min((x0+size)>>minCb, w.Pic.MinCbWidth); x++ {
			w.Pic.CtDepth[y*w.Pic.MinCbWidth+x] = uint8(cqtDepth)
		}
	}
	if w.cu == nil {
		return fmt.Errorf("%w: no coding unit decoder", ErrSliceData)
	}
	return w.cu(w, x0, y0, log2CbSize)
}

func (w *SliceWalker) ctDepth(x, y int) uint8 {
	minCb := uint(w.SPS.Log2MinCbSize)
	return w.Pic.CtDepth[(y>>minCb)*w.Pic.MinCbWidth+x>>minCb]
}
