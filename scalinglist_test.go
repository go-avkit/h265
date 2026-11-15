// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"
)

// writeLists writes a random scaling_list_data( ) and returns what 7.4.5 says it
// means, built here by keeping the lists as they are stated, one by one.
func writeLists(w *writer, rng *rand.Rand) *ScalingLists {
	want := new(ScalingLists)
	for sizeID := 0; sizeID < 4; sizeID++ {
		for matrixID := 0; matrixID < 6; matrixID += map[bool]int{false: 1, true: 3}[sizeID == 3] {
			coefs := 64
			if sizeID == 0 {
				coefs = 16
			}
			maxDelta := matrixID
			if sizeID == 3 {
				maxDelta = matrixID / 3
			}
			if rng.IntN(3) == 0 { // predicted
				w.flag(false)
				delta := rng.IntN(maxDelta + 1)
				w.ue(uint32(delta))
				if delta == 0 {
					def := DefaultScalingLists()
					want.List[sizeID][matrixID] = def.List[sizeID][matrixID]
					if sizeID > 1 {
						want.DC[sizeID-2][matrixID] = 16
					}
					continue
				}
				ref := matrixID - delta
				if sizeID == 3 {
					ref = matrixID - 3*delta
				}
				want.List[sizeID][matrixID] = want.List[sizeID][ref]
				if sizeID > 1 {
					want.DC[sizeID-2][matrixID] = want.DC[sizeID-2][ref]
				}
				continue
			}
			w.flag(true)
			next := 8
			if sizeID > 1 {
				dc := 1 + rng.IntN(255)
				w.se(int32(dc - 8))
				want.DC[sizeID-2][matrixID] = uint8(dc)
				next = dc
			}
			for i := 0; i < coefs; i++ {
				v := 1 + rng.IntN(255)
				delta := v - next
				if delta > 127 {
					delta -= 256
				}
				if delta < -128 {
					delta += 256
				}
				w.se(int32(delta))
				want.List[sizeID][matrixID][i] = uint8(v)
				next = v
			}
		}
	}
	return want
}

func TestScalingListDataRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(111, 112))
	for iter := 0; iter < 1000; iter++ {
		w := &writer{}
		want := writeLists(w, rng)
		w.bits(0x15, 5) // what follows is left alone
		r := newSticky(w.data)
		got, err := readScalingListData(r)
		if err != nil || r.err != nil {
			t.Fatalf("iter %d: %v %v", iter, err, r.err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("iter %d: lists differ", iter)
		}
		if r.bits(5) != 0x15 {
			t.Fatalf("iter %d: the structure ended at the wrong bit", iter)
		}
	}
}

// Table 7-5 and Table 7-6, copied again from the text of the Recommendation, one
// row of the table to a line.
func TestDefaultScalingListsAgainstTheTables(t *testing.T) {
	intra := [][]uint8{
		{16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 16, 17, 16, 17, 18},
		{17, 18, 18, 17, 18, 21, 19, 20, 21, 20, 19, 21, 24, 22, 22, 24},
		{24, 22, 22, 24, 25, 25, 27, 30, 27, 25, 25, 29, 31, 35, 35, 31},
		{29, 36, 41, 44, 41, 36, 47, 54, 54, 47, 65, 70, 65, 88, 88, 115},
	}
	inter := [][]uint8{
		{16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 17, 18},
		{18, 18, 18, 18, 18, 20, 20, 20, 20, 20, 20, 20, 24, 24, 24, 24},
		{24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 28, 28, 28, 28, 28},
		{28, 33, 33, 33, 33, 33, 41, 41, 41, 41, 54, 54, 54, 71, 71, 91},
	}
	d := DefaultScalingLists()
	for m := 0; m < 6; m++ {
		for i := 0; i < 16; i++ {
			if d.List[0][m][i] != 16 {
				t.Errorf("4x4 default %d/%d", m, i)
			}
		}
		rows := intra
		if m >= 3 {
			rows = inter
		}
		for sizeID := 1; sizeID < 4; sizeID++ {
			for i := 0; i < 64; i++ {
				if d.List[sizeID][m][i] != rows[i/16][i%16] {
					t.Errorf("default %d/%d/%d is %d, the table's %d", sizeID, m, i, d.List[sizeID][m][i], rows[i/16][i%16])
				}
			}
		}
	}
	for i := range d.DC {
		for m := range d.DC[i] {
			if d.DC[i][m] != 16 {
				t.Errorf("default DC %d/%d", i, m)
			}
		}
	}
}

// refDiag is ScanOrder[ log2BlockSize ][ 0 ] (6.5.3) by walking the anti-diagonals
// of the block, each from its bottom left (x = 0 upward) to its top right.
func refDiag(size int) [][2]int {
	var out [][2]int
	for d := 0; d < 2*size-1; d++ {
		for x := 0; x <= d; x++ {
			y := d - x
			if x < size && y < size {
				out = append(out, [2]int{x, y})
			}
		}
	}
	return out
}

