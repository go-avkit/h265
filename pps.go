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
	// ErrRefIdxRange means the set states more reference indices than a list
	// can hold. 7.4.3.3 puts num_ref_idx_lX_default_active_minus1 in 0..14; a
	// larger value is not a big stream, it is a few bytes asking a reader to
	// size a list from an attacker's number.
	ErrRefIdxRange = errors.New("h265: num_ref_idx_lX_default_active_minus1 out of range")
)

// MaxRefIdxActive is the most entries a reference picture list may have,
// 7.4.3.3. It bounds what a parameter set may state and what Lists will build.
const MaxRefIdxActive = 15

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

	// DiffCuQPDeltaDepth is diff_cu_qp_delta_depth: Log2MinCuQpDeltaSize is the
	// SPS's CtbLog2SizeY less it (7-36). Zero when cu_qp_delta is not enabled.
	DiffCuQPDeltaDepth uint8
	// ColumnWidthMinus1 and RowHeightMinus1 are the stated tile sizes, in
	// coding tree blocks, one fewer than the tile count when spacing is not
	// uniform; the last column and row are what is left.
	ColumnWidthMinus1, RowHeightMinus1 []uint32
	LoopFilterAcrossTiles              bool
	LoopFilterAcrossSlices             bool
	ScalingListPresent                 bool
	// Log2ParMrgLevel is Log2ParMrgLevel (7-37): log2_parallel_merge_level_minus2 + 2.
	Log2ParMrgLevel uint8
	// Range is pps_range_extension; its zero value is what an absent one infers.
	Range PPSRangeExtension
}

// PPSRangeExtension is pps_range_extension (7.3.2.3.2).
type PPSRangeExtension struct {
	// Log2MaxTransformSkipSize is log2_max_transform_skip_block_size_minus2 + 2;
	// 2 when absent.
	Log2MaxTransformSkipSize       uint8
	CrossComponentPrediction       bool
	ChromaQPOffsetListEnabled      bool
	DiffCuChromaQPOffsetDepth      uint8
	CbQPOffsetList, CrQPOffsetList []int32
	Log2SAOOffsetScaleLuma         uint8
	Log2SAOOffsetScaleChroma       uint8
}

// maxTiles is a bound on tile columns and rows that holds before the SPS is
// known (7.4.3.3 limits them to the picture's size in coding tree blocks, which
// the slice header step checks); it keeps a few bytes from asking for a loop of
// four billion.
const maxTiles = 1024

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
	// ⛔ Read BEFORE the +1 the syntax element carries: at the top of the range
	// the increment wraps to zero, and a bound applied after it would pass the
	// one value that most needs refusing.
	//
	// ⛔ Checked HERE, not where a list is built: every consumer of this set
	// inherits the number, and a bound on one of them leaves the rest exposed.
	l0, l1 := r.ue(), r.ue()
	if r.err == nil && (l0 >= MaxRefIdxActive || l1 >= MaxRefIdxActive) {
		return PPS{}, fmt.Errorf("%w: %d and %d", ErrRefIdxRange, l0, l1)
	}
	p.NumRefIdxL0, p.NumRefIdxL1 = l0+1, l1+1
	initQP := r.se()
	// 7.4.3.3: -( 26 + QpBdOffsetY ) to +25; QpBdOffsetY is at most 48.
	if r.err == nil && (initQP < -74 || initQP > 25) {
		return PPS{}, fmt.Errorf("%w: init_qp_minus26 of %d", ErrUnsupportedPPS, initQP)
	}
	p.InitQP = initQP + 26
	p.ConstrainedIntraPred = r.flag()
	p.TransformSkip = r.flag()
	p.CUQPDeltaEnabled = r.flag()
	if p.CUQPDeltaEnabled {
		d := r.ue() // diff_cu_qp_delta_depth: at most log2_diff_max_min_luma_coding_block_size, 3
		if r.err == nil && d > 3 {
			return PPS{}, fmt.Errorf("%w: diff_cu_qp_delta_depth of %d", ErrUnsupportedPPS, d)
		}
		p.DiffCuQPDeltaDepth = uint8(d)
	}
	p.CbQPOffset = r.se()
	p.CrQPOffset = r.se()
	if r.err == nil && (p.CbQPOffset < -12 || p.CbQPOffset > 12 || p.CrQPOffset < -12 || p.CrQPOffset > 12) {
		return PPS{}, fmt.Errorf("%w: chroma QP offsets %d and %d", ErrUnsupportedPPS, p.CbQPOffset, p.CrQPOffset)
	}
	p.SliceChromaQPOffsets = r.flag()
	p.WeightedPred = r.flag()
	p.WeightedBipred = r.flag()
	p.TransquantBypass = r.flag()
	p.TilesEnabled = r.flag()
	p.EntropySyncEnabled = r.flag()

	p.TileColumns, p.TileRows = 1, 1
	if p.TilesEnabled {
		cols, rows := r.ue(), r.ue() // minus 1; bounded before the +1 can wrap
		if r.err == nil && (cols >= maxTiles || rows >= maxTiles) {
			return PPS{}, fmt.Errorf("%w: %d tile columns and %d rows", ErrUnsupportedPPS, uint64(cols)+1, uint64(rows)+1)
		}
		p.TileColumns, p.TileRows = cols+1, rows+1
		p.UniformTileSpacing = r.flag()
		if !p.UniformTileSpacing {
			// ⛔ One width per column and one height per row, each one LESS the
			// last: the final column and row are what is left over and are not
			// stated. Reading one too many would take the flag that follows as a
			// width, and everything after it from the wrong bits.
			for i := uint32(1); i < p.TileColumns; i++ {
				p.ColumnWidthMinus1 = append(p.ColumnWidthMinus1, r.ue())
			}
			for i := uint32(1); i < p.TileRows; i++ {
				p.RowHeightMinus1 = append(p.RowHeightMinus1, r.ue())
			}
		}
		p.LoopFilterAcrossTiles = r.flag()
	}
	p.LoopFilterAcrossSlices = r.flag()

	if r.flag() { // deblocking_filter_control_present_flag
		p.DeblockingOverride = r.flag()
		p.DeblockingDisabled = r.flag()
		if !p.DeblockingDisabled {
			p.BetaOffsetDiv2 = r.se()
			p.TCOffsetDiv2 = r.se()
		}
	}
	if p.ScalingListPresent = r.flag(); p.ScalingListPresent { // pps_scaling_list_data_present_flag
		skipScalingListData(r)
	}
	p.ListsModification = r.flag()
	merge := r.ue() // log2_parallel_merge_level_minus2: at most CtbLog2SizeY - 2, 4
	if r.err == nil && merge > 4 {
		return PPS{}, fmt.Errorf("%w: log2_parallel_merge_level_minus2 of %d", ErrUnsupportedPPS, merge)
	}
	p.Log2ParMrgLevel = uint8(merge) + 2
	p.SliceHeaderExtension = r.flag()
	extData, err := p.readExtensions(r)
	if err != nil {
		return p, err
	}

	if r.err != nil {
		return p, r.err
	}
	// ⛔ A picture parameter set ends on the bits that end every payload -- a one,
	// then zeros to the byte. Landing anywhere else means a field was consumed
	// wrongly, and there is nothing else in a parameter set to check its values
	// against. This identity IS the check, and it is what turns a misread scaling
	// list from a plausible quantiser into a refusal.
	if !extData {
		if r.moreData() {
			return p, fmt.Errorf("%w: %d bits left over, so a field was read wrongly",
				ErrUnsupportedPPS, r.r.Left())
		}
		if r.bit() != 1 || r.err != nil { // rbsp_stop_one_bit
			return p, fmt.Errorf("%w: no stop bit at the end", ErrUnsupportedPPS)
		}
	}
	return p, nil
}

