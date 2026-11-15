// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"

	"github.com/go-avkit/h265/internal/cabac"
)

// ErrSliceData means slice segment data is not what 7.3.8 allows: it does not
// end where its last coding tree unit says, a substream does not start where the
// entry points say, or a flag that must be 1 is 0.
var ErrSliceData = errors.New("h265: slice segment data is not valid")

// SaoParams is the sample adaptive offset of one coding tree block, as 7.4.9.3
// derives it (merged or stated), for the colour components 0 to 2.
type SaoParams struct {
	// Type is SaoTypeIdx (0 none, 1 band, 2 edge), BandPos sao_band_position and
	// EoClass SaoEoClass.
	Type, BandPos, EoClass [3]uint8
	// Offset is SaoOffsetVal[ 1..4 ] (7-72): signed and scaled by log2OffsetScale.
	Offset [3][4]int32
}

// PictureState is what the slices of one picture share while their data is
// decoded: the layout, which slice each coding tree block belongs to (for the
// availability of 6.4.1), the parameters read so far, and the context variables
// stored for the synchronisation of 9.3.2.4 and 9.3.2.5.
type PictureState struct {
	SPS    SPS
	Layout *CtbLayout
	// SliceAddr is SliceAddrRs of each coding tree block in raster scan, -1 for
	// one not yet decoded in this picture.
	SliceAddr []int32
	// Sao holds the parameters of each coding tree block, raster scan.
	Sao []SaoParams
	// CtDepth is CtDepth at the granularity of the minimum coding block, raster
	// scan of a grid MinCbWidth wide.
	CtDepth    []uint8
	MinCbWidth int
	// CuSkip is cu_skip_flag on the same grid as CtDepth.
	CuSkip []bool
	// IntraMode is IntraPredModeY per 4x4 luma block, raster scan of a grid
	// IntraWidth wide; INTRA_DC (1) for blocks of coding units that are not
	// intra or are PCM, which is what 8.4.2 takes a neighbour's mode to be.
	IntraMode  []uint8
	IntraWidth int
	// CuMode is CuPredMode (a PredMode) on the same grid.
	CuMode []uint8
	// QpY is QpY of each coding unit on the grid of CtDepth; lastQpY is the QpY of
	// the last coding unit decoded, which a dependent slice segment continues from.
	QpY     []int8
	lastQpY int32

	wpp, ds cabac.Model
}

// NewPictureState starts the decoding of a picture.
func NewPictureState(sps SPS, l *CtbLayout) *PictureState {
	n := l.WidthCtbs * l.HeightCtbs
	ps := &PictureState{SPS: sps, Layout: l, SliceAddr: make([]int32, n), Sao: make([]SaoParams, n)}
	for i := range ps.SliceAddr {
		ps.SliceAddr[i] = -1
	}
	minCb := uint(sps.Log2MinCbSize)
	ps.MinCbWidth = (int(sps.CodedWidth) + 1<<minCb - 1) >> minCb
	ps.CtDepth = make([]uint8, ps.MinCbWidth*((int(sps.CodedHeight)+1<<minCb-1)>>minCb))
	ps.CuSkip = make([]bool, len(ps.CtDepth))
	ps.QpY = make([]int8, len(ps.CtDepth))
	ps.CuMode = make([]uint8, len(ps.CtDepth))
	ps.IntraWidth = (int(sps.CodedWidth) + 3) >> 2
	ps.IntraMode = make([]uint8, ps.IntraWidth*((int(sps.CodedHeight)+3)>>2))
	return ps
}

// CodingUnitFunc decodes coding_unit( x0, y0, log2CbSize ) with the walker w.
type CodingUnitFunc func(w *SliceWalker, x0, y0, log2CbSize int) error

// SliceWalker is the state of slice_segment_data( ): the engine, the context
// variables, and the picture and slice it decodes.
type SliceWalker struct {
	Pic *PictureState
	SPS SPS
	PPS PPS
	Hdr SliceHeader
	D   cabac.Decoder
	M   cabac.Model

	cu CodingUnitFunc

	// IsCuQpDeltaCoded, CuQpDeltaVal and IsCuChromaQpOffsetCoded of 7.3.8.4.
	IsCuQpDeltaCoded, IsCuChromaQpOffsetCoded bool
	CuQpDeltaVal                              int32

	ctbAddrRs int
	chroma    int
	// CuQpOffsetCb and CuQpOffsetCr are set by chroma_qp_offset( ) (7.4.9.15); they
	// start at 0 for each slice segment (7.4.7.1).
	CuQpOffsetCb, CuQpOffsetCr int32

	// lastQpY is the QpY of the last coding unit decoded (qPY_PREV of the next
	// quantization group); qpPred is qPY_PRED of the current one.
	lastQpY, qpPred int32

	// The residual blocks of the transform unit being read, and their levels.
	tuBlocks [5]TUBlock
	levels   [5][1 << 10]int32

	// data is the unescaped slice segment data and pos the offset in it of the
	// substream being read, which a PCM block moves.
	data []byte
	pos  int
}

