// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// BenchmarkDecoderSeedStream decodes the pictures of the fuzz seed with one decoder per
// iteration; allocs/op is exact and is what the reuse of the reconstructor across
// slice segments and pictures lowers.
func BenchmarkDecoderSeedStream(b *testing.B) {
	units := seedUnits(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		d := NewDecoder(Options{})
		for _, u := range units {
			if _, err := d.Decode(u); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := d.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
