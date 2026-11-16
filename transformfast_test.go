// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"math/rand/v2"
	"testing"
)

// The 32-bit butterfly transform against transform2D, the matrix product of the text in
// 64 bits: same coefficients, same output, for dense, sparse, DC-only and extreme blocks.
func TestFastInverseTransformAgainstTheMatrixProduct(t *testing.T) {
	rng := rand.New(rand.NewPCG(501, 502))
	var dcOnly, sparse, extreme, empty int
	for iter := 0; iter < 20000; iter++ {
		n := 4 << uint(rng.IntN(4))
		dst := n == 4 && rng.IntN(2) == 0
		var d32, r32 [1024]int32
		d64, want := make([]int64, n*n), make([]int64, n*n)
		set := func(x, y int, v int32) {
			d32[y*n+x], d64[y*n+x] = v, int64(v)
		}
		switch kind := rng.IntN(6); kind {
		case 0: // dense, small
			for i := 0; i < n*n; i++ {
				set(i%n, i/n, int32(rng.IntN(2001)-1000))
			}
		case 1: // dense, the full 16-bit range
			for i := 0; i < n*n; i++ {
				set(i%n, i/n, int32(rng.IntN(65536)-32768))
			}
		case 2: // the corners of the range: the largest sums there can be
			extreme++
			for i := 0; i < n*n; i++ {
				v := int32(32767)
				if rng.IntN(2) == 0 {
					v = -32768
				}
				set(i%n, i/n, v)
			}
		case 3: // only a low-frequency corner
			sparse++
			w, h := 1+rng.IntN(n), 1+rng.IntN(n)
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					set(x, y, int32(rng.IntN(4001)-2000))
				}
			}
		case 4: // DC only
			dcOnly++
			set(0, 0, int32(rng.IntN(65536)-32768))
		default: // a few isolated positions
			sparse++
			for k := 0; k < 1+rng.IntN(4); k++ {
				set(rng.IntN(n), rng.IntN(n), int32(rng.IntN(65536)-32768))
			}
		}
		if iter%997 == 0 { // nothing at all
			empty++
			d32, d64 = [1024]int32{}, make([]int64, n*n)
		}
		transform2D(d64, want, n, dst, -32768, 32767)
		transform2DFast(&d32, &r32, n, dst)
		for i := 0; i < n*n; i++ {
			if int64(r32[i]) != want[i] {
				t.Fatalf("iter %d: %dx%d dst %v: sample %d is %d, the matrix product gives %d", iter, n, n, dst, i, r32[i], want[i])
			}
		}
	}
	if dcOnly == 0 || sparse == 0 || extreme == 0 || empty == 0 {
		t.Error("a block kind was never exercised")
	}
}
