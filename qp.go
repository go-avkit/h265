// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// The derivation process for quantization parameters (8.6.1).

// startQuantizationGroup begins the quantization group at ( xQg, yQg ): qPY_PRED is
// derived once for every coding unit in it, from qPY_PREV (the QpY of the last
// coding unit before the group) and the groups to the left and above when they lie
// in the same coding tree block.
func (w *SliceWalker) startQuantizationGroup(xQg, yQg int) {
	prev := w.lastQpY
	ctb := uint(w.SPS.Log2CtbSize)
	l := w.Pic.Layout
	at := func(xN, yN int) int32 {
		if !w.Available(xQg, yQg, xN, yN) || (yN>>ctb)*l.WidthCtbs+xN>>ctb != w.ctbAddrRs {
			return prev
		}
		return int32(w.Pic.QpY[w.cell(xN, yN)])
	}
	a, b := at(xQg-1, yQg), at(xQg, yQg-1)
	w.qpPred = (a + b + 1) >> 1 // (8-282)
}

// qpBdOffsetY and qpBdOffsetC are QpBdOffsetY and QpBdOffsetC (7-4, 7-5).
func (w *SliceWalker) qpBdOffsetY() int32 { return 6 * (int32(w.SPS.BitDepthLuma) - 8) }
func (w *SliceWalker) qpBdOffsetC() int32 { return 6 * (int32(w.SPS.BitDepthChroma) - 8) }

// qpY is QpY (8-283) of the coding unit being decoded, with CuQpDeltaVal as it
// stands.
func (w *SliceWalker) qpY() int32 {
	bd := w.qpBdOffsetY()
	return (w.qpPred+w.CuQpDeltaVal+52+2*bd)%(52+bd) - bd
}

// chromaQP is Qp'Cb ( cIdx 1 ) or Qp'Cr ( cIdx 2 ) of (8-285), (8-286), (8-289) and
// (8-290) for the QpY given.
func (w *SliceWalker) chromaQP(qpY int32, cIdx int) int32 {
	off, cu := w.PPS.CbQPOffset+w.Hdr.CbQPOffset, w.CuQpOffsetCb
	if cIdx == 2 {
		off, cu = w.PPS.CrQPOffset+w.Hdr.CrQPOffset, w.CuQpOffsetCr
	}
	bdC := w.qpBdOffsetC()
	qPi := min(max(qpY+off+cu, -bdC), 57)
	qPc := qPi
	if w.chroma == 1 {
		qPc = int32(ChromaQPTable(int(qPi)))
	} else {
		qPc = min(qPi, 51)
	}
	return qPc + bdC
}

// endCodingUnit records QpY of the coding unit just decoded: the next quantization
// group predicts from the last one's.
func (w *SliceWalker) endCodingUnit(x0, y0, log2CbSize int) {
	q := w.qpY()
	w.lastQpY = q
	s := &w.SPS
	size := 1 << uint(log2CbSize)
	m := uint(s.Log2MinCbSize)
	for y := y0 >> m; y < min((y0+size)>>m, (int(s.CodedHeight)+1<<m-1)>>m); y++ {
		for x := x0 >> m; x < min((x0+size)>>m, w.Pic.MinCbWidth); x++ {
			w.Pic.QpY[y*w.Pic.MinCbWidth+x] = int8(q)
		}
	}
}
