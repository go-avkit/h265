// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"

	"github.com/go-avkit/bitstream"
)

// Errors a sequence parameter set can be refused with.
var (
	// ErrNotSPS means the unit handed over is not a sequence parameter set.
	ErrNotSPS = errors.New("h265: unit is not a sequence parameter set")
	// ErrUnsupportedSPS means the set states something this reader does not
	// follow.
	ErrUnsupportedSPS = errors.New("h265: unsupported sequence parameter set")
)

// SPS is what a sequence parameter set says about the pictures that follow.
//
// Width and Height are what a player shows and what a container states: the coded
// size less the conformance window. Unlike H.264 the coded size is stated in luma
// samples directly rather than in macroblocks, so there is no multiply -- but the
// window still has to be taken off, and its offsets are in CHROMA units.
type SPS struct {
	VPSID          uint8
	MaxSubLayers   uint8
	ProfileSpace   uint8
	Tier           uint8
	ProfileIDC     uint8
	LevelIDC       uint8
	ID             uint32
	ChromaFormat   uint8
	SeparatePlanes bool
	CodedWidth     uint32
	CodedHeight    uint32
	WinLeft        uint32
	WinRight       uint32
	WinTop         uint32
	WinBottom      uint32
	Width          uint32
	Height         uint32
	BitDepthLuma   uint8
	BitDepthChroma uint8
	Log2MaxPOCLSB  uint8
	// ShortTermRefPicSets are the sets a slice may name instead of stating one
	// of its own, in the order the sequence lists them. A set may be written as
	// a difference from an earlier one, so the order is load-bearing.
	// SAOEnabled and TemporalMVPEnabled are not used here, but a slice header
	// cannot be read without them: both decide whether a field is present,
	// and a reader without them is misaligned from that point on.
	SAOEnabled         bool
	TemporalMVPEnabled bool

	// The block geometry, as the base-2 logarithms of sizes in luma samples
	// (7.4.3.2.1): MinCbLog2SizeY, CtbLog2SizeY, MinTbLog2SizeY, MaxTbLog2SizeY.
	Log2MinCbSize, Log2CtbSize   uint8
	Log2MinTbSize, Log2MaxTbSize uint8
	// MaxTransformHierarchyDepthInter and ...Intra are the depths of the
	// transform tree below a coding unit.
	MaxTransformHierarchyDepthInter, MaxTransformHierarchyDepthIntra uint8
	ScalingListEnabled                                               bool
	AMPEnabled                                                       bool
	StrongIntraSmoothing                                             bool
	// PCM is the pcm_enabled_flag group; its sample bit depths are in bits.
	PCM struct {
		Enabled                      bool
		BitDepthLuma, BitDepthChroma uint8
		Log2MinCbSize, Log2MaxCbSize uint8
		LoopFilterDisabled           bool
	}
	// Range is sps_range_extension; all false without one.
	Range RangeExtension

	ShortTermRefPicSets []ShortTermRPS
	// LongTermRefPics are the long-term pictures the sequence offers, named by
	// the low bits of their order count.
	LongTermRefPics []LongTermRefPic
	// LongTermRefPicsPresent says the sequence allows long-term pictures at
	// all. It is not the same as offering any: a sequence may allow them and
	// list none, and then a slice states its own.
	LongTermRefPicsPresent bool
}

// LongTermRefPic is one long-term picture a sequence offers to its slices.
type LongTermRefPic struct {
	POCLSB uint32
	Used   bool
}

// generalPTLBits is how many bits the general profile, tier and level spend.
//
// ⛔ 2 for the profile space, 1 for the tier, 5 for the profile, 32 compatibility
// flags, 4 source and packing flags, 43 that a profile either names as constraints
// or leaves reserved, 1 more, and 8 for the level: 96 in all. The count does NOT
// vary with the profile -- what varies is only whether those 44 bits have names --
// and a reader that shortened them for one profile would misread every field after
// them.
//
// The constant is confirmed by measurement rather than by a clean reading: on every
// HEVC file measured, the size this yields is the size the container states, and a
// count wrong by one bit would make the sizes nonsense.
const generalPTLBits = 96

