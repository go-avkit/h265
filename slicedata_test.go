// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/go-avkit/h265/internal/cabac"
	"github.com/go-avkit/h265/internal/cabac/cabactest"
)

// The writer of slice_segment_data( ) for pictures whose coding units are one
// bypass bin: sao( ), coding_quadtree( ), the end_of_slice_segment_flag and
// end_of_subset_one_bit of 7.3.8.1, the context initialisation, storage and
// synchronisation of 9.3.2 (as the flowchart of Figure 9-3 lays them out), and the
// entry points over the ESCAPED bytes. Availability is not the z-scan address
// comparison of 6.4.1 but the set of blocks already written, in the same slice and
// tile: a different way to the same answer.

type cuRecord struct{ x, y, log2 int }

type walkWriter struct {
	sps     SPS
	pps     PPS
	l       *CtbLayout
	rng     *rand.Rand
	done    []bool // minimum coding blocks written, raster
	depth   []int
	cbW     int
	sliceOf []int32 // per CTB raster, -1 before it is written
	sao     []SaoParams
	cus     []cuRecord
	cuBits  []int
	qpSeen  []bool // IsCuQpDeltaCoded at each CU, before the CU sets it
	wpp, ds cabac.Model
	m       cabac.Model
	e       *cabactest.Encoder
	qpDone  bool
	// cqDone and cqSeen are IsCuChromaQpOffsetCoded; endZero and subsetZero make
	// the writer break the stream: end_of_slice_segment_flag 0 at the last block,
	// end_of_subset_one_bit 0.
	cqDone, endZero, subsetZero bool
	cqSeen, cqEnabledAt         []bool
	hdr                         SliceHeader
	// cu, when set, writes the coding unit; prefix holds the bytes of the current
	// substream before a PCM block restarted the encoder.
	cu     func(x0, y0, log2, depth int)
	prefix []byte
	// skip is cu_skip_flag per minimum coding block; expect the coding units
	// written and treeBits the bin each stub transform tree wrote.
	skip     []bool
	expect   []CodingUnit
	treeBits []int
	// The writer's own state for QpY (8.6.1): the QpY of the last coding unit, now
	// and at the end of the segment before, qPY_PRED of the quantization group in
	// progress, and QpY of every minimum coding block.
	lastQp, dsLastQp, qpPred int32
	qpMap                    []int32
	pcmBad                   bool
	// imode is IntraPredModeY per 4x4 block; fullTree writes real transform trees
	// and expectTU what the decoder must read from them; cbOff, crOff and
	// cuQpDeltaVal are the state of delta_qp( ) and chroma_qp_offset( ).
	imode        []uint8
	fullTree     bool
	expectTU     []TransformUnit
	cbOff, crOff int32
	cuQpDeltaVal int32
	badMvd       int // which broken abs_mvd_minus2 writeMvd writes, 0 for none
}

// finish ends the substream in progress and returns its bytes.
func (w *walkWriter) finish() []byte {
	out := append(w.prefix, w.e.Bytes()...)
	w.prefix = nil
	return out
}

func (w *walkWriter) cell(x, y int) int {
	s := uint(w.sps.Log2MinCbSize)
	return (y>>s)*w.cbW + x>>s
}

func (w *walkWriter) avail(cur, nb [2]int) bool {
	if nb[0] < 0 || nb[1] < 0 || nb[0] >= int(w.sps.CodedWidth) || nb[1] >= int(w.sps.CodedHeight) {
		return false
	}
	if !w.done[w.cell(nb[0], nb[1])] {
		return false
	}
	c := uint(w.sps.Log2CtbSize)
	rsN, rsC := (nb[1]>>c)*w.l.WidthCtbs+nb[0]>>c, (cur[1]>>c)*w.l.WidthCtbs+cur[0]>>c
	return w.sliceOf[rsN] == w.sliceOf[rsC] && w.l.TileID[w.l.RsToTs[rsN]] == w.l.TileID[w.l.RsToTs[rsC]]
}

