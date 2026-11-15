// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/go-avkit/h265/internal/cabac"
	"github.com/go-avkit/h265/internal/cabac/cabactest"
)

// The writer of coding_unit( ) and prediction_unit( ) (7.3.8.5 to 7.3.8.7, 7.3.8.9)
// for the walk of slicedata_test.go. Its binarizations are the bin strings of
// Tables 9-45 to 9-47 written out, and its transform tree is one bypass bin.

func (w *walkWriter) markSkip(x0, y0, size int, skip bool) {
	m := uint(w.sps.Log2MinCbSize)
	for y := y0 >> m; y < min((y0+size)>>m, (int(w.sps.CodedHeight)+1<<m-1)>>m); y++ {
		for x := x0 >> m; x < min((x0+size)>>m, w.cbW); x++ {
			w.skip[y*w.cbW+x] = skip
		}
	}
}

// bins writes a bin string, binIdx i using ctx(i), or bypass when that is -1.
func (w *walkWriter) bins(s string, ctx func(i int) int) {
	for i, c := range s {
		b := int(c - '0')
		if k := ctx(i); k < 0 {
			w.e.Bypass(b)
		} else {
			w.bin(k, b)
		}
	}
}

func trString(v, cMax int) string {
	s := ""
	for i := 0; i < v; i++ {
		s += "1"
	}
	if v < cMax {
		s += "0"
	}
	return s
}

func (w *walkWriter) writePartMode(intra bool, log2 int) PartMode {
	type opt struct {
		mode PartMode
		bins string
	}
	minCb := int(w.sps.Log2MinCbSize)
	var opts []opt
	switch {
	case intra:
		opts = []opt{{Part2Nx2N, "1"}, {PartNxN, "0"}}
	case log2 == minCb && log2 == 3:
		opts = []opt{{Part2Nx2N, "1"}, {Part2NxN, "01"}, {PartNx2N, "00"}}
	case log2 == minCb:
		opts = []opt{{Part2Nx2N, "1"}, {Part2NxN, "01"}, {PartNx2N, "001"}, {PartNxN, "000"}}
	case !w.sps.AMPEnabled:
		opts = []opt{{Part2Nx2N, "1"}, {Part2NxN, "01"}, {PartNx2N, "00"}}
	default:
		opts = []opt{{Part2Nx2N, "1"}, {Part2NxN, "011"}, {PartNx2N, "001"}, {Part2NxnU, "0100"},
			{Part2NxnD, "0101"}, {PartnLx2N, "0000"}, {PartnRx2N, "0001"}}
	}
	o := opts[w.rng.IntN(len(opts))]
	w.bins(o.bins, func(i int) int {
		switch {
		case intra || log2 == minCb || i < 2:
			return cabac.CtxPartMode + i
		case i == 2:
			return cabac.CtxPartMode + 3
		}
		return -1
	})
	return o.mode
}

func (w *walkWriter) writePCM(cu *CodingUnit) {
	s := &w.sps
	n := 1 << (cu.Log2Size << 1)
	cn := 0
	if s.ChromaFormat != 0 {
		cn = 2 * n / [4]int{1, 4, 2, 1}[s.ChromaFormat]
	}
	var bits []int
	put := func(depth int) uint16 {
		v := uint16(w.rng.IntN(1 << depth))
		for i := depth - 1; i >= 0; i-- {
			bits = append(bits, int(v>>uint(i))&1)
		}
		return v
	}
	cu.PcmLuma = make([]uint16, n)
	for i := range cu.PcmLuma {
		cu.PcmLuma[i] = put(int(s.PCM.BitDepthLuma))
	}
	cu.PcmChroma = make([]uint16, cn)
	for i := range cu.PcmChroma {
		cu.PcmChroma[i] = put(int(s.PCM.BitDepthChroma))
	}
	for len(bits)%8 != 0 {
		bits = append(bits, 0)
	}
	w.prefix = append(w.prefix, w.e.Bytes()...) // the flush of pcm_flag, then pcm_alignment_zero_bits
	for i := 0; i < len(bits); i += 8 {
		var b byte
		for j := 0; j < 8; j++ {
			b = b<<1 | byte(bits[i+j])
		}
		w.prefix = append(w.prefix, b)
	}
	if w.pcmBad { // what follows the samples starts with nine one bits, which no engine may start on
		w.prefix = append(w.prefix, 0xff, 0xff)
	}
	w.e = cabactest.New()
}