// ParseSPS reads a sequence parameter set.
func ParseSPS(u Unit) (SPS, error) {
	if u.Type != UnitSPS {
		return SPS{}, fmt.Errorf("%w: type %d", ErrNotSPS, u.Type)
	}
	r := newSticky(u.Unescape())
	var s SPS

	s.VPSID = uint8(r.bits(4))
	s.MaxSubLayers = uint8(r.bits(3)) + 1
	r.bit() // sps_temporal_id_nesting_flag

	// The general profile, tier and level, then whatever the sub-layers state.
	s.ProfileSpace = uint8(r.bits(2))
	s.Tier = uint8(r.bits(1))
	s.ProfileIDC = uint8(r.bits(5))
	r.skip(32 + 4 + 43 + 1) // compatibility, source and packing flags, the rest
	s.LevelIDC = uint8(r.bits(8))
	s.skipSubLayerPTL(r)

	s.ID = r.ue()
	s.ChromaFormat = uint8(r.ue())
	if s.ChromaFormat > 3 {
		return s, fmt.Errorf("%w: chroma format %d is not 0 to 3", ErrUnsupportedSPS, s.ChromaFormat)
	}
	if s.ChromaFormat == 3 {
		s.SeparatePlanes = r.flag()
	}
	s.CodedWidth = r.ue()
	s.CodedHeight = r.ue()
	if r.flag() { // conformance_window_flag
		s.WinLeft, s.WinRight = r.ue(), r.ue()
		s.WinTop, s.WinBottom = r.ue(), r.ue()
	}
	// ⛔ Each of these is read and bounded BEFORE the conversion that narrows
	// it. uint8(1048576) is 0, so a value a million past the range comes out
	// looking like the smallest legal one -- and a bound applied afterwards
	// passes it. That is how the check below used to be written.
	//
	// The two bit depths are 7.4.3.2.1's 0..6, and the order count width its
	// 0..12. The last is a BIT COUNT that a slice header reads a field with:
	// a reader given a wrong one is misaligned from that field on.
	bdY, bdC, pocW := r.ue(), r.ue(), r.ue()
	if r.err != nil {
		return s, r.err
	}
	for _, f := range []struct {
		name  string
		v, hi uint32
	}{
		{"bit_depth_luma_minus8", bdY, 6},
		{"bit_depth_chroma_minus8", bdC, 6},
		{"log2_max_pic_order_cnt_lsb_minus4", pocW, 12},
	} {
		if f.v > f.hi {
			return s, fmt.Errorf("%w: %s of %d, at most %d", ErrUnsupportedSPS, f.name, f.v, f.hi)
		}
	}
	s.BitDepthLuma, s.BitDepthChroma = uint8(bdY)+8, uint8(bdC)+8
	s.Log2MaxPOCLSB = uint8(pocW) + 4

	s.skipSubLayerOrdering(r)
	if err := s.readCodingBlockSizes(r); err != nil {
		return s, err
	}
	if r.flag() { // scaling_list_enabled_flag
		s.ScalingListEnabled = true
		if r.flag() { // sps_scaling_list_data_present_flag
			// The same structure the picture parameter set carries, and the
			// same reader: a second copy would be a second thing to drift.
			skipScalingListData(r)
		}
	}
	s.AMPEnabled = r.flag()
	s.SAOEnabled = r.flag()
	if r.flag() { // pcm_enabled_flag
		s.PCM.Enabled = true
		s.PCM.BitDepthLuma = uint8(r.bits(4)) + 1
		s.PCM.BitDepthChroma = uint8(r.bits(4)) + 1
		minPCM, diffPCM := r.ue(), r.ue()
		s.PCM.LoopFilterDisabled = r.flag()
		// A reader that has failed returns zeros, which pass the checks below;
		// the failure itself is reported after them.
		// 7.4.3.2.1: Log2MinIpcmCbSizeY is 3 to Min( CtbLog2SizeY, 5 ), and the
		// largest is Min( CtbLog2SizeY, 5 ).
		if minPCM > 2 || diffPCM > 2 || uint8(minPCM)+3+uint8(diffPCM) > min(s.Log2CtbSize, 5) {
			return s, fmt.Errorf("%w: PCM block sizes %d and %d past the coding tree", ErrUnsupportedSPS, minPCM, diffPCM)
		}
		if s.PCM.BitDepthLuma > s.BitDepthLuma || s.PCM.BitDepthChroma > s.BitDepthChroma {
			return s, fmt.Errorf("%w: PCM bit depths past the coded ones", ErrUnsupportedSPS)
		}
		s.PCM.Log2MinCbSize = uint8(minPCM) + 3
		s.PCM.Log2MaxCbSize = s.PCM.Log2MinCbSize + uint8(diffPCM)
	}
	if r.err != nil {
		return s, r.err
	}
	if err := s.readShortTermRefPicSets(r); err != nil {
		return s, err
	}
	s.readLongTermRefPics(r)
	s.TemporalMVPEnabled = r.flag()
	s.StrongIntraSmoothing = r.flag()
	if r.flag() { // vui_parameters_present_flag
		readVUI(r, int(s.MaxSubLayers))
	}
	extData, err := s.readExtensions(r)
	if err != nil {
		return s, err
	}
	if r.err != nil {
		return s, r.err
	}
	// ⛔ As for a picture parameter set: the payload ends on the stop bit, or a
	// field was read wrongly. Extension data (sps_extension_4bits) is the one
	// thing allowed to follow, and it is ignored.
	if !extData {
		if r.moreData() {
			return s, fmt.Errorf("%w: %d bits left over, so a field was read wrongly", ErrUnsupportedSPS, r.r.Left())
		}
		if r.bit() != 1 || r.err != nil { // rbsp_stop_one_bit
			return s, fmt.Errorf("%w: no stop bit at the end", ErrUnsupportedSPS)
		}
	}
	if s.CodedWidth == 0 || s.CodedHeight == 0 {
		return s, fmt.Errorf("%w: a coded size of %dx%d", ErrUnsupportedSPS, s.CodedWidth, s.CodedHeight)
	}
	s.size()
	return s, nil
}

