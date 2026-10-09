// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "fmt"

// maxLog2WeightDenom is the largest shift 7.4.7.3 allows for either component.
const maxLog2WeightDenom = 7

// RefWeight is how one reference picture is weighted, 7.4.7.3.
//
// Stated says the slice gave a weight for that component. Where it did not, the
// weight is the neutral one -- 1 << the denominator -- and the offset is zero,
// which this fills in so that a caller never has to ask whether a field was
// present before using it.
type RefWeight struct {
	LumaStated   bool
	LumaWeight   int32
	LumaOffset   int32
	ChromaStated bool
	// ChromaWeight and ChromaOffset are Cb then Cr.
	ChromaWeight [2]int32
	ChromaOffset [2]int32
}

// PredWeights is pred_weight_table, 7.3.6.3: how a slice weighs each picture it
// predicts from.
//
// ⛔ The weights here are VALUES, not the deltas the syntax carries. The format
// states each as a difference from the neutral weight, and the offsets of the
// chroma components as a difference that has to be folded with the weight
// before it means anything. A caller handed the raw deltas would scale by
// something close to zero.
type PredWeights struct {
	LumaLog2Denom   uint8
	ChromaLog2Denom uint8
	// L0 and L1 are as long as the lists they weigh.
	L0 []RefWeight
	L1 []RefWeight
}

// readPredWeights reads the table, which is stated only when the picture
// parameter set turns weighting on for this kind of slice.
func readPredWeights(r *sticky, sps SPS, refs *PictureRefs) (*PredWeights, error) {
	var w PredWeights
	denom := r.ue()
	if r.err != nil {
		return nil, r.err
	}
	if denom > maxLog2WeightDenom {
		return nil, fmt.Errorf("%w: a luma weight denominator of %d", ErrSliceHeader, denom)
	}
	w.LumaLog2Denom = uint8(denom)
	w.ChromaLog2Denom = w.LumaLog2Denom

	chroma := chromaArrayType(sps) != 0
	if chroma {
		// Stated as a difference from the luma one, and bounded the same way.
		c := int32(denom) + r.se()
		if r.err != nil {
			return nil, r.err
		}
		if c < 0 || c > maxLog2WeightDenom {
			return nil, fmt.Errorf("%w: a chroma weight denominator of %d", ErrSliceHeader, c)
		}
		w.ChromaLog2Denom = uint8(c)
	}

	var err error
	if w.L0, err = readWeightList(r, w, chroma, int(refs.NumRefIdxL0Active)); err != nil {
		return nil, fmt.Errorf("list 0: %w", err)
	}
	if refs.Type == SliceB {
		if w.L1, err = readWeightList(r, w, chroma, int(refs.NumRefIdxL1Active)); err != nil {
			return nil, fmt.Errorf("list 1: %w", err)
		}
	}
	return &w, nil
}

// readWeightList reads one list's weights.
//
// ⛔ Every luma flag comes FIRST, as one field of n bits, then every chroma
// flag, and only then the values. H.264 interleaves a flag with its values;
// HEVC does not, and a reader that carried the sibling's shape over would read
// the first entry's weight out of the second entry's flag.
func readWeightList(r *sticky, w PredWeights, chroma bool, n int) ([]RefWeight, error) {
	if n <= 0 || n > MaxRefIdxActive {
		return nil, fmt.Errorf("%w: %d active references", ErrSliceHeader, n)
	}
	lumaFlags := r.bits(n)
	chromaFlags := uint32(0)
	if chroma {
		chromaFlags = r.bits(n)
	}
	if r.err != nil {
		return nil, r.err
	}

	out := make([]RefWeight, n)
	for i := range out {
		// The flags are stated most significant first, so entry zero is the
		// top bit of the field rather than the bottom one.
		bit := uint32(1) << (n - 1 - i)
		e := &out[i]
		e.LumaWeight = 1 << w.LumaLog2Denom
		e.ChromaWeight = [2]int32{1 << w.ChromaLog2Denom, 1 << w.ChromaLog2Denom}

		if lumaFlags&bit != 0 {
			e.LumaStated = true
			d := r.se()
			if r.err != nil {
				return nil, r.err
			}
			// 7.4.7.3 puts the difference in -128..127, and the value is read
			// into eight bits by every decoder that uses it.
			if d < -128 || d > 127 {
				return nil, fmt.Errorf("%w: a luma weight difference of %d", ErrSliceHeader, d)
			}
			e.LumaWeight = 1<<w.LumaLog2Denom + d
			e.LumaOffset = r.se()
		}
		if chroma && chromaFlags&bit != 0 {
			e.ChromaStated = true
			for j := 0; j < 2; j++ {
				dw, do := r.se(), r.se()
				if r.err != nil {
					return nil, r.err
				}
				if dw < -128 || dw > 127 {
					return nil, fmt.Errorf("%w: a chroma weight difference of %d", ErrSliceHeader, dw)
				}
				if do < -(1<<17) || do > 1<<17 {
					return nil, fmt.Errorf("%w: a chroma offset difference of %d", ErrSliceHeader, do)
				}
				e.ChromaWeight[j] = 1<<w.ChromaLog2Denom + dw
				// ⛔ The chroma offset is not the difference the slice states:
				// the weight has to be folded back out of it first, 7.4.7.3.
				v := do - (128*e.ChromaWeight[j])>>w.ChromaLog2Denom + 128
				e.ChromaOffset[j] = clip(v, -128, 127)
			}
		}
		if r.err != nil {
			return nil, r.err
		}
	}
	return out, nil
}

func clip(v, lo, hi int32) int32 {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	}
	return v
}
