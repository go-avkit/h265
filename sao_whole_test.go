// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"math/rand/v2"
	"reflect"
	"testing"
)

// saoScene is a picture of w x h luma samples of 16 x 16 coding tree blocks, one block of
// which is run through saoCTB with an edge offset, and the same block through the text's
// per-sample conditions (8.7.3.2) written from the scene's own slice, tile and picture facts.
type saoScene struct {
	name        string
	width       int      // picture width and height in luma samples
	rx, ry      int      // the block run
	slice       []int32  // SliceAddrRs of each block, raster scan (nil: one slice)
	across      []bool   // slice_loop_filter_across_slices_enabled_flag per block (nil: all true)
	tileCols    uint32   // tile columns (0: no tiles)
	acrossTiles bool     // loop_filter_across_tiles_enabled_flag
	noFilter    [][2]int // 8x8 coding units (x, y in samples) that pcm or bypass leave alone
	whole       bool     // the block must take the whole-block path
	_           struct{}
}

func (sc saoScene) run(t *testing.T, class uint8, seed uint64) {
	t.Helper()
	rng := rand.New(rand.NewPCG(seed, 99))
	sps := SPS{CodedWidth: uint32(sc.width), CodedHeight: uint32(sc.width), Log2CtbSize: 4, Log2MinCbSize: 3, Log2MinTbSize: 2,
		ChromaFormat: 1, BitDepthLuma: 8, BitDepthChroma: 8}
	pps := PPS{TileColumns: 1, TileRows: 1, LoopFilterAcrossTiles: sc.acrossTiles}
	if sc.tileCols > 0 {
		pps.TilesEnabled, pps.UniformTileSpacing, pps.TileColumns = true, true, sc.tileCols
	}
	l, err := NewCtbLayout(sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	ps := NewPictureState(sps, l)
	n := l.WidthCtbs * l.HeightCtbs
	for i := 0; i < n; i++ {
		ps.SliceAddr[i] = 0
		if sc.slice != nil {
			ps.SliceAddr[i] = sc.slice[i]
		}
		ps.DbAcross[i] = sc.across == nil || sc.across[i]
	}
	anyNoFilter := len(sc.noFilter) > 0
	for _, xy := range sc.noFilter {
		ps.NoFilter[ps.cuAt(xy[0], xy[1])] = true
	}
	p := SaoParams{}
	p.Type[0], p.EoClass[0], p.Offset[0] = 2, class, [4]int32{3, 2, -2, -3}
	src := make([]uint16, sc.width*sc.width)
	for i := range src {
		src[i] = uint16(100 + rng.IntN(6))
	}
	got := &Plane{Width: sc.width, Height: sc.width, Stride: sc.width, Pix: append([]uint16(nil), src...)}
	want := append([]uint16(nil), src...)

	taken := 0
	saoWholeBlockHook = func() { taken++ }
	defer func() { saoWholeBlockHook = nil }()
	ps.saoCTB(got, src, 0, &p, sc.rx, sc.ry, 16, 16, 1, 1, 8, anyNoFilter, pps)
	if (taken > 0) != sc.whole {
		t.Errorf("%s class %d: the whole-block path taken %d times, expected %v", sc.name, class, taken, sc.whole)
	}

	// the text's conditions, one sample at a time
	rsOf := func(x, y int) int { return (y/16)*l.WidthCtbs + x/16 }
	for y := sc.ry * 16; y < min((sc.ry+1)*16, sc.width); y++ {
		for x := sc.rx * 16; x < min((sc.rx+1)*16, sc.width); x++ {
			if anyNoFilter && ps.NoFilter[ps.cuAt(x, y)] {
				continue
			}
			var nb [2]int
			skip := false
			for k := 0; k < 2; k++ {
				xk, yk := x+saoHPos[class][k], y+saoVPos[class][k]
				if xk < 0 || yk < 0 || xk >= sc.width || yk >= sc.width {
					skip = true
					break
				}
				a, b := rsOf(x, y), rsOf(xk, yk)
				if ps.SliceAddr[a] != ps.SliceAddr[b] {
					// the one later in decoding order says whether the filter crosses
					later := a
					if l.RsToTs[b] > l.RsToTs[a] {
						later = b
					}
					if !ps.DbAcross[later] {
						skip = true
						break
					}
				}
				if !sc.acrossTiles && l.TileID[l.RsToTs[a]] != l.TileID[l.RsToTs[b]] {
					skip = true
					break
				}
				nb[k] = int(src[yk*sc.width+xk])
			}
			if skip {
				continue
			}
			cur := int(src[y*sc.width+x])
			e := 2 + sign(cur-nb[0]) + sign(cur-nb[1])
			if d := [5]int{3, 2, 0, -2, -3}[e]; d != 0 {
				want[y*sc.width+x] = uint16(min(max(cur+d, 0), 255))
			}
		}
	}
	if !reflect.DeepEqual(got.Pix, want) {
		for i := range want {
			if got.Pix[i] != want[i] {
				t.Fatalf("%s class %d: sample (%d,%d) is %d, the text gives %d", sc.name, class, i%sc.width, i/sc.width, got.Pix[i], want[i])
			}
		}
	}
	changed := 0
	for i := range want {
		if want[i] != src[i] {
			changed++
		}
	}
	if changed < 20 {
		t.Errorf("%s class %d: only %d samples change, the scene tests nothing", sc.name, class, changed)
	}
}

// TestSAOWholeBlockRoutingAgainstTheText: every condition that must keep a block off the
// whole-block kernel call is a scene where the call would give another answer than the text:
// the result is compared with the text's conditions and the path taken is asserted.
func TestSAOWholeBlockRoutingAgainstTheText(t *testing.T) {
	// a 3 x 3 picture of blocks, the middle one (1,1) run unless a scene says otherwise
	slices := func(addr ...int32) []int32 { return addr }
	scenes := []saoScene{
		{name: "all neighbours usable", width: 48, rx: 1, ry: 1, whole: true},
		{name: "picture corner", width: 48, rx: 0, ry: 0},
		{name: "picture right edge", width: 48, rx: 2, ry: 1},
		{name: "picture bottom edge", width: 48, rx: 1, ry: 2},
		// 40 wide: the last column of blocks is cut at the picture edge
		{name: "partial block at the picture edge", width: 40, rx: 2, ry: 1},
		{name: "slice above, filter across the slice boundary off in the block",
			width: 48, rx: 1, ry: 1, slice: slices(0, 0, 0, 3, 3, 3, 3, 3, 3), across: []bool{true, true, true, false, false, false, false, false, false}},
		{name: "slice above, filter across the slice boundary on",
			width: 48, rx: 1, ry: 1, slice: slices(0, 0, 0, 3, 3, 3, 3, 3, 3), whole: true},
		// the later slice is the neighbours': its flag counts, not the block's
		{name: "slice below, its flag off",
			width: 48, rx: 1, ry: 1, slice: slices(0, 0, 0, 0, 0, 0, 6, 6, 6), across: []bool{true, true, true, true, true, true, false, false, false}},
		{name: "slice below, its flag on, the block's off",
			width: 48, rx: 1, ry: 1, slice: slices(0, 0, 0, 0, 0, 0, 6, 6, 6), across: []bool{false, false, false, false, false, false, true, true, true}, whole: true},
		{name: "one corner neighbour in another slice with its flag off",
			width: 48, rx: 1, ry: 1, slice: slices(0, 0, 0, 0, 0, 0, 0, 0, 8), across: []bool{true, true, true, true, true, true, true, true, false}},
		{name: "tile boundary, filter across tiles off", width: 48, rx: 1, ry: 1, tileCols: 3},
		{name: "tile boundary, filter across tiles on", width: 48, rx: 1, ry: 1, tileCols: 3, acrossTiles: true, whole: true},
		{name: "one sample of the picture is pcm with the loop filter disabled", width: 48, rx: 1, ry: 1, noFilter: [][2]int{{40, 40}}},
		{name: "a no-filter unit inside the block", width: 48, rx: 1, ry: 1, noFilter: [][2]int{{20, 20}}},
	}
	// each of the eight neighbours alone in another slice whose flag keeps the filter out
	for nb := 0; nb < 9; nb++ {
		if nb == 4 {
			continue
		}
		slice, across := make([]int32, 9), make([]bool, 9)
		for i := range across {
			across[i] = true
		}
		slice[nb] = int32(nb) + 10
		if nb < 4 { // decoded before the block: the block's flag decides
			across[4] = false
		} else {
			across[nb] = false
		}
		scenes = append(scenes, saoScene{name: "neighbour " + string(rune('0'+nb)) + " alone in another slice", width: 48, rx: 1, ry: 1, slice: slice, across: across})
	}
	for _, sc := range scenes {
		for class := uint8(0); class < 4; class++ {
			sc.run(t, class, uint64(class)+7)
		}
	}
}