// skipSubLayerOrdering consumes what each sub-layer states about its decoded
// picture buffer. None of it is kept: this package reads syntax, and a buffer
// size describes a decoder rather than the stream.
func (s *SPS) skipSubLayerOrdering(r *sticky) {
	start := 0
	if !r.flag() { // sps_sub_layer_ordering_info_present_flag
		// One set of values stands for every sub-layer.
		start = int(s.MaxSubLayers) - 1
	}
	for i := start; i < int(s.MaxSubLayers); i++ {
		r.ue() // sps_max_dec_pic_buffering_minus1
		r.ue() // sps_max_num_reorder_pics
		r.ue() // sps_max_latency_increase_plus1
	}
}

// readCodingBlockSizes reads the coding and transform block geometry and the
// transform hierarchy depths, each bounded before it is narrowed (7.4.3.2.1:
// CtbLog2SizeY 4 to 6 in every profile of the design, MinCbLog2SizeY at least 3,
// a transform block never larger than 32 or than the coding tree block).
func (s *SPS) readCodingBlockSizes(r *sticky) error {
	minCb, diffCb := r.ue(), r.ue() // log2_min_luma_coding_block_size_minus3, log2_diff_max_min_luma_coding_block_size
	minTb, diffTb := r.ue(), r.ue() // log2_min_luma_transform_block_size_minus2, log2_diff_max_min_luma_transform_block_size
	depthInter, depthIntra := r.ue(), r.ue()
	if r.err != nil {
		return r.err
	}
	if minCb > 3 || diffCb > 3 || minCb+diffCb+3 > 6 {
		return fmt.Errorf("%w: coding block sizes %d and %d", ErrUnsupportedSPS, minCb, diffCb)
	}
	s.Log2MinCbSize = uint8(minCb) + 3
	s.Log2CtbSize = s.Log2MinCbSize + uint8(diffCb)
	if s.Log2CtbSize < 4 {
		return fmt.Errorf("%w: a coding tree block of %d samples", ErrUnsupportedSPS, 1<<s.Log2CtbSize)
	}
	if minTb > 3 || diffTb > 3 || minTb+2+diffTb > 5 {
		return fmt.Errorf("%w: transform block sizes %d and %d", ErrUnsupportedSPS, minTb, diffTb)
	}
	s.Log2MinTbSize = uint8(minTb) + 2
	s.Log2MaxTbSize = s.Log2MinTbSize + uint8(diffTb)
	if s.Log2MinTbSize >= s.Log2MinCbSize || s.Log2MaxTbSize > s.Log2CtbSize {
		return fmt.Errorf("%w: transform blocks of %d to %d in coding blocks of %d to %d",
			ErrUnsupportedSPS, 1<<s.Log2MinTbSize, 1<<s.Log2MaxTbSize, 1<<s.Log2MinCbSize, 1<<s.Log2CtbSize)
	}
	// max_transform_hierarchy_depth_*: 0 to CtbLog2SizeY - MinTbLog2SizeY.
	if limit := uint32(s.Log2CtbSize - s.Log2MinTbSize); depthInter > limit || depthIntra > limit {
		return fmt.Errorf("%w: transform hierarchy depths %d and %d", ErrUnsupportedSPS, depthInter, depthIntra)
	}
	s.MaxTransformHierarchyDepthInter, s.MaxTransformHierarchyDepthIntra = uint8(depthInter), uint8(depthIntra)
	return nil
}

