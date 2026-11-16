// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"math"
	"math/rand/v2"
	"testing"
)

// specCols16to31 is transMatrixCol16to31 (8-321) as the text prints it: row k is
// basis function k at positions 16..31.
var specCols16to31 = [32][16]int32{
	{64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64, 64},
	{-4, -13, -22, -31, -38, -46, -54, -61, -67, -73, -78, -82, -85, -88, -90, -90},
	{-90, -87, -80, -70, -57, -43, -25, -9, 9, 25, 43, 57, 70, 80, 87, 90},
	{13, 38, 61, 78, 88, 90, 85, 73, 54, 31, 4, -22, -46, -67, -82, -90},
	{89, 75, 50, 18, -18, -50, -75, -89, -89, -75, -50, -18, 18, 50, 75, 89},
	{-22, -61, -85, -90, -73, -38, 4, 46, 78, 90, 82, 54, 13, -31, -67, -88},
	{-87, -57, -9, 43, 80, 90, 70, 25, -25, -70, -90, -80, -43, 9, 57, 87},
	{31, 78, 90, 61, 4, -54, -88, -82, -38, 22, 73, 90, 67, 13, -46, -85},
	{83, 36, -36, -83, -83, -36, 36, 83, 83, 36, -36, -83, -83, -36, 36, 83},
	{-38, -88, -73, -4, 67, 90, 46, -31, -85, -78, -13, 61, 90, 54, -22, -82},
	{-80, -9, 70, 87, 25, -57, -90, -43, 43, 90, 57, -25, -87, -70, 9, 80},
	{46, 90, 38, -54, -90, -31, 61, 88, 22, -67, -85, -13, 73, 82, 4, -78},
	{75, -18, -89, -50, 50, 89, 18, -75, -75, 18, 89, 50, -50, -89, -18, 75},
	{-54, -85, 4, 88, 46, -61, -82, 13, 90, 38, -67, -78, 22, 90, 31, -73},
	{-70, 43, 87, -9, -90, -25, 80, 57, -57, -80, 25, 90, 9, -87, -43, 70},
	{61, 73, -46, -82, 31, 88, -13, -90, -4, 90, 22, -85, -38, 78, 54, -67},
	{64, -64, -64, 64, 64, -64, -64, 64, 64, -64, -64, 64, 64, -64, -64, 64},
	{-67, -54, 78, 38, -85, -22, 90, 4, -90, 13, 88, -31, -82, 46, 73, -61},
	{-57, 80, 25, -90, 9, 87, -43, -70, 70, 43, -87, -9, 90, -25, -80, 57},
	{73, 31, -90, 22, 78, -67, -38, 90, -13, -82, 61, 46, -88, 4, 85, -54},
	{50, -89, 18, 75, -75, -18, 89, -50, -50, 89, -18, -75, 75, 18, -89, 50},
	{-78, -4, 82, -73, -13, 85, -67, -22, 88, -61, -31, 90, -54, -38, 90, -46},
	{-43, 90, -57, -25, 87, -70, -9, 80, -80, 9, 70, -87, 25, 57, -90, 43},
	{82, -22, -54, 90, -61, -13, 78, -85, 31, 46, -90, 67, 4, -73, 88, -38},
	{36, -83, 83, -36, -36, 83, -83, 36, 36, -83, 83, -36, -36, 83, -83, 36},
	{-85, 46, 13, -67, 90, -73, 22, 38, -82, 88, -54, -4, 61, -90, 78, -31},
	{-25, 70, -90, 80, -43, -9, 57, -87, 87, -57, 9, 43, -80, 90, -70, 25},
	{88, -67, 31, 13, -54, 82, -90, 78, -46, 4, 38, -73, 90, -85, 61, -22},
	{18, -50, 75, -89, 89, -75, 50, -18, -18, 50, -75, 89, -89, 75, -50, 18},
	{-90, 82, -67, 46, -22, -4, 31, -54, 73, -85, 90, -88, 78, -61, 38, -13},
	{-9, 25, -43, 57, -70, 80, -87, 90, -90, 87, -80, 70, -57, 43, -25, 9},
	{90, -90, 88, -85, 82, -78, 73, -67, 61, -54, 46, -38, 31, -22, 13, -4},
}

// The second half of the matrix derived from the first by symmetry is the second
// half the text prints.
func TestDCTMatrixSecondHalfIsTheTextsSecondHalf(t *testing.T) {
	for k := 0; k < 32; k++ {
		for m := 0; m < 16; m++ {
			if dctMatrix[k][16+m] != specCols16to31[k][m] {
				t.Errorf("basis %d position %d: %d, the text's %d", k, 16+m, dctMatrix[k][16+m], specCols16to31[k][m])
			}
		}
	}
}