func (w *walkWriter) bin(ctx, v int) { w.e.Decision(&w.m.Ctx[ctx], v) }

func (w *walkWriter) writeSao(rs int) {
	l := w.l
	rx, ry := rs%l.WidthCtbs, rs/l.WidthCtbs
	slice := int(w.hdr.SliceAddrRs)
	tile := func(r int) int32 { return l.TileID[l.RsToTs[r]] }
	left := rx > 0 && rs > slice && tile(rs) == tile(rs-1)
	mergeLeft := left && w.rng.IntN(3) == 0
	if left {
		w.bin(cabac.CtxSAOMerge, b2n(mergeLeft))
	}
	up := ry > 0 && !mergeLeft && rs-l.WidthCtbs >= slice && tile(rs) == tile(rs-l.WidthCtbs)
	mergeUp := up && w.rng.IntN(3) == 0
	if up {
		w.bin(cabac.CtxSAOMerge, b2n(mergeUp))
	}
	var p SaoParams
	switch {
	case mergeLeft:
		p = w.sao[rs-1]
	case mergeUp:
		p = w.sao[rs-l.WidthCtbs]
	default:
		comps := 1
		if w.sps.ChromaFormat != 0 {
			comps = 3
		}
		for c := 0; c < comps; c++ {
			if !((w.hdr.Refs.SAOLuma && c == 0) || (w.hdr.Refs.SAOChroma && c > 0)) {
				continue
			}
			if c < 2 {
				typ := w.rng.IntN(3)
				w.bin(cabac.CtxSAOTypeIdx, b2n(typ != 0))
				if typ != 0 {
					w.e.Bypass(b2n(typ == 2))
				}
				p.Type[c] = uint8(typ)
			} else {
				p.Type[2] = p.Type[1]
			}
			if p.Type[c] == 0 {
				continue
			}
			bd := int(w.sps.BitDepthLuma)
			scale := uint(w.pps.Range.Log2SAOOffsetScaleLuma)
			if c > 0 {
				bd, scale = int(w.sps.BitDepthChroma), uint(w.pps.Range.Log2SAOOffsetScaleChroma)
			}
			cMax := (1 << (min(bd, 10) - 5)) - 1
			var abs [4]int
			for i := range abs {
				abs[i] = w.rng.IntN(cMax + 1)
				for k := 0; k < abs[i]; k++ { // truncated unary, bypass
					w.e.Bypass(1)
				}
				if abs[i] < cMax {
					w.e.Bypass(0)
				}
			}
			sign := [4]bool{false, false, true, true} // edge offset
			if p.Type[c] == 1 {
				for i := range abs {
					sign[i] = false
					if abs[i] != 0 {
						sign[i] = w.rng.IntN(2) == 0
						w.e.Bypass(b2n(sign[i]))
					}
				}
				p.BandPos[c] = uint8(w.rng.IntN(32))
				w.e.BypassBits(uint32(p.BandPos[c]), 5)
			} else if c < 2 {
				p.EoClass[c] = uint8(w.rng.IntN(4))
				w.e.BypassBits(uint32(p.EoClass[c]), 2)
			} else {
				p.EoClass[2] = p.EoClass[1]
			}
			for i := range abs {
				v := int32(abs[i]) << scale
				if sign[i] {
					v = -v
				}
				p.Offset[c][i] = v
			}
		}
	}
	w.sao[rs] = p
}