// Available is the derivation of 6.4.1: whether the block covering ( xN, yN ) is
// available to the block at ( xCurr, yCurr ).
func (w *SliceWalker) Available(xCurr, yCurr, xN, yN int) bool {
	s, l := &w.SPS, w.Pic.Layout
	if xN < 0 || yN < 0 || xN >= int(s.CodedWidth) || yN >= int(s.CodedHeight) {
		return false
	}
	tb := uint(s.Log2MinTbSize)
	if l.MinTbAddrZs(xN>>tb, yN>>tb) > l.MinTbAddrZs(xCurr>>tb, yCurr>>tb) {
		return false
	}
	ctb := uint(s.Log2CtbSize)
	rsN := (yN>>ctb)*l.WidthCtbs + xN>>ctb
	rsC := (yCurr>>ctb)*l.WidthCtbs + xCurr>>ctb
	return w.Pic.SliceAddr[rsN] == w.Pic.SliceAddr[rsC] && l.TileID[l.RsToTs[rsN]] == l.TileID[l.RsToTs[rsC]]
}

// DecodeSliceData is slice_segment_data( ) (7.3.8.1): the coding tree units of
// the slice segment u, whose header is hdr, with the initialisation and the
// synchronisation of the context variables of 9.3.2 for tiles, wavefronts and
// dependent segments. cu decodes each coding unit. It succeeds only if the data
// ends exactly where end_of_slice_segment_flag says, each substream starts where
// the entry points say, and every end_of_subset_one_bit is 1.
func DecodeSliceData(ps *PictureState, u Unit, hdr SliceHeader, pps PPS, cu CodingUnitFunc) error {
	sps, l := ps.SPS, ps.Layout
	w := &SliceWalker{Pic: ps, SPS: sps, PPS: pps, Hdr: hdr, cu: cu, chroma: int(chromaArrayType(sps))}
	unesc := u.Unescape()
	if hdr.DataOffset < 0 || hdr.DataOffset > len(unesc) {
		return fmt.Errorf("%w: slice data starts at %d of %d bytes", ErrSliceData, hdr.DataOffset, len(unesc))
	}
	data := unesc[hdr.DataOffset:]
	starts, err := substreamStarts(u.Payload, hdr)
	if err != nil {
		return err
	}
	ts := int(l.RsToTs[hdr.SegmentAddress])
	n := len(l.TsToRs)
	sub := 0 // index of the substream being read
	base := hdr.DataOffset
	w.data = data // w.pos is the byte offset of the current substream in data
	first := true
	for {
		rs := int(l.TsToRs[ts])
		w.ctbAddrRs = rs
		ps.SliceAddr[rs] = int32(hdr.SliceAddrRs)
		x0, y0 := (rs%l.WidthCtbs)<<sps.Log2CtbSize, (rs/l.WidthCtbs)<<sps.Log2CtbSize
		firstInTile := ts == 0 || l.TileID[ts] != l.TileID[ts-1]
		rowStart := pps.EntropySyncEnabled && (rs%l.WidthCtbs == 0 || l.TileID[ts] != l.TileID[l.RsToTs[rs-1]])
		if first || firstInTile || rowStart {
			// 9.3.2.1 (Figure 9-3): which of the three ways the contexts begin.
			switch {
			case (first && !hdr.Dependent) || firstInTile:
				w.initContexts()
			case rowStart:
				if w.Available(x0, y0, x0+1<<sps.Log2CtbSize, y0-1<<sps.Log2CtbSize) {
					w.M = ps.wpp
				} else {
					w.initContexts()
				}
			default: // the first CTU of a dependent slice segment
				w.M = ps.ds
			}
			// 8.6.1: qPY_PREV is SliceQpY for the first quantization group of a
			// slice, of a tile and (with wavefronts) of a CTB row of a tile, and
			// otherwise the QpY of the last coding unit before it, which a dependent
			// slice segment takes from the segment before.
			if (first && !hdr.Dependent) || firstInTile || rowStart {
				w.lastQpY = hdr.SliceQP
			} else {
				w.lastQpY = ps.lastQpY
			}
			if err := w.D.Start(data[w.pos:], cabac.Config{}); err != nil {
				return fmt.Errorf("%w: %w", ErrSliceData, err)
			}
			first = false
		}

		if hdr.Refs.SAOLuma || hdr.Refs.SAOChroma {
			w.sao(rs)
		}
		if err := w.quadtree(x0, y0, int(sps.Log2CtbSize), 0); err != nil {
			return err
		}
		// 9.3.1: the contexts after the second CTB of a row are kept for the next row.
		if pps.EntropySyncEnabled && (rs%l.WidthCtbs == 1 ||
			(rs > 1 && l.TileID[ts] != l.TileID[l.RsToTs[rs-2]])) {
			ps.wpp = w.M
		}
		end := w.D.Terminate() == 1 // end_of_slice_segment_flag
		ts++
		if end {
			ps.lastQpY = w.lastQpY
			if pps.DependentSlicesEnabled {
				ps.ds = w.M
			}
			// The terminating bin's last bit is rbsp_stop_one_bit: the rest of the
			// data is the zero bits that finish its byte.
			if got := int((w.D.BitPos() + 7) / 8); w.pos+got != len(data) {
				return fmt.Errorf("%w: the slice ends at byte %d of %d", ErrSliceData, w.pos+got, len(data))
			}
			if sub != len(starts) {
				return fmt.Errorf("%w: %d entry points but %d substreams", ErrSliceData, len(starts), sub+1)
			}
			return nil
		}
		if ts >= n {
			return fmt.Errorf("%w: no end_of_slice_segment_flag by the last coding tree block", ErrSliceData)
		}
		nrs := int(l.TsToRs[ts])
		if (pps.TilesEnabled && l.TileID[ts] != l.TileID[ts-1]) ||
			(pps.EntropySyncEnabled && (nrs%l.WidthCtbs == 0 || l.TileID[ts] != l.TileID[l.RsToTs[nrs-1]])) {
			if w.D.Terminate() != 1 { // end_of_subset_one_bit
				return fmt.Errorf("%w: end_of_subset_one_bit is 0", ErrSliceData)
			}
			// byte_alignment( ): the bit just read is its one; zeros follow.
			next := w.pos + int((w.D.BitPos()+7)/8)
			sub++
			if sub > len(starts) || starts[sub-1] != base+next {
				return fmt.Errorf("%w: substream %d starts at byte %d, the entry points say %v", ErrSliceData, sub, base+next, starts)
			}
			w.pos = next
		}
	}
}

