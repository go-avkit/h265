// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/go-avkit/h265/internal/cabac"
	"github.com/go-avkit/h265/internal/cabac/cabactest"
)

// The writer of residual_coding( ): the syntax of 7.3.8.11 run forwards, from
// levels to bins, with its own derivation of every context increment, written
// from the text of 9.3.4.2.3 to 9.3.4.2.7 in a different shape from the decoder's
// (the greater1 context from the history of invocations, the significance
// context from the global coordinates). A block it writes must come back
// level for level, and the context variables on both sides must end equal, which
// they only do if every bin was decoded in the context it was written in.

type rBlock struct {
	levels            []int32
	ts, rdpcm, rdpcmD bool
}

type resWriter struct {
	e *cabactest.Encoder
	m cabac.Model
	t *ResidualTools
}

func scanFor(idx, size int) Scan {
	switch idx {
	case 1:
		return HorScan(size)
	case 2:
		return VerScan(size)
	}
	return DiagScan(size)
}

// refScanIdx is 7.4.9.11 again, as a table of conditions.
func refScanIdx(in ResidualInput, cat int) int {
	if !in.Intra {
		return 0
	}
	small := in.Log2Size == 2 || (in.Log2Size == 3 && (in.CIdx == 0 || cat == 3))
	if !small {
		return 0
	}
	if in.PredModeIntra >= 6 && in.PredModeIntra <= 14 {
		return 2
	}
	if in.PredModeIntra >= 22 && in.PredModeIntra <= 30 {
		return 1
	}
	return 0
}

// refSigCtx is 9.3.4.2.5 from the global coordinates of the coefficient.
func refSigCtx(t *ResidualTools, in ResidualInput, ts bool, scanIdx, xC, yC int, csbf func(xS, yS int) int) int {
	log2, cIdx := in.Log2Size, in.CIdx
	var sigCtx int
	if t.TransformSkipContext && (ts || in.CuTransquantBypass) {
		if cIdx == 0 {
			sigCtx = 42
		} else {
			sigCtx = 16
		}
	} else if log2 == 2 {
		sigCtx = int([]uint8{0, 1, 4, 5, 2, 3, 4, 5, 6, 6, 8, 8, 7, 7, 8}[(yC<<2)+xC])
	} else if xC+yC == 0 {
		sigCtx = 0
	} else {
		xS, yS := xC>>2, yC>>2
		prev := csbf(xS+1, yS) + csbf(xS, yS+1)<<1
		xP, yP := xC&3, yC&3
		switch prev {
		case 0:
			switch {
			case xP+yP == 0:
				sigCtx = 2
			case xP+yP < 3:
				sigCtx = 1
			}
		case 1:
			switch yP {
			case 0:
				sigCtx = 2
			case 1:
				sigCtx = 1
			}
		case 2:
			switch xP {
			case 0:
				sigCtx = 2
			case 1:
				sigCtx = 1
			}
		default:
			sigCtx = 2
		}
		if cIdx == 0 {
			if xS+yS > 0 {
				sigCtx += 3
			}
			if log2 == 3 {
				if scanIdx == 0 {
					sigCtx += 9
				} else {
					sigCtx += 15
				}
			} else {
				sigCtx += 21
			}
		} else if log2 == 3 {
			sigCtx += 9
		} else {
			sigCtx += 12
		}
	}
	if cIdx == 0 {
		return sigCtx
	}
	return 27 + sigCtx
}

type g1Inv struct{ sb, ctx, flag int }

func (w *resWriter) bin(ctx int, v int) { w.e.Decision(&w.m.Ctx[ctx], v) }

func b2n(b bool) int {
	if b {
		return 1
	}
	return 0
}

