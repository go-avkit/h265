// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"testing"
)

// The models below are the equations of 8.5.3.3.3 and 8.5.3.3.4 written out one by one,
// with the letters of Figures 8-4 and 8-5, sharing nothing with the tables of inter.go.

// f8 are the three 8-tap sums of (8-224) to (8-226): x( k ) is the sample at offset k.
func f8(which int, x func(k int) int) int {
	switch which {
	case 1:
		return -x(-3) + 4*x(-2) - 10*x(-1) + 58*x(0) + 17*x(1) - 5*x(2) + x(3)
	case 2:
		return -x(-3) + 4*x(-2) - 11*x(-1) + 40*x(0) + 40*x(1) - 11*x(2) + 4*x(3) - x(4)
	}
	return x(-2) - 5*x(-1) + 17*x(0) + 58*x(1) - 10*x(2) + 4*x(3) - x(4)
}

// modelLuma is predSampleLX of 8.5.3.3.3.2: A( i, j ) is the clipped reference sample.
func modelLuma(A func(i, j int) int, xFrac, yFrac, bd int) int {
	shift1, shift2, shift3 := min(4, bd-8), 6, max(2, 14-bd)
	if xFrac == 0 && yFrac == 0 {
		return A(0, 0) << uint(shift3)
	}
	// a, b and c at row j (8-224 to 8-226), for xFrac 1, 2 and 3.
	horiz := func(j int) int { return f8(xFrac, func(k int) int { return A(k, j) }) >> uint(shift1) }
	if yFrac == 0 {
		return horiz(0)
	}
	if xFrac == 0 { // d, h, n (8-227 to 8-229)
		return f8(yFrac, func(k int) int { return A(0, k) }) >> uint(shift1)
	}
	// e, i, p, f, j, q, g, k, r (8-230 to 8-238): the vertical filter of the row values
	return f8(yFrac, func(k int) int { return horiz(k) }) >> uint(shift2)
}

// modelChroma is predSampleLX of 8.5.3.3.3.3, the seven filters of (8-241) to (8-247).
func modelChroma(B func(i, j int) int, xFrac, yFrac, bd int) int {
	shift1, shift2, shift3 := min(4, bd-8), 6, max(2, 14-bd)
	f4 := func(frac int, x func(k int) int) int {
		switch frac {
		case 1:
			return -2*x(-1) + 58*x(0) + 10*x(1) - 2*x(2)
		case 2:
			return -4*x(-1) + 54*x(0) + 16*x(1) - 2*x(2)
		case 3:
			return -6*x(-1) + 46*x(0) + 28*x(1) - 4*x(2)
		case 4:
			return -4*x(-1) + 36*x(0) + 36*x(1) - 4*x(2)
		case 5:
			return -4*x(-1) + 28*x(0) + 46*x(1) - 6*x(2)
		case 6:
			return -2*x(-1) + 16*x(0) + 54*x(1) - 4*x(2)
		}
		return -2*x(-1) + 10*x(0) + 58*x(1) - 2*x(2)
	}
	switch {
	case xFrac == 0 && yFrac == 0:
		return B(0, 0) << uint(shift3)
	case yFrac == 0:
		return f4(xFrac, func(k int) int { return B(k, 0) }) >> uint(shift1)
	case xFrac == 0:
		return f4(yFrac, func(k int) int { return B(0, k) }) >> uint(shift1)
	}
	row := func(j int) int { return f4(xFrac, func(k int) int { return B(k, j) }) >> uint(shift1) }
	return f4(yFrac, row) >> uint(shift2)
}

func randomPlane(rng *rand.Rand, w, h, bd int) *Plane {
	pl := &Plane{Width: w, Height: h, Stride: w, Pix: make([]uint16, w*h)}
	for i := range pl.Pix {
		pl.Pix[i] = uint16(rng.IntN(1 << uint(bd)))
	}
	return pl
}

func clampedAt(pl *Plane, x, y int) int {
	return int(pl.Pix[min(max(y, 0), pl.Height-1)*pl.Stride+min(max(x, 0), pl.Width-1)])
}

