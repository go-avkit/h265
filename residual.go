// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"

	"github.com/go-avkit/h265/internal/cabac"
)

// ErrResidual means the residual coding of a transform block is not one the
// syntax allows: a coefficient outside CoeffMin..CoeffMax, or a bin string the
// binarisation does not have.
var ErrResidual = errors.New("h265: residual_coding is not valid")

// scans holds ScanOrder[ log2BlockSize ][ scanIdx ] for the block sizes the
// residual syntax scans (1 to 8 sub-blocks a side, and the 4x4 within one), and
// its inverse: the position of each ( x, y ) in the scan.
var scans [4][3]struct {
	order Scan
	pos   [64]uint8 // y*size + x -> scan position
}

func init() {
	for log2 := range scans {
		size := 1 << log2
		for idx, s := range []Scan{DiagScan(size), HorScan(size), VerScan(size)} {
			scans[log2][idx].order = s
			for i, p := range s {
				scans[log2][idx].pos[int(p[1])*size+int(p[0])] = uint8(i)
			}
		}
	}
}

// ctxIdxMap is Table 9-50: sigCtx of a 4x4 block from ( yC << 2 ) + xC.
var ctxIdxMap = [15]uint8{0, 1, 4, 5, 2, 3, 4, 5, 6, 6, 8, 8, 7, 7, 8}

// ResidualTools are the switches of the SPS and PPS that residual_coding( )
// reads.
type ResidualTools struct {
	ChromaArrayType int
	// SignDataHiding is sign_data_hiding_enabled_flag; TransformSkip is
	// transform_skip_enabled_flag with Log2MaxTransformSkipSize.
	SignDataHiding           bool
	TransformSkip            bool
	Log2MaxTransformSkipSize int
	// The range extension's switches (7.3.2.2.2).
	TransformSkipContext     bool
	ImplicitRDPCM            bool
	ExplicitRDPCM            bool
	PersistentRiceAdaptation bool
	ExtendedPrecision        bool
	// BypassAlignment is cabac_bypass_alignment_enabled_flag.
	BypassAlignment bool
	// BitDepthY and BitDepthC give CoeffMinY, CoeffMaxY and the C pair
	// (7-27 to 7-30): 15 bits, or Max( 15, BitDepth + 6 ) when
	// extended_precision_processing_flag is set.
	BitDepthY, BitDepthC int
}

// ResidualBlock is one transform block of residual_coding( ): the syntax it
// states and TransCoeffLevel, the levels, in raster order within the block.
type ResidualBlock struct {
	Log2Size int
	CIdx     int
	// TransformSkip, ExplicitRDPCM and ExplicitRDPCMDir are the flags of the
	// block, 0 when absent.
	TransformSkip, ExplicitRDPCM, ExplicitRDPCMDir bool
	// Levels is TransCoeffLevel[ yC * size + xC ]: size = 1 << Log2Size.
	Levels []int32
}

// ResidualInput is what the coding unit tells residual_coding( ).
type ResidualInput struct {
	Log2Size int
	CIdx     int
	// Intra is CuPredMode == MODE_INTRA; PredModeIntra is IntraPredModeY for
	// luma and IntraPredModeC for chroma (used when Intra).
	Intra         bool
	PredModeIntra int
	// CuTransquantBypass is cu_transquant_bypass_flag.
	CuTransquantBypass bool
}

// ScanIdx is 7.4.9.11: the scan order index of a transform block.
func ScanIdx(in ResidualInput, chromaArrayType int) int {
	if in.Intra && (in.Log2Size == 2 ||
		(in.Log2Size == 3 && in.CIdx == 0) ||
		(in.Log2Size == 3 && chromaArrayType == 3)) {
		switch m := in.PredModeIntra; {
		case m >= 6 && m <= 14:
			return 2
		case m >= 22 && m <= 30:
			return 1
		}
	}
	return 0
}

// maxSubBlocks is the 8x8 grid of 4x4 sub-blocks of a 32x32 block.
const maxSubBlocks = 8