func (w *walkWriter) writeQuadtree(x0, y0, log2, depth int) {
	s := w.sps
	W, H := int(s.CodedWidth), int(s.CodedHeight)
	size := 1 << log2
	split := log2 > int(s.Log2MinCbSize)
	if x0+size <= W && y0+size <= H && log2 > int(s.Log2MinCbSize) {
		split = w.rng.IntN(3) != 0
		inc := 0
		if w.avail([2]int{x0, y0}, [2]int{x0 - 1, y0}) && w.depth[w.cell(x0-1, y0)] > depth {
			inc++
		}
		if w.avail([2]int{x0, y0}, [2]int{x0, y0 - 1}) && w.depth[w.cell(x0, y0-1)] > depth {
			inc++
		}
		w.bin(cabac.CtxSplitCU+inc, b2n(split))
	}
	if log2 >= int(s.Log2CtbSize)-int(w.pps.DiffCuQPDeltaDepth) {
		w.startQG(x0, y0)
	}
	if w.pps.CUQPDeltaEnabled && log2 >= int(s.Log2CtbSize)-int(w.pps.DiffCuQPDeltaDepth) {
		w.qpDone, w.cuQpDeltaVal = false, 0
	}
	if w.hdr.CuChromaQPOffsetEnabled && log2 >= int(s.Log2CtbSize)-int(w.pps.Range.DiffCuChromaQPOffsetDepth) {
		w.cqDone = false
	}
	if split {
		h := size / 2
		w.writeQuadtree(x0, y0, log2-1, depth+1)
		if x0+h < W {
			w.writeQuadtree(x0+h, y0, log2-1, depth+1)
		}
		if y0+h < H {
			w.writeQuadtree(x0, y0+h, log2-1, depth+1)
		}
		if x0+h < W && y0+h < H {
			w.writeQuadtree(x0+h, y0+h, log2-1, depth+1)
		}
		return
	}
	if w.cu != nil {
		w.cu(x0, y0, log2, depth)
	} else {
		// The coding unit of the test: one bypass bin, and IsCuQpDeltaCoded set.
		bit := w.rng.IntN(2)
		w.e.Bypass(bit)
		w.cus = append(w.cus, cuRecord{x0, y0, log2})
		w.cuBits = append(w.cuBits, bit)
		w.qpSeen = append(w.qpSeen, w.qpDone)
		w.qpDone = true
		w.cqSeen = append(w.cqSeen, w.cqDone)
		w.cqEnabledAt = append(w.cqEnabledAt, w.hdr.CuChromaQPOffsetEnabled)
		w.cqDone = true
	}
	w.endCU(x0, y0, log2)
	m := uint(s.Log2MinCbSize)
	for y := y0 >> m; y < min((y0+size)>>m, (H+1<<m-1)>>m); y++ {
		for x := x0 >> m; x < min((x0+size)>>m, w.cbW); x++ {
			w.done[y*w.cbW+x] = true
			w.depth[y*w.cbW+x] = depth
		}
	}
}

func (w *walkWriter) initContexts() {
	w.m.Init(cabac.InitType(uint8(w.hdr.Type), w.hdr.Refs.CabacInit), int(w.hdr.SliceQP))
}

type segPlan struct {
	startTs, endTs int // [start, end) in tile scan
	dependent      bool
	hdr            SliceHeader
}