// initContexts is the initialisation of 9.3.2.2 and StatCoeff for the slice.
func (w *SliceWalker) initContexts() {
	w.M.Init(cabac.InitType(uint8(w.Hdr.Type), w.Hdr.Refs.CabacInit), int(w.Hdr.SliceQP))
}

// substreamStarts turns the entry points of a header into the offsets, in the
// UNESCAPED payload, at which substreams 1.. start: the entry point offsets count
// the emulation prevention bytes of the slice segment data (7.4.7.1), and the
// engine reads the bytes without them.
func substreamStarts(payload []byte, hdr SliceHeader) ([]int, error) {
	if len(hdr.EntryPoints) == 0 {
		return nil, nil
	}
	// unescIndex[i] is the unescaped index of escaped byte i, or -1 for a removed one.
	unescIndex := make([]int32, len(payload)+1)
	zeros, out := 0, int32(0)
	for i, b := range payload {
		if zeros >= 2 && b == 3 {
			unescIndex[i] = -1
			zeros = 0
			continue
		}
		unescIndex[i] = out
		out++
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	unescIndex[len(payload)] = out
	// The escaped position of the first slice data byte.
	esc := 0
	for esc < len(payload) && int(unescIndex[esc]) != hdr.DataOffset {
		esc++
	}
	if esc == len(payload) && hdr.DataOffset != int(out) {
		return nil, fmt.Errorf("%w: the slice data offset %d is not a byte of the payload", ErrSliceData, hdr.DataOffset)
	}
	starts := make([]int, 0, len(hdr.EntryPoints))
	for _, off := range hdr.EntryPoints {
		esc += int(off)
		if esc > len(payload) || unescIndex[esc] < 0 {
			return nil, fmt.Errorf("%w: an entry point past the data or on an emulation prevention byte", ErrSliceData)
		}
		starts = append(starts, int(unescIndex[esc]))
	}
	return starts, nil
}