// write encodes b as residual_coding( ) and rewrites b's sign of each hidden
// coefficient so that it is what the decoder will infer.
func (w *resWriter) write(in ResidualInput, b *rBlock) {
	t, log2, cIdx := w.t, in.Log2Size, in.CIdx
	luma := cIdx == 0
	size := 1 << log2
	pick := func(a, c int) int {
		if luma {
			return a
		}
		return c
	}
	if t.TransformSkip && !in.CuTransquantBypass && log2 <= t.Log2MaxTransformSkipSize {
		w.bin(pick(cabac.CtxTransformSkipLuma, cabac.CtxTransformSkipChroma), b2n(b.ts))
	} else {
		b.ts = false
	}
	if !in.Intra && t.ExplicitRDPCM && (b.ts || in.CuTransquantBypass) {
		w.bin(pick(cabac.CtxExplicitRdpcmLuma, cabac.CtxExplicitRdpcmChroma), b2n(b.rdpcm))
		if b.rdpcm {
			w.bin(pick(cabac.CtxExplicitRdpcmDirLuma, cabac.CtxExplicitRdpcmDirChroma), b2n(b.rdpcmD))
		} else {
			b.rdpcmD = false
		}
	} else {
		b.rdpcm, b.rdpcmD = false, false
	}
	scanIdx := refScanIdx(in, t.ChromaArrayType)
	sbScan, inScan := scanFor(scanIdx, size/4), scanFor(scanIdx, 4)
	at := func(xS, yS, n int) int { // raster index of position n of sub-block ( xS, yS )
		return (yS*4+int(inScan[n][1]))*size + xS*4 + int(inScan[n][0])
	}
	// The last significant position in scan order.
	lastSB, lastPos := -1, -1
	for i := len(sbScan) - 1; i >= 0 && lastSB < 0; i-- {
		for n := 15; n >= 0; n-- {
			if b.levels[at(int(sbScan[i][0]), int(sbScan[i][1]), n)] != 0 {
				lastSB, lastPos = i, n
				break
			}
		}
	}
	lastX := int(sbScan[lastSB][0])*4 + int(inScan[lastPos][0])
	lastY := int(sbScan[lastSB][1])*4 + int(inScan[lastPos][1])
	wx, wy := lastX, lastY
	if scanIdx == 2 {
		wx, wy = wy, wx
	}
	var ctxOffset, ctxShift int
	if luma {
		ctxOffset, ctxShift = 3*(log2-2)+((log2-1)>>2), (log2+1)>>2
	} else {
		ctxOffset, ctxShift = 15, log2-2
	}
	// pre computes prefix and suffix of a position (7-74 to 7-77, inverted).
	split := func(pos int) (prefix, suffix, bits int) {
		if pos < 4 {
			return pos, 0, 0
		}
		for p := 2*log2 - 1; p >= 4; p-- {
			if base := (1 << ((p >> 1) - 1)) * (2 + p&1); base <= pos {
				return p, pos - base, (p >> 1) - 1
			}
		}
		panic("no prefix")
	}
	px, sx, bx := split(wx)
	py, sy, by := split(wy)
	for _, p := range []struct{ base, v int }{{cabac.CtxLastSigCoeffXPrefix, px}, {cabac.CtxLastSigCoeffYPrefix, py}} {
		for binIdx := 0; binIdx < p.v; binIdx++ {
			w.bin(p.base+(binIdx>>ctxShift)+ctxOffset, 1)
		}
		if p.v < 2*log2-1 {
			w.bin(p.base+(p.v>>ctxShift)+ctxOffset, 0)
		}
	}
	if px > 3 {
		w.e.BypassBits(uint32(sx), bx)
	}
	if py > 3 {
		w.e.BypassBits(uint32(sy), by)
	}

	side := size / 4
	csbf := make([]int, side*side)
	get := func(xS, yS int) int {
		if xS >= side || yS >= side {
			return 0
		}
		return csbf[yS*side+xS]
	}
	var history []g1Inv
	var rice struct {
		lastAbs, lastRice uint64
		started           bool
	}
	for i := lastSB; i >= 0; i-- {
		xS, yS := int(sbScan[i][0]), int(sbScan[i][1])
		nonzero := false
		for n := 0; n < 16; n++ {
			nonzero = nonzero || b.levels[at(xS, yS, n)] != 0
		}
		inferDC := false
		coded := true
		if i < lastSB && i > 0 {
			coded = nonzero
			inc := get(xS+1, yS) + get(xS, yS+1)
			if inc > 1 {
				inc = 1
			}
			if !luma {
				inc += 2
			}
			w.bin(cabac.CtxCodedSubBlock+inc, b2n(coded))
			inferDC = true
		}
		csbf[yS*side+xS] = b2n(coded)
		var sigN []int
		start := 15
		if i == lastSB {
			start = lastPos - 1
			sigN = append(sigN, lastPos)
		}
		for n := start; n >= 0 && coded; n-- {
			sig := b.levels[at(xS, yS, n)] != 0
			if n > 0 || !inferDC {
				xC, yC := xS*4+int(inScan[n][0]), yS*4+int(inScan[n][1])
				w.bin(cabac.CtxSigCoeff+refSigCtx(t, in, b.ts, scanIdx, xC, yC, get), b2n(sig))
				if sig {
					inferDC = false
				}
			}
			if sig {
				sigN = append(sigN, n)
			}
		}
		if len(sigN) == 0 {
			continue
		}
		// greater1 flags, the context from the history of invocations (9.3.4.2.6).
		abs := func(n int) int { return absInt(int(b.levels[at(xS, yS, n)])) }
		var ctxSet, g1Ctx int
		first := true
		lastG1Idx := -1
		for k := 0; k < len(sigN) && k < 8; k++ {
			if k == 0 {
				ctxSet = 2
				if i == 0 || !luma {
					ctxSet = 0
				}
				lastG1 := 1
				if len(history) > 0 {
					p := history[len(history)-1]
					lastG1 = p.ctx
					if lastG1 > 0 {
						if p.flag == 1 {
							lastG1 = 0
						} else {
							lastG1++
						}
					}
				}
				if lastG1 == 0 {
					ctxSet++
				}
				g1Ctx = 1
				first = false
			} else {
				p := history[len(history)-1]
				g1Ctx = p.ctx
				if g1Ctx > 0 {
					if p.flag == 1 {
						g1Ctx = 0
					} else {
						g1Ctx++
					}
				}
			}
			_ = first
			flag := b2n(abs(sigN[k]) > 1)
			inc := ctxSet*4 + min(3, g1Ctx)
			if !luma {
				inc += 16
			}
			w.bin(cabac.CtxCoeffAbsGreater1+inc, flag)
			history = append(history, g1Inv{i, g1Ctx, flag})
			if flag == 1 && lastG1Idx == -1 {
				lastG1Idx = k
			}
		}
		if lastG1Idx != -1 {
			inc := ctxSet
			if !luma {
				inc += 4
			}
			w.bin(cabac.CtxCoeffAbsGreater2+inc, b2n(abs(sigN[lastG1Idx]) > 2))
		}
		firstSig, lastSig := sigN[len(sigN)-1], sigN[0]
		hidden := lastSig-firstSig > 3
		if in.CuTransquantBypass || (in.Intra && t.ImplicitRDPCM && b.ts && (in.PredModeIntra == 10 || in.PredModeIntra == 26)) || b.rdpcm {
			hidden = false
		}
		hide := t.SignDataHiding && hidden
		// escapeDataPresent (7.3.8.11), read off the levels: more than eight
		// coefficients, a second greater1 flag set, or a greater2 flag set.
		escape, nG1 := len(sigN) > 8, 0
		for k := 0; k < min(len(sigN), 8); k++ {
			if abs(sigN[k]) > 1 {
				nG1++
			}
		}
		escape = escape || nG1 > 1 || (lastG1Idx != -1 && abs(sigN[lastG1Idx]) > 2)
		if t.BypassAlignment && escape {
			w.e.Align() // 9.3.5.5: ivlCurrRange is set to 256 before the aligned bins
		}
		if hide { // the sign of the first coefficient is what the parity of the sum says
			sum := 0
			for _, n := range sigN {
				sum += abs(n)
			}
			idx := at(xS, yS, firstSig)
			a := int32(abs(firstSig))
			if sum%2 == 1 {
				a = -a
			}
			b.levels[idx] = a
		}
		for k, n := range sigN {
			if hide && k == len(sigN)-1 {
				continue
			}
			w.e.Bypass(b2n(b.levels[at(xS, yS, n)] < 0))
		}
		rice.started = false
		for k, n := range sigN {
			base := 1
			if k < 8 {
				if abs(n) > 1 {
					base++
				}
				if k == lastG1Idx && abs(n) > 2 {
					base++
				}
			}
			threshold := 1
			if k < 8 {
				threshold = 2
				if k == lastG1Idx {
					threshold = 3
				}
			}
			if base == threshold {
				w.writeRemaining(in, b, &rice.lastAbs, &rice.lastRice, &rice.started, uint64(abs(n)-base), uint64(base))
			}
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// writeRemaining is the binarisation of coeff_abs_level_remaining (9.3.3.11) run
// forwards, with the Rice statistics of the persistent adaptation (9-22 to 9-25).
func (w *resWriter) writeRemaining(in ResidualInput, b *rBlock, lastAbs, lastRice *uint64, started *bool, value, base uint64) {
	t := w.t
	sbType := cabac.SbType(in.CIdx, b.ts || in.CuTransquantBypass)
	if !*started {
		*lastAbs, *lastRice = 0, 0
		if t.PersistentRiceAdaptation {
			*lastRice = uint64(w.m.StatCoeff[sbType]) / 4
		}
	}
	rice := *lastRice
	if *lastAbs > 3*(1<<*lastRice) {
		rice++
	}
	if !t.PersistentRiceAdaptation {
		rice = min(rice, 4)
	}
	cMax := uint64(4) << rice
	if value < cMax {
		for i := uint64(0); i < value>>rice; i++ {
			w.e.Bypass(1)
		}
		w.e.Bypass(0)
		w.e.BypassBits(uint32(value&(1<<rice-1)), int(rice))
	} else {
		for i := 0; i < 4; i++ {
			w.e.Bypass(1)
		}
		suffix := value - cMax
		if t.ExtendedPrecision {
			for _, bin := range refEncLimitedEGk(suffix, uint(rice)+1, uint(coeffBits(t, in.CIdx))) {
				w.e.Bypass(bin)
			}
		} else {
			for _, bin := range refEncEGk(suffix, uint(rice)+1) {
				w.e.Bypass(bin)
			}
		}
	}
	if t.PersistentRiceAdaptation && !*started {
		sc := &w.m.StatCoeff[sbType]
		if value >= 3<<(*sc/4) {
			*sc++
		} else if 2*value < 1<<(*sc/4) && *sc > 0 {
			*sc--
		}
	}
	*started = true
	*lastAbs, *lastRice = base+value, rice
}

func randomTools(rng *rand.Rand) *ResidualTools {
	t := &ResidualTools{
		ChromaArrayType: []int{1, 3}[rng.IntN(2)], SignDataHiding: rng.IntN(2) == 0,
		TransformSkip: rng.IntN(2) == 0, Log2MaxTransformSkipSize: 2 + rng.IntN(4),
		TransformSkipContext: rng.IntN(3) == 0, ImplicitRDPCM: rng.IntN(2) == 0, ExplicitRDPCM: rng.IntN(2) == 0,
		PersistentRiceAdaptation: rng.IntN(2) == 0, ExtendedPrecision: rng.IntN(4) == 0,
		BitDepthY: 8 + rng.IntN(9), BitDepthC: 8 + rng.IntN(9),
		BypassAlignment: rng.IntN(3) == 0,
	}
	return t
}

func randomBlock(rng *rand.Rand, size int, t *ResidualTools, cIdx int) *rBlock {
	b := &rBlock{levels: make([]int32, size*size), ts: rng.IntN(3) == 0, rdpcm: rng.IntN(2) == 0, rdpcmD: rng.IntN(2) == 0}
	_, hi := coeffLimits(t, cIdx)
	density := []int{1, 4, 16, 64}[rng.IntN(4)]
	for i := range b.levels {
		if rng.IntN(100) < density || i == rng.IntN(size*size) {
			mag := int64(1 + rng.IntN(3))
			switch rng.IntN(8) {
			case 0:
				mag = 1 + int64(rng.IntN(40))
			case 1:
				mag = 1 + rng.Int64N(hi)
			}
			if rng.IntN(2) == 0 {
				mag = -mag
			}
			b.levels[i] = int32(mag)
		}
	}
	b.levels[rng.IntN(size*size)] = 1 // never an empty block: cbf says there is something
	return b
}

func TestResidualRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(51, 52))
	for iter := 0; iter < 1500; iter++ {
		tools := randomTools(rng)
		in := ResidualInput{
			Log2Size: 2 + rng.IntN(4), CIdx: rng.IntN(3), Intra: rng.IntN(2) == 0,
			PredModeIntra: rng.IntN(35), CuTransquantBypass: rng.IntN(5) == 0,
		}
		size := 1 << in.Log2Size
		blk := randomBlock(rng, size, tools, in.CIdx)
		var m0 cabac.Model
		m0.Init(rng.IntN(3), rng.IntN(52))
		if tools.PersistentRiceAdaptation {
			for i := range m0.StatCoeff {
				m0.StatCoeff[i] = uint8(rng.IntN(12))
			}
		}
		w := &resWriter{e: cabactest.New(), m: m0, t: tools}
		w.write(in, blk)
		w.e.Terminate(1)

		var d cabac.Decoder
		if err := d.Init(w.e.Bytes()); err != nil {
			t.Fatal(err)
		}
		dm := m0
		out := ResidualBlock{Levels: make([]int32, size*size)}
		if err := DecodeResidual(&d, &dm, tools, in, &out); err != nil {
			t.Fatalf("iter %d: %+v %+v: %v", iter, tools, in, err)
		}
		for i := range blk.levels {
			if out.Levels[i] != blk.levels[i] {
				t.Fatalf("iter %d: %+v %+v: level %d is %d, written %d", iter, *tools, in, i, out.Levels[i], blk.levels[i])
			}
		}
		if out.TransformSkip != blk.ts || out.ExplicitRDPCM != blk.rdpcm || out.ExplicitRDPCMDir != blk.rdpcmD {
			t.Fatalf("iter %d: flags %v %v %v, written %v %v %v", iter, out.TransformSkip, out.ExplicitRDPCM, out.ExplicitRDPCMDir, blk.ts, blk.rdpcm, blk.rdpcmD)
		}
		if dm != w.m {
			t.Fatalf("iter %d: %+v %+v: context variables end different from the writer's", iter, *tools, in)
		}
		if d.Terminate() != 1 || d.BitPos() != int64(w.e.BitCount()) {
			t.Fatalf("iter %d: the block did not end where it was written (%d bits, %d)", iter, d.BitPos(), w.e.BitCount())
		}
	}
}

// TestEveryResidualContextIsUsed: with every context variable at state 0, a
// context that is used changes (an MPS moves the state, an LPS flips the MPS),
// so the contexts that differ afterwards are the ones the syntax reached. All of
// last_sig_coeff_{x,y}_prefix (18 each), coded_sub_block_flag (4), sig_coeff_flag
// (44), the greater1 (24) and greater2 (6) flags and the transform skip and RDPCM
// flags must be reached: a context nothing ever selects is a context
// derivation that cannot reach it.
func TestEveryResidualContextIsUsed(t *testing.T) {
	rng := rand.New(rand.NewPCG(53, 54))
	var zero cabac.Model
	used := map[int]bool{}
	for iter := 0; iter < 2500; iter++ {
		tools := randomTools(rng)
		tools.TransformSkipContext = iter%2 == 0
		in := ResidualInput{Log2Size: 2 + rng.IntN(4), CIdx: rng.IntN(3), Intra: rng.IntN(2) == 0, PredModeIntra: rng.IntN(35)}
		size := 1 << in.Log2Size
		blk := randomBlock(rng, size, tools, in.CIdx)
		w := &resWriter{e: cabactest.New(), m: zero, t: tools}
		w.write(in, blk)
		w.e.Terminate(1)
		var d cabac.Decoder
		if err := d.Init(w.e.Bytes()); err != nil {
			t.Fatal(err)
		}
		dm := zero
		out := ResidualBlock{Levels: make([]int32, size*size)}
		if err := DecodeResidual(&d, &dm, tools, in, &out); err != nil {
			t.Fatal(err)
		}
		for i := range dm.Ctx {
			if dm.Ctx[i] != 0 {
				used[i] = true
			}
		}
	}
	for _, r := range []struct {
		name        string
		first, size int
	}{
		{"last_sig_coeff_x_prefix", cabac.CtxLastSigCoeffXPrefix, 18}, {"last_sig_coeff_y_prefix", cabac.CtxLastSigCoeffYPrefix, 18},
		{"coded_sub_block_flag", cabac.CtxCodedSubBlock, 4}, {"sig_coeff_flag", cabac.CtxSigCoeff, 44},
		{"coeff_abs_level_greater1_flag", cabac.CtxCoeffAbsGreater1, 24}, {"coeff_abs_level_greater2_flag", cabac.CtxCoeffAbsGreater2, 6},
		{"transform_skip_flag luma", cabac.CtxTransformSkipLuma, 1}, {"transform_skip_flag chroma", cabac.CtxTransformSkipChroma, 1},
		{"explicit_rdpcm_flag luma", cabac.CtxExplicitRdpcmLuma, 1}, {"explicit_rdpcm_flag chroma", cabac.CtxExplicitRdpcmChroma, 1},
		{"explicit_rdpcm_dir_flag luma", cabac.CtxExplicitRdpcmDirLuma, 1}, {"explicit_rdpcm_dir_flag chroma", cabac.CtxExplicitRdpcmDirChroma, 1},
	} {
		n := 0
		for i := 0; i < r.size; i++ {
			if used[r.first+i] {
				n++
			} else {
				t.Errorf("%s: context %d is never reached", r.name, i)
			}
		}
		if n != r.size {
			t.Errorf("%s: %d of %d contexts reached", r.name, n, r.size)
		}
	}
}

// TestSigCtxByHand: ctxInc of sig_coeff_flag worked out from 9-40 to 9-55.
func TestSigCtxByHand(t *testing.T) {
	tools := &ResidualTools{}
	luma := ResidualInput{Log2Size: 3, CIdx: 0}
	for _, c := range []struct {
		name                       string
		in                         ResidualInput
		ts                         bool
		skipCtx                    bool
		scanIdx, xS, yS, xP, yP, p int
		want                       int
	}{
		// 8x8 luma, diagonal, sub-block (1,0), (2,1) in it, no coded neighbours:
		// xP+yP = 3, not below 3: 0; +3 for xS+yS>0; +9 for 8x8 diagonal.
		{"luma 8x8, far corner", luma, false, false, 0, 1, 0, 2, 1, 0, 12},
		{"luma 8x8, horizontal scan", luma, false, false, 1, 1, 0, 2, 1, 0, 18},
		// Right neighbour coded (prevCsbf 1): yP = 1 gives 1; sub-block (0,1): +3, +9.
		{"luma 8x8, right neighbour", luma, false, false, 0, 0, 1, 3, 1, 1, 13},
		// Lower neighbour coded (prevCsbf 2): xP = 0 gives 2; sub-block (0,0): +9.
		{"luma 8x8, lower neighbour", luma, false, false, 0, 0, 0, 0, 3, 2, 11},
		// Both coded: 2, sub-block (0,0): +9 (not DC: xP+yP > 0).
		{"luma 8x8, both", luma, false, false, 0, 0, 0, 1, 1, 3, 11},
		{"luma 16x16", ResidualInput{Log2Size: 4, CIdx: 0}, false, false, 0, 2, 2, 0, 0, 0, 2 + 3 + 21},
		// Table 9-50 at ( yC << 2 ) + xC = 6 is 4; at 14, 8.
		{"luma 4x4 by the map", ResidualInput{Log2Size: 2, CIdx: 0}, false, false, 0, 0, 0, 2, 1, 0, 4},
		{"luma 4x4 by the map, last cell", ResidualInput{Log2Size: 2, CIdx: 0}, false, false, 0, 0, 0, 2, 3, 0, 8},
		{"luma DC", luma, false, false, 0, 0, 0, 0, 0, 0, 0},
		{"chroma 8x8", ResidualInput{Log2Size: 3, CIdx: 1}, false, false, 0, 1, 0, 0, 0, 3, 27 + 2 + 9},
		{"chroma 16x16", ResidualInput{Log2Size: 4, CIdx: 2}, false, false, 0, 1, 1, 3, 3, 0, 27 + 0 + 12},
		{"luma, transform skip context", luma, true, true, 0, 1, 1, 1, 1, 0, 42},
		{"chroma, transform skip context", ResidualInput{Log2Size: 3, CIdx: 1}, true, true, 0, 1, 1, 1, 1, 0, 27 + 16},
		{"transform skip, context not enabled", luma, true, false, 0, 0, 0, 0, 0, 0, 0},
	} {
		tools.TransformSkipContext = c.skipCtx
		if got := sigCtxInc(tools, c.in, c.ts, c.in.CIdx == 0, c.in.Log2Size, c.scanIdx, c.xS, c.yS, c.xP, c.yP, c.p); got != c.want {
			t.Errorf("%s: ctxInc %d, want %d", c.name, got, c.want)
		}
	}
}

func TestScanIdxOfIntraBlocks(t *testing.T) {
	for _, c := range []struct {
		in   ResidualInput
		cat  int
		want int
	}{
		{ResidualInput{Log2Size: 2, Intra: true, PredModeIntra: 6}, 1, 2},
		{ResidualInput{Log2Size: 2, Intra: true, PredModeIntra: 14}, 1, 2},
		{ResidualInput{Log2Size: 2, Intra: true, PredModeIntra: 15}, 1, 0},
		{ResidualInput{Log2Size: 2, Intra: true, PredModeIntra: 22}, 1, 1},
		{ResidualInput{Log2Size: 2, Intra: true, PredModeIntra: 30}, 1, 1},
		{ResidualInput{Log2Size: 2, Intra: true, PredModeIntra: 31}, 1, 0},
		{ResidualInput{Log2Size: 3, Intra: true, PredModeIntra: 10}, 1, 2},
		{ResidualInput{Log2Size: 3, CIdx: 1, Intra: true, PredModeIntra: 10}, 1, 0}, // 4:2:0 chroma 8x8
		{ResidualInput{Log2Size: 3, CIdx: 1, Intra: true, PredModeIntra: 10}, 3, 2}, // 4:4:4
		{ResidualInput{Log2Size: 4, Intra: true, PredModeIntra: 10}, 1, 0},
		{ResidualInput{Log2Size: 2, Intra: false, PredModeIntra: 10}, 1, 0},
	} {
		if got := ScanIdx(c.in, c.cat); got != c.want || got != refScanIdx(c.in, c.cat) {
			t.Errorf("%+v, ChromaArrayType %d: %d, want %d", c.in, c.cat, got, c.want)
		}
	}
}

func TestResidualRefusals(t *testing.T) {
	tools := &ResidualTools{BitDepthY: 8, BitDepthC: 8}
	var d cabac.Decoder
	_ = d.Init([]byte{0, 0, 0, 0})
	var m cabac.Model
	m.Init(0, 30)
	for name, in := range map[string]ResidualInput{
		"log2 1":  {Log2Size: 1},
		"log2 6":  {Log2Size: 6},
		"cIdx 3":  {Log2Size: 2, CIdx: 3},
		"cIdx -1": {Log2Size: 2, CIdx: -1},
	} {
		if err := DecodeResidual(&d, &m, tools, in, &ResidualBlock{Levels: make([]int32, 4096)}); !errors.Is(err, ErrResidual) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := DecodeResidual(&d, &m, tools, ResidualInput{Log2Size: 3}, &ResidualBlock{Levels: make([]int32, 63)}); !errors.Is(err, ErrResidual) {
		t.Errorf("a block with no room: %v", err)
	}
	// A level past CoeffMax: written by the writer (which does not mind), refused.
	for _, c := range []struct {
		level int32
		ext   bool
		fail  bool
	}{{1 << 15, false, true}, {1<<15 - 1, false, false}, {-(1 << 15), false, false}, {-(1 << 15) - 1, false, true}, {1 << 15, true, false}, {1 << 22, true, true}} {
		tools := &ResidualTools{BitDepthY: 16, BitDepthC: 16, ExtendedPrecision: c.ext}
		blk := &rBlock{levels: make([]int32, 16)}
		blk.levels[5] = c.level
		w := &resWriter{e: cabactest.New(), m: m, t: tools}
		w.write(ResidualInput{Log2Size: 2}, blk)
		w.e.Terminate(1)
		var d cabac.Decoder
		if err := d.Init(w.e.Bytes()); err != nil {
			t.Fatal(err)
		}
		dm := m
		err := DecodeResidual(&d, &dm, tools, ResidualInput{Log2Size: 2}, &ResidualBlock{Levels: make([]int32, 16)})
		if c.fail != (err != nil) || (err != nil && !errors.Is(err, ErrResidual)) {
			t.Errorf("level %d, extended %v: err = %v, want failure %v", c.level, c.ext, err, c.fail)
		}
	}
	// An endless prefix of coeff_abs_level_remaining: all-ones bypass bins.
	e := cabactest.New()
	w := &resWriter{e: e, m: m, t: &ResidualTools{BitDepthY: 8, BitDepthC: 8}}
	blk := &rBlock{levels: make([]int32, 16)}
	blk.levels[0] = 3
	w.write(ResidualInput{Log2Size: 2}, blk)
	// Replace the tail: write one more block's worth of ones where remaining goes.
	e2 := cabactest.New()
	m2 := m
	// last position (0,0): prefixes 0 0, then greater1 flag 1, greater2 flag 1, sign, remaining.
	e2.Decision(&m2.Ctx[cabac.CtxLastSigCoeffXPrefix+0], 0)
	e2.Decision(&m2.Ctx[cabac.CtxLastSigCoeffYPrefix+0], 0)
	e2.Decision(&m2.Ctx[cabac.CtxCoeffAbsGreater1+1*0+1], 1)
	e2.Decision(&m2.Ctx[cabac.CtxCoeffAbsGreater2+0], 1)
	e2.Bypass(0)
	for i := 0; i < 80; i++ {
		e2.Bypass(1)
	}
	e2.Terminate(1)
	var d2 cabac.Decoder
	if err := d2.Init(e2.Bytes()); err != nil {
		t.Fatal(err)
	}
	dm := m
	if err := DecodeResidual(&d2, &dm, &ResidualTools{BitDepthY: 8, BitDepthC: 8}, ResidualInput{Log2Size: 2}, &ResidualBlock{Levels: make([]int32, 16)}); !errors.Is(err, ErrResidual) {
		t.Errorf("an endless coeff_abs_level_remaining: %v", err)
	}
}
