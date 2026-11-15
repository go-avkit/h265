// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"testing"
)

// ppsFor is a PPS naming sps with the tile grid given (nil sizes: uniform).
func ppsFor(sps SPS, cols, rows int, colW, rowH []uint32) PPS {
	p := PPS{SPSID: sps.ID, TileColumns: uint32(cols), TileRows: uint32(rows), TilesEnabled: cols*rows > 1}
	p.UniformTileSpacing = colW == nil && rowH == nil
	p.ColumnWidthMinus1, p.RowHeightMinus1 = colW, rowH
	return p
}

// refRsToTs is the same conversion written by definition rather than by the
// formulas of 6.5.1: visit the tiles in raster order and the blocks of each tile
// in raster order, numbering them as they come.
func refRsToTs(wCtbs int, colBd, rowBd []int) []int32 {
	out := make([]int32, wCtbs*rowBd[len(rowBd)-1])
	ts := int32(0)
	for j := 0; j+1 < len(rowBd); j++ {
		for i := 0; i+1 < len(colBd); i++ {
			for y := rowBd[j]; y < rowBd[j+1]; y++ {
				for x := colBd[i]; x < colBd[i+1]; x++ {
					out[y*wCtbs+x] = ts
					ts++
				}
			}
		}
	}
	return out
}