// RangeExtension is sps_range_extension (7.3.2.2.2), all false when the set
// does not carry one.
type RangeExtension struct {
	TransformSkipRotation       bool
	TransformSkipContext        bool
	ImplicitRDPCM               bool
	ExplicitRDPCM               bool
	ExtendedPrecisionProcessing bool
	IntraSmoothingDisabled      bool
	HighPrecisionOffsets        bool
	PersistentRiceAdaptation    bool
	// CabacBypassAlignment is cabac_bypass_alignment_enabled_flag, which the
	// CABAC engine refuses (internal/cabac.ErrBypassAlignment).
	CabacBypassAlignment bool
}

// readExtensions reads sps_extension_present_flag and what it announces. The
// range extension is read; the multilayer extension is one bit and is spent;
// the 3D and screen content coding extensions, whose pictures this decoder
// does not decode, are refused rather than skipped, because their syntax
// follows and a skipped extension would hide a stream the decoder cannot
// follow.
func (s *SPS) readExtensions(r *sticky) (extData bool, err error) {
	if !r.flag() { // sps_extension_present_flag
		return false, nil
	}
	rangeExt, multilayer, ext3D, scc := r.flag(), r.flag(), r.flag(), r.flag()
	extData = r.bits(4) != 0 // sps_extension_4bits: sps_extension_data_flag follows, ignored
	if r.err != nil {
		return false, r.err
	}
	if rangeExt {
		s.Range = RangeExtension{
			TransformSkipRotation:       r.flag(),
			TransformSkipContext:        r.flag(),
			ImplicitRDPCM:               r.flag(),
			ExplicitRDPCM:               r.flag(),
			ExtendedPrecisionProcessing: r.flag(),
			IntraSmoothingDisabled:      r.flag(),
			HighPrecisionOffsets:        r.flag(),
			PersistentRiceAdaptation:    r.flag(),
			CabacBypassAlignment:        r.flag(),
		}
	}
	if multilayer {
		r.bit() // inter_view_mv_vert_constraint_flag (Annex F)
	}
	if ext3D || scc {
		return false, fmt.Errorf("%w: a 3D or screen content coding extension", ErrUnsupportedSPS)
	}
	return extData, nil
}

// readVUI reads vui_parameters (E.2.1) without keeping it: the fields after it
// cannot be found otherwise. Truncation is the sticky reader's to report.
func readVUI(r *sticky, maxSubLayers int) {
	if r.flag() { // aspect_ratio_info_present_flag
		if r.bits(8) == 255 { // aspect_ratio_idc == EXTENDED_SAR
			r.skip(32) // sar_width, sar_height
		}
	}
	if r.flag() { // overscan_info_present_flag
		r.bit()
	}
	if r.flag() { // video_signal_type_present_flag
		r.skip(3 + 1) // video_format, video_full_range_flag
		if r.flag() { // colour_description_present_flag
			r.skip(24)
		}
	}
	if r.flag() { // chroma_loc_info_present_flag
		r.ue()
		r.ue()
	}
	r.skip(3)     // neutral_chroma_indication_flag, field_seq_flag, frame_field_info_present_flag
	if r.flag() { // default_display_window_flag
		for i := 0; i < 4; i++ {
			r.ue()
		}
	}
	if r.flag() { // vui_timing_info_present_flag
		r.skip(64)    // vui_num_units_in_tick, vui_time_scale
		if r.flag() { // vui_poc_proportional_to_timing_flag
			r.ue()
		}
		if r.flag() { // vui_hrd_parameters_present_flag
			readHRD(r, maxSubLayers)
		}
	}
	if r.flag() { // bitstream_restriction_flag
		r.skip(3) // tiles_fixed_structure_flag, motion_vectors_over_pic_boundaries_flag, restricted_ref_pic_lists_flag
		for i := 0; i < 5; i++ {
			r.ue() // min_spatial_segmentation_idc .. log2_max_mv_length_vertical
		}
	}
}

