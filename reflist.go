// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

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
// l0Active and l1Active are how many entries each list has -- the picture
// parameter set states them and a slice may override them. A list SHORTER than
// the pictures available simply stops; a list LONGER repeats them, which is
// legal and is why this cycles rather than filling once.
//
// ⛔ An active count above MaxRefIdxActive is refused by ParsePPS, but Lists is
// also reachable with a count a caller worked out itself, so it bounds the
// count again here. The entries are small and the count is a 32-bit syntax
// element: a handful of bytes otherwise asks for tens of gigabytes.
//
// An I slice predicts from nothing and gets no lists. A P slice gets list 0.
func Lists(set RefPicSet, sliceType SliceType, l0Active, l1Active int) (l0, l1 []RefListEntry) {
	if sliceType == SliceI {
		return nil, nil
	}
	l0 = buildList(set, false, l0Active)
	if sliceType == SliceB {
		l1 = buildList(set, true, l1Active)
	}
	return l0, l1
}

// buildList concatenates the candidates in the order the list wants, repeating
// them until it is long enough, then cuts it to length.
func buildList(set RefPicSet, second bool, active int) []RefListEntry {
	if active <= 0 {
		return nil
	}
	if active > MaxRefIdxActive {
		active = MaxRefIdxActive
	}
	first, after := set.StCurrBefore, set.StCurrAfter
	if second {
		first, after = after, first
	}
	// Nothing to cycle: a list cannot be filled from no pictures, and looping
	// for one would not end.
	if len(first)+len(after)+len(set.LtCurr) == 0 {
		return nil
	}
	out := make([]RefListEntry, 0, active)
	for len(out) < active {
		for _, p := range first {
			out = append(out, RefListEntry{POC: p, Exact: true})
		}
		for _, p := range after {
			out = append(out, RefListEntry{POC: p, Exact: true})
		}
		for _, p := range set.LtCurr {
			out = append(out, RefListEntry{POC: p.POC, LongTerm: true, Exact: p.Exact})
		}
	}
	return out[:active]
}