// writeSegment writes the slice segment data and returns the payload (escaped), and
// the header with its entry points.
func (w *walkWriter) writeSegment(p segPlan) ([]byte, SliceHeader) {
	l := w.l
	w.hdr = p.hdr
	w.cbOff, w.crOff = 0, 0 // CuQpOffsetCb and CuQpOffsetCr start at 0 for each segment (7.4.7.1)
	w.e = cabactest.New()
	var substreams [][]byte
	W := l.WidthCtbs
	for ts := p.startTs; ts < p.endTs; ts++ {
		rs := int(l.TsToRs[ts])
		w.sliceOf[rs] = int32(p.hdr.SliceAddrRs)
		x0, y0 := (rs%W)<<w.sps.Log2CtbSize, (rs/W)<<w.sps.Log2CtbSize
		firstInTile := ts == 0 || l.TileID[ts] != l.TileID[ts-1]
		wppRow := w.pps.EntropySyncEnabled && (rs%W == 0 || l.TileID[ts] != l.TileID[l.RsToTs[rs-1]])
		segStart := ts == p.startTs
		if segStart || firstInTile || wppRow {
			switch {
			case (segStart && !p.dependent) || firstInTile:
				w.initContexts()
			case wppRow:
				tx, ty := rs%W+1, rs/W-1
				ok := ty >= 0 && tx < W
				if ok {
					trs := ty*W + tx
					ok = w.sliceOf[trs] == int32(p.hdr.SliceAddrRs) && l.TileID[l.RsToTs[trs]] == l.TileID[ts] && int(l.RsToTs[trs]) < ts
				}
				if ok {
					w.m = w.wpp
				} else {
					w.initContexts()
				}
			default:
				w.m = w.ds
			}
			// qPY_PREV starts at SliceQpY with a slice, a tile or a wavefront row.
			if (segStart && !p.dependent) || firstInTile || wppRow {
				w.lastQp = p.hdr.SliceQP
			} else {
				w.lastQp = w.dsLastQp
			}
			if !segStart {
				w.e = cabactest.New()
			}
		}
		if w.hdr.Refs.SAOLuma || w.hdr.Refs.SAOChroma {
			w.writeSao(rs)
		}
		w.writeQuadtree(x0, y0, int(w.sps.Log2CtbSize), 0)
		// storage: after the second CTB of a row of a tile, or the only one of a width-1 tile
		tileCol := rs%W - colBdOf(l, rs%W)
		colW := colWidthOf(l, rs%W)
		if w.pps.EntropySyncEnabled && (tileCol == 1 || (colW == 1 && tileCol == 0)) {
			w.wpp = w.m
		}
		last := ts == p.endTs-1
		w.e.Terminate(b2n(last && !w.endZero))
		if last {
			w.dsLastQp = w.lastQp
			if w.pps.DependentSlicesEnabled {
				w.ds = w.m
			}
			substreams = append(substreams, w.finish())
			break
		}
		nrs := int(l.TsToRs[ts+1])
		if (w.pps.TilesEnabled && l.TileID[ts+1] != l.TileID[ts]) ||
			(w.pps.EntropySyncEnabled && (nrs%W == 0 || l.TileID[ts+1] != l.TileID[l.RsToTs[nrs-1]])) {
			w.e.Terminate(b2n(!w.subsetZero)) // end_of_subset_one_bit, which flushes
			substreams = append(substreams, w.finish())
		}
	}
	// The data, and the entry points: the sizes of the substreams in ESCAPED bytes.
	var raw []byte
	var bounds []int // raw index at which each substream after the first starts
	for i, s := range substreams {
		if i > 0 {
			bounds = append(bounds, len(raw))
		}
		raw = append(raw, s...)
	}
	payload := escapeRBSP(raw)
	hdr := p.hdr
	hdr.EntryPoints = nil
	prevEsc := 0
	for _, b := range bounds {
		esc := len(escapeRBSP(raw[:b+1])) - 1 // the escaped position of the byte that starts the substream
		hdr.EntryPoints = append(hdr.EntryPoints, uint32(esc-prevEsc))
		prevEsc = esc
	}
	return payload, hdr
}

func colBdOf(l *CtbLayout, x int) int {
	for i := len(l.ColBd) - 2; i >= 0; i-- {
		if x >= l.ColBd[i] {
			return l.ColBd[i]
		}
	}
	return 0
}

func colWidthOf(l *CtbLayout, x int) int {
	for i := len(l.ColBd) - 2; i >= 0; i-- {
		if x >= l.ColBd[i] {
			return l.ColBd[i+1] - l.ColBd[i]
		}
	}
	return 0
}

