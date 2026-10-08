// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"
	"math/bits"
)

// LongTermRef is one long-term picture a slice names.
//
// A long-term picture is named by the LOW bits of its order count, which is
// enough while no two candidates share them. When two would, the slice states
// the high bits as well, which is what MSBPresent says.
type LongTermRef struct {
	POCLSB uint32
	Used   bool
	// MSBPresent says DeltaMSBCycle was stated. Without it the picture is the
	// one whose low bits match; with it, the one that many wraps further back.
	MSBPresent    bool
	DeltaMSBCycle uint32
	// FromSPS says the entry was named out of the sequence's own list rather
	// than stated by the slice.
	FromSPS bool
}

// PictureRefs is what one coded picture states about the pictures it may still
// need: a short-term set, and the long-term pictures it names.
type PictureRefs struct {
	ShortTerm ShortTermRPS
	LongTerm  []LongTermRef
	// NamedSet says the short-term set was NAMED out of the sequence's sets
	// rather than stated by the slice, and SetIndex says which.
	NamedSet bool
	SetIndex int
}

// ReferencesOf reads what a coded picture says it still needs.
//
// An IDR says nothing: it begins a coded video sequence, so nothing before it
// is available and nothing after it may reach back. That is not an error and
// comes back as an empty set.
//
// Pass the FIRST slice segment of the picture. A later segment states no set of
// its own -- the set belongs to the picture -- and its address, which this
// package cannot yet work out, sits in the way.
func ReferencesOf(u Unit, sps SPS, pps PPS) (PictureRefs, error) {
	var out PictureRefs
	if !u.Type.IsSliceSegment() {
		return out, fmt.Errorf("%w: type %d", ErrNotSlice, u.Type)
	}
	if u.Type.IsIDR() {
		return out, nil
	}
	r, _, err := walkToPOC(u, sps, pps)
	if err != nil {
		return out, err
	}

	if r.flag() { // short_term_ref_pic_set_sps_flag
		if len(sps.ShortTermRefPicSets) == 0 {
			return out, fmt.Errorf("%w: a slice names a set and the sequence carries none", ErrSliceHeader)
		}
		idx := 0
		// The index is only sent when there is a choice, and it is as wide as
		// the number of sets needs: one set means no bits at all.
		if n := ceilLog2(len(sps.ShortTermRefPicSets)); n > 0 {
			idx = int(r.bits(n))
		}
		if r.err != nil {
			return out, r.err
		}
		if idx >= len(sps.ShortTermRefPicSets) {
			return out, fmt.Errorf("%w: a slice names set %d of %d",
				ErrSliceHeader, idx, len(sps.ShortTermRefPicSets))
		}
		out.ShortTerm, out.NamedSet, out.SetIndex = sps.ShortTermRefPicSets[idx], true, idx
	} else {
		// Stated outright, and it may still be a difference from one of the
		// sequence's sets: a slice's own set sits after all of them.
		set, err := parseShortTermRPS(r, len(sps.ShortTermRefPicSets), sps.ShortTermRefPicSets, true)
		if err != nil {
			return out, err
		}
		out.ShortTerm = set
	}

	// No final error check: every path above either returns r.err itself or
	// reads nothing at all, so a reader that ran out has already been reported.
	if err := readSliceLongTerm(r, sps, &out); err != nil {
		return out, err
	}
	return out, nil
}

// readSliceLongTerm reads the long-term pictures a slice names: some out of the
// sequence's own list, some stated here.
func readSliceLongTerm(r *sticky, sps SPS, out *PictureRefs) error {
	if len(sps.LongTermRefPics) == 0 && !sps.LongTermRefPicsPresent {
		return nil
	}
	fromSPS := uint32(0)
	if len(sps.LongTermRefPics) > 0 {
		fromSPS = r.ue()
	}
	stated := r.ue()
	if r.err != nil {
		return r.err
	}
	if int(fromSPS) > len(sps.LongTermRefPics) {
		return fmt.Errorf("%w: a slice names %d of the sequence's %d long-term pictures",
			ErrSliceHeader, fromSPS, len(sps.LongTermRefPics))
	}
	if total := uint64(fromSPS) + uint64(stated); total > maxRefPics*2 {
		return fmt.Errorf("%w: %d long-term pictures", ErrSliceHeader, total)
	}
	for i := uint32(0); i < fromSPS+stated; i++ {
		var e LongTermRef
		if i < fromSPS {
			idx := 0
			if n := ceilLog2(len(sps.LongTermRefPics)); n > 0 {
				idx = int(r.bits(n))
			}
			if r.err != nil {
				return r.err
			}
			if idx >= len(sps.LongTermRefPics) {
				return fmt.Errorf("%w: a slice names long-term picture %d of %d",
					ErrSliceHeader, idx, len(sps.LongTermRefPics))
			}
			e.POCLSB, e.Used, e.FromSPS = sps.LongTermRefPics[idx].POCLSB, sps.LongTermRefPics[idx].Used, true
		} else {
			e.POCLSB = r.bits(int(sps.Log2MaxPOCLSB))
			e.Used = r.flag()
		}
		e.MSBPresent = r.flag()
		if e.MSBPresent {
			e.DeltaMSBCycle = r.ue()
		}
		if r.err != nil {
			return r.err
		}
		out.LongTerm = append(out.LongTerm, e)
	}
	return nil
}

// ceilLog2 is how many bits an index into n things needs. One thing needs none,
// which is why this is not simply a bit length.
func ceilLog2(n int) int {
	if n <= 1 {
		return 0
	}
	return bits.Len(uint(n - 1))
}
