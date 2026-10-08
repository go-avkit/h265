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

// maxRefPics bounds a set. A conforming stream states at most sixteen, and a
// bound is wanted here because the counts are read from the stream before
// anything is allocated for them.
const maxRefPics = 16

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
// A slice header may also state a set of its own, and there the predicted form
// says how far back to look rather than always meaning the set before. That is
// not here: nothing reads a slice header's own set yet, and a parameter carried
// for a caller that does not exist is a branch no test can reach.
func parseShortTermRPS(r *sticky, idx int, prior []ShortTermRPS) (ShortTermRPS, error) {
	var out ShortTermRPS
	predict := false
	if idx != 0 {
		predict = r.flag()
	}
	if !predict {
		return parseShortTermRPSExplicit(r)
	}

	// Which earlier set this one is a difference from: in a sequence parameter
	// set, always the one before. No bound check: the only caller appends
	// exactly one set per turn and passes the slice it has built, so prior
	// holds idx sets and idx is at least one here -- predict is false at zero.
	from := prior[idx-1]

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
	if len(out.Before) > maxRefPics || len(out.After) > maxRefPics {
		return ShortTermRPS{}, fmt.Errorf("%w: a predicted set of %d pictures", ErrUnsupportedSPS, out.Count())
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
	if nNeg > maxRefPics || nPos > maxRefPics {
		return out, fmt.Errorf("%w: a set of %d before and %d after", ErrUnsupportedSPS, nNeg, nPos)
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