func TestInterpolationAgainstTheEquations(t *testing.T) {
	rng := rand.New(rand.NewPCG(401, 402))
	var luma, chroma [8]int
	for iter := 0; iter < 3000; iter++ {
		bd := 8 + rng.IntN(9)
		pl := randomPlane(rng, 5+rng.IntN(20), 5+rng.IntN(20), bd)
		w, h := 1+rng.IntN(8), 1+rng.IntN(8)
		xInt, yInt := rng.IntN(pl.Width+30)-15, rng.IntN(pl.Height+30)-15
		out := make([]int32, w*h)
		isLuma := rng.IntN(2) == 0
		xFrac, yFrac := rng.IntN(4), rng.IntN(4)
		if !isLuma {
			xFrac, yFrac = rng.IntN(8), rng.IntN(8)
			interpolate(pl, xInt, yInt, xFrac, yFrac, w, h, bd, 1, chromaTaps[:], out)
			chroma[xFrac]++
		} else {
			interpolate(pl, xInt, yInt, xFrac, yFrac, w, h, bd, 3, lumaTaps[:], out)
			luma[xFrac]++
		}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				at := func(i, j int) int { return clampedAt(pl, xInt+x+i, yInt+y+j) }
				var want int
				if isLuma {
					want = modelLuma(at, xFrac, yFrac, bd)
				} else {
					want = modelChroma(at, xFrac, yFrac, bd)
				}
				if int(out[y*w+x]) != want {
					t.Fatalf("iter %d: luma %v bit depth %d, frac (%d,%d) at (%d,%d): %d, the equations give %d", iter, isLuma, bd, xFrac, yFrac, x, y, out[y*w+x], want)
				}
			}
		}
	}
	for i := 0; i < 4; i++ {
		if luma[i] == 0 {
			t.Errorf("luma horizontal fraction %d never exercised", i)
		}
	}
	for i := 0; i < 8; i++ {
		if chroma[i] == 0 {
			t.Errorf("chroma horizontal fraction %d never exercised", i)
		}
	}
}

// modelWeighted is one sample of 8.5.3.3.4.2 and 8.5.3.3.4.3.
func modelWeighted(p0, p1 int, f0, f1 bool, bd int, ew *explicitWeights) int {
	clip := func(v int) int { return min(max(v, 0), 1<<uint(bd)-1) }
	shift1 := max(2, 14-bd)
	if ew == nil {
		shift2, offset1 := max(3, 15-bd), 1<<uint(shift1-1)
		offset2 := 1 << uint(shift2-1)
		switch {
		case f0 && !f1:
			return clip((p0 + offset1) >> uint(shift1))
		case !f0 && f1:
			return clip((p1 + offset1) >> uint(shift1))
		}
		return clip((p0 + p1 + offset2) >> uint(shift2))
	}
	log2Wd, w0, w1, o0, o1 := ew.log2Wd, int(ew.w[0]), int(ew.w[1]), int(ew.o[0]), int(ew.o[1])
	switch {
	case f0 && !f1:
		return clip(((p0*w0 + 1<<uint(log2Wd-1)) >> uint(log2Wd)) + o0)
	case !f0 && f1:
		return clip(((p1*w1 + 1<<uint(log2Wd-1)) >> uint(log2Wd)) + o1)
	}
	return clip((p0*w0 + p1*w1 + (o0+o1+1)<<uint(log2Wd)) >> uint(log2Wd+1))
}

func TestWeightedPredictionAgainstTheEquations(t *testing.T) {
	rng := rand.New(rand.NewPCG(403, 404))
	var kinds [6]int
	for iter := 0; iter < 4000; iter++ {
		bd := 8 + rng.IntN(9)
		w, h := 1+rng.IntN(6), 1+rng.IntN(6)
		pl := &Plane{Width: w + 3, Height: h + 2, Stride: w + 3, Pix: make([]uint16, (w+3)*(h+2))}
		var p [2][]int32
		for l := range p {
			p[l] = make([]int32, w*h)
			for i := range p[l] {
				p[l][i] = int32(rng.IntN(1<<15) - 1<<13) // beyond the range of valid samples on purpose
			}
		}
		flags := [2]bool{rng.IntN(2) == 0, rng.IntN(2) == 0}
		if !flags[0] && !flags[1] {
			flags[rng.IntN(2)] = true
		}
		var ew *explicitWeights
		if rng.IntN(2) == 0 {
			ew = &explicitWeights{log2Wd: max(2, 14-bd) + rng.IntN(8)}
			for l := 0; l < 2; l++ {
				ew.w[l], ew.o[l] = int32(rng.IntN(256)-128), int32(rng.IntN(1<<uint(min(bd, 12)))-1<<uint(min(bd, 12)-1))
			}
		}
		weighted(pl, 3, 2, w, h, p, flags, bd, ew)
		kind := 2 // both lists
		if !flags[1] {
			kind = 0
		} else if !flags[0] {
			kind = 1
		}
		kinds[b2n(ew != nil)*3+kind]++
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				want := modelWeighted(int(p[0][y*w+x]), int(p[1][y*w+x]), flags[0], flags[1], bd, ew)
				if got := int(pl.Pix[(2+y)*pl.Stride+3+x]); got != want {
					t.Fatalf("iter %d: bit depth %d flags %v explicit %v: %d, the equations give %d", iter, bd, flags, ew != nil, got, want)
				}
			}
		}
	}
	for i, n := range kinds {
		if n == 0 {
			t.Errorf("weighting kind %d never exercised", i)
		}
	}
}