// walkCase builds a random picture of coding tree blocks, with tiles, wavefronts
// and slice segments, and the stub coding units.
func walkCase(t *testing.T, rng *rand.Rand) (*walkWriter, *PictureState, []segPlan) {
	t.Helper()
	log2Min := 3 + rng.IntN(2)
	diff := 1 + rng.IntN(max(1, 6-log2Min-1))
	ctb := 1 << (log2Min + diff)
	wc, hc := 1+rng.IntN(6), 1+rng.IntN(5)
	w, h := wc*ctb, hc*ctb
	if rng.IntN(2) == 0 { // a picture whose last blocks are partial
		w -= (log2Min2(log2Min)) * rng.IntN(max(1, ctb/(1<<log2Min)-1))
		h -= (log2Min2(log2Min)) * rng.IntN(max(1, ctb/(1<<log2Min)-1))
	}
	// Transform block sizes: MinTbLog2SizeY below MinCbLog2SizeY, MaxTbLog2SizeY up to 5 and
	// the coding tree block.
	minTb := 2 + rng.IntN(log2Min-2)
	maxTb := minTb + rng.IntN(min(5, log2Min+diff)-minTb+1)
	sizes := []uint32{uint32(log2Min - 3), uint32(diff), uint32(minTb - 2), uint32(maxTb - minTb), 0, 0}
	sps := mustSPS(t, set{chroma: []uint32{0, 1, 2, 3}[rng.IntN(4)], width: uint32(w), height: uint32(h), sao: true, sizes: sizes,
		bdLuma: uint32(rng.IntN(5)), bdChroma: uint32(rng.IntN(5))})
	cols, rows := 1+rng.IntN(min(wc, 3)), 1+rng.IntN(min(hc, 3))
	p := pset{
		entropySync: rng.IntN(2) == 0, dependentSlices: rng.IntN(2) == 0, cuQPDelta: rng.IntN(2) == 0,
		diffDepth: u32(uint32(rng.IntN(diff + 1))), tiles: cols*rows > 1, tileCols: uint32(cols), tileRows: uint32(rows), uniform: true,
	}
	pps := mustPPS(t, p)
	pps.Range.DiffCuChromaQPOffsetDepth = uint8(rng.IntN(diff + 1))
	l, err := NewCtbLayout(sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	ps := NewPictureState(sps, l)
	ww := &walkWriter{sps: sps, pps: pps, l: l, rng: rng, cbW: ps.MinCbWidth,
		done: make([]bool, len(ps.CtDepth)), depth: make([]int, len(ps.CtDepth)),
		sliceOf: make([]int32, wc*hc), sao: make([]SaoParams, wc*hc), skip: make([]bool, len(ps.CtDepth)),
		imode: make([]uint8, ps.IntraWidth*((int(sps.CodedHeight)+3)>>2)), qpMap: make([]int32, len(ps.CtDepth))}
	for i := range ww.sliceOf {
		ww.sliceOf[i] = -1
	}
	// Segments: random cut points in tile scan.
	var plans []segPlan
	n := wc * hc
	for ts := 0; ts < n; {
		end := min(n, ts+1+rng.IntN(n))
		rs := int(l.TsToRs[ts])
		dep := pps.DependentSlicesEnabled && ts > 0 && rng.IntN(2) == 0
		hdr := SliceHeader{SegmentAddress: uint32(rs), Dependent: dep, FirstSliceSegmentInPic: ts == 0}
		if dep {
			hdr.SliceAddrRs = plans[len(plans)-1].hdr.SliceAddrRs
			hdr.Type, hdr.SliceQP, hdr.Refs = plans[len(plans)-1].hdr.Type, plans[len(plans)-1].hdr.SliceQP, plans[len(plans)-1].hdr.Refs
		} else {
			hdr.SliceAddrRs = uint32(rs)
			hdr.Type, hdr.SliceQP = SliceType(rng.IntN(3)), int32(15+rng.IntN(30))
			hdr.Refs.SAOLuma, hdr.Refs.SAOChroma, hdr.Refs.CabacInit = rng.IntN(4) != 0, rng.IntN(3) != 0, rng.IntN(2) == 0
			hdr.CuChromaQPOffsetEnabled = rng.IntN(2) == 0
		}
		plans = append(plans, segPlan{ts, end, dep, hdr})
		ts = end
	}
	return ww, ps, plans
}

func log2Min2(l int) int { return 1 << l }

func TestSliceDataWalk(t *testing.T) {
	rng := rand.New(rand.NewPCG(61, 62))
	for iter := 0; iter < 400; iter++ {
		ww, ps, plans := walkCase(t, rng)
		var got []cuRecord
		var gotBits []int
		var gotQP, gotCQ []bool
		cu := func(w *SliceWalker, x0, y0, log2 int) error {
			got = append(got, cuRecord{x0, y0, log2})
			gotQP = append(gotQP, w.IsCuQpDeltaCoded)
			gotCQ = append(gotCQ, w.IsCuChromaQpOffsetCoded)
			w.IsCuQpDeltaCoded, w.IsCuChromaQpOffsetCoded = true, true
			gotBits = append(gotBits, int(w.D.Bypass()))
			return nil
		}
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, cu); err != nil {
				t.Fatalf("iter %d segment %d of %d (%dx%d CTBs, %v): %v", iter, si, len(plans), ps.Layout.WidthCtbs, ps.Layout.HeightCtbs, ww.pps, err)
			}
		}
		if len(got) != len(ww.cus) {
			t.Fatalf("iter %d: %d coding units, written %d", iter, len(got), len(ww.cus))
		}
		for i := range got {
			// IsCuQpDeltaCoded only means something when cu_qp_delta_enabled_flag is set.
			qpDiffers := ww.pps.CUQPDeltaEnabled && gotQP[i] != ww.qpSeen[i]
			// likewise IsCuChromaQpOffsetCoded, with cu_chroma_qp_offset_enabled_flag of
			// the slice (the writer's state of a block in an earlier segment is its own)
			cqDiffers := ww.cqEnabledAt[i] && gotCQ[i] != ww.cqSeen[i]
			if got[i] != ww.cus[i] || gotBits[i] != ww.cuBits[i] || qpDiffers || cqDiffers {
				t.Fatalf("iter %d: coding unit %d is %+v bit %d qp %v, written %+v bit %d qp %v", iter, i, got[i], gotBits[i], gotQP[i], ww.cus[i], ww.cuBits[i], ww.qpSeen[i])
			}
		}
		for rs := range ww.sao {
			if ps.Sao[rs] != ww.sao[rs] {
				t.Fatalf("iter %d: SAO of block %d: %+v, written %+v", iter, rs, ps.Sao[rs], ww.sao[rs])
			}
		}
		for i := range ww.depth {
			if int(ps.CtDepth[i]) != ww.depth[i] {
				t.Fatalf("iter %d: CtDepth of cell %d is %d, written %d", iter, i, ps.CtDepth[i], ww.depth[i])
			}
		}
	}
}