func (w *walkWriter) writeIntraModes(cu *CodingUnit) {
	n := 1
	if cu.Part == PartNxN {
		n = 4
	}
	for i := 0; i < n; i++ {
		cu.PrevIntraLumaPred[i] = w.rng.IntN(2) == 0
		w.bin(cabac.CtxPrevIntraLumaPred, b2n(cu.PrevIntraLumaPred[i]))
	}
	for i := 0; i < n; i++ {
		if cu.PrevIntraLumaPred[i] {
			cu.MpmIdx[i] = uint8(w.rng.IntN(3))
			w.bins(trString(int(cu.MpmIdx[i]), 2), func(int) int { return -1 })
		} else {
			cu.RemIntraLumaPredMode[i] = uint8(w.rng.IntN(32))
			w.e.BypassBits(uint32(cu.RemIntraLumaPredMode[i]), 5)
		}
	}
	chroma := func(i int) {
		v := w.rng.IntN(5)
		cu.IntraChromaPredMode[i] = uint8(v)
		if v == 4 {
			w.bin(cabac.CtxIntraChromaPredMode, 0)
			return
		}
		w.bin(cabac.CtxIntraChromaPredMode, 1)
		w.e.BypassBits(uint32(v), 2)
	}
	switch w.sps.ChromaFormat {
	case 0:
	case 3:
		for i := 0; i < n; i++ {
			chroma(i)
		}
	default:
		chroma(0)
	}
}

func (w *walkWriter) writeMvd(out *[2]int32) {
	if w.badMvd != 0 {
		// abs_mvd_greater0_flag[ 0 ], [ 1 ] = 1, 0 and abs_mvd_greater1_flag[ 0 ] = 1,
		// then an abs_mvd_minus2 the decoder must refuse.
		w.bin(cabac.CtxAbsMvdGreater0, 1)
		w.bin(cabac.CtxAbsMvdGreater0, 0)
		w.bin(cabac.CtxAbsMvdGreater1, 1)
		switch w.badMvd {
		case 1: // an Exp-Golomb prefix with no end
			for i := 0; i < 40; i++ {
				w.e.Bypass(1)
			}
		case 2: // 40000 + 2
			for _, b := range refEncEGk(40000, 1) {
				w.e.Bypass(b)
			}
			w.e.Bypass(0)
		case 3: // +32768, which only a negative sign allows
			for _, b := range refEncEGk(32766, 1) {
				w.e.Bypass(b)
			}
			w.e.Bypass(0)
		}
		return
	}
	var abs [2]int
	var neg [2]bool
	for c := range abs {
		switch w.rng.IntN(4) {
		case 0:
		case 1:
			abs[c] = 1
		case 2:
			abs[c] = 2 + w.rng.IntN(20)
		default:
			abs[c] = 2 + w.rng.IntN(32767)
		}
		neg[c] = abs[c] > 0 && w.rng.IntN(2) == 0
		if abs[c] == 32768 {
			neg[c] = true
		}
		out[c] = int32(abs[c])
		if neg[c] {
			out[c] = -out[c]
		}
	}
	zero := func(c int) int { return cabac.CtxAbsMvdGreater0 }
	w.bin(zero(0), b2n(abs[0] > 0))
	w.bin(zero(1), b2n(abs[1] > 0))
	for c := range abs {
		if abs[c] > 0 {
			w.bin(cabac.CtxAbsMvdGreater1, b2n(abs[c] > 1))
		}
	}
	for c := range abs {
		if abs[c] > 0 {
			if abs[c] > 1 {
				for _, b := range refEncEGk(uint64(abs[c]-2), 1) {
					w.e.Bypass(b)
				}
			}
			w.e.Bypass(b2n(neg[c]))
		}
	}
}

