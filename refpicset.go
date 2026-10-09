// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// LtPicture is one long-term picture a set names.
//
// ⛔ POC is a full order count only when Exact is set. Otherwise it is the LOW
// bits of one, and the picture meant is whichever held in the decoded picture
// buffer matches them -- which is enough while no two candidates share them, and
// is why the format lets a slice state the high bits when two would.
type LtPicture struct {
	POC   int32
	Exact bool
}

// RefPicSet is what one coded picture needs kept, derived from what its slice
// stated: five lists, and the difference between them is the whole point.
//
// ⛔ Curr and Foll are NOT the same thing. A picture in Curr may be predicted
// from by this picture. A picture in Foll may not -- it is listed because a
// LATER picture will want it, and a decoder that dropped it would break that
// one instead. Both must be kept; only one may be used.
type RefPicSet struct {
	// StCurrBefore are pictures already decoded that this one may use, nearest
	// first; StCurrAfter are those that follow it in output order and precede
	// it in the stream.
	StCurrBefore []int32
	StCurrAfter  []int32
	// StFoll are short-term pictures to keep and not use.
	StFoll []int32
	// LtCurr and LtFoll are the long-term pictures, likewise split.
	LtCurr []LtPicture
	LtFoll []LtPicture
}

// Count is how many pictures the set names in all, which is what a decoded
// picture buffer must hold for this picture.
func (s RefPicSet) Count() int {
	return len(s.StCurrBefore) + len(s.StCurrAfter) + len(s.StFoll) +
		len(s.LtCurr) + len(s.LtFoll)
}

// Derive turns what a slice stated into the pictures a decoder must keep, given
// the order count of the picture that stated it.
//
// The short-term entries are relative and become absolute here; the long-term
// ones are already counts, or the low bits of counts.
func Derive(refs PictureRefs, poc int32, sps SPS) RefPicSet {
	var out RefPicSet
	for _, e := range refs.ShortTerm.Before {
		switch {
		case !e.Used:
			out.StFoll = append(out.StFoll, poc+e.DeltaPOC)
		default:
			out.StCurrBefore = append(out.StCurrBefore, poc+e.DeltaPOC)
		}
	}
	for _, e := range refs.ShortTerm.After {
		switch {
		case !e.Used:
			out.StFoll = append(out.StFoll, poc+e.DeltaPOC)
		default:
			out.StCurrAfter = append(out.StCurrAfter, poc+e.DeltaPOC)
		}
	}

	// The high bits of a long-term picture are stated as a NUMBER OF WRAPS back
	// from the current picture, and that number accumulates across the entries:
	// each is counted from the one before rather than from zero. The first
	// entry of each group -- those taken from the sequence, and those stated by
	// the slice -- starts the count afresh.
	maxLSB := int32(1) << sps.Log2MaxPOCLSB
	lsb := poc % maxLSB
	if lsb < 0 {
		lsb += maxLSB
	}
	cycle := int32(0)
	firstStated := true
	for i, e := range refs.LongTerm {
		p := LtPicture{POC: int32(e.POCLSB)}
		if e.MSBPresent {
			delta := int32(e.DeltaMSBCycle)
			starts := i == 0 || (!e.FromSPS && firstStated)
			if !starts {
				delta += cycle
			}
			cycle = delta
			p.POC = int32(e.POCLSB) + poc - delta*maxLSB - lsb
			p.Exact = true
		}
		if !e.FromSPS {
			firstStated = false
		}
		if e.Used {
			out.LtCurr = append(out.LtCurr, p)
		} else {
			out.LtFoll = append(out.LtFoll, p)
		}
	}
	return out
}
