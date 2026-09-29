// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// Errors a picture parameter set can be refused with.
var (
	// ErrNotPPS means the unit handed over is not a picture parameter set.
	ErrNotPPS = errors.New("h265: unit is not a picture parameter set")
	// ErrUnsupportedPPS means the set does not end where a set must end, so
	// something in it was read wrongly.
	ErrUnsupportedPPS = errors.New("h265: picture parameter set was not consumed exactly")
)

// PPS is what a picture parameter set says about the slices that refer to it.
//
// Two of these fields are what a slice segment header cannot be read without:
// whether a segment may depend on the one before it, and how many bits of the
// header the stream reserves before the slice type. A reader without them is
// misaligned from the first field that matters.
type PPS struct {
	ID                     uint32
	SPSID                  uint32
	DependentSlicesEnabled bool
	OutputFlagPresent      bool
	ExtraSliceHeaderBits   uint8
	SignDataHiding         bool
	CABACInitPresent       bool
	NumRefIdxL0            uint32
	NumRefIdxL1            uint32
	InitQP                 int32 // already un-biased
	ConstrainedIntraPred   bool
	TransformSkip          bool
	CUQPDeltaEnabled       bool
	CbQPOffset             int32
	CrQPOffset             int32
	SliceChromaQPOffsets   bool
	WeightedPred           bool
	WeightedBipred         bool
	TransquantBypass       bool
	TilesEnabled           bool
	EntropySyncEnabled     bool
	TileColumns            uint32
	TileRows               uint32
	UniformTileSpacing     bool
	DeblockingOverride     bool
	DeblockingDisabled     bool
	BetaOffsetDiv2         int32
	TCOffsetDiv2           int32
	ListsModification      bool
	SliceHeaderExtension   bool
}

// ParsePPS reads a picture parameter set.
//
// ⛔ Unlike H.264's, this needs no other set to be consumed exactly: HEVC states
// the number of scaling lists by the size class rather than by the chroma format,
// so the variable parts here depend only on fields the set itself carries.
func ParsePPS(u Unit) (PPS, error) {
	if u.Type != UnitPPS {
		return PPS{}, fmt.Errorf("%w: type %d", ErrNotPPS, u.Type)
	}
	r := newSticky(u.Unescape())
	var p PPS

	p.ID = r.ue()
	p.SPSID = r.ue()
	p.DependentSlicesEnabled = r.flag()
	p.OutputFlagPresent = r.flag()
	p.ExtraSliceHeaderBits = uint8(r.bits(3))
	p.SignDataHiding = r.flag()
	p.CABACInitPresent = r.flag()
	p.NumRefIdxL0 = r.ue() + 1
	p.NumRefIdxL1 = r.ue() + 1
	p.InitQP = r.se() + 26
	p.ConstrainedIntraPred = r.flag()
	p.TransformSkip = r.flag()
	p.CUQPDeltaEnabled = r.flag()
	if p.CUQPDeltaEnabled {
		r.ue() // diff_cu_qp_delta_depth
	}
	p.CbQPOffset = r.se()
	p.CrQPOffset = r.se()
	p.SliceChromaQPOffsets = r.flag()
	p.WeightedPred = r.flag()
	p.WeightedBipred = r.flag()
	p.TransquantBypass = r.flag()
	p.TilesEnabled = r.flag()
	p.EntropySyncEnabled = r.flag()

	p.TileColumns, p.TileRows = 1, 1
	if p.TilesEnabled {
		p.TileColumns = r.ue() + 1
		p.TileRows = r.ue() + 1
		p.UniformTileSpacing = r.flag()
		if !p.UniformTileSpacing {
			// ⛔ One width per column and one height per row, each one LESS the
			// last: the final column and row are what is left over and are not
			// stated. Reading one too many would take the flag that follows as a
			// width, and everything after it from the wrong bits.
			for i := uint32(1); i < p.TileColumns; i++ {
				r.ue()
			}
			for i := uint32(1); i < p.TileRows; i++ {
				r.ue()
			}
		}
		r.flag() // loop_filter_across_tiles_enabled_flag
	}
	r.flag() // pps_loop_filter_across_slices_enabled_flag

	if r.flag() { // deblocking_filter_control_present_flag
		p.DeblockingOverride = r.flag()
		p.DeblockingDisabled = r.flag()
		if !p.DeblockingDisabled {
			p.BetaOffsetDiv2 = r.se()
			p.TCOffsetDiv2 = r.se()
		}
	}
	if r.flag() { // pps_scaling_list_data_present_flag
		skipScalingListData(r)
	}
	p.ListsModification = r.flag()
	r.ue() // log2_parallel_merge_level_minus2
	p.SliceHeaderExtension = r.flag()
	if r.flag() { // pps_extension_present_flag
		// Four named extension flags and four reserved bits. Whatever they turn
		// on is not read here, so a set that states any of them is refused by the
		// check below rather than half-read.
		r.bits(8)
	}

	if r.err != nil {
		return p, r.err
	}
	// ⛔ A picture parameter set ends on the bits that end every payload -- a one,
	// then zeros to the byte. Landing anywhere else means a field was consumed
	// wrongly, and there is nothing else in a parameter set to check its values
	// against. This identity IS the check, and it is what turns a misread scaling
	// list from a plausible quantiser into a refusal.
	if r.moreData() {
		return p, fmt.Errorf("%w: %d bits left over, so a field was read wrongly",
			ErrUnsupportedPPS, r.r.Left())
	}
	return p, nil
}

// skipScalingListData consumes the scaling lists a set may carry.
//
// ⛔ Four size classes. The first three state six matrices each and the largest
// states two, and a list is either predicted from another -- one number -- or given
// outright, as sixteen coefficients for the smallest class and sixty-four for the
// rest, preceded by a direct-current term for the two largest. Reading the wrong
// count for any of them leaves the rest of the set misaligned, and the set has no
// field that would show it.
func skipScalingListData(r *sticky) {
	for sizeID := 0; sizeID < 4; sizeID++ {
		step := 1
		if sizeID == 3 {
			// The largest class states two matrices rather than six.
			step = 3
		}
		for matrixID := 0; matrixID < 6; matrixID += step {
			if !r.flag() { // scaling_list_pred_mode_flag
				r.ue() // which earlier list this one copies
				continue
			}
			if sizeID > 1 {
				r.se() // scaling_list_dc_coef_minus8
			}
			n := 64
			if sizeID == 0 {
				n = 16
			}
			for i := 0; i < n; i++ {
				r.se()
			}
		}
	}
}