func (w *walkWriter) writePU(cu *CodingUnit, pu *PredictionUnit, x, y, pw, ph, depth int) {
	h := &w.hdr
	*pu = PredictionUnit{X: x, Y: y, W: pw, H: ph}
	maxMerge := int(h.Refs.MaxMergeCand)
	if cu.Mode == ModeSkip {
		pu.Merge = true
	} else {
		pu.Merge = w.rng.IntN(2) == 0
		w.bin(cabac.CtxMergeFlag, b2n(pu.Merge))
	}
	if pu.Merge {
		if maxMerge > 1 {
			pu.MergeIdx = uint8(w.rng.IntN(maxMerge))
			w.bins(trString(int(pu.MergeIdx), maxMerge-1), func(i int) int {
				if i == 0 {
					return cabac.CtxMergeIdx
				}
				return -1
			})
		}
		return
	}
	if h.Type == SliceB {
		pu.Idc = InterPredIdc(w.rng.IntN(3))
		if pw+ph == 12 && pu.Idc == PredBi {
			pu.Idc = PredL1
		}
		bins := [3]string{"00", "01", "1"}[pu.Idc]
		if pw+ph == 12 {
			bins = [2]string{"0", "1"}[pu.Idc]
		}
		w.bins(bins, func(i int) int {
			if pw+ph == 12 || i == 1 {
				return cabac.CtxInterPredIdc + 4
			}
			return cabac.CtxInterPredIdc + depth
		})
	}
	for list := 0; list < 2; list++ {
		if (list == 0 && pu.Idc == PredL1) || (list == 1 && pu.Idc == PredL0) {
			continue
		}
		active := int(h.Refs.NumRefIdxL0Active)
		if list == 1 {
			active = int(h.Refs.NumRefIdxL1Active)
		}
		if active > 1 {
			pu.RefIdx[list] = uint8(w.rng.IntN(active))
			w.bins(trString(int(pu.RefIdx[list]), active-1), func(i int) int {
				if i < 2 {
					return cabac.CtxRefIdx + i
				}
				return -1
			})
		}
		if !(list == 1 && h.Refs.MvdL1Zero && pu.Idc == PredBi) {
			w.writeMvd(&pu.Mvd[list])
		}
		pu.MvpFlag[list] = uint8(w.rng.IntN(2))
		w.bin(cabac.CtxMvpFlag, int(pu.MvpFlag[list]))
	}
}

