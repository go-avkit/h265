// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// The encoder side of the Exp-Golomb binarisations, from equations 9-13 and 9-16
// (a second writing of what internal/cabac's tests hold, for the syntax tests of
// this package).

func refEncEGk(v uint64, k uint) []int { // 9-13
	var bins []int
	absV := v
	for {
		if absV >= 1<<k {
			bins = append(bins, 1)
			absV -= 1 << k
			k++
		} else {
			bins = append(bins, 0)
			for k > 0 {
				k--
				bins = append(bins, int(absV>>k)&1)
			}
			return bins
		}
	}
}

func refEncLimitedEGk(symbolVal uint64, riceParam, log2TransformRange uint) []int { // 9-14 to 9-16
	var bins []int
	maxPre := 28 - log2TransformRange
	codeValue := symbolVal >> riceParam
	var pre uint
	for pre < maxPre && codeValue > (2<<pre)-2 {
		pre++
		bins = append(bins, 1)
	}
	var escapeLength uint
	if pre == maxPre {
		escapeLength = log2TransformRange
	} else {
		escapeLength = pre + riceParam
		bins = append(bins, 0)
	}
	symbolVal -= ((1 << pre) - 1) << riceParam
	for escapeLength > 0 {
		escapeLength--
		bins = append(bins, int(symbolVal>>escapeLength)&1)
	}
	return bins
}