// A coding unit's prediction blocks, predicted into the picture by the Reconstructor,
// against the equations composed: the chroma motion vector of (8-210), (8-211), the
// block positions of (8-218) to (8-221), and the weights of 8.5.3.3.4.3.
func TestInterPredictionOfACodingUnit(t *testing.T) {
	rng := rand.New(rand.NewPCG(405, 406))
	var explicit, bi, formats [4]int
	for iter := 0; iter < 1500; iter++ {
		chromaFormat := rng.IntN(4)
		sps := SPS{ChromaFormat: uint8(chromaFormat), CodedWidth: 64, CodedHeight: 48,
			BitDepthLuma: uint8(8 + rng.IntN(5)), BitDepthChroma: uint8(8 + rng.IntN(5))}
		sps.Range.HighPrecisionOffsets = rng.IntN(2) == 0
		frame := NewDecodedPicture(sps)
		sl := &InterSlice{Type: SliceB}
		for l := 0; l < 2; l++ {
			for i := 0; i < 3; i++ {
				ref := NewDecodedPicture(sps)
				for c := 0; c < ref.Components; c++ {
					ref.Planes[c] = *randomPlane(rng, ref.Planes[c].Width, ref.Planes[c].Height, ref.BitDepth[c])
				}
				sl.List[l] = append(sl.List[l], RefEntry{POC: int32(i), Pic: &DPBPicture{Frame: ref}})
			}
		}
		if rng.IntN(2) == 0 {
			pw := &PredWeights{LumaLog2Denom: uint8(rng.IntN(8)), ChromaLog2Denom: uint8(rng.IntN(8))}
			for l := 0; l < 2; l++ {
				list := make([]RefWeight, 3)
				for i := range list {
					list[i] = RefWeight{LumaWeight: int32(rng.IntN(256) - 128), LumaOffset: int32(rng.IntN(256) - 128),
						ChromaWeight: [2]int32{int32(rng.IntN(256) - 128), int32(rng.IntN(256) - 128)},
						ChromaOffset: [2]int32{int32(rng.IntN(256) - 128), int32(rng.IntN(256) - 128)}}
				}
				if l == 0 {
					pw.L0 = list
				} else {
					pw.L1 = list
				}
			}
			sl.Weights = pw
			explicit[0]++
		}
		rec := NewReconstructor(frame, sps, PPS{})
		rec.Inter = sl
		// a 16x16 coding unit split into four 8x8 blocks, each with its own motion
		cu := &CodingUnit{X: 16, Y: 16, Log2Size: 4, Mode: ModeInter, Part: PartNxN, NumPU: 4}
		for i := 0; i < 4; i++ {
			pu := &cu.PU[i]
			*pu = PredictionUnit{X: 16 + 8*(i%2), Y: 16 + 8*(i/2), W: 8, H: 8}
			m := &pu.Motion
			m.Pred = [2]bool{rng.IntN(2) == 0, rng.IntN(2) == 0}
			if !m.Pred[0] && !m.Pred[1] {
				m.Pred[rng.IntN(2)] = true
			}
			for l := 0; l < 2; l++ {
				m.RefIdx[l] = -1
				if m.Pred[l] {
					m.RefIdx[l] = int8(rng.IntN(3))
					m.MV[l] = [2]int16{int16(rng.IntN(161) - 80), int16(rng.IntN(161) - 80)}
				}
			}
			if m.Pred[0] && m.Pred[1] {
				bi[0]++
			}
		}
		rec.CodingUnit(cu)
		if err := rec.Err(); err != nil {
			t.Fatal(err)
		}
		formats[min(chromaFormat, 3)]++
		subW, subH := 1, 1
		if chromaFormat == 1 || chromaFormat == 2 {
			subW = 2
		}
		if chromaFormat == 1 {
			subH = 2
		}
		for i := 0; i < 4; i++ {
			pu := &cu.PU[i]
			for c := 0; c < frame.Components; c++ {
				sw, sh, bd := 1, 1, int(sps.BitDepthLuma)
				if c > 0 {
					sw, sh, bd = subW, subH, int(sps.BitDepthChroma)
				}
				w, h := pu.W/sw, pu.H/sh
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						var p [2]int
						for l := 0; l < 2; l++ {
							if !pu.Motion.Pred[l] {
								continue
							}
							ref := &sl.List[l][pu.Motion.RefIdx[l]].Pic.Frame.Planes[c]
							mv := [2]int{int(pu.Motion.MV[l][0]), int(pu.Motion.MV[l][1])}
							if c == 0 {
								at := func(i, j int) int { return clampedAt(ref, pu.X+(mv[0]>>2)+x+i, pu.Y+(mv[1]>>2)+y+j) }
								p[l] = modelLuma(at, mv[0]&3, mv[1]&3, bd)
							} else {
								mvC := [2]int{mv[0] * 2 / sw, mv[1] * 2 / sh}
								at := func(i, j int) int { return clampedAt(ref, pu.X/sw+(mvC[0]>>3)+x+i, pu.Y/sh+(mvC[1]>>3)+y+j) }
								p[l] = modelChroma(at, mvC[0]&7, mvC[1]&7, bd)
							}
						}
						var ew *explicitWeights
						if sl.Weights != nil {
							ew = &explicitWeights{}
							bdShift := bd - 8
							if sps.Range.HighPrecisionOffsets {
								bdShift = 0
							}
							denom := int(sl.Weights.LumaLog2Denom)
							if c > 0 {
								denom = int(sl.Weights.ChromaLog2Denom)
							}
							ew.log2Wd = denom + max(2, 14-bd)
							for l := 0; l < 2; l++ {
								if !pu.Motion.Pred[l] {
									continue
								}
								e := sl.Weights.L0[max(pu.Motion.RefIdx[l], 0)]
								if l == 1 {
									e = sl.Weights.L1[pu.Motion.RefIdx[l]]
								}
								if c == 0 {
									ew.w[l], ew.o[l] = e.LumaWeight, e.LumaOffset*(1<<uint(bdShift))
								} else {
									ew.w[l], ew.o[l] = e.ChromaWeight[c-1], e.ChromaOffset[c-1]*(1<<uint(bdShift))
								}
							}
						}
						want := modelWeighted(p[0], p[1], pu.Motion.Pred[0], pu.Motion.Pred[1], bd, ew)
						pl := &frame.Planes[c]
						if got := int(pl.Pix[(pu.Y/sh+y)*pl.Stride+pu.X/sw+x]); got != want {
							t.Fatalf("iter %d: format %d block %d component %d at (%d,%d): %d, the equations give %d", iter, chromaFormat, i, c, x, y, got, want)
						}
					}
				}
			}
		}
	}
	t.Logf("explicit %d bi %d formats %v", explicit[0], bi[0], formats)
	for f, n := range formats {
		if n == 0 {
			t.Errorf("chroma format %d never exercised", f)
		}
	}
}

