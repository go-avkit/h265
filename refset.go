// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "fmt"

// RefPic is one entry of a short-term reference picture set: where the picture
// sits relative to the current one, and whether the current one may use it.
//
// A set lists pictures the DECODER must keep, which is not the same as the
// pictures this one predicts from. An entry that is not Used is kept because a
// LATER picture will want it, and dropping it would break that one instead.
type RefPic struct {
	// DeltaPOC is the entry's order count minus the current picture's, so it is
	// negative for a picture already seen and positive for one still to come.
	DeltaPOC int32
	Used     bool
}

// ShortTermRPS is a short-term reference picture set: the pictures a coded
// picture may still need, stated relative to its own order count.
//
// The two halves are kept apart because the format states them apart and
// because they mean different things: Before holds pictures already decoded,
// After holds pictures that follow in order but precede this one in the
// stream. Both are ordered by distance, nearest first.
type ShortTermRPS struct {
	Before []RefPic // negative deltas, nearest first: -1, -2, ...
	After  []RefPic // positive deltas, nearest first: 1, 2, ...
}

// Count is how many pictures the set names, which is the NumDeltaPocs the next
// set may predict from.
func (s ShortTermRPS) Count() int { return len(s.Before) + len(s.After) }

// maxLongTermPics bounds how many long-term pictures a sequence or a slice may
// name, from libde265's MAX_NUM_LT_REF_PICS_SPS. The counts are read from the
// stream before anything is allocated for them.
//
// ⛔ It used to be maxRefPics, "bounds a set", used BOTH for the short-term
// halves and -- doubled -- for the long-term count. The short-term bound is now
// maxDeltaPocs and sits on the sum, so this name says the one thing it does.
const maxLongTermPics = 32

// maxDeltaPocs bounds a set's TWO halves TOGETHER.
//
// ⛔ 7.4.8 bounds num_negative_pics by sps_max_dec_pic_buffering_minus1 and
// num_positive_pics by what is LEFT of it, so the ceiling is on the sum, not
// on each half. Bounding them one at a time allowed twice what any stream may
// state. sps_max_dec_pic_buffering_minus1 is at most MaxDpbSize - 1, and A.4.2
// puts MaxDpbSize at sixteen whatever the level, so fifteen is the ceiling for
// every stream -- which is what this package can say without keeping the
// sequence's own value, skipped with the rest of the sub-layer ordering.
//
// gstreamer bounds NumDeltaPocs at MAX_DPB_SIZE - 1 for the same reason.
const maxDeltaPocs = 15

// maxShortTermRPS is how many sets a sequence parameter set may carry, from
// 7.4.3.2.1.
const maxShortTermRPS = 64