func TestSliceDataRefusals(t *testing.T) {
	rng := rand.New(rand.NewPCG(63, 64))
	ww, ps, plans := walkCase(t, rng)
	stub := func(w *SliceWalker, x0, y0, log2 int) error { w.D.Bypass(); return nil }
	payload, hdr := ww.writeSegment(plans[0])
	// The plain decode succeeds.
	if err := DecodeSliceData(ps, Unit{Payload: payload}, hdr, ww.pps, stub); err != nil {
		t.Fatalf("the fixture does not decode: %v", err)
	}
	fresh := func() *PictureState { return NewPictureState(ps.SPS, ps.Layout) }
	// A trailing byte the slice does not account for.
	if err := DecodeSliceData(fresh(), Unit{Payload: append(append([]byte(nil), payload...), 0x80)}, hdr, ww.pps, stub); !errors.Is(err, ErrSliceData) {
		t.Errorf("a byte past the end: %v", err)
	}
	// Data cut one byte short.
	if err := DecodeSliceData(fresh(), Unit{Payload: payload[:len(payload)-1]}, hdr, ww.pps, stub); err == nil {
		t.Error("data one byte short decoded")
	}
	// A data offset outside the payload.
	bad := hdr
	bad.DataOffset = len(payload) + 1
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, bad, ww.pps, stub); !errors.Is(err, ErrSliceData) {
		t.Errorf("a data offset past the payload: %v", err)
	}
	// An entry point far past the payload.
	far := hdr
	far.EntryPoints = []uint32{1 << 20}
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, far, ww.pps, stub); !errors.Is(err, ErrSliceData) {
		t.Errorf("an entry point past the payload: %v", err)
	}
	// A coding unit decoder that fails fails the slice; none at all is refused.
	boom := errors.New("boom")
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, hdr, ww.pps, func(*SliceWalker, int, int, int) error { return boom }); !errors.Is(err, boom) {
		t.Errorf("a failing coding unit: %v", err)
	}
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, hdr, ww.pps, nil); !errors.Is(err, ErrSliceData) {
		t.Errorf("no coding unit decoder: %v", err)
	}
}

