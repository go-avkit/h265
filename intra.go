// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// Intra sample prediction (8.4.4.2). The 4 * nTbS + 1 neighbouring samples are kept
// in one array in the order the substitution process of 8.4.4.2.2 searches them:
// p[ -1 ][ 2 * nTbS - 1 ] first, up the left column to p[ -1 ][ -1 ] (index
// 2 * nTbS), then along the top row p[ 0 ][ -1 ] to p[ 2 * nTbS - 1 ][ -1 ].

// SubstituteReferences is 8.4.4.2.2: the samples of refs that are not available
// take the value of the one before them in the search order, the first taking that of
// the first available one, or 1 << ( bitDepth - 1 ) when none is.
func SubstituteReferences(refs []int32, avail []bool, bitDepth int) {
	first := -1
	for i, a := range avail {
		if a {
			first = i
			break
		}
	}
	if first < 0 {
		for i := range refs {
			refs[i] = 1 << uint(bitDepth-1)
		}
		return
	}
	if first > 0 {
		refs[0] = refs[first]
	}
	for i := 1; i < len(refs); i++ {
		if !avail[i] {
			refs[i] = refs[i-1]
		}
	}
}

// IntraParams are the variables of 8.4.4.2.1 to 8.4.4.2.6 that are not samples.
type IntraParams struct {
	Log2Size int
	CIdx     int
	Mode     int // predModeIntra, 0 to 34
	BitDepth int
	// FilterRefs is intra_smoothing_disabled_flag equal to 0 and cIdx equal to 0 or
	// ChromaArrayType equal to 3 (8.4.4.2.1, step 1).
	FilterRefs bool
	// Strong is strong_intra_smoothing_enabled_flag.
	Strong bool
	// NoBoundaryFilter is disableIntraBoundaryFilter of 8.4.4.2.6: implicit RDPCM
	// with transquant bypass.
	NoBoundaryFilter bool
}

// intraPredAngle is Table 8-5, by predModeIntra 2 to 34 (index 0 and 1 unused).
var intraPredAngle = [35]int{0, 0, 32, 26, 21, 17, 13, 9, 5, 2, 0, -2, -5, -9, -13, -17, -21, -26,
	-32, -26, -21, -17, -13, -9, -5, -2, 0, 2, 5, 9, 13, 17, 21, 26, 32}

// invAngle is Table 8-6, by predModeIntra 11 to 25.
var invAngle = [26]int{11: -4096, 12: -1638, 13: -910, 14: -630, 15: -482, 16: -390, 17: -315, 18: -256,
	19: -315, 20: -390, 21: -482, 22: -630, 23: -910, 24: -1638, 25: -4096}