// parseShortTermRPS reads one st_ref_pic_set.
//
// prior is every set already read from this sequence parameter set, which the
// predicted form needs: a set may be stated as a DIFFERENCE from an earlier
// one rather than listed outright, and the difference cannot be applied -- nor
// even its bits counted -- without the set it is a difference from.
//
// inSliceHeader changes one thing: a slice's own set says how far back to look
// rather than always meaning the set before, because it comes after every set
// the sequence carries rather than among them.
//
// ⛔ Whether a set is predicted is stated only when idx is not zero, in a slice
// header as anywhere else (7.3.7: "if( stRpsIdx != 0 )"). A slice's own set has
// idx num_short_term_ref_pic_sets, so a sequence carrying NO set -- what x265
// writes by default -- has slices whose own set states no such flag. Reading
// one there anyway took the first bit of num_negative_pics, and every field
// after it was misread: a P slice at POC 3 predicting from POC 0 came out as
// predicting from POC 4.
func parseShortTermRPS(r *sticky, idx int, prior []ShortTermRPS, inSliceHeader bool) (ShortTermRPS, error) {
	var out ShortTermRPS
	predict := false
	if idx != 0 {
		predict = r.flag()
	}
	if !predict {
		return parseShortTermRPSExplicit(r)
	}

	// Which earlier set this one is a difference from. In a sequence parameter
	// set it is always the one before; a slice header counts back from the end
	// of what the sequence carries.
	ref := idx - 1
	if inSliceHeader {
		ref = len(prior) - (int(r.ue()) + 1)
	}
	if ref < 0 || ref >= len(prior) {
		return out, fmt.Errorf("%w: a set predicted from %d, which is not there", ErrSliceHeader, ref)
	}
	from := prior[ref]

	sign := int32(1)
	if r.flag() { // delta_rps_sign
		sign = -1
	}
	abs := int32(r.ue()) + 1
	if abs > 32768 {
		return out, fmt.Errorf("%w: a delta of %d between sets", ErrUnsupportedSPS, abs)
	}
	deltaRPS := sign * abs

	// One pair of flags per entry of the set predicted from, plus one for the
	// predicted-from picture itself.
	n := from.Count()
	used := make([]bool, n+1)
	useDelta := make([]bool, n+1)
	for i := 0; i <= n; i++ {
		used[i] = r.flag()
		useDelta[i] = true
		if !used[i] {
			useDelta[i] = r.flag()
		}
	}
	if r.err != nil {
		return out, r.err
	}

	// 7.4.8: the entries are rebuilt in order, which is why this walks the
	// positives backwards and the negatives forwards. Taking them in stream
	// order instead would leave both halves unsorted, and every later set that
	// predicts from this one would inherit the disorder.
	for j := len(from.After) - 1; j >= 0; j-- {
		if d := from.After[j].DeltaPOC + deltaRPS; d < 0 && useDelta[len(from.Before)+j] {
			out.Before = append(out.Before, RefPic{d, used[len(from.Before)+j]})
		}
	}
	if deltaRPS < 0 && useDelta[n] {
		out.Before = append(out.Before, RefPic{deltaRPS, used[n]})
	}
	for j := range from.Before {
		if d := from.Before[j].DeltaPOC + deltaRPS; d < 0 && useDelta[j] {
			out.Before = append(out.Before, RefPic{d, used[j]})
		}
	}
	for j := len(from.Before) - 1; j >= 0; j-- {
		if d := from.Before[j].DeltaPOC + deltaRPS; d > 0 && useDelta[j] {
			out.After = append(out.After, RefPic{d, used[j]})
		}
	}
	if deltaRPS > 0 && useDelta[n] {
		out.After = append(out.After, RefPic{deltaRPS, used[n]})
	}
	for j := range from.After {
		if d := from.After[j].DeltaPOC + deltaRPS; d > 0 && useDelta[len(from.Before)+j] {
			out.After = append(out.After, RefPic{d, used[len(from.Before)+j]})
		}
	}
	if out.Count() > maxDeltaPocs {
		return ShortTermRPS{}, fmt.Errorf("%w: a predicted set of %d pictures, at most %d",
			ErrUnsupportedSPS, out.Count(), maxDeltaPocs)
	}
	return out, nil
}

// parseShortTermRPSExplicit reads a set stated outright.
func parseShortTermRPSExplicit(r *sticky) (ShortTermRPS, error) {
	var out ShortTermRPS
	nNeg, nPos := r.ue(), r.ue()
	if r.err != nil {
		return out, r.err
	}
	// ⛔ On the SUM. Each half within the ceiling says nothing about the two
	// together, and the format bounds the second by what the first left.
	if uint64(nNeg)+uint64(nPos) > maxDeltaPocs {
		return out, fmt.Errorf("%w: a set of %d before and %d after, at most %d together",
			ErrUnsupportedSPS, nNeg, nPos, maxDeltaPocs)
	}
	// The deltas are stated as gaps from the previous entry, so each is read
	// against a running total rather than on its own.
	prev := int32(0)
	for i := uint32(0); i < nNeg; i++ {
		d := int32(r.ue()) + 1
		if d < 1 || d > 32768 {
			return ShortTermRPS{}, fmt.Errorf("%w: a gap of %d between references", ErrUnsupportedSPS, d)
		}
		prev -= d
		out.Before = append(out.Before, RefPic{prev, r.flag()})
	}
	prev = 0
	for i := uint32(0); i < nPos; i++ {
		d := int32(r.ue()) + 1
		if d < 1 || d > 32768 {
			return ShortTermRPS{}, fmt.Errorf("%w: a gap of %d between references", ErrUnsupportedSPS, d)
		}
		prev += d
		out.After = append(out.After, RefPic{prev, r.flag()})
	}
	if r.err != nil {
		return ShortTermRPS{}, r.err
	}
	return out, nil
}
