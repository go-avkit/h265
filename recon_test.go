// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"
)

// The reference reconstruction of intra pictures: 8.4.1 and 8.4.4.1 followed over the
// coding units and transform units the writer wrote, with the intra prediction of
// 8.4.4.2 as refPredict has it, the residual as refResidual has it, and the
// neighbouring samples "available for intra prediction" decided not by the z-scan
// address of 6.4.1 but by whether a sample has been reconstructed, in the same slice
// and tile.

type refRecon struct {
	ww      *walkWriter
	frame   *DecodedPicture
	known   [3][]bool
	seq     [3][]int // the order in which each sample's block was reconstructed
	blocks  int
	factors *ScalingFactors
	subW    [3]int
	subH    [3]int
}

func newRefRecon(ww *walkWriter) *refRecon {
	r := &refRecon{ww: ww, frame: NewDecodedPicture(ww.sps), factors: ScalingFactorsOf(ww.sps, ww.pps)}
	for c := 0; c < r.frame.Components; c++ {
		r.known[c] = make([]bool, r.frame.Planes[c].Len())
		r.seq[c] = make([]int, r.frame.Planes[c].Len())
		r.subW[c], r.subH[c] = int(ww.sps.CodedWidth)/r.frame.Planes[c].Width, int(ww.sps.CodedHeight)/r.frame.Planes[c].Height
	}
	return r
}

func (r *refRecon) set(c, x, y int, v uint16) {
	pl := &r.frame.Planes[c]
	pl.set(y*pl.Stride+x, int(v))
	r.known[c][y*pl.Stride+x] = true
	r.seq[c][y*pl.Stride+x] = r.blocks
}

// sameSliceAndTile says whether the sample ( xn, yn ) of component c lies in the
// slice and tile of the one at ( xc, yc ).
func (r *refRecon) sameSliceAndTile(c, xn, yn, xc, yc int) bool {
	w := r.ww
	ctb := uint(w.sps.Log2CtbSize)
	rsN := ((yn*r.subH[c])>>ctb)*w.l.WidthCtbs + (xn*r.subW[c])>>ctb
	rsC := ((yc*r.subH[c])>>ctb)*w.l.WidthCtbs + (xc*r.subW[c])>>ctb
	return w.sliceOf[rsN] == w.sliceOf[rsC] && w.l.TileID[w.l.RsToTs[rsN]] == w.l.TileID[w.l.RsToTs[rsC]]
}

func (r *refRecon) refBlock(c, x, y, log2, mode int, res []int32, cu *CodingUnit) {
	w := r.ww
	n := 1 << uint(log2)
	pl := &r.frame.Planes[c]
	pm, am := pmap{}, map[[2]int]bool{}
	for yy := -1; yy <= 2*n-1; yy++ {
		xn, yn := x-1, y+yy
		ok := xn >= 0 && yn >= 0 && yn < pl.Height && r.known[c][yn*pl.Stride+xn] && r.sameSliceAndTile(c, xn, yn, x, y)
		am[[2]int{-1, yy}] = ok
		if ok {
			pm[[2]int{-1, yy}] = int32(pl.get(yn*pl.Stride + xn))
		}
	}
	for xx := 0; xx <= 2*n-1; xx++ {
		xn, yn := x+xx, y-1
		ok := yn >= 0 && xn < pl.Width && r.known[c][yn*pl.Stride+xn] && r.sameSliceAndTile(c, xn, yn, x, y)
		am[[2]int{xx, -1}] = ok
		if ok {
			pm[[2]int{xx, -1}] = int32(pl.get(yn*pl.Stride + xn))
		}
	}
	bd := r.frame.BitDepth[c]
	refSubstitute(pm, am, n, bd)
	q := IntraParams{
		Log2Size: log2, CIdx: c, Mode: mode, BitDepth: bd,
		FilterRefs:       !w.sps.Range.IntraSmoothingDisabled && (c == 0 || w.sps.ChromaFormat == 3),
		Strong:           w.sps.StrongIntraSmoothing,
		NoBoundaryFilter: w.sps.Range.ImplicitRDPCM && cu.TransquantBypass,
	}
	r.blocks++
	pred := refPredict(pm, q)
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			v := min(max(pred[j*n+i]+res[j*n+i], 0), 1<<uint(bd)-1)
			r.set(c, x+i, y+j, uint16(v))
		}
	}
}