// fixtureAt rebuilds the same random picture for seed, so a segment can be
// written with different breakages on fresh state.
func fixtureAt(t *testing.T, seed uint64) (*walkWriter, *PictureState, []segPlan) {
	t.Helper()
	return walkCase(t, rand.New(rand.NewPCG(seed, seed+1)))
}

func stubCU(w *SliceWalker, x0, y0, log2 int) error { w.D.Bypass(); return nil }

func sliceErrHas(err error, s string) bool {
	return err != nil && errors.Is(err, ErrSliceData) && strings.Contains(err.Error(), s)
}

func TestSliceDataBreakages(t *testing.T) {
	var noEnd, subsetZero, tooMany, mismatch, tooFew int
	for seed := uint64(1); seed < 400; seed++ {
		// end_of_slice_segment_flag 0 on the last block of a segment reaching the picture's end.
		ww, ps, plans := fixtureAt(t, seed)
		last := plans[len(plans)-1]
		for _, p := range plans[:len(plans)-1] {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Payload: payload}, hdr, ww.pps, stubCU); err != nil {
				t.Fatal(err)
			}
		}
		ww.endZero = true
		payload, hdr := ww.writeSegment(last)
		if err := DecodeSliceData(ps, Unit{Payload: payload}, hdr, ww.pps, stubCU); err == nil {
			t.Fatalf("seed %d: a last segment without its end flag decoded", seed)
		} else if sliceErrHas(err, "no end_of_slice_segment_flag") {
			noEnd++
		}

		// end_of_subset_one_bit 0.
		ww, ps, plans = fixtureAt(t, seed)
		ww.subsetZero = true
		payload, hdr = ww.writeSegment(plans[0])
		if len(hdr.EntryPoints) > 0 {
			// A terminating bin of 0 is not flushed, so what follows may be read
			// as another error; it must fail, and usually names the flag.
			err := DecodeSliceData(ps, Unit{Payload: payload}, hdr, ww.pps, stubCU)
			if err == nil {
				t.Fatalf("seed %d: end_of_subset_one_bit 0 decoded", seed)
			}
			if sliceErrHas(err, "end_of_subset_one_bit is 0") {
				subsetZero++
			}
		}

		// Entry points that do not describe the substreams.
		ww, ps, plans = fixtureAt(t, seed)
		payload, hdr = ww.writeSegment(plans[0])
		if len(hdr.EntryPoints) == 0 {
			continue
		}
		fresh := func() *PictureState { return NewPictureState(ps.SPS, ps.Layout) }
		bad := hdr
		bad.EntryPoints = append([]uint32(nil), hdr.EntryPoints...)
		bad.EntryPoints[0]++
		if err := DecodeSliceData(fresh(), Unit{Payload: payload}, bad, ww.pps, stubCU); err == nil {
			t.Fatalf("seed %d: a shifted entry point decoded", seed)
		} else if sliceErrHas(err, "entry points say") {
			mismatch++
		}
		bad.EntryPoints = hdr.EntryPoints[:len(hdr.EntryPoints)-1]
		if err := DecodeSliceData(fresh(), Unit{Payload: payload}, bad, ww.pps, stubCU); sliceErrHas(err, "entry points say") {
			tooFew++
		} else {
			t.Fatalf("seed %d: a missing entry point: %v", seed, err)
		}
		for _, extra := range []uint32{1, 2, 3} {
			bad.EntryPoints = append(append([]uint32(nil), hdr.EntryPoints...), extra)
			if err := DecodeSliceData(fresh(), Unit{Payload: payload}, bad, ww.pps, stubCU); sliceErrHas(err, "entry points but") {
				tooMany++
				break
			}
		}
	}
	t.Logf("no-end %d subset-zero %d mismatch %d too-few %d too-many %d", noEnd, subsetZero, mismatch, tooFew, tooMany)
	if noEnd == 0 || subsetZero == 0 || mismatch == 0 || tooFew == 0 || tooMany == 0 {
		t.Error("a breakage was never exercised")
	}
}