func (w *walkWriter) writeCU(x0, y0, log2, depth int) {
	s, h := &w.sps, &w.hdr
	cu := CodingUnit{X: x0, Y: y0, Log2Size: log2}
	if w.pps.TransquantBypass {
		cu.TransquantBypass = w.rng.IntN(2) == 0
		w.bin(cabac.CtxCUTransquantBypass, b2n(cu.TransquantBypass))
	}
	n := 1 << log2
	skip := false
	if h.Type != SliceI {
		inc := 0
		if w.avail([2]int{x0, y0}, [2]int{x0 - 1, y0}) && w.skip[w.cell(x0-1, y0)] {
			inc++
		}
		if w.avail([2]int{x0, y0}, [2]int{x0, y0 - 1}) && w.skip[w.cell(x0, y0-1)] {
			inc++
		}
		skip = w.rng.IntN(4) == 0
		w.bin(cabac.CtxCUSkip+inc, b2n(skip))
	}
	w.markSkip(x0, y0, n, skip)
	w.setIMode(x0, y0, n, 1)
	if skip {
		cu.Mode, cu.NumPU = ModeSkip, 1
		w.writePU(&cu, &cu.PU[0], x0, y0, n, n, depth)
		w.expect = append(w.expect, cu)
		return
	}
	cu.Mode = ModeIntra
	if h.Type != SliceI {
		inter := w.rng.IntN(2) == 0
		w.bin(cabac.CtxPredMode, b2n(!inter))
		if inter {
			cu.Mode = ModeInter
		}
	}
	if cu.Mode != ModeIntra || log2 == int(s.Log2MinCbSize) {
		cu.Part = w.writePartMode(cu.Mode == ModeIntra, log2)
	}
	if cu.Mode == ModeIntra {
		cu.IntraSplit = cu.Part == PartNxN
		if cu.Part == Part2Nx2N && s.PCM.Enabled && log2 >= int(s.PCM.Log2MinCbSize) && log2 <= int(s.PCM.Log2MaxCbSize) {
			cu.PCM = w.rng.IntN(3) == 0
			w.e.Terminate(b2n(cu.PCM))
		}
		if cu.PCM {
			w.writePCM(&cu)
		} else {
			w.writeIntraModes(&cu)
			w.deriveModes(&cu)
		}
	} else {
		h2 := n / 2
		type blk struct{ x, y, w, h int }
		var bs []blk
		switch cu.Part {
		case Part2Nx2N:
			bs = []blk{{0, 0, n, n}}
		case Part2NxN:
			bs = []blk{{0, 0, n, h2}, {0, h2, n, h2}}
		case PartNx2N:
			bs = []blk{{0, 0, h2, n}, {h2, 0, h2, n}}
		case Part2NxnU:
			bs = []blk{{0, 0, n, n / 4}, {0, n / 4, n, 3 * n / 4}}
		case Part2NxnD:
			bs = []blk{{0, 0, n, 3 * n / 4}, {0, 3 * n / 4, n, n / 4}}
		case PartnLx2N:
			bs = []blk{{0, 0, n / 4, n}, {n / 4, 0, 3 * n / 4, n}}
		case PartnRx2N:
			bs = []blk{{0, 0, 3 * n / 4, n}, {3 * n / 4, 0, n / 4, n}}
		default:
			bs = []blk{{0, 0, h2, h2}, {h2, 0, h2, h2}, {0, h2, h2, h2}, {h2, h2, h2, h2}}
		}
		cu.NumPU = len(bs)
		for i, b := range bs {
			w.writePU(&cu, &cu.PU[i], x0+b.x, y0+b.y, b.w, b.h, depth)
		}
	}
	cu.RqtRootCbf = true
	if !cu.PCM {
		if cu.Mode != ModeIntra && !(cu.Part == Part2Nx2N && cu.PU[0].Merge) {
			cu.RqtRootCbf = w.rng.IntN(3) != 0
			w.bin(cabac.CtxRqtRootCbf, b2n(cu.RqtRootCbf))
		}
		if cu.RqtRootCbf {
			switch {
			case cu.Mode == ModeIntra && cu.IntraSplit:
				cu.MaxTrafoDepth = int(s.MaxTransformHierarchyDepthIntra) + 1
			case cu.Mode == ModeIntra:
				cu.MaxTrafoDepth = int(s.MaxTransformHierarchyDepthIntra)
			default:
				cu.MaxTrafoDepth = int(s.MaxTransformHierarchyDepthInter)
			}
			if w.fullTree {
				w.expect = append(w.expect, cu) // the units are reported before their tree is read
				w.writeTree(&cu, cu.X, cu.Y, cu.X, cu.Y, cu.Log2Size, 0, 0, [2]bool{}, [2]bool{})
				return
			}
			bit := w.rng.IntN(2)
			w.e.Bypass(bit)
			w.treeBits = append(w.treeBits, bit)
		}
	}
	w.expect = append(w.expect, cu)
}