// refResidualOf is the residual of a coded block with 8.4.4.1's implicit RDPCM.
func (r *refRecon) refResidualOf(cu *CodingUnit, tu *TransformUnit, b *TUBlock, c, mode int) []int32 {
	w := r.ww
	bp := BlockParams{
		Log2Size: b.Block.Log2Size, CIdx: c, QP: int(tu.QpPrime[c]), BitDepth: r.frame.BitDepth[c],
		ExtendedPrecision: w.sps.Range.ExtendedPrecisionProcessing, TransquantBypass: cu.TransquantBypass,
		TransformSkip: b.Block.TransformSkip, Intra: true, RotateEnabled: w.sps.Range.TransformSkipRotation,
	}
	if r.factors != nil {
		bp.Factor = r.factors[b.Block.Log2Size-2][c]
	}
	res := refResidual(b.Block.Levels, bp)
	n := 1 << uint(b.Block.Log2Size)
	if w.sps.Range.ImplicitRDPCM && (b.Block.TransformSkip || cu.TransquantBypass) && (mode == 10 || mode == 26) {
		if mode == 10 { // horizontal: along each row
			for y := 0; y < n; y++ {
				for x := 1; x < n; x++ {
					res[y*n+x] += res[y*n+x-1]
				}
			}
		} else {
			for y := 1; y < n; y++ {
				for x := 0; x < n; x++ {
					res[y*n+x] += res[(y-1)*n+x]
				}
			}
		}
	}
	return res
}

func (r *refRecon) unit(cu *CodingUnit, tu *TransformUnit) {
	w := r.ww
	cat := int(w.sps.ChromaFormat)
	part := 0
	if cu.IntraSplit {
		part = b2n(tu.X0-cu.X >= 1<<(cu.Log2Size-1)) + 2*b2n(tu.Y0-cu.Y >= 1<<(cu.Log2Size-1))
	}
	n := 1 << uint(tu.Log2Size)
	byComp := func(c int) (out []*TUBlock) {
		for i := range tu.Blocks {
			if tu.Blocks[i].Block.CIdx == c {
				out = append(out, &tu.Blocks[i])
			}
		}
		return
	}
	rY := make([]int32, n*n)
	if lb := byComp(0); len(lb) == 1 {
		rY = r.refResidualOf(cu, tu, lb[0], 0, int(cu.IntraPredModeY[part]))
	}
	r.refBlock(0, tu.X0, tu.Y0, tu.Log2Size, int(cu.IntraPredModeY[part]), rY, cu)
	if cat == 0 {
		return
	}
	var xC, yC, log2C int
	switch {
	case tu.Log2Size > 2 || cat == 3:
		xC, yC, log2C = tu.X0/r.subW[1], tu.Y0/r.subH[1], max(2, tu.Log2Size-b2n(cat != 3))
	case tu.BlkIdx == 3:
		xC, yC, log2C = tu.XBase/r.subW[1], tu.YBase/r.subH[1], 2
	default:
		return
	}
	nC := 1 << uint(log2C)
	flags := [3][2]bool{{}, tu.CbfCb, tu.CbfCr}
	for c := 1; c <= 2; c++ {
		blocks := byComp(c)
		next := 0
		for t := 0; t < map[bool]int{false: 1, true: 2}[cat == 2]; t++ {
			rc := make([]int32, nC*nC)
			if flags[c][t] {
				rc = r.refResidualOf(cu, tu, blocks[next], c, int(cu.IntraPredModeC[part]))
				next++
			}
			if cat == 3 && w.pps.Range.CrossComponentPrediction {
				for i := range rc {
					rc[i] += int32((int64(tu.ResScaleVal[c-1]) * ((int64(rY[i]) << uint(r.frame.BitDepth[1])) >> uint(r.frame.BitDepth[0]))) >> 3)
				}
			}
			r.refBlock(c, xC, yC+t*nC, log2C, int(cu.IntraPredModeC[part]), rc, cu)
		}
	}
}

func (r *refRecon) pcm(cu *CodingUnit) {
	s := &r.ww.sps
	n := 1 << uint(cu.Log2Size)
	for j := 0; j < n; j++ {
		for i := 0; i < n; i++ {
			r.set(0, cu.X+i, cu.Y+j, cu.PcmLuma[n*j+i]<<uint(int(s.BitDepthLuma)-int(s.PCM.BitDepthLuma)))
		}
	}
	for c := 1; c < r.frame.Components; c++ {
		cw, ch := n/r.subW[c], n/r.subH[c]
		for j := 0; j < ch; j++ {
			for i := 0; i < cw; i++ {
				r.set(c, cu.X/r.subW[c]+i, cu.Y/r.subH[c]+j, cu.PcmChroma[cw*(j+(c-1)*ch)+i]<<uint(int(s.BitDepthChroma)-int(s.PCM.BitDepthChroma)))
			}
		}
	}
}

// reconCase is a picture of intra slices with the tools of intra reconstruction
// switched on at random: scaling lists, strong and disabled smoothing, rotation.
func reconCase(t *testing.T, rng *rand.Rand) (*walkWriter, *PictureState, []segPlan) {
	t.Helper()
	ww, ps, plans := ttCase(t, rng)
	for i := range plans {
		plans[i].hdr.Type = SliceI
	}
	s, p := &ww.sps, &ww.pps
	s.Range.IntraSmoothingDisabled = rng.IntN(4) == 0
	s.StrongIntraSmoothing = rng.IntN(2) == 0
	s.Range.TransformSkipRotation = rng.IntN(2) == 0
	if s.ScalingListEnabled = rng.IntN(2) == 0; s.ScalingListEnabled {
		switch rng.IntN(3) {
		case 0:
			s.ScalingList = writeLists(&writer{}, rng)
		case 1:
			p.ScalingList = writeLists(&writer{}, rng)
		}
	}
	ps.SPS = ww.sps
	return ww, ps, plans
}

func TestIntraReconstructionAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(141, 142))
	var pcm, tus, nxn int
	for iter := 0; iter < 400; iter++ {
		ww, ps, plans := reconCase(t, rng)
		frame := NewDecodedPicture(ww.sps)
		rec := NewReconstructor(frame, ww.sps, ww.pps)
		for si, p := range plans {
			payload, hdr := ww.writeSegment(p)
			if err := DecodeSliceData(ps, Unit{Type: UnitTrailR, Payload: payload}, hdr, ww.pps, rec.Decoder()); err != nil {
				t.Fatalf("iter %d segment %d: %v", iter, si, err)
			}
		}
		ref := newRefRecon(ww)
		for ci := range ww.expect {
			cu := &ww.expect[ci]
			if cu.PCM {
				ref.pcm(cu)
				pcm++
			}
			if cu.IntraSplit {
				nxn++
			}
			for ti := range ww.expectTU {
				if ww.tuCU[ti] == ci {
					ref.unit(cu, &ww.expectTU[ti])
					tus++
				}
			}
		}
		for c := 0; c < frame.Components; c++ {
			got, want := px(&frame.Planes[c]), px(&ref.frame.Planes[c])
			if !reflect.DeepEqual(got, want) {
				// The first difference in reconstruction order is the one that explains the rest.
				w, first := frame.Planes[c].Width, -1
				for i := range got {
					if got[i] != want[i] && (first < 0 || ref.seq[c][i] < ref.seq[c][first]) {
						first = i
					}
				}
				t.Fatalf("iter %d: component %d differs first, in reconstruction order, at (%d,%d) of block %d: %d, the text's %d",
					iter, c, first%w, first/w, ref.seq[c][first], got[first], want[first])
			}
		}
	}
	t.Logf("pcm %d, intra NxN %d, transform units %d", pcm, nxn, tus)
}

func TestReconstructionRefusesInterCodingUnits(t *testing.T) {
	// Any picture with a P or B slice reaches a coding unit that is not intra.
	for seed := uint64(1); seed < 50; seed++ {
		ww, ps, plans := ttCase(t, rand.New(rand.NewPCG(seed, seed+11)))
		payload, hdr := ww.writeSegment(plans[0])
		frame := NewDecodedPicture(ww.sps)
		rec := NewReconstructor(frame, ww.sps, ww.pps)
		_ = DecodeSliceData(ps, Unit{Payload: payload}, hdr, ww.pps, rec.Decoder())
		if errors.Is(rec.Err(), ErrNotReconstructed) {
			// and once refused, no later unit is reconstructed on top of it
			if err := rec.Unit(&SliceWalker{chroma: 1}, &CodingUnit{}, &TransformUnit{}); !errors.Is(err, ErrNotReconstructed) {
				t.Errorf("a unit after the refusal gave %v", err)
			}
			return
		}
	}
	t.Error("no inter coding unit was refused")
}

// A transform unit the syntax would not produce is refused rather than guessed at,
// and constrained intra prediction treats a block of another mode as unavailable.
func TestReconstructorRefusalsAndConstrainedIntra(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 64, height: 64, sizes: []uint32{0, 3, 0, 3, 0, 0}})
	pps := mustPPS(t, pset{})
	l, err := NewCtbLayout(sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	ps := NewPictureState(sps, l)
	ps.SliceAddr[0] = 0
	w := &SliceWalker{Pic: ps, SPS: sps, PPS: pps, chroma: 1}
	rec := NewReconstructor(NewDecodedPicture(sps), sps, pps)
	cu := &CodingUnit{Mode: ModeIntra, Log2Size: 3}
	bad := TUBlock{Block: ResidualBlock{Log2Size: 1}} // a 2x2 block: the scaling process refuses it

	if err := rec.Unit(w, cu, &TransformUnit{Log2Size: 3, CbfLuma: true}); err == nil {
		t.Error("a coded luma block that is not in the unit")
	}
	if err := rec.Unit(w, cu, &TransformUnit{Log2Size: 3, CbfLuma: true, Blocks: []TUBlock{bad}}); err == nil {
		t.Error("a luma block the scaling process refuses")
	}
	badC := bad
	badC.Block.CIdx = 1
	if err := rec.Unit(w, cu, &TransformUnit{Log2Size: 3, Blocks: []TUBlock{badC}}); err == nil {
		t.Error("a chroma block the scaling process refuses")
	}
	// Constrained intra prediction: the neighbours at the left lie in an inter coding unit.
	rec.pps.ConstrainedIntraPred = true
	ps.CuMode[0] = uint8(ModeInter)
	rec.block(w, cu, 0, 8, 0, 2, 1, make([]int32, 16))
}