// DecodeResidual is residual_coding( x0, y0, log2TrafoSize, cIdx ), 7.3.8.11,
// with the context selection of 9.3.4.2.3 to 9.3.4.2.7, from the arithmetic
// decoder d and the context variables m. out.Levels must hold 1 << (2*Log2Size)
// entries; it is zeroed here.
func DecodeResidual(d *cabac.Decoder, m *cabac.Model, t *ResidualTools, in ResidualInput, out *ResidualBlock) error {
	log2, cIdx := in.Log2Size, in.CIdx
	if log2 < 2 || log2 > 5 || cIdx < 0 || cIdx > 2 || len(out.Levels) < 1<<(2*log2) {
		return fmt.Errorf("%w: a block of 2^%d for component %d with room for %d levels", ErrResidual, log2, cIdx, len(out.Levels))
	}
	size := 1 << log2
	out.Log2Size, out.CIdx = log2, cIdx
	out.TransformSkip, out.ExplicitRDPCM, out.ExplicitRDPCMDir = false, false, false
	clear(out.Levels[:size*size])
	luma := cIdx == 0

	if t.TransformSkip && !in.CuTransquantBypass && log2 <= t.Log2MaxTransformSkipSize {
		out.TransformSkip = d.Decision(&m.Ctx[pick(luma, cabac.CtxTransformSkipLuma, cabac.CtxTransformSkipChroma)]) == 1
	}
	if !in.Intra && t.ExplicitRDPCM && (out.TransformSkip || in.CuTransquantBypass) {
		out.ExplicitRDPCM = d.Decision(&m.Ctx[pick(luma, cabac.CtxExplicitRdpcmLuma, cabac.CtxExplicitRdpcmChroma)]) == 1
		if out.ExplicitRDPCM {
			out.ExplicitRDPCMDir = d.Decision(&m.Ctx[pick(luma, cabac.CtxExplicitRdpcmDirLuma, cabac.CtxExplicitRdpcmDirChroma)]) == 1
		}
	}

	// last_sig_coeff_{x,y}_prefix: truncated unary, cMax (log2 << 1) - 1, each
	// bin in its own context (9.3.4.2.3), then the suffixes in bypass bins.
	var ctxOffset, ctxShift int
	if luma {
		ctxOffset, ctxShift = 3*(log2-2)+((log2-1)>>2), (log2+1)>>2
	} else {
		ctxOffset, ctxShift = 15, log2-2
	}
	prefix := func(base int) int {
		cMax := log2<<1 - 1
		n := 0
		for n < cMax && d.Decision(&m.Ctx[base+(n>>ctxShift)+ctxOffset]) == 1 {
			n++
		}
		return n
	}
	xPrefix := prefix(cabac.CtxLastSigCoeffXPrefix)
	yPrefix := prefix(cabac.CtxLastSigCoeffYPrefix)
	suffixed := func(p int) int {
		if p <= 3 {
			return p
		}
		bits := uint(p>>1) - 1
		return (1<<bits)*(2+p&1) + int(d.BypassBits(bits)) // 7-75, 7-77
	}
	lastX := suffixed(xPrefix)
	lastY := suffixed(yPrefix)
	scanIdx := ScanIdx(in, t.ChromaArrayType)
	if scanIdx == 2 {
		lastX, lastY = lastY, lastX // 7-78
	}

	subLog2 := log2 - 2
	sbScan, inScan := &scans[subLog2][scanIdx], &scans[2][scanIdx]
	lastSubBlock := int(sbScan.pos[(lastY>>2)<<subLog2+(lastX>>2)])
	lastScanPos := int(inScan.pos[(lastY&3)<<2+(lastX&3)])

	var csbf [maxSubBlocks][maxSubBlocks]bool
	sbSide := 1 << subLog2
	var (
		prevG1 = 1 // greater1Ctx left by the last sub-block that had flags
		haveG1 bool
	)
	lo, hi := coeffLimits(t, cIdx)
	for i := lastSubBlock; i >= 0; i-- {
		xS, yS := int(sbScan.order[i][0]), int(sbScan.order[i][1])
		inferSbDc := false
		coded := i == lastSubBlock || i == 0 // coded_sub_block_flag inferred 1
		if i < lastSubBlock && i > 0 {
			csbfCtx := 0 // 9.3.4.2.4
			if xS < sbSide-1 && csbf[xS+1][yS] {
				csbfCtx = 1
			}
			if yS < sbSide-1 && csbf[xS][yS+1] {
				csbfCtx = 1
			}
			inc := csbfCtx
			if !luma {
				inc += 2
			}
			coded = d.Decision(&m.Ctx[cabac.CtxCodedSubBlock+inc]) == 1
			inferSbDc = true
		}
		csbf[xS][yS] = coded

		// sig_coeff_flag, from the last position of the sub-block down: sigN
		// lists the scan positions n of the significant coefficients, descending.
		var sigN [16]int
		nSig := 0
		start := 15
		if i == lastSubBlock {
			start = lastScanPos - 1
			sigN[0], nSig = lastScanPos, 1 // the last position is significant, not coded
		}
		// prevCsbf (9-43, 9-44) is the same for every position of the sub-block.
		prevCsbf := 0
		if xS < sbSide-1 && csbf[xS+1][yS] {
			prevCsbf |= 1
		}
		if yS < sbSide-1 && csbf[xS][yS+1] {
			prevCsbf |= 2
		}
		for n := start; n >= 0; n-- {
			if !coded {
				break
			}
			if n > 0 || !inferSbDc {
				xP, yP := int(inScan.order[n][0]), int(inScan.order[n][1])
				inc := sigCtxInc(t, in, out.TransformSkip, luma, log2, scanIdx, xS, yS, xP, yP, prevCsbf)
				if d.Decision(&m.Ctx[cabac.CtxSigCoeff+inc]) == 1 {
					sigN[nSig], nSig = n, nSig+1
					inferSbDc = false
				}
			} else { // n == 0 with inferSbDcSigCoeffFlag still set: inferred 1
				sigN[nSig], nSig = 0, nSig+1
			}
		}
		if nSig == 0 {
			continue
		}

		// coeff_abs_level_greater1_flag and greater2 (9.3.4.2.6, 9.3.4.2.7).
		ctxSet := 0
		if i > 0 && luma {
			ctxSet = 2
		}
		if haveG1 && prevG1 == 0 {
			ctxSet++
		}
		g1 := 1
		var greater1, greater2 [16]bool
		lastGreater1Idx := -1 // index into sigN of lastGreater1ScanPos
		numG1 := min(nSig, 8)
		// escapeDataPresent (7.3.8.11): a coefficient past the eighth, a second
		// greater1 flag set, or a greater2 flag set, each of which has a
		// coeff_abs_level_remaining[ ] to follow.
		escapeDataPresent := nSig > numG1
		for k := 0; k < numG1; k++ {
			inc := ctxSet*4 + min(3, g1)
			if !luma {
				inc += 16
			}
			f := d.Decision(&m.Ctx[cabac.CtxCoeffAbsGreater1+inc]) == 1
			greater1[k] = f
			if f && lastGreater1Idx == -1 {
				lastGreater1Idx = k
			} else if f {
				escapeDataPresent = true
			}
			if g1 > 0 {
				if f {
					g1 = 0
				} else {
					g1++
				}
			}
		}
		prevG1, haveG1 = g1, true
		if lastGreater1Idx != -1 {
			inc := ctxSet
			if !luma {
				inc += 4
			}
			greater2[lastGreater1Idx] = d.Decision(&m.Ctx[cabac.CtxCoeffAbsGreater2+inc]) == 1
			escapeDataPresent = escapeDataPresent || greater2[lastGreater1Idx]
		}

		firstSig, lastSig := sigN[nSig-1], sigN[0]
		signHidden := false
		if !(in.CuTransquantBypass ||
			(in.Intra && t.ImplicitRDPCM && out.TransformSkip && (in.PredModeIntra == 10 || in.PredModeIntra == 26)) ||
			out.ExplicitRDPCM) {
			signHidden = lastSig-firstSig > 3
		}
		hide := t.SignDataHiding && signHidden
		nSigns := nSig
		if hide {
			nSigns--
		}
		// 9.3.1: with cabac_bypass_alignment_enabled_flag, the sign flags and
		// the coeff_abs_level_remaining[ ] of a sub-block with escapeDataPresent
		// are decoded after the alignment of 9.3.4.3.6. ivlCurrRange stays 256
		// through bypass bins, so one alignment before the first sign flag (the
		// first of the requests; escape data implies a sign flag is coded)
		// covers them all.
		if t.BypassAlignment && escapeDataPresent {
			if err := d.Align(); err != nil {
				return fmt.Errorf("%w: %w", ErrResidual, err)
			}
		}
		signs := d.BypassBits(uint(nSigns)) // first flag is the top bit

		// coeff_abs_level_remaining and the levels, in the order of the sign flags.
		var rice cabac.SubBlock
		riceStarted := false
		sumAbs := int64(0)
		for k := 0; k < nSig; k++ {
			n := sigN[k]
			base := uint32(1)
			if greater1[k] {
				base++
			}
			if greater2[k] {
				base++
			}
			threshold := uint32(1)
			if k < 8 {
				threshold = 2
				if k == lastGreater1Idx {
					threshold = 3
				}
			}
			abs := int64(base)
			if base == threshold {
				if !riceStarted {
					rice = m.StartSubBlock(cabac.RiceConfig{
						Persistent:         t.PersistentRiceAdaptation,
						SbType:             cabac.SbType(cIdx, out.TransformSkip || in.CuTransquantBypass),
						Extended:           t.ExtendedPrecision,
						Log2TransformRange: uint(coeffBits(t, cIdx)),
					})
					riceStarted = true
				}
				rem, err := d.CoeffAbsLevelRemaining(m, &rice, base)
				if err != nil {
					return fmt.Errorf("%w: %w", ErrResidual, err)
				}
				abs += int64(rem)
			}
			level := abs
			if !hide || k < nSig-1 {
				if signs>>(uint(nSigns-1-k))&1 == 1 {
					level = -abs
				}
			}
			if hide {
				sumAbs += abs
				if k == nSig-1 && sumAbs%2 == 1 { // the sign of the first coefficient, from parity
					level = -level
				}
			}
			if level < lo || level > hi {
				return fmt.Errorf("%w: a level of %d outside %d to %d", ErrResidual, level, lo, hi)
			}
			xC := xS<<2 + int(inScan.order[n][0])
			yC := yS<<2 + int(inScan.order[n][1])
			out.Levels[yC*size+xC] = int32(level)
		}
	}
	return nil
}