// readExtensions reads pps_extension_present_flag and the range extension. The
// multilayer, 3D and screen content coding extensions are refused rather than
// skipped, as for the SPS. extData says pps_extension_4bits announced data
// that is ignored.
func (p *PPS) readExtensions(r *sticky) (extData bool, err error) {
	p.Range.Log2MaxTransformSkipSize = 2
	if !r.flag() { // pps_extension_present_flag
		return false, nil
	}
	rangeExt, multilayer, ext3D, scc := r.flag(), r.flag(), r.flag(), r.flag()
	extData = r.bits(4) != 0
	if r.err != nil {
		return false, r.err
	}
	if multilayer || ext3D || scc {
		return false, fmt.Errorf("%w: a multilayer, 3D or screen content coding extension", ErrUnsupportedPPS)
	}
	if !rangeExt {
		return extData, nil
	}
	x := &p.Range
	if p.TransformSkip {
		sz := r.ue() // log2_max_transform_skip_block_size_minus2: at most MaxTbLog2SizeY - 2, 3
		if r.err == nil && sz > 3 {
			return false, fmt.Errorf("%w: log2_max_transform_skip_block_size_minus2 of %d", ErrUnsupportedPPS, sz)
		}
		x.Log2MaxTransformSkipSize = uint8(sz) + 2
	}
	x.CrossComponentPrediction = r.flag()
	if x.ChromaQPOffsetListEnabled = r.flag(); x.ChromaQPOffsetListEnabled {
		depth, n := r.ue(), r.ue()+1 // diff_cu_chroma_qp_offset_depth, chroma_qp_offset_list_len_minus1 + 1
		if r.err == nil && (depth > 3 || n > 6) {
			return false, fmt.Errorf("%w: chroma QP offset list of %d at depth %d", ErrUnsupportedPPS, n, depth)
		}
		x.DiffCuChromaQPOffsetDepth = uint8(depth)
		for i := uint32(0); i < n && r.err == nil; i++ {
			cb, cr := r.se(), r.se()
			if cb < -12 || cb > 12 || cr < -12 || cr > 12 {
				return false, fmt.Errorf("%w: chroma QP offsets %d and %d in the list", ErrUnsupportedPPS, cb, cr)
			}
			x.CbQPOffsetList, x.CrQPOffsetList = append(x.CbQPOffsetList, cb), append(x.CrQPOffsetList, cr)
		}
	}
	// At most Max( 0, BitDepth - 10 ), which is 6 at 16 bits.
	luma, chroma := r.ue(), r.ue()
	if r.err == nil && (luma > 6 || chroma > 6) {
		return false, fmt.Errorf("%w: SAO offset scales %d and %d", ErrUnsupportedPPS, luma, chroma)
	}
	x.Log2SAOOffsetScaleLuma, x.Log2SAOOffsetScaleChroma = uint8(luma), uint8(chroma)
	return extData, nil
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