// cuCase is a random picture and slices whose coding units carry real syntax.
func cuCase(t *testing.T, rng *rand.Rand) (*walkWriter, *PictureState, []segPlan) {
	t.Helper()
	ww, ps, plans := walkCase(t, rng)
	ww.sps.AMPEnabled = rng.IntN(2) == 0
	if rng.IntN(2) == 0 {
		hi := min(int(ww.sps.Log2CtbSize), 5)
		lo := 3 + rng.IntN(hi-2)
		ww.sps.PCM.Enabled = true
		ww.sps.PCM.Log2MinCbSize = uint8(lo)
		ww.sps.PCM.Log2MaxCbSize = uint8(lo + rng.IntN(hi-lo+1))
		ww.sps.PCM.BitDepthLuma = uint8(1 + rng.IntN(int(ww.sps.BitDepthLuma)))
		ww.sps.PCM.BitDepthChroma = uint8(1 + rng.IntN(int(ww.sps.BitDepthChroma)))
	}
	ps.SPS = ww.sps
	ww.pps.TransquantBypass = rng.IntN(2) == 0
	for i := range plans {
		h := &plans[i].hdr
		if plans[i].dependent {
			prev := plans[i-1].hdr
			h.Refs.MaxMergeCand, h.Refs.NumRefIdxL0Active, h.Refs.NumRefIdxL1Active, h.Refs.MvdL1Zero =
				prev.Refs.MaxMergeCand, prev.Refs.NumRefIdxL0Active, prev.Refs.NumRefIdxL1Active, prev.Refs.MvdL1Zero
			continue
		}
		h.Refs.MaxMergeCand = uint8(1 + rng.IntN(5))
		h.Refs.NumRefIdxL0Active, h.Refs.NumRefIdxL1Active = uint32(1+rng.IntN(4)), uint32(1+rng.IntN(4))
		h.Refs.MvdL1Zero = rng.IntN(2) == 0
	}
	ww.cu = ww.writeCU
	return ww, ps, plans
}

func TestCodingUnitSyntax(t *testing.T) {
	rng := rand.New(rand.NewPCG(71, 72))
	kinds := map[PartMode]int{}
	var pcm, skips, intraNxN, bi, mvdBig int
	for iter := 0; iter < 600; iter++ {
		ww, ps, plans := cuCase(t, rng)
		var got []CodingUnit
		var gotTree []int
		tree := func(w *SliceWalker, cu *CodingUnit) error {
			gotTree = append(gotTree, int(w.D.Bypass()))
			return nil
		}
		dec := CodingUnitDecoder(tree, func(cu *CodingUnit) { got = append(got, *cu) })
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, dec); err != nil {
				n := len(got)
				var last, next any
				if n > 0 {
					last = got[n-1]
				}
				if n < len(ww.expect) {
					next = ww.expect[n]
				}
				t.Fatalf("iter %d segment %d of %d: %v\nentry points %v data offset %d\npayload %x\n%d units decoded\nlast decoded %+v\nnext written %+v\ntree bins %v written %v",
					iter, si, len(plans), err, hdr.EntryPoints, hdr.DataOffset, payload, n, last, next, gotTree, ww.treeBits)
			}
		}
		if len(got) != len(ww.expect) {
			t.Fatalf("iter %d: %d coding units, written %d", iter, len(got), len(ww.expect))
		}
		for i := range got {
			if !reflect.DeepEqual(got[i], ww.expect[i]) {
				t.Fatalf("iter %d: coding unit %d is\n%+v\nwritten\n%+v", iter, i, got[i], ww.expect[i])
			}
			c := got[i]
			kinds[c.Part]++
			if c.PCM {
				pcm++
			}
			if c.Mode == ModeSkip {
				skips++
			}
			if c.Mode == ModeIntra && c.Part == PartNxN {
				intraNxN++
			}
			for k := 0; k < c.NumPU; k++ {
				if c.PU[k].Idc == PredBi && !c.PU[k].Merge {
					bi++
				}
				if v := c.PU[k].Mvd[0][0]; v > 1<<14 || v < -(1<<14) {
					mvdBig++
				}
			}
		}
		if !reflect.DeepEqual(gotTree, ww.treeBits) {
			t.Fatalf("iter %d: transform tree bins %v, written %v", iter, gotTree, ww.treeBits)
		}
		if !reflect.DeepEqual(ps.CuSkip, ww.skip) {
			t.Fatalf("iter %d: cu_skip_flag map differs", iter)
		}
	}
	t.Logf("part modes %v pcm %d skip %d intra NxN %d bi %d big mvd %d", kinds, pcm, skips, intraNxN, bi, mvdBig)
	if len(kinds) != 8 || pcm == 0 || skips == 0 || intraNxN == 0 || bi == 0 || mvdBig == 0 {
		t.Error("a syntax case was never exercised")
	}
}

