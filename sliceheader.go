// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"
)

// SliceHeader is slice_segment_header( ) (7.3.6.1) read in full, with the
// inferences of 7.4.7.1 applied, and where the slice segment data begins.
type SliceHeader struct {
	FirstSliceSegmentInPic bool
	NoOutputOfPriorPics    bool
	PPSID                  uint32
	Dependent              bool
	// SegmentAddress is slice_segment_address, in CTBs in raster scan; 0 for the
	// first segment of a picture.
	SegmentAddress uint32

	// Type is slice_type. PicOutput is pic_output_flag (1 when absent) and
	// ColourPlaneID colour_plane_id.
	Type          SliceType
	PicOutput     bool
	ColourPlaneID uint8
	// POCLSB is slice_pic_order_cnt_lsb, 0 for an IDR picture.
	POCLSB uint32
	// Refs is what the slice says about the pictures it predicts from: the
	// short-term and long-term sets, list sizes and modification, collocated
	// picture, weights, merge candidates, and the SAO, mvd_l1_zero and
	// cabac_init flags.
	Refs PictureRefs

	// QPDelta is slice_qp_delta and SliceQP is SliceQpY (7-54).
	QPDelta int32
	SliceQP int32
	// CbQPOffset and CrQPOffset are slice_cb_qp_offset and slice_cr_qp_offset (0
	// when absent); CuChromaQPOffsetEnabled is cu_chroma_qp_offset_enabled_flag.
	CbQPOffset, CrQPOffset  int32
	CuChromaQPOffsetEnabled bool
	// The deblocking and loop filter fields, with the PPS's values where the
	// slice states none.
	DeblockingDisabled     bool
	BetaOffsetDiv2         int32
	TCOffsetDiv2           int32
	LoopFilterAcrossSlices bool

	// EntryPoints are entry_point_offset_minus1 + 1: byte counts of the subsets
	// of the slice segment data, COUNTING emulation prevention bytes (7.4.7.1).
	EntryPoints []uint32

	// HeaderBits is how many bits of Unit.Unescape()'s output the syntax elements
	// take, before byte_alignment( ) (the unit's two header bytes are not part
	// of the payload).
	HeaderBits int
	// DataOffset is the offset of the slice segment data in Unit.Unescape()'s
	// output: the first byte after byte_alignment( ).
	DataOffset int
}

// ParseSliceSegmentHeaderFull reads a slice segment header against the sequence
// and picture parameter sets it activates. prev is the header of the previous
// independent slice segment of the same picture, which a dependent segment
// takes everything from but its address, entry points and data offset (nil
// otherwise).
//
// The activation is where PPS.Validate is called: a picture parameter set that
// does not fit its sequence is refused here, before any slice data is read.
func ParseSliceSegmentHeaderFull(u Unit, sps SPS, pps PPS, prev *SliceHeader) (SliceHeader, error) {
	var h SliceHeader
	// The slice segment types of Table 7-1 (0 to 9 and 16 to 21); the reserved
	// ones carry nothing this decoder can read.
	if !(u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA)) {
		return h, fmt.Errorf("%w: type %d", ErrNotSlice, u.Type)
	}
	if u.LayerID != 0 {
		return h, fmt.Errorf("%w: nuh_layer_id %d", ErrSliceHeader, u.LayerID)
	}
	if err := pps.Validate(sps); err != nil {
		return h, err
	}
	picSizeInCtbs := picSizeInCtbs(sps)
	if picSizeInCtbs > 1<<24 {
		return h, fmt.Errorf("%w: a picture of %d coding tree blocks", ErrSliceHeader, picSizeInCtbs)
	}
	r := newSticky(u.Unescape())

	h.FirstSliceSegmentInPic = r.flag()
	if u.Type.IsRandomAccess() { // BLA_W_LP to CRA, which includes IDR
		h.NoOutputOfPriorPics = r.flag()
	}
	id := r.ue()
	if r.err == nil && id != pps.ID {
		return h, fmt.Errorf("%w: slice names picture parameter set %d, given %d", ErrSliceHeader, id, pps.ID)
	}
	h.PPSID = id

	if !h.FirstSliceSegmentInPic {
		if pps.DependentSlicesEnabled {
			h.Dependent = r.flag()
		}
		h.SegmentAddress = r.bits(ceilLog2(int(picSizeInCtbs)))
		if r.err == nil && uint64(h.SegmentAddress) >= picSizeInCtbs {
			return h, fmt.Errorf("%w: slice_segment_address %d in a picture of %d coding tree blocks",
				ErrSliceHeader, h.SegmentAddress, picSizeInCtbs)
		}
	}

	if h.Dependent {
		if prev == nil {
			return h, fmt.Errorf("%w: a dependent slice segment with no slice segment before it", ErrSliceHeader)
		}
		// Everything an independent segment states, from the one before.
		keep := h
		h = *prev
		h.FirstSliceSegmentInPic, h.NoOutputOfPriorPics, h.PPSID = keep.FirstSliceSegmentInPic, keep.NoOutputOfPriorPics, keep.PPSID
		h.Dependent, h.SegmentAddress = true, keep.SegmentAddress
		h.EntryPoints, h.DataOffset = nil, 0
	} else if err := readIndependentFields(r, u, sps, pps, &h); err != nil {
		return h, err
	}

	if err := readEntryPoints(r, sps, pps, &h); err != nil {
		return h, err
	}
	if pps.SliceHeaderExtension {
		n := r.ue() // slice_segment_header_extension_length: 0 to 256
		if r.err == nil && n > 256 {
			return h, fmt.Errorf("%w: slice_segment_header_extension_length of %d", ErrSliceHeader, n)
		}
		r.skip(int(n) * 8)
	}
	h.HeaderBits = len(u.Unescape())*8 - r.r.Left()
	// byte_alignment( ): a one, then zeros to the byte boundary.
	if r.bit() != 1 {
		r.err = fmt.Errorf("%w: byte_alignment does not start with a one", ErrSliceHeader)
	}
	for r.err == nil && r.r.Left()%8 != 0 {
		if r.bit() != 0 {
			r.err = fmt.Errorf("%w: byte_alignment pads with a one", ErrSliceHeader)
		}
	}
	if r.err != nil {
		return h, r.err
	}
	h.DataOffset = len(u.Unescape()) - r.r.Left()/8
	return h, nil
}

