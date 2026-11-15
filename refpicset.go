// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
	"math"
)

// ErrPOCRange means a slice names a picture whose order count 8.3.1 forbids: one
// outside the 32-bit range, or more than 2^15 from the current picture's.
var ErrPOCRange = errors.New("h265: order count out of range")

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
//
// An order count that 8.3.1 forbids is refused with ErrPOCRange, never wrapped
// or clamped (a clamped 2^31-1 could be a real picture's count): PicOrderCntVal
// of every named picture is in -2^31 to 2^31-1, and DiffPicOrderCnt( current,
// picture ) in -2^15 to 2^15-1 (8-4).
func Derive(refs PictureRefs, poc int32, sps SPS) (RefPicSet, error) {
	var out RefPicSet
	// at is the absolute order count of a picture that is delta from the
	// current one, checked against both limits, in 64 bits.
	at := func(abs int64) (int32, error) {
		if d := int64(poc) - abs; abs < math.MinInt32 || abs > math.MaxInt32 || d < -(1<<15) || d > 1<<15-1 {
			return 0, fmt.Errorf("%w: a picture at %d from the current %d", ErrPOCRange, abs, poc)
		}
		return int32(abs), nil
	}
	for _, e := range refs.ShortTerm.Before {
		p, err := at(int64(poc) + int64(e.DeltaPOC))
		if err != nil {
			return RefPicSet{}, err
		}
		if e.Used {
			out.StCurrBefore = append(out.StCurrBefore, p)
		} else {
			out.StFoll = append(out.StFoll, p)
		}
	}
	for _, e := range refs.ShortTerm.After {
		p, err := at(int64(poc) + int64(e.DeltaPOC))
		if err != nil {
			return RefPicSet{}, err
		}
		if e.Used {
			out.StCurrAfter = append(out.StCurrAfter, p)
		} else {
			out.StFoll = append(out.StFoll, p)
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
	// ⛔ In 64 bits: DeltaPocMsbCycleLt accumulates (7-52) and is multiplied by
	// MaxPicOrderCntLsb, which overflows 32 bits long before the entries do.
	// Each cycle is bounded as read (7.4.7.1); an order count outside the
	// limits of 8.3.1 is a stream that cannot be conforming, and is refused.
	cycle := int64(0)
	firstStated := true
	for i, e := range refs.LongTerm {
		p := LtPicture{POC: int32(e.POCLSB)}
		if e.MSBPresent {
			delta := int64(e.DeltaMSBCycle)
			starts := i == 0 || (!e.FromSPS && firstStated)
			if !starts {
				delta += cycle
			}
			cycle = delta
			full := int64(e.POCLSB) + int64(poc) - delta*int64(maxLSB) - int64(lsb)
			var err error
			if p.POC, err = at(full); err != nil {
				return RefPicSet{}, err
			}
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
	return out, nil
}