func TestInterResidualIsAddedToThePrediction(t *testing.T) {
	rng := rand.New(rand.NewPCG(407, 408))
	sps := SPS{CodedWidth: 32, CodedHeight: 32, BitDepthLuma: 8, BitDepthChroma: 8}
	w := &SliceWalker{chroma: 0}
	for iter := 0; iter < 200; iter++ {
		frame := NewDecodedPicture(sps)
		pl := &frame.Planes[0]
		for i := range pl.Pix {
			pl.Pix[i] = uint16(rng.IntN(256))
		}
		before := append([]uint16(nil), pl.Pix...)
		levels := make([]int32, 64)
		for i := range levels {
			levels[i] = int32(rng.IntN(121) - 60)
		}
		rec := NewReconstructor(frame, sps, PPS{})
		cu := &CodingUnit{Mode: ModeInter, TransquantBypass: true}
		blk := ResidualBlock{Log2Size: 3, Levels: levels}
		switch iter % 3 {
		case 1:
			blk.ExplicitRDPCM = true
		case 2:
			blk.ExplicitRDPCM, blk.ExplicitRDPCMDir = true, true
		}
		tu := &TransformUnit{X0: 8, Y0: 8, Log2Size: 3, CbfLuma: true, Blocks: []TUBlock{{X: 8, Y: 8, Block: blk}}}
		if err := rec.Unit(w, cu, tu); err != nil {
			t.Fatal(err)
		}
		// the residual of a bypassed block is its levels, accumulated along the direction
		// of an explicit RDPCM (8.6.8): rows for 0, columns for 1
		res := append([]int32(nil), levels...)
		for y := 0; y < 8; y++ {
			for x := 0; x < 8; x++ {
				switch {
				case iter%3 == 1 && x > 0:
					res[y*8+x] += res[y*8+x-1]
				case iter%3 == 2 && y > 0:
					res[y*8+x] += res[(y-1)*8+x]
				}
			}
		}
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				want := int(before[y*32+x])
				if x >= 8 && x < 16 && y >= 8 && y < 16 {
					want = min(max(want+int(res[(y-8)*8+x-8]), 0), 255)
				}
				if int(pl.Pix[y*32+x]) != want {
					t.Fatalf("iter %d: sample (%d,%d) is %d, wanted %d", iter, x, y, pl.Pix[y*32+x], want)
				}
			}
		}
	}
}

