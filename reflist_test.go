// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"reflect"
	"testing"
)

// sample is a set with all three candidate kinds populated and every POC
// distinct, so that an order can be read off the result unambiguously. A set
// where two candidates shared a value would let a wrong order pass.
func sample() RefPicSet {
	return RefPicSet{
		StCurrBefore: []int32{8, 4},
		StCurrAfter:  []int32{20, 24},
		LtCurr:       []LtPicture{{POC: 0, Exact: true}},
		StFoll:       []int32{99},            // must never appear
		LtFoll:       []LtPicture{{POC: 98}}, // must never appear
	}
}

func pocs(l []RefListEntry) []int32 {
	out := make([]int32, len(l))
	for i, e := range l {
		out[i] = e.POC
	}
	return out
}

func TestListsOrder(t *testing.T) {
	set := sample()
	// Five candidates, five slots: the whole cycle fits exactly once, so the
	// result IS the concatenation order and nothing else.
	l0, l1 := Lists(set, SliceB, 5, 5)

	wantL0 := []int32{8, 4, 20, 24, 0}
	if got := pocs(l0); !reflect.DeepEqual(got, wantL0) {
		t.Errorf("list 0 = %v, want %v", got, wantL0)
	}
	// ⛔ List 1 starts on the OTHER side. The two lists holding the same
	// pictures in the same order would make a B slice predict backwards.
	wantL1 := []int32{20, 24, 8, 4, 0}
	if got := pocs(l1); !reflect.DeepEqual(got, wantL1) {
		t.Errorf("list 1 = %v, want %v", got, wantL1)
	}
	if reflect.DeepEqual(pocs(l0), pocs(l1)) {
		t.Fatal("the two lists came out identical; the test data cannot tell the orders apart")
	}
}

func TestListsTruncate(t *testing.T) {
	// Fewer slots than candidates: the list stops, it does not reorder.
	l0, _ := Lists(sample(), SliceP, 3, 0)
	want := []int32{8, 4, 20}
	if got := pocs(l0); !reflect.DeepEqual(got, want) {
		t.Errorf("list 0 = %v, want %v", got, want)
	}
}

func TestListsRepeat(t *testing.T) {
	// More slots than candidates: the cycle runs again. A list that filled only
	// once and stopped would be 5 long, not 7.
	l0, _ := Lists(sample(), SliceP, 7, 0)
	want := []int32{8, 4, 20, 24, 0, 8, 4}
	if got := pocs(l0); !reflect.DeepEqual(got, want) {
		t.Errorf("list 0 = %v, want %v", got, want)
	}
	if len(l0) != 7 {
		t.Errorf("list 0 has %d entries, want 7", len(l0))
	}
}

func TestListsSliceType(t *testing.T) {
	set := sample()
	if l0, l1 := Lists(set, SliceI, 4, 4); l0 != nil || l1 != nil {
		t.Errorf("I slice got lists %v / %v, want none", pocs(l0), pocs(l1))
	}
	l0, l1 := Lists(set, SliceP, 4, 4)
	if len(l0) != 4 {
		t.Errorf("P slice list 0 has %d entries, want 4", len(l0))
	}
	// A P slice predicts from one direction. Asking for four entries in list 1
	// must still yield none.
	if l1 != nil {
		t.Errorf("P slice got list 1 %v, want none", pocs(l1))
	}
}

func TestListsLongTermFlag(t *testing.T) {
	set := RefPicSet{
		StCurrBefore: []int32{4},
		LtCurr:       []LtPicture{{POC: 0, Exact: true}, {POC: 12, Exact: false}},
	}
	l0, _ := Lists(set, SliceP, 3, 0)
	if len(l0) != 3 {
		t.Fatalf("list 0 has %d entries, want 3", len(l0))
	}
	if l0[0].LongTerm {
		t.Error("the short-term entry is marked long-term")
	}
	if !l0[0].Exact {
		t.Error("a short-term POC is always a full count")
	}
	for i := 1; i < 3; i++ {
		if !l0[i].LongTerm {
			t.Errorf("entry %d came from LtCurr and is not marked long-term", i)
		}
	}
	// ⛔ Exact is carried from the picture, not set for the whole kind: the
	// second long-term entry's slice never stated its high bits.
	if !l0[1].Exact {
		t.Error("entry 1 lost the Exact its LtPicture carried")
	}
	if l0[2].Exact {
		t.Error("entry 2 gained an Exact its LtPicture did not carry")
	}
}

func TestListsNoCandidates(t *testing.T) {
	// A set with nothing current cannot fill a list. The cycle would not end,
	// so this is a termination witness, not a style check: it hangs if the
	// guard goes.
	for _, set := range []RefPicSet{
		{},
		{StFoll: []int32{4, 8}, LtFoll: []LtPicture{{POC: 0}}},
	} {
		l0, l1 := Lists(set, SliceB, 4, 4)
		if l0 != nil || l1 != nil {
			t.Errorf("set %+v produced %v / %v, want none", set, pocs(l0), pocs(l1))
		}
	}
}

func TestListsZeroActive(t *testing.T) {
	l0, l1 := Lists(sample(), SliceB, 0, 2)
	if l0 != nil {
		t.Errorf("list 0 with no active entries = %v, want none", pocs(l0))
	}
	if len(l1) != 2 {
		t.Errorf("list 1 has %d entries, want 2", len(l1))
	}
}

func TestListsBoundsTheCount(t *testing.T) {
	// ⛔ A count a caller worked out itself is not bounded by ParsePPS. The
	// entries are 8 bytes each and the syntax element is 32 bits, so an
	// unbounded list is a few bytes of stream asking for tens of gigabytes.
	l0, l1 := Lists(sample(), SliceB, 1<<30, 1<<30)
	if len(l0) != MaxRefIdxActive || len(l1) != MaxRefIdxActive {
		t.Errorf("lists have %d and %d entries, want %d each",
			len(l0), len(l1), MaxRefIdxActive)
	}
	// The bound must not cut a list that is within it: 15 is allowed.
	if l0, _ := Lists(sample(), SliceP, MaxRefIdxActive, 0); len(l0) != MaxRefIdxActive {
		t.Errorf("a list of exactly %d was cut to %d", MaxRefIdxActive, len(l0))
	}
}
