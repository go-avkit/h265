// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// ErrRefLists means a slice asked for a reference list that cannot be built.
var ErrRefLists = errors.New("h265: reference list cannot be built")

// RefListEntry is one entry of a reference picture list.
type RefListEntry struct {
	POC int32
	// LongTerm says the entry came from the long-term pictures, which a decoder
	// treats differently: a long-term reference is not scaled by distance the
	// way a short-term one is.
	LongTerm bool
	// Exact says POC is a full order count. A long-term entry whose slice did
	// not state its high bits carries only the low ones. See LtPicture.
	Exact bool
}

// Lists builds the reference picture lists a slice predicts from, clause 8.3.4.
//
// ⛔ The two lists hold the SAME pictures in a different order, and the order is
// the point: list 0 starts with what came before this picture, list 1 with what
// comes after. A decoder handed them the same way round predicts a B slice from
// the wrong side.
//
// refs carries how many entries each list has and, where the slice stated one,
// the order its entries are taken in. A list SHORTER than the pictures
// available simply stops; a list LONGER repeats them, which is legal and is why
// the candidates are cycled rather than laid down once.
//
// An I slice predicts from nothing and gets no lists. A P slice gets list 0.
func Lists(set RefPicSet, refs PictureRefs) (l0, l1 []RefListEntry, err error) {
	if refs.Type == SliceI {
		return nil, nil, nil
	}
	l0, err = buildList(set, false, int(refs.NumRefIdxL0Active), refs.ListEntryL0)
	if err != nil {
		return nil, nil, fmt.Errorf("list 0: %w", err)
	}
	if refs.Type == SliceB {
		if l1, err = buildList(set, true, int(refs.NumRefIdxL1Active), refs.ListEntryL1); err != nil {
			return nil, nil, fmt.Errorf("list 1: %w", err)
		}
	}
	return l0, l1, nil
}

// buildList concatenates the candidates in the order the list wants, repeating
// them until it is long enough, then takes the entries the slice asked for.
func buildList(set RefPicSet, second bool, active int, entries []uint32) ([]RefListEntry, error) {
	if active <= 0 {
		return nil, nil
	}
	// ⛔ An active count above MaxRefIdxActive is refused where a count is READ,
	// in ParsePPS and in the slice header. This is reachable with a count a
	// caller worked out itself: the entries are small and the count is a
	// 32-bit syntax element, so a handful of bytes otherwise asks for tens of
	// gigabytes.
	if active > MaxRefIdxActive {
		active = MaxRefIdxActive
	}
	first, after := set.StCurrBefore, set.StCurrAfter
	if second {
		first, after = after, first
	}
	// Nothing to cycle: a list cannot be filled from no pictures, and looping
	// for one would not end.
	total := len(first) + len(after) + len(set.LtCurr)
	if total == 0 {
		return nil, nil
	}

	// ⛔ The TEMPORARY list is as long as the GREATER of the active count and
	// the pictures available, and an entry the slice names indexes into THAT.
	// Cutting to the active count first would refuse a legal index, because a
	// slice may name a picture beyond the end of its own list.
	tempLen := active
	if total > tempLen {
		tempLen = total
	}
	temp := make([]RefListEntry, 0, tempLen)
	for len(temp) < tempLen {
		for _, p := range first {
			temp = append(temp, RefListEntry{POC: p, Exact: true})
		}
		for _, p := range after {
			temp = append(temp, RefListEntry{POC: p, Exact: true})
		}
		for _, p := range set.LtCurr {
			temp = append(temp, RefListEntry{POC: p.POC, LongTerm: true, Exact: p.Exact})
		}
	}
	temp = temp[:tempLen]

	if entries == nil {
		return temp[:active], nil
	}
	if len(entries) != active {
		return nil, fmt.Errorf("%w: %d entries stated for %d active references",
			ErrRefLists, len(entries), active)
	}
	out := make([]RefListEntry, active)
	for i, idx := range entries {
		// ⛔ Bounded by the PICTURES, 7.4.7.2, not by the temporary list it
		// indexes -- which is longer whenever the list has more entries than
		// there are pictures, and would let an index past the last picture
		// through. The entries past that point only repeat the candidates, so
		// nothing is lost by refusing them.
		if int(idx) >= total {
			return nil, fmt.Errorf("%w: entry %d names picture %d of %d",
				ErrRefLists, i, idx, total)
		}
		out[i] = temp[idx]
	}
	return out, nil
}