// TestScalingFactorsAgainstTheEquations derives ScalingFactor by (7-44) to (7-51) as
// the equations are written, with x and y as indices into ScalingFactor[ x ][ y ].
func TestScalingFactorsAgainstTheEquations(t *testing.T) {
	rng := rand.New(rand.NewPCG(113, 114))
	for iter := 0; iter < 200; iter++ {
		w := &writer{}
		lists := writeLists(w, rng)
		got := lists.Factors()
		at := func(sizeID, m, x, y int) uint8 { return got[sizeID][m][y*(4<<uint(sizeID))+x] }
		for m := 0; m < 6; m++ {
			for i, p := range refDiag(4) {
				if at(0, m, p[0], p[1]) != lists.List[0][m][i] {
					t.Fatalf("7-44 %d/%d", m, i)
				}
			}
			for i, p := range refDiag(8) {
				x, y := p[0], p[1]
				if at(1, m, x, y) != lists.List[1][m][i] {
					t.Fatalf("7-45 %d/%d", m, i)
				}
				for j := 0; j < 2; j++ {
					for k := 0; k < 2; k++ {
						if (x*2+k != 0 || y*2+j != 0) && at(2, m, x*2+k, y*2+j) != lists.List[2][m][i] {
							t.Fatalf("7-46 %d/%d", m, i)
						}
					}
				}
				for j := 0; j < 4; j++ {
					for k := 0; k < 4; k++ {
						if x*4+k == 0 && y*4+j == 0 {
							continue
						}
						// (7-48) for matrixId 0 and 3, (7-50) from the 16x16 lists otherwise.
						src := lists.List[3][m][i]
						if m%3 != 0 {
							src = lists.List[2][m][i]
						}
						if at(3, m, x*4+k, y*4+j) != src {
							t.Fatalf("7-48/7-50 %d/%d", m, i)
						}
					}
				}
			}
			if at(2, m, 0, 0) != lists.DC[0][m] { // 7-47
				t.Fatalf("7-47 %d", m)
			}
			dc := lists.DC[1][m] // 7-49
			if m%3 != 0 {
				dc = lists.DC[0][m] // 7-51
			}
			if at(3, m, 0, 0) != dc {
				t.Fatalf("7-49/7-51 %d", m)
			}
		}
	}
}

func TestScalingListRefusals(t *testing.T) {
	// Each case writes the start of a structure that states something forbidden.
	cases := map[string]func(w *writer){
		"a copy of a later matrix": func(w *writer) { w.flag(false); w.ue(1) }, // sizeId 0, matrixId 0: delta 1 > 0
		"a zero coefficient": func(w *writer) {
			w.flag(true)
			w.se(-8) // 8 - 8 = 0
		},
		"a delta out of range": func(w *writer) { w.flag(true); w.se(200) },
		"a DC out of range": func(w *writer) {
			// Fill sizeId 0 and 1 with defaults (6 + 6 lists), then a DC of -8.
			for i := 0; i < 12; i++ {
				w.flag(false)
				w.ue(0)
			}
			w.flag(true)
			w.se(-8)
		},
	}
	for name, fn := range cases {
		w := &writer{}
		fn(w)
		w.bits(0, 32)
		r := newSticky(w.data)
		if _, err := readScalingListData(r); !errors.Is(err, ErrScalingList) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Truncated data is the reader's error, not a panic.
	r := newSticky([]byte{0x00})
	if l, err := readScalingListData(r); l != nil || err != nil || r.err == nil {
		t.Errorf("truncated: %v %v %v", l, err, r.err)
	}
}

func TestScalingFactorsOf(t *testing.T) {
	var sps SPS
	var pps PPS
	if ScalingFactorsOf(sps, pps) != nil {
		t.Error("scaling lists disabled must give the flat 16")
	}
	sps.ScalingListEnabled = true
	def := DefaultScalingLists().Factors()
	if !reflect.DeepEqual(ScalingFactorsOf(sps, pps), def) {
		t.Error("enabled with no data must give the defaults")
	}
	own := DefaultScalingLists()
	own.List[1][0][5] = 99
	sps.ScalingList = own
	if ScalingFactorsOf(sps, pps)[1][0][int(DiagScan(8)[5][1])*8+int(DiagScan(8)[5][0])] != 99 {
		t.Error("the SPS's lists are not used")
	}
	other := DefaultScalingLists()
	other.List[1][0][5] = 77
	pps.ScalingList = other
	if ScalingFactorsOf(sps, pps)[1][0][int(DiagScan(8)[5][1])*8+int(DiagScan(8)[5][0])] != 77 {
		t.Error("the PPS's lists do not override the SPS's")
	}
}

// A parameter set whose scaling lists are forbidden is refused whole.
func TestParameterSetsRefuseBadScalingLists(t *testing.T) {
	if _, err := ParseSPS(set{subLayers: 1, chroma: 1, width: 64, height: 64, badScalingList: true}.build()); !errors.Is(err, ErrScalingList) {
		t.Errorf("SPS: %v", err)
	}
	if _, err := ParsePPS(pset{badScalingList: true}.build()); !errors.Is(err, ErrScalingList) {
		t.Errorf("PPS: %v", err)
	}
}