func TestCtbLayoutWithoutTilesIsTheRasterScan(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 80, height: 48}) // 5x3 blocks of 16
	l, err := NewCtbLayout(sps, ppsFor(sps, 1, 1, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if l.WidthCtbs != 5 || l.HeightCtbs != 3 {
		t.Fatalf("%dx%d", l.WidthCtbs, l.HeightCtbs)
	}
	for i := range l.RsToTs {
		if l.RsToTs[i] != int32(i) || l.TsToRs[i] != int32(i) || l.TileID[i] != 0 {
			t.Fatalf("address %d: ts %d, rs %d, tile %d", i, l.RsToTs[i], l.TsToRs[i], l.TileID[i])
		}
	}
}

func TestCtbLayoutAgainstTheDefinition(t *testing.T) {
	rng := rand.New(rand.NewPCG(41, 42))
	for iter := 0; iter < 300; iter++ {
		w, h := 16*(1+rng.IntN(20)), 16*(1+rng.IntN(15))
		sps := mustSPS(t, set{chroma: 1, width: uint32(w), height: uint32(h)})
		wc, hc := w/16, h/16
		cols, rows := 1+rng.IntN(min(wc, 6)), 1+rng.IntN(min(hc, 6))
		var colW, rowH []uint32
		if rng.IntN(2) == 0 { // stated sizes that leave at least one block each
			colW, rowH = statedSizes(rng, wc, cols), statedSizes(rng, hc, rows)
		}
		pps := ppsFor(sps, cols, rows, colW, rowH)
		l, err := NewCtbLayout(sps, pps)
		if err != nil {
			t.Fatalf("%dx%d blocks, %dx%d tiles, %v %v: %v", wc, hc, cols, rows, colW, rowH, err)
		}
		// colBd and rowBd: strictly increasing, from 0 to the picture's size.
		for _, bd := range []struct {
			bd    []int
			total int
			n     int
		}{{l.ColBd, wc, cols}, {l.RowBd, hc, rows}} {
			if len(bd.bd) != bd.n+1 || bd.bd[0] != 0 || bd.bd[bd.n] != bd.total {
				t.Fatalf("boundaries %v of %d in %d", bd.bd, bd.total, bd.n)
			}
			for i := 0; i < bd.n; i++ {
				if bd.bd[i+1] <= bd.bd[i] {
					t.Fatalf("boundaries %v are not increasing", bd.bd)
				}
			}
		}
		want := refRsToTs(wc, l.ColBd, l.RowBd)
		for rs := range want {
			if l.RsToTs[rs] != want[rs] {
				t.Fatalf("%dx%d blocks, %v %v: RsToTs[%d] = %d, by definition %d", wc, hc, colW, rowH, rs, l.RsToTs[rs], want[rs])
			}
			if l.TsToRs[l.RsToTs[rs]] != int32(rs) {
				t.Fatalf("TsToRs is not the inverse at %d", rs)
			}
		}
		// A tile's blocks are consecutive in tile scan, and tile ids rise.
		prev := int32(0)
		for ts, id := range l.TileID {
			if id < prev || id > prev+1 {
				t.Fatalf("tile id %d after %d at %d", id, prev, ts)
			}
			prev = id
		}
		if int(prev) != cols*rows-1 {
			t.Fatalf("%d tiles, last id %d", cols*rows, prev)
		}
	}
}

// statedSizes are n-1 column widths (or row heights) minus 1 that leave the last
// one at least one block of total.
func statedSizes(rng *rand.Rand, total, n int) []uint32 {
	out := make([]uint32, 0, n-1)
	left := total
	for i := 0; i < n-1; i++ {
		room := left - (n - 1 - i) // each remaining one still needs a block
		w := 1 + rng.IntN(max(1, room))
		if w > room {
			w = room
		}
		out = append(out, uint32(w-1))
		left -= w
	}
	return out
}

func TestMinTbAddrZsIsTheZOrder(t *testing.T) {
	// 32-blocks of 4x4 transform blocks: 8x8 minimum blocks per coding tree
	// block, three levels of the z-order.
	sps := mustSPS(t, set{chroma: 1, width: 96, height: 64, sizes: []uint32{0, 2, 0, 3, 0, 0}})
	l, err := NewCtbLayout(sps, ppsFor(sps, 2, 1, []uint32{0}, nil))
	if err != nil {
		t.Fatal(err)
	}
	perCtb := 8
	// The z-order of one block, by recursion.
	var z func(x0, y0, size int, next *int, out map[[2]int]int)
	z = func(x0, y0, size int, next *int, out map[[2]int]int) {
		if size == 1 {
			out[[2]int{x0, y0}] = *next
			*next++
			return
		}
		h := size / 2
		for _, q := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
			z(x0+q[0]*h, y0+q[1]*h, h, next, out)
		}
	}
	local := map[[2]int]int{}
	n := 0
	z(0, 0, perCtb, &n, local)
	for y := 0; y < l.HeightCtbs*perCtb; y++ {
		for x := 0; x < l.WidthCtbs*perCtb; x++ {
			ctb := (y/perCtb)*l.WidthCtbs + x/perCtb
			want := int(l.RsToTs[ctb])*perCtb*perCtb + local[[2]int{x % perCtb, y % perCtb}]
			if got := l.MinTbAddrZs(x, y); got != want {
				t.Fatalf("MinTbAddrZs(%d, %d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestCtbLayoutRefusals(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 64, height: 64})
	if _, err := NewCtbLayout(sps, PPS{SPSID: 1, TileColumns: 1, TileRows: 1}); !errors.Is(err, ErrUnsupportedPPS) {
		t.Errorf("a PPS of another sequence: %v", err)
	}
	huge := mustSPS(t, set{chroma: 1, width: 1 << 14, height: 1 << 14}) // 2^20 blocks of 16
	if _, err := NewCtbLayout(huge, ppsFor(huge, 1, 1, nil, nil)); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a picture of 2^20 blocks: %v", err)
	}
}

func TestScanOrders(t *testing.T) {
	// 6-11 worked by hand for 4x4.
	want := []Scan{{
		{0, 0}, {0, 1}, {1, 0}, {0, 2}, {1, 1}, {2, 0}, {0, 3}, {1, 2},
		{2, 1}, {3, 0}, {1, 3}, {2, 2}, {3, 1}, {2, 3}, {3, 2}, {3, 3},
	}}
	got := DiagScan(4)
	for i := range want[0] {
		if got[i] != want[0][i] {
			t.Fatalf("diagonal scan[%d] = %v, want %v", i, got[i], want[0][i])
		}
	}
	if h := HorScan(2); h[0] != [2]uint8{0, 0} || h[1] != [2]uint8{1, 0} || h[2] != [2]uint8{0, 1} || h[3] != [2]uint8{1, 1} {
		t.Errorf("horizontal scan %v", h)
	}
	if v := VerScan(2); v[0] != [2]uint8{0, 0} || v[1] != [2]uint8{0, 1} || v[2] != [2]uint8{1, 0} || v[3] != [2]uint8{1, 1} {
		t.Errorf("vertical scan %v", v)
	}
	// Each is a permutation of the block, and the diagonal scan visits a
	// diagonal at a time, upwards and to the right.
	for _, size := range []int{1, 2, 4, 8, 16, 32} {
		for name, s := range map[string]Scan{"diag": DiagScan(size), "hor": HorScan(size), "ver": VerScan(size)} {
			seen := map[[2]uint8]bool{}
			for _, p := range s {
				if int(p[0]) >= size || int(p[1]) >= size || seen[p] {
					t.Fatalf("%s %d: position %v", name, size, p)
				}
				seen[p] = true
			}
			if len(s) != size*size {
				t.Fatalf("%s %d: %d positions", name, size, len(s))
			}
		}
		d := DiagScan(size)
		for i := 1; i < len(d); i++ {
			a, b := int(d[i-1][0])+int(d[i-1][1]), int(d[i][0])+int(d[i][1])
			if b < a || b > a+1 || (a == b && (d[i][0] != d[i-1][0]+1 || d[i][1]+1 != d[i-1][1])) {
				t.Fatalf("diagonal %d: %v then %v", size, d[i-1], d[i])
			}
		}
	}
}