func TestSliceDataQuadtreeFailsAtEveryUnit(t *testing.T) {
	boom := errors.New("boom")
	for seed := uint64(1); seed < 40; seed++ {
		ww, ps, plans := fixtureAt(t, seed)
		payload, hdr := ww.writeSegment(plans[0])
		for k := 0; ; k++ {
			n := 0
			cu := func(w *SliceWalker, x0, y0, log2 int) error {
				if n == k {
					return boom
				}
				n++
				w.D.Bypass()
				return nil
			}
			err := DecodeSliceData(NewPictureState(ps.SPS, ps.Layout), Unit{Payload: payload}, hdr, ww.pps, cu)
			if err == nil {
				break // k is past the last coding unit
			}
			if !errors.Is(err, boom) {
				t.Fatalf("seed %d unit %d: %v", seed, k, err)
			}
		}
	}
}

func TestSliceDataStartRefused(t *testing.T) {
	ww, ps, plans := fixtureAt(t, 3)
	_, hdr := ww.writeSegment(plans[0])
	hdr.DataOffset, hdr.EntryPoints = 0, nil
	// Nine one bits are 511, which the engine may not start on (9.3.2.5).
	err := DecodeSliceData(ps, Unit{Payload: []byte{0xff, 0xff, 0xff}}, hdr, ww.pps, stubCU)
	if !errors.Is(err, ErrSliceData) {
		t.Errorf("a stream starting 0xff 0xff: %v", err)
	}
}

func TestSubstreamStarts(t *testing.T) {
	payload := []byte{0, 0, 3, 1, 5, 6} // 00 00 [03] 01 05 06: five bytes unescaped
	cases := []struct {
		off  int
		eps  []uint32
		want []int
		bad  bool
	}{
		{0, []uint32{4}, []int{3}, false}, // escaped byte 4 is unescaped byte 3
		{0, []uint32{3}, []int{2}, false},
		{0, []uint32{2}, nil, true},       // the emulation prevention byte itself
		{0, []uint32{7}, nil, true},       // past the payload
		{3, []uint32{1}, []int{4}, false}, // counted from the data's first escaped byte
		{5, nil, nil, false},
		{5, []uint32{1}, nil, true},  // data at the end, an entry past it
		{99, []uint32{1}, nil, true}, // a data offset that is no byte of the payload
	}
	for i, c := range cases {
		got, err := substreamStarts(payload, SliceHeader{DataOffset: c.off, EntryPoints: c.eps})
		if (err != nil) != c.bad {
			t.Errorf("case %d: err %v, want bad %v", i, err, c.bad)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("case %d: %v, want %v", i, got, c.want)
			continue
		}
		for j := range got {
			if got[j] != c.want[j] {
				t.Errorf("case %d: %v, want %v", i, got, c.want)
			}
		}
	}
}
