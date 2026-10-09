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

	// Type is how the slice is coded. An I slice predicts from nothing, so the
	// fields below are not stated for one.
	Type SliceType
	// NumRefIdxL0Active and NumRefIdxL1Active are how many entries each
	// reference list has. They start at the picture parameter set's and a
	// slice may override them.
	NumRefIdxL0Active uint32
	NumRefIdxL1Active uint32
	// ListEntryL0 and ListEntryL1 are ref_pic_lists_modification, 7.3.6.2: the
	// position in the temporary list that each entry of the final list is
	// taken from. nil means the slice stated no modification for that list and
	// the default order stands -- which is NOT the same as an empty slice.
	ListEntryL0 []uint32
	ListEntryL1 []uint32

	// TemporalMVP says the slice predicts motion from another picture, which
	// is what the two fields below name.
	TemporalMVP bool
	// CollocatedFromL0 says which list the picture is taken from, and
	// CollocatedRefIdx its position in that list. Both are only meaningful
	// while TemporalMVP is set.
	CollocatedFromL0 bool
	CollocatedRefIdx uint32
	// Weights is pred_weight_table, nil where the picture parameter set does
	// not weight this kind of slice.
	Weights *PredWeights
	// MaxMergeCand is 5 - five_minus_max_num_merge_cand, in 1..5.
	MaxMergeCand uint8
}

// numPicTotalCurr is 7.4.7.2: how many pictures this slice may predict from,
// counting only those marked used. It is what the width of a list_entry is
// taken from, and a P or B slice with none of them is malformed.
//
// The current-picture-as-reference of the screen content extension is not
// counted, because this package does not read that extension.
func (p PictureRefs) numPicTotalCurr() int {
	n := 0
	for _, e := range p.ShortTerm.Before {
		if e.Used {
			n++
		}
	}
	for _, e := range p.ShortTerm.After {
		if e.Used {
			n++
		}
	}
	for _, e := range p.LongTerm {
		if e.Used {
			n++
		}
	}
	return n
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
	r, _, sliceType, err := walkToPOC(u, sps, pps)
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
	out.Type = sliceType
	if err := readSliceLists(r, sps, pps, &out); err != nil {
		return out, err
	}
	return out, nil
}

// readSliceLists reads what the slice says about its reference lists: how many
// entries each has, and the order they are taken in.
//
// The two flags before them state nothing this package keeps, but both are
// CONDITIONAL on the sequence, and a reader that guessed either is misaligned
// from that point on.
func readSliceLists(r *sticky, sps SPS, pps PPS, out *PictureRefs) error {
	if sps.TemporalMVPEnabled {
		out.TemporalMVP = r.flag()
	}
	if sps.SAOEnabled {
		r.bit() // slice_sao_luma_flag
		// ⛔ ChromaArrayType, not chroma_format_idc. They differ by exactly one
		// case: separate colour planes make the format 4:4:4 and the array type
		// ZERO, so a reader using the format reads a bit that is not there.
		if chromaArrayType(sps) != 0 {
			r.bit() // slice_sao_chroma_flag
		}
	}
	if out.Type != SliceP && out.Type != SliceB {
		return r.err
	}

	out.NumRefIdxL0Active, out.NumRefIdxL1Active = pps.NumRefIdxL0, pps.NumRefIdxL1
	if r.flag() { // num_ref_idx_active_override_flag
		// ⛔ Read before the +1, and bounded here: see ErrRefIdxRange. A slice
		// may override the set's counts, so bounding the set alone would leave
		// this door open.
		l0 := r.ue()
		l1 := uint32(0)
		if out.Type == SliceB {
			l1 = r.ue()
		}
		if r.err != nil {
			return r.err
		}
		if l0 >= MaxRefIdxActive || l1 >= MaxRefIdxActive {
			return fmt.Errorf("%w: a slice states %d and %d", ErrRefIdxRange, l0, l1)
		}
		out.NumRefIdxL0Active = l0 + 1
		if out.Type == SliceB {
			out.NumRefIdxL1Active = l1 + 1
		}
	}

	total := out.numPicTotalCurr()
	if r.err != nil {
		return r.err
	}
	if total == 0 {
		return fmt.Errorf("%w: a %s slice that may predict from no picture", ErrSliceHeader, out.Type)
	}
	// Nothing is stated when there is only one picture to choose from: there is
	// no other order it could be put in.
	if pps.ListsModification && total > 1 {
		var err error
		if r.flag() { // ref_pic_list_modification_flag_l0
			out.ListEntryL0, err = readListEntries(r, total, int(out.NumRefIdxL0Active))
		}
		if err == nil && out.Type == SliceB && r.flag() { // ..._flag_l1
			out.ListEntryL1, err = readListEntries(r, total, int(out.NumRefIdxL1Active))
		}
		if err != nil {
			return err
		}
	}
	return readSlicePrediction(r, sps, pps, out)
}