// An inter block is scaled with the inter matrix of the scaling list (matrixId 3 to 5).
func TestInterBlocksUseTheInterScalingMatrices(t *testing.T) {
	sps := SPS{CodedWidth: 32, CodedHeight: 32, BitDepthLuma: 8, BitDepthChroma: 8}
	var f ScalingFactors
	for m := 0; m < 6; m++ {
		f[1][m] = make([]uint8, 64)
		for i := range f[1][m] {
			f[1][m][i] = uint8(8 + 3*m + i%5)
		}
	}
	levels := make([]int32, 64)
	levels[0], levels[9] = 20, -7
	for _, mode := range []PredMode{ModeInter, ModeIntra} {
		frame := NewDecodedPicture(sps)
		rec := NewReconstructor(frame, sps, PPS{})
		rec.factors = &f
		cu := &CodingUnit{Mode: mode}
		tu := &TransformUnit{X0: 0, Y0: 0, Log2Size: 3, CbfLuma: true, QpPrime: [3]int32{30},
			Blocks: []TUBlock{{Block: ResidualBlock{Log2Size: 3, Levels: levels}}}}
		matrixID := 3
		if mode == ModeIntra {
			matrixID = 0
		}
		want := make([]int32, 64)
		if err := Residual(levels, BlockParams{Log2Size: 3, QP: 30, BitDepth: 8, Factor: f[1][matrixID], Intra: mode == ModeIntra}, want); err != nil {
			t.Fatal(err)
		}
		got := make([]int32, 64)
		if err := rec.residual(&SliceWalker{chroma: 0}, cu, tu, tu.lumaBlock(), 0, 1, got); err != nil {
			t.Fatal(err)
		}
		if !equalInts(got, want) {
			t.Errorf("mode %v: the block was not scaled with matrix %d", mode, matrixID)
		}
	}
}