// The integer matrices approximate the orthonormal DCT and DST scaled by
// 64 * sqrt( N ) for every size N (the rows of the smaller ones are every
// 32/N-th row of the 32-point matrix): a float oracle for every entry.
func TestTransformMatricesAgainstFloatBases(t *testing.T) {
	for _, n := range []int{4, 8, 16, 32} {
		step := 32 / n
		scale := 64 * math.Sqrt(float64(n))
		for k := 0; k < n; k++ {
			for m := 0; m < n; m++ {
				ck := math.Sqrt(2.0 / float64(n))
				if k == 0 {
					ck = math.Sqrt(1.0 / float64(n))
				}
				want := scale * ck * math.Cos(float64((2*m+1)*k)*math.Pi/float64(2*n))
				if got := float64(dctMatrix[k*step][m]); math.Abs(got-want) > 1.5 { // the matrix is hand-adjusted by up to 1.4
					t.Errorf("DCT %d: basis %d position %d is %v, %.2f expected", n, k, m, got, want)
				}
			}
		}
	}
	// DST-VII: sin( pi * ( 2m + 1 )( k + 1 ) / ( 2N + 1 ) ) * 2 / sqrt( 2N + 1 ), N = 4.
	for k := 0; k < 4; k++ {
		for m := 0; m < 4; m++ {
			want := 128 * 2 / 3.0 * math.Sin(float64((m+1)*(2*k+1))*math.Pi/9)
			if got := float64(dst4[k][m]); math.Abs(got-want) > 1.0 {
				t.Errorf("DST: basis %d position %d is %v, %.2f expected", k, m, got, want)
			}
		}
	}
}

// refResidual is 8.6.2 to 8.6.4 written as the text has it, with arrays indexed [ x ][ y ].
func refResidual(levels []int32, p BlockParams) []int32 {
	n := 1 << uint(p.Log2Size)
	lvl := func(x, y int) int64 { return int64(levels[y*n+x]) }
	r := make([][]int64, n)
	for x := range r {
		r[x] = make([]int64, n)
	}
	rotate := p.RotateEnabled && n == 4 && p.Intra
	if p.TransquantBypass {
		for x := 0; x < n; x++ {
			for y := 0; y < n; y++ {
				if rotate {
					r[x][y] = lvl(n-x-1, n-y-1)
				} else {
					r[x][y] = lvl(x, y)
				}
			}
		}
	} else {
		bitDepth := p.BitDepth
		log2Range := 15
		if p.ExtendedPrecision {
			log2Range = max(15, bitDepth+6)
		}
		coeffMin, coeffMax := -(int64(1) << uint(log2Range)), int64(1)<<uint(log2Range)-1
		bdShift := bitDepth + p.Log2Size + 10 - log2Range
		d := make([][]int64, n)
		for x := range d {
			d[x] = make([]int64, n)
			for y := 0; y < n; y++ {
				m := int64(16)
				if p.Factor != nil && !(p.TransformSkip && n > 4) {
					m = int64(p.Factor[y*n+x])
				}
				ls := [6]int64{40, 45, 51, 57, 64, 72}[p.QP%6]
				v := (lvl(x, y)*m*ls<<uint(p.QP/6) + 1<<uint(bdShift-1)) >> uint(bdShift)
				d[x][y] = min(max(v, coeffMin), coeffMax)
			}
		}
		if p.TransformSkip {
			for x := 0; x < n; x++ {
				for y := 0; y < n; y++ {
					if rotate {
						r[x][y] = d[n-x-1][n-y-1] << uint(5+p.Log2Size)
					} else {
						r[x][y] = d[x][y] << uint(5+p.Log2Size)
					}
				}
			}
		} else {
			dst := p.Intra && n == 4 && p.CIdx == 0
			coef := func(freq, pos int) int64 { // the matrix of (8-316), (8-319), (8-321)
				if dst {
					return int64(dst4[freq][pos])
				}
				full := int64(dctCols0to15[freq*(32/n)%32][pos%16])
				if pos < 16 {
					return full
				}
				return int64(specCols16to31[freq*(32/n)][pos-16])
			}
			e := make([][]int64, n)
			g := make([][]int64, n)
			for x := range e {
				e[x], g[x] = make([]int64, n), make([]int64, n)
			}
			for x := 0; x < n; x++ { // columns
				for i := 0; i < n; i++ {
					var s int64
					for j := 0; j < n; j++ {
						s += coef(j, i) * d[x][j]
					}
					e[x][i] = s
					g[x][i] = min(max((s+64)>>7, coeffMin), coeffMax)
				}
			}
			for y := 0; y < n; y++ { // rows
				for i := 0; i < n; i++ {
					var s int64
					for j := 0; j < n; j++ {
						s += coef(j, i) * g[j][y]
					}
					r[i][y] = s
				}
			}
		}
		shift := max(20-bitDepth, 0)
		if p.ExtendedPrecision {
			shift = max(20-bitDepth, 11)
		}
		for x := 0; x < n; x++ {
			for y := 0; y < n; y++ {
				r[x][y] = (r[x][y] + 1<<uint(shift-1)) >> uint(shift)
			}
		}
	}
	out := make([]int32, n*n)
	for x := 0; x < n; x++ {
		for y := 0; y < n; y++ {
			out[y*n+x] = int32(r[x][y])
		}
	}
	return out
}

func TestResidualAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(121, 122))
	for iter := 0; iter < 3000; iter++ {
		p := BlockParams{
			Log2Size: 2 + rng.IntN(4), CIdx: rng.IntN(3), BitDepth: 8 + rng.IntN(9),
			ExtendedPrecision: rng.IntN(4) == 0, TransquantBypass: rng.IntN(8) == 0,
			TransformSkip: rng.IntN(4) == 0, Intra: rng.IntN(2) == 0, RotateEnabled: rng.IntN(2) == 0,
		}
		n := 1 << uint(p.Log2Size)
		p.QP = rng.IntN(52 + 6*(p.BitDepth-8))
		if rng.IntN(2) == 0 {
			p.Factor = make([]uint8, n*n)
			for i := range p.Factor {
				p.Factor[i] = uint8(1 + rng.IntN(255))
			}
		}
		bits := 15
		if p.ExtendedPrecision {
			bits = max(15, p.BitDepth+6)
		}
		levels := make([]int32, n*n)
		for i := range levels {
			if rng.IntN(3) == 0 {
				levels[i] = int32(rng.IntN(1<<uint(bits)) - 1<<uint(bits-1))
			}
			if rng.IntN(20) == 0 {
				levels[i] = int32(1<<uint(bits) - 1)
			}
		}
		got := make([]int32, n*n)
		if err := Residual(levels, p, got); err != nil {
			t.Fatal(err)
		}
		want := refResidual(levels, p)
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("iter %d %+v: sample %d is %d, the text gives %d", iter, p, i, got[i], want[i])
			}
		}
	}
}

func TestBlockResidualRefusals(t *testing.T) {
	ok := BlockParams{Log2Size: 2, BitDepth: 8}
	if err := Residual(make([]int32, 16), ok, make([]int32, 15)); err == nil {
		t.Error("an output too short")
	}
	if err := Residual(make([]int32, 15), ok, make([]int32, 16)); err == nil {
		t.Error("levels too few")
	}
	if err := Residual(make([]int32, 4), BlockParams{Log2Size: 1, BitDepth: 8}, make([]int32, 4)); err == nil {
		t.Error("a 2x2 block")
	}
}

func TestChromaQPTableAgainstTable8_10(t *testing.T) {
	for qPi, want := range map[int]int{-5: -5, 0: 0, 29: 29, 30: 29, 31: 30, 32: 31, 33: 32, 34: 33, 35: 33, 36: 34, 37: 34,
		38: 35, 39: 35, 40: 36, 41: 36, 42: 37, 43: 37, 44: 38, 51: 45, 57: 51} {
		if got := ChromaQPTable(qPi); got != want {
			t.Errorf("qPi %d: %d, the table's %d", qPi, got, want)
		}
	}
}

func TestResidualModifications(t *testing.T) {
	// 8.6.5 on a 4x4 block, written out: horizontal accumulates along rows.
	r := []int32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	h := append([]int32(nil), r...)
	RDPCM(h, 4, 0)
	v := append([]int32(nil), r...)
	RDPCM(v, 4, 1)
	wantH := []int32{1, 3, 6, 10, 5, 11, 18, 26, 9, 19, 30, 42, 13, 27, 42, 58}
	wantV := []int32{1, 2, 3, 4, 6, 8, 10, 12, 15, 18, 21, 24, 28, 32, 36, 40}
	for i := range r {
		if h[i] != wantH[i] || v[i] != wantV[i] {
			t.Fatalf("RDPCM sample %d: %d %d, want %d %d", i, h[i], v[i], wantH[i], wantV[i])
		}
	}
	// 8.6.6: ( 4 * ( ( 100 << 10 ) >> 8 ) ) >> 3 = 200, and a negative scale rounds down.
	c := []int32{1000, 1000}
	CrossComponent(c, []int32{100, -100}, 4, 8, 10)
	if c[0] != 1200 || c[1] != 800 {
		t.Errorf("cross-component: %v", c)
	}
	c = []int32{0}
	CrossComponent(c, []int32{3}, -1, 8, 8) // ( -1 * 3 ) >> 3 = -1
	if c[0] != -1 {
		t.Errorf("a negative scale: %v", c)
	}
}

// A bit depth of 20 makes the final shift of the residual (8-299) zero, which the kernels'
// transforms do not take: they refuse (counted), and the Go butterflies give the text's result.
func TestResidualFallsBackToTheGoTransformWhenTheKernelRefuses(t *testing.T) {
	rng := rand.New(rand.NewPCG(123, 124))
	for _, intraLuma := range []bool{false, true} {
		p := BlockParams{Log2Size: 2, BitDepth: 20, QP: 4, Intra: intraLuma}
		levels := make([]int32, 16)
		for i := range levels {
			levels[i] = int32(rng.IntN(200) - 100)
		}
		got := make([]int32, 16)
		refusals := countRefusals(t, func() {
			if err := Residual(levels, p, got); err != nil {
				t.Fatal(err)
			}
		})
		if refusals["IDCT"] != 1 {
			t.Errorf("intra %v: refusals %v", intraLuma, refusals)
		}
		for i, w := range refResidual(levels, p) {
			if got[i] != w {
				t.Fatalf("intra %v: sample %d is %d, the text gives %d", intraLuma, i, got[i], w)
			}
		}
	}
}
