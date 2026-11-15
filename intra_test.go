// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"math/rand/v2"
	"testing"
)

// refIntra is 8.4.4.2.1 to 8.4.4.2.6 as the text has them: the neighbouring samples
// are p[ x ][ y ] on the coordinates -1..2n-1, held in a map, and each process is
// written with its own loops.
type pmap map[[2]int]int32

func refSubstitute(p pmap, avail map[[2]int]bool, n, bitDepth int) {
	var order [][2]int
	for y := 2*n - 1; y >= -1; y-- {
		order = append(order, [2]int{-1, y})
	}
	for x := 0; x <= 2*n-1; x++ {
		order = append(order, [2]int{x, -1})
	}
	any := false
	for _, k := range order {
		any = any || avail[k]
	}
	if !any {
		for _, k := range order {
			p[k] = 1 << uint(bitDepth-1)
		}
		return
	}
	if !avail[[2]int{-1, 2*n - 1}] {
		for _, k := range order {
			if avail[k] {
				p[[2]int{-1, 2*n - 1}] = p[k]
				break
			}
		}
	}
	for y := 2*n - 2; y >= -1; y-- {
		if !avail[[2]int{-1, y}] {
			p[[2]int{-1, y}] = p[[2]int{-1, y + 1}]
		}
	}
	for x := 0; x <= 2*n-1; x++ {
		if !avail[[2]int{x, -1}] {
			p[[2]int{x, -1}] = p[[2]int{x - 1, -1}]
		}
	}
}

func refPredict(p pmap, q IntraParams) []int32 {
	n := 1 << uint(q.Log2Size)
	P := func(x, y int) int32 { return p[[2]int{x, y}] }
	mode := q.Mode
	if q.FilterRefs && mode != 1 && n != 4 {
		thres := map[int]int{8: 7, 16: 1, 32: 0}[n]
		minDist := min(abs(mode-26), abs(mode-10))
		if minDist > thres {
			f := pmap{}
			if q.Strong && q.CIdx == 0 && n == 32 &&
				abs32(P(-1, -1)+P(2*n-1, -1)-2*P(n-1, -1)) < 1<<uint(q.BitDepth-5) &&
				abs32(P(-1, -1)+P(-1, 2*n-1)-2*P(-1, n-1)) < 1<<uint(q.BitDepth-5) {
				f[[2]int{-1, -1}] = P(-1, -1)
				for y := 0; y <= 62; y++ {
					f[[2]int{-1, y}] = (int32(63-y)*P(-1, -1) + int32(y+1)*P(-1, 63) + 32) >> 6
				}
				f[[2]int{-1, 63}] = P(-1, 63)
				for x := 0; x <= 62; x++ {
					f[[2]int{x, -1}] = (int32(63-x)*P(-1, -1) + int32(x+1)*P(63, -1) + 32) >> 6
				}
				f[[2]int{63, -1}] = P(63, -1)
			} else {
				f[[2]int{-1, -1}] = (P(-1, 0) + 2*P(-1, -1) + P(0, -1) + 2) >> 2
				for y := 0; y <= 2*n-2; y++ {
					f[[2]int{-1, y}] = (P(-1, y+1) + 2*P(-1, y) + P(-1, y-1) + 2) >> 2
				}
				f[[2]int{-1, 2*n - 1}] = P(-1, 2*n-1)
				for x := 0; x <= 2*n-2; x++ {
					f[[2]int{x, -1}] = (P(x-1, -1) + 2*P(x, -1) + P(x+1, -1) + 2) >> 2
				}
				f[[2]int{2*n - 1, -1}] = P(2*n-1, -1)
			}
			p = f
		}
	}
	P = func(x, y int) int32 { return p[[2]int{x, y}] }
	pred := make([][]int32, n) // [ x ][ y ]
	for x := range pred {
		pred[x] = make([]int32, n)
	}
	clip1 := func(v int32) int32 { return min(max(v, 0), 1<<uint(q.BitDepth)-1) }
	k := q.Log2Size
	switch {
	case mode == 0:
		for x := 0; x < n; x++ {
			for y := 0; y < n; y++ {
				pred[x][y] = (int32(n-1-x)*P(-1, y) + int32(x+1)*P(n, -1) + int32(n-1-y)*P(x, -1) + int32(y+1)*P(-1, n) + int32(n)) >> uint(k+1)
			}
		}
	case mode == 1:
		dc := int32(n)
		for x := 0; x < n; x++ {
			dc += P(x, -1)
		}
		for y := 0; y < n; y++ {
			dc += P(-1, y)
		}
		dc >>= uint(k + 1)
		for x := 0; x < n; x++ {
			for y := 0; y < n; y++ {
				pred[x][y] = dc
			}
		}
		if q.CIdx == 0 && n < 32 {
			pred[0][0] = (P(-1, 0) + 2*dc + P(0, -1) + 2) >> 2
			for x := 1; x < n; x++ {
				pred[x][0] = (P(x, -1) + 3*dc + 2) >> 2
			}
			for y := 1; y < n; y++ {
				pred[0][y] = (P(-1, y) + 3*dc + 2) >> 2
			}
		}
	default:
		angles := map[int]int{2: 32, 3: 26, 4: 21, 5: 17, 6: 13, 7: 9, 8: 5, 9: 2, 10: 0, 11: -2, 12: -5, 13: -9, 14: -13, 15: -17, 16: -21, 17: -26,
			18: -32, 19: -26, 20: -21, 21: -17, 22: -13, 23: -9, 24: -5, 25: -2, 26: 0, 27: 2, 28: 5, 29: 9, 30: 13, 31: 17, 32: 21, 33: 26, 34: 32}
		invs := map[int]int{11: -4096, 12: -1638, 13: -910, 14: -630, 15: -482, 16: -390, 17: -315, 18: -256,
			19: -315, 20: -390, 21: -482, 22: -630, 23: -910, 24: -1638, 25: -4096}
		angle := angles[mode]
		ref := map[int]int32{}
		if mode >= 18 {
			for x := 0; x <= n; x++ {
				ref[x] = P(-1+x, -1)
			}
			if angle < 0 {
				if (n*angle)>>5 < -1 {
					for x := -1; x >= (n*angle)>>5; x-- {
						ref[x] = P(-1, -1+((x*invs[mode]+128)>>8))
					}
				}
			} else {
				for x := n + 1; x <= 2*n; x++ {
					ref[x] = P(-1+x, -1)
				}
			}
			for x := 0; x < n; x++ {
				for y := 0; y < n; y++ {
					iIdx := ((y + 1) * angle) >> 5
					iFact := int32(((y + 1) * angle) & 31)
					if iFact != 0 {
						pred[x][y] = ((32-iFact)*ref[x+iIdx+1] + iFact*ref[x+iIdx+2] + 16) >> 5
					} else {
						pred[x][y] = ref[x+iIdx+1]
					}
				}
			}
			if mode == 26 && q.CIdx == 0 && n < 32 && !q.NoBoundaryFilter {
				for y := 0; y < n; y++ {
					pred[0][y] = clip1(P(0, -1) + ((P(-1, y) - P(-1, -1)) >> 1))
				}
			}
		} else {
			for x := 0; x <= n; x++ {
				ref[x] = P(-1, -1+x)
			}
			if angle < 0 {
				if (n*angle)>>5 < -1 {
					for x := -1; x >= (n*angle)>>5; x-- {
						ref[x] = P(-1+((x*invs[mode]+128)>>8), -1)
					}
				}
			} else {
				for x := n + 1; x <= 2*n; x++ {
					ref[x] = P(-1, -1+x)
				}
			}
			for x := 0; x < n; x++ {
				for y := 0; y < n; y++ {
					iIdx := ((x + 1) * angle) >> 5
					iFact := int32(((x + 1) * angle) & 31)
					if iFact != 0 {
						pred[x][y] = ((32-iFact)*ref[y+iIdx+1] + iFact*ref[y+iIdx+2] + 16) >> 5
					} else {
						pred[x][y] = ref[y+iIdx+1]
					}
				}
			}
			if mode == 10 && q.CIdx == 0 && n < 32 && !q.NoBoundaryFilter {
				for x := 0; x < n; x++ {
					pred[x][0] = clip1(P(-1, 0) + ((P(x, -1) - P(-1, -1)) >> 1))
				}
			}
		}
	}
	out := make([]int32, n*n)
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			out[y*n+x] = pred[x][y]
		}
	}
	return out
}

func TestIntraPredictionAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(131, 132))
	for iter := 0; iter < 4000; iter++ {
		q := IntraParams{
			Log2Size: 2 + rng.IntN(4), CIdx: rng.IntN(3), Mode: rng.IntN(35), BitDepth: 8 + rng.IntN(9),
			FilterRefs: rng.IntN(4) != 0, Strong: rng.IntN(2) == 0, NoBoundaryFilter: rng.IntN(4) == 0,
		}
		n := 1 << uint(q.Log2Size)
		refs := make([]int32, 4*n+1)
		avail := make([]bool, 4*n+1)
		pm, am := pmap{}, map[[2]int]bool{}
		coord := func(i int) [2]int { // the search order index to p[ x ][ y ]
			if i <= 2*n {
				return [2]int{-1, 2*n - 1 - i}
			}
			return [2]int{i - 2*n - 1, -1}
		}
		smooth := rng.IntN(3) == 0
		run, shift := 1+rng.IntN(2*n), rng.IntN(4*n)
		mode := rng.IntN(5) // how much is available: nothing, everything, or runs of it
		for i := range refs {
			switch mode {
			case 0:
				avail[i] = false
			case 1:
				avail[i] = true
			default:
				avail[i] = ((i+shift)/run)%2 == 0
			}
			refs[i] = int32(rng.IntN(1 << uint(q.BitDepth)))
			if smooth { // a smooth neighbourhood, so strong smoothing is taken
				refs[i] = int32(1<<uint(q.BitDepth-1)) + int32(rng.IntN(3))
			}
			pm[coord(i)] = refs[i]
			am[coord(i)] = avail[i]
		}
		SubstituteReferences(refs, avail, q.BitDepth)
		refSubstitute(pm, am, n, q.BitDepth)
		for i := range refs {
			if refs[i] != pm[coord(i)] {
				t.Fatalf("iter %d: substituted sample %d is %d, the text gives %d", iter, i, refs[i], pm[coord(i)])
			}
		}
		keep := append([]int32(nil), refs...)
		got := make([]int32, n*n)
		PredictIntra(refs, q, got)
		for i := range keep {
			if keep[i] != refs[i] {
				t.Fatalf("iter %d: the reference samples were modified", iter)
			}
		}
		want := refPredict(pm, q)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("iter %d %+v: sample %d is %d, the text gives %d", iter, q, i, got[i], want[i])
			}
		}
	}
}