// readSlicePrediction reads the rest of what a P or B slice says about how it
// predicts: which picture its motion comes from, how each reference is
// weighted, and how many merge candidates it uses.
//
// Two fields here are read and not kept -- mvd_l1_zero_flag and
// cabac_init_flag. Neither says anything about the pictures a caller of this
// package is after, and both still have to be consumed: one is conditional on
// the slice type and the other on the picture parameter set.
func readSlicePrediction(r *sticky, sps SPS, pps PPS, out *PictureRefs) error {
	if out.Type == SliceB {
		r.bit() // mvd_l1_zero_flag
	}
	if pps.CABACInitPresent {
		r.bit() // cabac_init_flag
	}
	if out.TemporalMVP {
		// A P slice has only list 0 to take it from, and says nothing.
		out.CollocatedFromL0 = true
		if out.Type == SliceB {
			out.CollocatedFromL0 = r.flag()
		}
		n := out.NumRefIdxL0Active
		if !out.CollocatedFromL0 {
			n = out.NumRefIdxL1Active
		}
		// ⛔ Stated only when there is a choice. A reader that always read it
		// would consume a field that is not there.
		if n > 1 {
			out.CollocatedRefIdx = r.ue()
			// No check on the reader here: a failed read returns zero, which
			// is below n whenever n is above one, so a truncated stream is
			// reported by the check after five_minus_max_num_merge_cand rather
			// than as a picture it never named. TestAFailedReadReturnsZero
			// holds that. ⛔ Ablating this guard left the suite green, which
			// is what said it changed nothing.
			if out.CollocatedRefIdx >= n {
				return fmt.Errorf("%w: a collocated picture at %d of %d",
					ErrSliceHeader, out.CollocatedRefIdx, n)
			}
		}
	}
	if (pps.WeightedPred && out.Type == SliceP) ||
		(pps.WeightedBipred && out.Type == SliceB) {
		w, err := readPredWeights(r, sps, out)
		if err != nil {
			return err
		}
		out.Weights = w
	}
	five := r.ue()
	if r.err != nil {
		return r.err
	}
	if five > 4 {
		return fmt.Errorf("%w: five_minus_max_num_merge_cand of %d", ErrSliceHeader, five)
	}
	out.MaxMergeCand = 5 - uint8(five)
	return nil
}

// readListEntries reads one list's entries, each as wide as the number of
// pictures to choose from needs.
//
// ⛔ The width is taken from NumPicTotalCurr and the COUNT from the active
// entries. They are different numbers: a list may be longer or shorter than the
// pictures available.
//
// ⛔ And the VALUE is bounded by NumPicTotalCurr too, 7.4.7.2 -- not by the
// temporary list it indexes, which is longer when the list has more entries
// than there are pictures. The field is Ceil(Log2(N)) bits wide, so it holds
// values past the last picture: three pictures are named in two bits.
func readListEntries(r *sticky, total, n int) ([]uint32, error) {
	width := ceilLog2(total)
	out := make([]uint32, 0, n)
	for i := 0; i < n; i++ {
		v := r.bits(width)
		if r.err != nil {
			return nil, r.err
		}
		if int(v) >= total {
			return nil, fmt.Errorf("%w: entry %d names picture %d of %d",
				ErrSliceHeader, i, v, total)
		}
		out = append(out, v)
	}
	return out, nil
}

// chromaArrayType is 7.4.3.2.1: the chroma format EXCEPT where the planes are
// coded separately, which makes every plane monochrome.
func chromaArrayType(sps SPS) uint8 {
	if sps.SeparatePlanes {
		return 0
	}
	return sps.ChromaFormat
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
