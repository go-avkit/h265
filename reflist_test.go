// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"reflect"
	"strings"
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

// asks is what a slice states about its lists.
func asks(k SliceType, l0, l1 int) PictureRefs {
	return PictureRefs{Type: k, NumRefIdxL0Active: uint32(l0), NumRefIdxL1Active: uint32(l1)}
}

func pocs(l []RefListEntry) []int32 {
	out := make([]int32, len(l))
	for i, e := range l {
		out[i] = e.POC
	}
	return out
}

func TestListsOrder(t *testing.T) {
	// Five candidates, five slots: the whole cycle fits exactly once, so the
	// result IS the concatenation order and nothing else.
	l0, l1, err := Lists(sample(), asks(SliceB, 5, 5))
	if err != nil {
		t.Fatal(err)
	}
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
	l0, _, err := Lists(sample(), asks(SliceP, 3, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := []int32{8, 4, 20}
	if got := pocs(l0); !reflect.DeepEqual(got, want) {
		t.Errorf("list 0 = %v, want %v", got, want)
	}
}

func TestListsRepeat(t *testing.T) {
	// More slots than candidates: the cycle runs again. A list that filled only
	// once and stopped would be 5 long, not 7.
	l0, _, err := Lists(sample(), asks(SliceP, 7, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := []int32{8, 4, 20, 24, 0, 8, 4}
	if got := pocs(l0); !reflect.DeepEqual(got, want) {
		t.Errorf("list 0 = %v, want %v", got, want)
	}
}

func TestListsSliceType(t *testing.T) {
	if l0, l1, err := Lists(sample(), asks(SliceI, 4, 4)); l0 != nil || l1 != nil || err != nil {
		t.Errorf("I slice got %v / %v, err %v, want none", pocs(l0), pocs(l1), err)
	}
	l0, l1, err := Lists(sample(), asks(SliceP, 4, 4))
	if err != nil {
		t.Fatal(err)
	}
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
	l0, _, err := Lists(set, asks(SliceP, 3, 0))
	if err != nil {
		t.Fatal(err)
	}
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
		l0, l1, err := Lists(set, asks(SliceB, 4, 4))
		if l0 != nil || l1 != nil || err != nil {
			t.Errorf("set %+v produced %v / %v, err %v, want none", set, pocs(l0), pocs(l1), err)
		}
	}
}

func TestListsZeroActive(t *testing.T) {
	l0, l1, err := Lists(sample(), asks(SliceB, 0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if l0 != nil {
		t.Errorf("list 0 with no active entries = %v, want none", pocs(l0))
	}
	if len(l1) != 2 {
		t.Errorf("list 1 has %d entries, want 2", len(l1))
	}
}

func TestListsBoundsTheCount(t *testing.T) {
	// ⛔ A count a caller worked out itself is not bounded by ParsePPS or by
	// the slice header. The entries are 8 bytes each and the syntax element is
	// 32 bits, so an unbounded list is a few bytes of stream asking for tens of
	// gigabytes.
	l0, l1, err := Lists(sample(), asks(SliceB, 1<<30, 1<<30))
	if err != nil {
		t.Fatal(err)
	}
	if len(l0) != MaxRefIdxActive || len(l1) != MaxRefIdxActive {
		t.Errorf("lists have %d and %d entries, want %d each",
			len(l0), len(l1), MaxRefIdxActive)
	}
	// The bound must not cut a list that is within it: 15 is allowed.
	if l0, _, err := Lists(sample(), asks(SliceP, MaxRefIdxActive, 0)); err != nil || len(l0) != MaxRefIdxActive {
		t.Errorf("a list of exactly %d was cut to %d, err %v", MaxRefIdxActive, len(l0), err)
	}
}

// TestListsModificationReorders: the slice names, for each position of its
// list, the position of the temporary list it is taken from.
func TestListsModificationReorders(t *testing.T) {
	refs := asks(SliceB, 3, 2)
	refs.ListEntryL0 = []uint32{4, 0, 2} // long-term first, then 8, then 20
	refs.ListEntryL1 = []uint32{1, 1}    // the SAME picture twice, which is legal
	l0, l1, err := Lists(sample(), refs)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pocs(l0), []int32{0, 8, 20}; !reflect.DeepEqual(got, want) {
		t.Errorf("list 0 = %v, want %v", got, want)
	}
	if !l0[0].LongTerm {
		t.Error("the entry taken from LtCurr lost its long-term mark")
	}
	// ⛔ List 1's temporary list is the OTHER order, so index 1 is 24, not 4.
	// Reordering a list that was built the wrong way round gives a wrong
	// picture with no error at all.
	if got, want := pocs(l1), []int32{24, 24}; !reflect.DeepEqual(got, want) {
		t.Errorf("list 1 = %v, want %v", got, want)
	}
}

// TestListsModificationReachesPastTheActiveCount.
//
// ⛔ The temporary list is as long as the GREATER of the active count and the
// pictures available, and an entry indexes into THAT. A reader that cut to the
// active count first would refuse this legal stream.
func TestListsModificationReachesPastTheActiveCount(t *testing.T) {
	refs := asks(SliceP, 2, 0) // two entries, five pictures to choose from
	refs.ListEntryL0 = []uint32{4, 3}
	l0, _, err := Lists(sample(), refs)
	if err != nil {
		t.Fatalf("an entry past the active count was refused: %v", err)
	}
	if got, want := pocs(l0), []int32{0, 24}; !reflect.DeepEqual(got, want) {
		t.Errorf("list 0 = %v, want %v", got, want)
	}
}

// TestListsModificationRefusals: an entry is as wide as Ceil(Log2(N)) bits,
// which holds values past the last picture -- three pictures are named in two
// bits -- so an index out of range is reachable from a conformant-width field.
func TestListsModificationRefusals(t *testing.T) {
	refs := asks(SliceP, 2, 0)
	refs.ListEntryL0 = []uint32{5, 0} // five pictures: the last index is 4
	if _, _, err := Lists(sample(), refs); !errors.Is(err, ErrRefLists) {
		t.Errorf("err = %v, want ErrRefLists", err)
	}
	refs.ListEntryL0 = []uint32{0} // one entry stated for two active
	if _, _, err := Lists(sample(), refs); !errors.Is(err, ErrRefLists) {
		t.Errorf("a short modification gave %v, want ErrRefLists", err)
	}
}

// TestListsNamesWhichListWasRefused: both lists are built from the same set and
// a bare index out of range would not say which one it belonged to.
func TestListsNamesWhichListWasRefused(t *testing.T) {
	refs := asks(SliceB, 2, 2)
	refs.ListEntryL1 = []uint32{9, 0}
	_, _, err := Lists(sample(), refs)
	if !errors.Is(err, ErrRefLists) {
		t.Fatalf("err = %v, want ErrRefLists", err)
	}
	if got := err.Error(); !strings.Contains(got, "list 1") {
		t.Errorf("err = %q, want it to name list 1", got)
	}
}