func TestCodingUnitRefusals(t *testing.T) {
	boom := errors.New("boom")
	// A failing transform tree fails the slice; none at all is refused.
	var ww *walkWriter
	var ps *PictureState
	var plans []segPlan
	var payload []byte
	var hdr SliceHeader
	for seed := uint64(1); ; seed++ {
		ww, ps, plans = cuCase(t, rand.New(rand.NewPCG(seed, seed+9)))
		payload, hdr = ww.writeSegment(plans[0])
		if len(ww.treeBits) > 0 {
			break
		}
	}
	fresh := func() *PictureState { return NewPictureState(ps.SPS, ps.Layout) }
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, hdr, ww.pps, CodingUnitDecoder(func(*SliceWalker, *CodingUnit) error { return boom }, nil)); !errors.Is(err, boom) {
		t.Errorf("a failing transform tree: %v", err)
	}
	if err := DecodeSliceData(fresh(), Unit{Payload: payload}, hdr, ww.pps, CodingUnitDecoder(nil, nil)); !errors.Is(err, ErrSliceData) {
		t.Errorf("no transform tree decoder: %v", err)
	}
}

func TestCodingUnitMvdRefused(t *testing.T) {
	want := map[int]string{1: "abs_mvd_minus2", 2: "motion vector difference", 3: "motion vector difference"}
	hit := map[int]int{}
	for bad, msg := range want {
		for seed := uint64(1); seed < 60; seed++ {
			ww, ps, plans := cuCase(t, rand.New(rand.NewPCG(seed, seed+9)))
			ww.badMvd = bad
			payload, hdr := ww.writeSegment(plans[0])
			tree := func(w *SliceWalker, cu *CodingUnit) error { w.D.Bypass(); return nil }
			err := DecodeSliceData(ps, Unit{Payload: payload}, hdr, ww.pps, CodingUnitDecoder(tree, nil))
			if sliceErrHas(err, msg) {
				hit[bad]++
			}
		}
		if hit[bad] == 0 {
			t.Errorf("mode %d: %q never refused", bad, msg)
		}
	}
}

// Cutting a stream short at every length finds a PCM block whose samples are not
// all there; one followed by bits the engine may not start on is refused.
func TestCodingUnitPCMTruncated(t *testing.T) {
	var runPast, restart int
	for seed := uint64(1); seed < 80; seed++ {
		ww, ps, plans := cuCase(t, rand.New(rand.NewPCG(seed, seed+9)))
		ww.pcmBad = true
		tree := func(w *SliceWalker, cu *CodingUnit) error { w.D.Bypass(); return nil }
		bad, badHdr := ww.writeSegment(plans[0])
		if err := DecodeSliceData(ps, Unit{Payload: bad}, badHdr, ww.pps, CodingUnitDecoder(tree, nil)); sliceErrHas(err, "after the PCM samples") {
			restart++
		}
		ww, ps, plans = cuCase(t, rand.New(rand.NewPCG(seed, seed+9)))
		payload, hdr := ww.writeSegment(plans[0])
		if len(hdr.EntryPoints) > 0 {
			continue
		}
		for n := len(payload) - 1; n > 0; n-- {
			err := DecodeSliceData(NewPictureState(ps.SPS, ps.Layout), Unit{Payload: payload[:n]}, hdr, ww.pps, CodingUnitDecoder(tree, nil))
			if sliceErrHas(err, "PCM samples run past") {
				runPast++
			}
		}
	}
	if runPast == 0 || restart == 0 {
		t.Errorf("run past %d, restart %d", runPast, restart)
	}
}