func pick(luma bool, a, b int) int {
	if luma {
		return a
	}
	return b
}

// sigCtxInc is 9.3.4.2.5: ctxInc of sig_coeff_flag.
func sigCtxInc(t *ResidualTools, in ResidualInput, transformSkip, luma bool, log2, scanIdx, xS, yS, xP, yP, prevCsbf int) int {
	var sigCtx int
	switch {
	case t.TransformSkipContext && (transformSkip || in.CuTransquantBypass): // 9-40
		sigCtx = pick(luma, 42, 16)
	case log2 == 2: // 9-41
		sigCtx = int(ctxIdxMap[yP<<2+xP])
	case xS == 0 && yS == 0 && xP == 0 && yP == 0: // xC + yC == 0, 9-42
		sigCtx = 0
	default:
		switch prevCsbf { // 9-45 to 9-48
		case 0:
			sigCtx = 0
			if xP+yP == 0 {
				sigCtx = 2
			} else if xP+yP < 3 {
				sigCtx = 1
			}
		case 1:
			sigCtx = 2 - min(yP, 2)
		case 2:
			sigCtx = 2 - min(xP, 2)
		default:
			sigCtx = 2
		}
		if luma {
			if xS+yS > 0 {
				sigCtx += 3 // 9-49
			}
			if log2 == 3 {
				sigCtx += pick(scanIdx == 0, 9, 15) // 9-50
			} else {
				sigCtx += 21 // 9-51
			}
		} else if log2 == 3 {
			sigCtx += 9 // 9-52
		} else {
			sigCtx += 12 // 9-53
		}
	}
	if luma {
		return sigCtx // 9-54
	}
	return 27 + sigCtx // 9-55
}

// coeffBits is log2TransformRange of a component (9-14): 15, or Max( 15,
// BitDepth + 6 ) with extended_precision_processing_flag.
func coeffBits(t *ResidualTools, cIdx int) int {
	bd := t.BitDepthY
	if cIdx > 0 {
		bd = t.BitDepthC
	}
	if t.ExtendedPrecision {
		return max(15, bd+6)
	}
	return 15
}

// coeffLimits is CoeffMin and CoeffMax of a component.
func coeffLimits(t *ResidualTools, cIdx int) (lo, hi int64) {
	bits := coeffBits(t, cIdx)
	return -(1 << bits), 1<<bits - 1
}
