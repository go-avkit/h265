// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"sync"
	"testing"
)

// countRefusals runs f with the refusal hook counting, by entry point.
func countRefusals(t *testing.T, f func()) map[string]int {
	t.Helper()
	got := map[string]int{}
	var mu sync.Mutex
	kernelRefusal = func(name string, err error) {
		mu.Lock()
		got[name]++
		mu.Unlock()
	}
	defer func() { kernelRefusal = nil }()
	f()
	return got
}

// benchSeeds are the first twelve frames of the streams tools/bench decodes (its README
// has the commands, here with -frames:v 12), with the md5 of the yuv file ffmpeg writes
// for each (ffmpeg -i X -map 0:v -c:v rawvideo -f md5 -). They hold the shapes the bench
// streams do: 64 x 64 blocks, bi-prediction, explicit weights for the fade, 8 and 10 bits.
var benchSeeds = []struct{ file, md5 string }{
	{"alien-main-12.265", "3cd994cdefc44cd5da899b518b354973"},
	{"fade-main-12.265", "c99d500f9ba78e90fcbb5cd20d5f77aa"},
	{"alien-main10-12.265", "2472b2f18d12c70b8c14ba152c2791cf"},
}

// TestTheBenchSeedsDecodeWithoutAKernelRefusal is the gate on a silent slowdown: a block
// the kernels refuse is computed by the Go loops, which gives the same samples, so nothing
// but a profile says so (a 64 x 64 block needing 71 rows for the horizontal pass cost 12.7 %
// of a decode until a profile showed it). Every shape in these streams is one the kernels
// claim to take, so no entry point may refuse one; the output must also be ffmpeg's.
func TestTheBenchSeedsDecodeWithoutAKernelRefusal(t *testing.T) {
	for _, s := range benchSeeds {
		t.Run(s.file, func(t *testing.T) {
			data, err := os.ReadFile("testdata/bench/" + s.file)
			if err != nil {
				t.Fatal(err)
			}
			units, err := SplitAnnexB(data)
			if err != nil {
				t.Fatal(err)
			}
			sum := md5.New()
			refusals := countRefusals(t, func() {
				d := NewDecoder(Options{})
				outs, errs := decodeUnits(d, units)
				if len(errs) != 0 || len(outs) != 12 {
					t.Fatalf("%d pictures, %v", len(outs), errs)
				}
				for _, o := range outs {
					writePicture(sum, o.Picture)
				}
			})
			if len(refusals) != 0 {
				t.Errorf("the kernels refused calls: %v", refusals)
			}
			if got := hex.EncodeToString(sum.Sum(nil)); got != s.md5 {
				t.Errorf("md5 %s, ffmpeg's is %s", got, s.md5)
			}
		})
	}
}

// TestTheRefusalHookCountsEachEntryPointItNames makes the gate above able to fail: a call
// the kernels cannot take (a width of 3) is counted under the entry point's name, whether
// it is the first pass or the second of a two-dimensional filter that refuses.
func TestTheRefusalHookCountsEachEntryPointItNames(t *testing.T) {
	var sc interpScratch
	src := make([]uint16, 80*80)
	taps8, taps4 := lumaTaps[1], chromaTaps[1]
	refusals := countRefusals(t, func() {
		for _, taps := range [][]int32{taps8, taps4} {
			if kernelH(&sc, src, 80, 3, 8, 8, taps) {
				t.Errorf("%d taps: a width of 3 was taken", len(taps))
			}
		}
		// the first pass takes the block, the second one finds the destination too short
		for _, taps := range [][]int32{taps8, taps4} {
			if kernelHV(&sc, src, 80, 8, 8, 8, taps, taps, make([]int32, 8*8-1)) {
				t.Errorf("%d taps: a destination one sample short was taken", len(taps))
			}
		}
	})
	want := map[string]int{"HPass8": 1, "HPass4": 1, "VPass8": 1, "VPass4": 1}
	for name, n := range want {
		if refusals[name] != n {
			t.Errorf("%s refused %d times, want %d (%v)", name, refusals[name], n, refusals)
		}
	}
}

// TestAHorizontalPassOver71RowsRunsInBands is the shape of the defect: 64 x 64 luma with
// both fractions needs 71 rows of the horizontal pass, and the kernels take 64. The two
// bands must give what the Go loops give.
func TestAHorizontalPassOver71RowsRunsInBands(t *testing.T) {
	const w, h = 64, 64
	for _, bytes := range []bool{false, true} { // a plane of 16-bit samples, and of bytes
		pl := &Plane{Width: 160, Height: 160, Stride: 160, Pix16: make([]uint16, 160*160)}
		if bytes {
			pl = &Plane{Width: 160, Height: 160, Stride: 160, Pix8: make([]uint8, 160*160)}
		}
		for i := 0; i < 160*160; i++ {
			pl.set(i, int(uint16(i*7919>>3)&255))
		}
		var sc interpScratch
		got, want := make([]int32, w*h), make([]int32, w*h)
		refusals := countRefusals(t, func() {
			interpolateFast(&sc, pl, 40, 40, 1, 3, w, h, 8, 3, lumaTaps[:], got)
		})
		if len(refusals) != 0 {
			t.Errorf("bytes %v: refused: %v", bytes, refusals)
		}
		src, ss := px(pl)[(40-3)*pl.Stride+40-3:], pl.Stride
		mid := make([]int16, (h+7)*w)
		hpass(src, ss, h+7, w, lumaTaps[1], 0, mid)
		vpass(mid, w, w, h, lumaTaps[3], 6, want)
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("bytes %v: sample %d: %d, the Go loops give %d", bytes, i, got[i], want[i])
			}
		}
	}
}

// TestAverageFallsBackToTheGoLoopWhenTheKernelRefuses: a width of 3 is one the kernel does not
// take, so the loop computes it, and the refusal is counted under Average.
func TestAverageFallsBackToTheGoLoopWhenTheKernelRefuses(t *testing.T) {
	mk := func(seed uint16) *Plane {
		pl := &Plane{Width: 8, Height: 4, Stride: 8, Pix16: make([]uint16, 32)}
		for i := range pl.Pix16 {
			pl.Pix16[i] = seed + uint16(i)*3001
		}
		return pl
	}
	ra, rb, dst := mk(1), mk(40000), mk(0)
	refusals := countRefusals(t, func() {
		average(dst, 1, 1, 3, 2, fullPel{ref: ra, xi: 2, yi: 1}, fullPel{ref: rb, xi: 4, yi: 2})
	})
	if refusals["Average"] != 1 {
		t.Errorf("refusals %v", refusals)
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			want := uint16((uint32(ra.Pix16[(1+y)*8+2+x]) + uint32(rb.Pix16[(2+y)*8+4+x]) + 1) >> 1)
			if got := dst.Pix16[(1+y)*8+1+x]; got != want {
				t.Errorf("(%d,%d): %d, want %d", x, y, got, want)
			}
		}
	}
}
