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
	s.BitDepthLuma = uint8(r.ue()) + 8
	s.BitDepthChroma = uint8(r.ue()) + 8
	s.Log2MaxPOCLSB = uint8(r.ue()) + 4
	if s.Log2MaxPOCLSB > 16 {
		// 7.4.3.2.1 bounds log2_max_pic_order_cnt_lsb_minus4 at 12.
		return s, fmt.Errorf("%w: log2 max poc lsb of %d", ErrUnsupportedSPS, s.Log2MaxPOCLSB)
	}

	s.skipSubLayerOrdering(r)
	s.skipCodingBlockSizes(r)
	if r.flag() { // scaling_list_enabled_flag
		if r.flag() { // sps_scaling_list_data_present_flag
			// The same structure the picture parameter set carries, and the
			// same reader: a second copy would be a second thing to drift.
			skipScalingListData(r)
		}
	}
	r.bit() // amp_enabled_flag
	s.SAOEnabled = r.flag()
	if r.flag() { // pcm_enabled_flag
		r.bits(4) // pcm_sample_bit_depth_luma_minus1
		r.bits(4) // pcm_sample_bit_depth_chroma_minus1
		r.ue()    // log2_min_pcm_luma_coding_block_size_minus3
		r.ue()    // log2_diff_max_min_pcm_luma_coding_block_size
		r.bit()   // pcm_loop_filter_disabled_flag
	}
	if r.err != nil {
		return s, r.err
	}
	if err := s.readShortTermRefPicSets(r); err != nil {
		return s, err
	}
	s.readLongTermRefPics(r)
	s.TemporalMVPEnabled = r.flag()

	if r.err != nil {
		return s, r.err
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

// skipCodingBlockSizes consumes the coding and transform block geometry and the
// transform hierarchy depths.
func (s *SPS) skipCodingBlockSizes(r *sticky) {
	r.ue() // log2_min_luma_coding_block_size_minus3
	r.ue() // log2_diff_max_min_luma_coding_block_size
	r.ue() // log2_min_luma_transform_block_size_minus2
	r.ue() // log2_diff_max_min_luma_transform_block_size
	r.ue() // max_transform_hierarchy_depth_inter
	r.ue() // max_transform_hierarchy_depth_intra
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
	if r.err != nil || n > maxRefPics*2 {
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