// PredictIntra is 8.4.4.2.1 from the substituted reference samples: the filtering
// of the neighbouring samples (8.4.4.2.3) and the planar, DC or angular prediction
// (8.4.4.2.4 to 8.4.4.2.6). refs is not modified; out is n * n samples, y * n + x.
func PredictIntra(refs []int32, p IntraParams, out []int32) {
	n := 1 << uint(p.Log2Size)
	s := refs
	if p.FilterRefs {
		s = filterNeighbours(refs, p, n)
	}
	left := func(y int) int32 { return s[2*n-1-y] } // p[ -1 ][ y ], y = -1..2n-1
	top := func(x int) int32 { return s[2*n+1+x] }  // p[ x ][ -1 ], x = -1..2n-1
	maxVal := int32(1)<<uint(p.BitDepth) - 1
	clip := func(v int32) int32 { return min(max(v, 0), maxVal) }
	switch {
	case p.Mode == 0: // INTRA_PLANAR (8-46)
		for y := 0; y < n; y++ {
			for x := 0; x < n; x++ {
				out[y*n+x] = (int32(n-1-x)*left(y) + int32(x+1)*top(n) +
					int32(n-1-y)*top(x) + int32(y+1)*left(n) + int32(n)) >> uint(p.Log2Size+1)
			}
		}
	case p.Mode == 1: // INTRA_DC (8-47 to 8-52)
		sum := int32(n)
		for i := 0; i < n; i++ {
			sum += top(i) + left(i)
		}
		dc := sum >> uint(p.Log2Size+1)
		for i := range out[:n*n] {
			out[i] = dc
		}
		if p.CIdx == 0 && n < 32 {
			out[0] = (left(0) + 2*dc + top(0) + 2) >> 2
			for x := 1; x < n; x++ {
				out[x] = (top(x) + 3*dc + 2) >> 2
			}
			for y := 1; y < n; y++ {
				out[y*n] = (left(y) + 3*dc + 2) >> 2
			}
		}
	default:
		angle := intraPredAngle[p.Mode]
		// main and side are the reference rows the prediction runs along and the one
		// the extension of 8-54 and 8-62 reads: for modes of 18 and up the top row,
		// otherwise the left column.
		main, side := top, left
		vertical := p.Mode >= 18
		if !vertical {
			main, side = left, top
		}
		ref := make([]int32, 3*n+2) // ref[ x ] at index x + n
		for x := 0; x <= n; x++ {
			ref[x+n] = main(x - 1)
		}
		if angle < 0 {
			if last := (n * angle) >> 5; last < -1 {
				for x := -1; x >= last; x-- {
					ref[x+n] = side(-1 + ((x*invAngle[p.Mode] + 128) >> 8))
				}
			}
		} else {
			for x := n + 1; x <= 2*n; x++ {
				ref[x+n] = main(x - 1)
			}
		}
		for j := 0; j < n; j++ { // j runs along the prediction direction's rows
			iIdx := ((j + 1) * angle) >> 5
			iFact := int32(((j + 1) * angle) & 31)
			for i := 0; i < n; i++ {
				var v int32
				if iFact != 0 {
					v = ((32-iFact)*ref[i+iIdx+1+n] + iFact*ref[i+iIdx+2+n] + 16) >> 5
				} else {
					v = ref[i+iIdx+1+n]
				}
				if vertical {
					out[j*n+i] = v // predSamples[ x = i ][ y = j ]
				} else {
					out[i*n+j] = v // predSamples[ x = j ][ y = i ]
				}
			}
		}
		if p.CIdx == 0 && n < 32 && !p.NoBoundaryFilter {
			switch p.Mode {
			case 26: // (8-60)
				for y := 0; y < n; y++ {
					out[y*n] = clip(top(0) + ((left(y) - left(-1)) >> 1))
				}
			case 10: // (8-68)
				for x := 0; x < n; x++ {
					out[x] = clip(left(0) + ((top(x) - top(-1)) >> 1))
				}
			}
		}
	}
}

// filterNeighbours is 8.4.4.2.3.
func filterNeighbours(s []int32, p IntraParams, n int) []int32 {
	if p.Mode == 1 || n == 4 {
		return s
	}
	thres := map[int]int{8: 7, 16: 1, 32: 0}[n]
	if min(abs(p.Mode-26), abs(p.Mode-10)) <= thres {
		return s
	}
	out := make([]int32, len(s))
	last := 4 * n
	corner := s[2*n]
	if p.Strong && p.CIdx == 0 && n == 32 &&
		abs32(corner+s[last]-2*s[3*n]) < 1<<uint(p.BitDepth-5) &&
		abs32(corner+s[0]-2*s[n]) < 1<<uint(p.BitDepth-5) {
		// (8-36) to (8-40): the left column and the top row become ramps.
		out[2*n] = corner
		out[0], out[last] = s[0], s[last]
		for i := 0; i < 63; i++ {
			out[2*n-1-i] = (int32(63-i)*corner + int32(i+1)*s[0] + 32) >> 6
			out[2*n+1+i] = (int32(63-i)*corner + int32(i+1)*s[last] + 32) >> 6
		}
		return out
	}
	out[0], out[last] = s[0], s[last]
	for i := 1; i < last; i++ {
		out[i] = (s[i-1] + 2*s[i] + s[i+1] + 2) >> 2
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