// picSizeInCtbs is PicSizeInCtbsY (7-15 to 7-19).
func picSizeInCtbs(sps SPS) uint64 {
	ctb := uint64(1) << sps.Log2CtbSize
	return ((uint64(sps.CodedWidth) + ctb - 1) / ctb) * ((uint64(sps.CodedHeight) + ctb - 1) / ctb)
}

// readIndependentFields reads what only an independent slice segment states.
func readIndependentFields(r *sticky, u Unit, sps SPS, pps PPS, h *SliceHeader) error {
	for i := uint8(0); i < pps.ExtraSliceHeaderBits; i++ {
		r.bit() // slice_reserved_flag
	}
	rawType := r.ue() // bounded before it is narrowed
	if r.err == nil && rawType > uint32(SliceI) {
		return fmt.Errorf("%w: slice type %d", ErrSliceHeader, rawType)
	}
	h.Type = SliceType(rawType)
	h.PicOutput = true
	if pps.OutputFlagPresent {
		h.PicOutput = r.flag()
	}
	if sps.SeparatePlanes {
		h.ColourPlaneID = uint8(r.bits(2))
		if h.ColourPlaneID > 2 {
			return fmt.Errorf("%w: colour_plane_id %d", ErrSliceHeader, h.ColourPlaneID)
		}
	}
	idr := u.Type.IsIDR()
	refs := &h.Refs
	refs.Type = h.Type
	if !idr {
		h.POCLSB = r.bits(int(sps.Log2MaxPOCLSB))
		if err := readSliceShortTerm(r, sps, refs); err != nil {
			return err
		}
		if err := readSliceLongTerm(r, sps, refs); err != nil {
			return err
		}
	}
	if err := readSliceLists(r, sps, pps, refs, idr); err != nil {
		return err
	}

	// slice_qp_delta, and SliceQpY of equation 7-54, in -QpBdOffsetY to 51.
	h.QPDelta = r.se()
	// Bounded before the sum: a delta near 2^31 would wrap it into range.
	if r.err == nil && (h.QPDelta < -128 || h.QPDelta > 128) {
		return fmt.Errorf("%w: slice_qp_delta of %d", ErrSliceHeader, h.QPDelta)
	}
	h.SliceQP = pps.InitQP + h.QPDelta
	if qpBd := 6 * (int32(sps.BitDepthLuma) - 8); r.err == nil && (h.SliceQP < -qpBd || h.SliceQP > 51) {
		return fmt.Errorf("%w: SliceQpY of %d", ErrSliceHeader, h.SliceQP)
	}
	if pps.SliceChromaQPOffsets {
		h.CbQPOffset, h.CrQPOffset = r.se(), r.se()
		if r.err == nil && (!qpOffsetOK(h.CbQPOffset, pps.CbQPOffset) || !qpOffsetOK(h.CrQPOffset, pps.CrQPOffset)) {
			return fmt.Errorf("%w: slice chroma QP offsets %d and %d", ErrSliceHeader, h.CbQPOffset, h.CrQPOffset)
		}
	}
	if pps.Range.ChromaQPOffsetListEnabled {
		h.CuChromaQPOffsetEnabled = r.flag()
	}
	override := false
	if pps.DeblockingOverride {
		override = r.flag() // deblocking_filter_override_flag
	}
	h.DeblockingDisabled, h.BetaOffsetDiv2, h.TCOffsetDiv2 = pps.DeblockingDisabled, pps.BetaOffsetDiv2, pps.TCOffsetDiv2
	if override {
		h.DeblockingDisabled = r.flag()
		if !h.DeblockingDisabled {
			h.BetaOffsetDiv2, h.TCOffsetDiv2 = r.se(), r.se()
			if r.err == nil && (h.BetaOffsetDiv2 < -6 || h.BetaOffsetDiv2 > 6 || h.TCOffsetDiv2 < -6 || h.TCOffsetDiv2 > 6) {
				return fmt.Errorf("%w: slice deblocking offsets %d and %d", ErrSliceHeader, h.BetaOffsetDiv2, h.TCOffsetDiv2)
			}
		}
	}
	h.LoopFilterAcrossSlices = pps.LoopFilterAcrossSlices
	if pps.LoopFilterAcrossSlices && (refs.SAOLuma || refs.SAOChroma || !h.DeblockingDisabled) {
		h.LoopFilterAcrossSlices = r.flag()
	}
	return r.err
}