func equalInts(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWeightOffsetRanges(t *testing.T) {
	var s SPS
	s.BitDepthLuma, s.BitDepthChroma = 12, 10
	if y, c := halfRanges(s); y != 128 || c != 128 {
		t.Errorf("without high precision: %d %d", y, c)
	}
	s.Range.HighPrecisionOffsets = true
	if y, c := halfRanges(s); y != 2048 || c != 512 {
		t.Errorf("with high precision: %d %d", y, c)
	}
	// and the offset is not shifted by the bit depth then (8-268)
	m := &Motion{Pred: [2]bool{true, false}, RefIdx: [2]int8{0, -1}}
	pw := &PredWeights{L0: []RefWeight{{LumaWeight: 5, LumaOffset: 3, ChromaWeight: [2]int32{6, 7}, ChromaOffset: [2]int32{-2, 4}}}}
	if ew := explicitFor(pw, s, m, 0); ew.o[0] != 3 || ew.w[0] != 5 {
		t.Errorf("luma %+v", ew)
	}
	s.Range.HighPrecisionOffsets = false
	if ew := explicitFor(pw, s, m, 2); ew.o[0] != 4<<2 || ew.w[0] != 7 {
		t.Errorf("chroma %+v", ew)
	}
}

func TestInterPredictionRefusals(t *testing.T) {
	sps := SPS{ChromaFormat: 1, CodedWidth: 16, CodedHeight: 16, BitDepthLuma: 8, BitDepthChroma: 8}
	cu := &CodingUnit{Mode: ModeInter, Log2Size: 3, NumPU: 1}
	cu.PU[0] = PredictionUnit{W: 8, H: 8, Motion: Motion{Pred: [2]bool{true, false}, RefIdx: [2]int8{0, -1}}}
	good := NewDecodedPicture(sps)
	for name, pic := range map[string]*DPBPicture{
		"no picture":      nil,
		"no frame":        {},
		"another format":  {Frame: NewDecodedPicture(SPS{ChromaFormat: 0, CodedWidth: 16, CodedHeight: 16, BitDepthLuma: 8})},
		"another size":    {Frame: NewDecodedPicture(SPS{ChromaFormat: 1, CodedWidth: 32, CodedHeight: 16, BitDepthLuma: 8, BitDepthChroma: 8})},
		"a good one fits": {Frame: good},
	} {
		rec := NewReconstructor(NewDecodedPicture(sps), sps, PPS{})
		rec.Inter = &InterSlice{Type: SliceP, List: [2][]RefEntry{{{Pic: pic}}}}
		rec.CodingUnit(cu)
		if (rec.Err() == nil) != (name == "a good one fits") {
			t.Errorf("%s: %v", name, rec.Err())
		}
	}
	rec := NewReconstructor(NewDecodedPicture(sps), sps, PPS{})
	rec.CodingUnit(cu)
	if !errors.Is(rec.Err(), ErrNotReconstructed) {
		t.Errorf("without lists: %v", rec.Err())
	}
	first := rec.Err()
	rec.Inter = &InterSlice{Type: SliceP, List: [2][]RefEntry{{{Pic: &DPBPicture{Frame: good}}}}}
	rec.CodingUnit(cu) // a later success does not replace the first error
	if rec.Err() != first {
		t.Error("the first error was replaced")
	}
}

// The no-call-per-sample interpolation against the reference one: every block size up to
// 64 x 64, windows inside the plane and across its edges, samples at the extremes of the
// range (so the first-stage sums reach the int16 bounds stated in interpfast.go).
func TestFastInterpolationAgainstTheReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(409, 410))
	var sc interpScratch
	var inside, across int
	for iter := 0; iter < 4000; iter++ {
		bd := 8 + rng.IntN(5)
		pl := randomPlane(rng, 20+rng.IntN(120), 20+rng.IntN(120), bd)
		if rng.IntN(2) == 0 { // the extremes only
			for i := range pl.Pix {
				pl.Pix[i] = uint16(rng.IntN(2)) * uint16(1<<uint(bd)-1)
			}
		}
		w, h := 1+rng.IntN(64), 1+rng.IntN(64)
		xInt, yInt := rng.IntN(pl.Width+40)-20, rng.IntN(pl.Height+40)-20
		taps, before, fracs := lumaTaps[:], 3, 4
		if rng.IntN(2) == 0 {
			taps, before, fracs = chromaTaps[:], 1, 8
		}
		xFrac, yFrac := rng.IntN(fracs), rng.IntN(fracs)
		if rng.IntN(4) == 0 {
			xFrac = 0
		}
		if rng.IntN(4) == 0 {
			yFrac = 0
		}
		want, got := make([]int32, w*h), make([]int32, w*h)
		interpolate(pl, xInt, yInt, xFrac, yFrac, w, h, bd, before, taps, want)
		interpolateFast(&sc, pl, xInt, yInt, xFrac, yFrac, w, h, bd, before, taps, got)
		if xInt-before >= 0 && yInt-before >= 0 && xInt+w+8 <= pl.Width && yInt+h+8 <= pl.Height {
			inside++
		} else {
			across++
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("iter %d: %dx%d bit depth %d frac (%d,%d) at (%d,%d): sample %d is %d, the reference gives %d", iter, w, h, bd, xFrac, yFrac, xInt, yInt, i, got[i], want[i])
			}
		}
	}
	if inside == 0 || across == 0 {
		t.Errorf("windows inside %d, across the edge %d", inside, across)
	}
}