// readHRD reads hrd_parameters (E.2.2) and sub_layer_hrd_parameters (E.2.3),
// with the inferences of E.3.2: fixed_pic_rate_within_cvs_flag is 1 when
// fixed_pic_rate_general_flag is, low_delay_hrd_flag and cpb_cnt_minus1 are 0
// when absent.
func readHRD(r *sticky, maxSubLayers int) { // commonInfPresentFlag is 1 in the VUI
	nal, vcl := r.flag(), r.flag()
	var subPic bool
	if nal || vcl {
		if subPic = r.flag(); subPic {
			r.skip(8 + 5 + 1 + 5) // tick_divisor_minus2, du_cpb_removal_delay_increment_length_minus1, sub_pic_cpb_params_in_pic_timing_sei_flag, dpb_output_delay_du_length_minus1
		}
		r.skip(4 + 4) // bit_rate_scale, cpb_size_scale
		if subPic {
			r.skip(4) // cpb_size_du_scale
		}
		r.skip(5 + 5 + 5) // the three delay lengths
	}
	for i := 0; i < maxSubLayers && r.err == nil; i++ {
		fixedGeneral := r.flag()
		fixedCVS := true
		if !fixedGeneral {
			fixedCVS = r.flag()
		}
		lowDelay := false
		if fixedCVS {
			r.ue() // elemental_duration_in_tc_minus1
		} else {
			lowDelay = r.flag()
		}
		cpbCnt := uint32(1)
		if !lowDelay {
			cpbCnt = r.ue() + 1
		}
		if cpbCnt > 32 { // cpb_cnt_minus1 is 0 to 31
			r.err = fmt.Errorf("%w: %d coded picture buffers", ErrUnsupportedSPS, cpbCnt)
			return
		}
		for _, present := range []bool{nal, vcl} {
			if !present {
				continue
			}
			for j := uint32(0); j < cpbCnt; j++ {
				r.ue() // bit_rate_value_minus1
				r.ue() // cpb_size_value_minus1
				if subPic {
					r.ue() // cpb_size_du_value_minus1
					r.ue() // bit_rate_du_value_minus1
				}
				r.bit() // cbr_flag
			}
		}
	}
}

// readShortTermRefPicSets reads every set the sequence carries.
func (s *SPS) readShortTermRefPicSets(r *sticky) error {
	n := r.ue()
	if r.err != nil {
		return r.err
	}
	if n > maxShortTermRPS {
		return fmt.Errorf("%w: %d short-term reference picture sets", ErrUnsupportedSPS, n)
	}
	for i := uint32(0); i < n; i++ {
		// Each set may be stated as a difference from the one before, so they
		// are read in order and every one is kept.
		set, err := parseShortTermRPS(r, int(i), s.ShortTermRefPicSets, false)
		if err != nil {
			return err
		}
		s.ShortTermRefPicSets = append(s.ShortTermRefPicSets, set)
	}
	return nil
}

// readLongTermRefPics reads the long-term pictures a sequence offers. They are
// named by the low bits of their order count, which is all a slice header needs
// to pick one.
func (s *SPS) readLongTermRefPics(r *sticky) {
	if !r.flag() { // long_term_ref_pics_present_flag
		return
	}
	s.LongTermRefPicsPresent = true
	n := r.ue()
	if r.err != nil || n > maxLongTermPics {
		return
	}
	for i := uint32(0); i < n; i++ {
		lsb := r.bits(int(s.Log2MaxPOCLSB))
		s.LongTermRefPics = append(s.LongTermRefPics, LongTermRefPic{POCLSB: lsb, Used: r.flag()})
	}
}

