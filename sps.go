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

	if r.err != nil {
		return s, r.err
	}
	if s.CodedWidth == 0 || s.CodedHeight == 0 {
		return s, fmt.Errorf("%w: a coded size of %dx%d", ErrUnsupportedSPS, s.CodedWidth, s.CodedHeight)
	}
	s.size()
	return s, nil
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

func (s *sticky) ue() uint32 {
	if s.err != nil {
		return 0
	}
	v, err := s.r.UE()
	s.err = err
	return v
}