// qpOffsetOK says slice_cb/cr_qp_offset is in -12 to 12 and so is its sum with
// the picture parameter set's (7.4.7.1).
func qpOffsetOK(slice, pps int32) bool {
	return slice >= -12 && slice <= 12 && slice+pps >= -12 && slice+pps <= 12
}

// readEntryPoints reads num_entry_point_offsets and the offsets.
func readEntryPoints(r *sticky, sps SPS, pps PPS, h *SliceHeader) error {
	if !pps.TilesEnabled && !pps.EntropySyncEnabled {
		return nil
	}
	n := r.ue() // a failed read gives 0, and the failure is reported at the end
	// 7.4.7.1: the most subsets a segment can have.
	ctb := uint64(1) << sps.Log2CtbSize
	heightInCtbs := (uint64(sps.CodedHeight) + ctb - 1) / ctb
	var limit uint64
	switch {
	case !pps.TilesEnabled:
		limit = heightInCtbs
	case !pps.EntropySyncEnabled:
		limit = uint64(pps.TileColumns) * uint64(pps.TileRows)
	default:
		limit = uint64(pps.TileColumns) * heightInCtbs
	}
	if uint64(n) >= limit { // num_entry_point_offsets is 0 to limit-1
		return fmt.Errorf("%w: %d entry points, at most %d", ErrSliceHeader, n, limit-1)
	}
	if n == 0 {
		return nil
	}
	lenMinus1 := r.ue()
	if r.err == nil && lenMinus1 > 31 {
		return fmt.Errorf("%w: offset_len_minus1 of %d", ErrSliceHeader, lenMinus1)
	}
	width := int(lenMinus1) + 1
	h.EntryPoints = make([]uint32, 0, n)
	for i := uint32(0); i < n && r.err == nil; i++ {
		// u(v) of up to 32 bits, read in two parts: sticky.bits takes at most 24 at
		// a time in this package's other callers.
		var v uint64
		if width > 16 {
			v = uint64(r.bits(width-16)) << 16
			v |= uint64(r.bits(16))
		} else {
			v = uint64(r.bits(width))
		}
		if v == 1<<32-1 { // entry_point_offset_minus1 + 1 would be 2^32
			return fmt.Errorf("%w: an entry point offset of 2^32", ErrSliceHeader)
		}
		h.EntryPoints = append(h.EntryPoints, uint32(v)+1)
	}
	return r.err
}