// skipSubLayerPTL consumes what the sub-layers state about profile and level.
//
// ⛔ The reserved bits between the present flags and the sub-layer structures are
// there to align the flags to sixteen: two bits for every sub-layer the stream does
// NOT have. A reader that skipped a fixed amount, or none, would be misaligned for
// every stream with more than one sub-layer -- and such streams are ordinary, since
// that is how a stream is made thinnable.
// It returns nothing: a refusal here can only be the reader running out, and that
// is answered once at the end of ParseSPS rather than at every field.
func (s *SPS) skipSubLayerPTL(r *sticky) {
	if s.MaxSubLayers == 1 {
		return
	}
	n := int(s.MaxSubLayers) - 1
	profilePresent := make([]bool, n)
	levelPresent := make([]bool, n)
	for i := 0; i < n; i++ {
		profilePresent[i] = r.flag()
		levelPresent[i] = r.flag()
	}
	r.skip(2 * (8 - n))
	for i := 0; i < n; i++ {
		if profilePresent[i] {
			// The same structure as the general one, without its level.
			r.skip(generalPTLBits - 8)
		}
		if levelPresent[i] {
			r.skip(8)
		}
	}
}

// size works out the size a player shows.
//
// ⛔ The window offsets are in CHROMA units, so a 4:2:0 stream takes off two luma
// samples for every unit stated. A reader that treated them as samples would report
// a picture narrower than it is by exactly half the crop, which looks like a
// plausible size.
func (s *SPS) size() {
	// ⛔ Written as a switch on the format rather than as a condition that
	// excludes monochrome: a condition of that shape lumps 4:4:4 in with the
	// subsampled formats, because it only asks whether there IS chroma. 4:4:4 has
	// chroma at FULL size, so its unit is one sample -- and a reader that took two
	// reports a picture narrower than it is by half the crop, which looks like a
	// plausible size. The same condition was already merged in go-avkit/h264, and
	// its own 4:4:4 test used separate colour planes, which takes the other
	// branch: the case was never exercised there.
	var subW, subH uint32
	switch {
	case s.ChromaFormat == 1: // 4:2:0, half in both directions
		subW, subH = 2, 2
	case s.ChromaFormat == 2: // 4:2:2, half width only
		subW, subH = 2, 1
	default: // monochrome and 4:4:4: one sample each way
		// Separate colour planes need no case of their own: the flag only exists
		// when the format is 4:4:4, which lands here already.
		subW, subH = 1, 1
	}
	s.Width = s.CodedWidth - subW*(s.WinLeft+s.WinRight)
	s.Height = s.CodedHeight - subH*(s.WinTop+s.WinBottom)
}

// sticky reads fields in a straight line, keeping the first error.
type sticky struct {
	r   *bitstream.Reader
	err error
}

func newSticky(data []byte) *sticky { return &sticky{r: bitstream.NewReader(data)} }

func (s *sticky) bit() uint32 {
	if s.err != nil {
		return 0
	}
	v, err := s.r.Bit()
	s.err = err
	return v
}

func (s *sticky) bits(n int) uint32 {
	if s.err != nil {
		return 0
	}
	v, err := s.r.Bits(n)
	s.err = err
	return v
}

func (s *sticky) flag() bool { return s.bit() == 1 }

// skip spends n bits without keeping them, in chunks a uint32 can hold.
func (s *sticky) skip(n int) {
	for n > 0 {
		take := n
		if take > 24 {
			take = 24
		}
		s.bits(take)
		n -= take
	}
}

// se reads a signed Exp-Golomb integer.
func (s *sticky) se() int32 {
	if s.err != nil {
		return 0
	}
	v, err := s.r.SE()
	s.err = err
	return v
}

// moreData says whether any syntax element remains before the bits that end a
// payload.
//
// It carries no guard for a reader that has already failed: the one caller checks
// that first, because a leftover-bits complaint about a payload that ran out would
// name the wrong fault. A guard here would be a branch no input can reach.
func (s *sticky) moreData() bool { return s.r.MoreData() }

func (s *sticky) ue() uint32 {
	if s.err != nil {
		return 0
	}
	v, err := s.r.UE()
	s.err = err
	return v
}
