// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// The inverse transform of 8.6.4.2 in 32-bit arithmetic with partial butterflies.
//
// Exactness. Without extended_precision_processing_flag the coefficients d are
// clipped to -2^15..2^15-1 (7-27) and so is the intermediate g after the first
// stage (8-314). Each output of a stage is a sum of at most 32 terms of magnitude at
// most 90 * 2^15 (the largest entry of the matrix is 90), so at most 32 * 90 * 2^15 =
// 94 371 840 < 2^31: no sum can overflow an int32, and the result is identical to the
// 64-bit evaluation of transform2D, which stays as the literal model. The rounding
// offset and the shift of the second stage are applied by the caller in 64 bits.
//
// The cost. A basis function of even frequency is symmetric and one of odd frequency
// antisymmetric (the matrix is built so, see dctMatrix), so an n-point inverse
// transform is the n/2-point one of the even-frequency inputs plus a sum over the
// odd-frequency inputs, with outputs i and n-1-i taken as their sum and difference.
// And only the inputs up to the last significant position can be non-zero: columns
// and rows past it are skipped.

// idct1D computes y[0..n) of the n-point inverse DCT of x[0..n), whose entries past
// index last are zero.
func idct1D(x *[32]int32, n, last int, y *[32]int32) {
	if n == 4 {
		e0 := 64*x[0] + 64*x[2]
		e1 := 64*x[0] - 64*x[2]
		o0 := 83*x[1] + 36*x[3]
		o1 := 36*x[1] - 83*x[3]
		y[0], y[3] = e0+o0, e0-o0
		y[1], y[2] = e1+o1, e1-o1
		return
	}
	half := n / 2
	var ev, e [32]int32
	for j := 0; 2*j < n; j++ {
		ev[j] = x[2*j]
	}
	idct1D(&ev, half, last/2, &e)
	step := 32 / n
	for k := 0; k < half; k++ {
		var o int32
		for j := 1; j <= last; j += 2 {
			o += int32(dctCols0to15[j*step][k]) * x[j]
		}
		y[k], y[n-1-k] = e[k]+o, e[k]-o
	}
}

// idst4 is the 4-point inverse DST of (8-316).
func idst4(x *[32]int32, y *[32]int32) {
	for i := 0; i < 4; i++ {
		y[i] = dst4[0][i]*x[0] + dst4[1][i]*x[1] + dst4[2][i]*x[2] + dst4[3][i]*x[3]
	}
}

// transform2DFast is transform2D of the non-extended case in 32 bits: d and the
// result r are n x n, indexed y * n + x, and r is left unscaled (before the final
// bdShift).
func transform2DFast(d, r *[1024]int32, n int, dst bool) {
	maxX, maxY := -1, -1
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if d[y*n+x] != 0 {
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		for i := 0; i < n*n; i++ {
			r[i] = 0
		}
		return
	}
	if !dst && maxX == 0 && maxY == 0 { // only the DC coefficient: every output is the same
		v := min(max((64*d[0]+64)>>7, -32768), 32767)
		for i := 0; i < n*n; i++ {
			r[i] = 64 * v
		}
		return
	}
	var g [1024]int32
	var col, out [32]int32
	for x := 0; x <= maxX; x++ { // the columns, first stage; the others of g are zero
		for j := 0; j <= maxY; j++ {
			col[j] = d[j*n+x]
		}
		for j := maxY + 1; j < n; j++ {
			col[j] = 0
		}
		if dst {
			idst4(&col, &out)
		} else {
			idct1D(&col, n, maxY, &out)
		}
		for i := 0; i < n; i++ {
			g[i*n+x] = min(max((out[i]+64)>>7, -32768), 32767)
		}
	}
	for y := 0; y < n; y++ { // the rows, second stage
		for i := 0; i <= maxX; i++ {
			col[i] = g[y*n+i]
		}
		for i := maxX + 1; i < n; i++ {
			col[i] = 0
		}
		if dst {
			idst4(&col, &out)
		} else {
			idct1D(&col, n, maxX, &out)
		}
		copy(r[y*n:y*n+n], out[:n])
	}
}
