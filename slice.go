// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// Errors a slice segment header can be refused with.
var (
	// ErrNotSlice means the unit handed over does not carry a slice segment.
	ErrNotSlice = errors.New("h265: unit does not carry a slice segment")
	// ErrSliceHeader means a segment states something this reader does not follow.
	ErrSliceHeader = errors.New("h265: unsupported slice segment header")
)

// SliceType is how a slice is coded. HEVC numbers them the other way round from
// H.264, and naming them here rather than reusing a number is the point.
type SliceType uint8

// The slice types.
const (
	SliceB SliceType = 0
	SliceP SliceType = 1
	SliceI SliceType = 2
)

// String names a slice type the way tools report picture types.
func (t SliceType) String() string {
	switch t {
	case SliceB:
		return "B"
	case SliceP:
		return "P"
	case SliceI:
		return "I"
	}
	return fmt.Sprintf("slice type %d", uint8(t))
}

// SliceSegmentHeader is the beginning of a slice segment: which picture it belongs
// to, whether it starts one, and -- when it starts one -- how that picture is coded.
//
// ⛔ Type is read only for a segment that BEGINS a picture, and TypeRead says so.
// Reaching the type of a later segment means first reading its address, whose width
// comes from the coding tree block size in the sequence set -- a field this package
// does not read yet. Reporting a type it had not read would be worse than saying it
// did not: a picture's type would then come from whichever segment was looked at.
type SliceSegmentHeader struct {
	First        bool // first_slice_segment_in_pic_flag
	Dependent    bool // only meaningful when First is false
	PPSID        uint32
	Type         SliceType
	TypeRead     bool
	RandomAccess bool // the unit type says this picture can stand alone
	// NoOutputOfPriorPics is stated only by a random access picture.
	NoOutputOfPriorPics bool
}

// ParseSliceSegmentHeader reads the beginning of a slice segment.
//
// It needs the picture parameter set, which states whether a segment may depend on
// the one before it and how many bits the stream reserves before the slice type.
// Both change where the type sits, so a reader without them is misaligned from the
// first field that matters.
func ParseSliceSegmentHeader(u Unit, pps PPS) (SliceSegmentHeader, error) {
	var h SliceSegmentHeader
	if !u.Type.IsSliceSegment() {
		return h, fmt.Errorf("%w: type %d", ErrNotSlice, u.Type)
	}
	h.RandomAccess = u.Type.IsRandomAccess()

	r := newSticky(u.Unescape())
	h.First = r.flag()
	if h.RandomAccess {
		h.NoOutputOfPriorPics = r.flag()
	}
	h.PPSID = r.ue()

	if !h.First {
		if pps.DependentSlicesEnabled {
			h.Dependent = r.flag()
		}
		// The address would follow, in a width this package cannot work out yet.
		// Everything above it has been read, and that is what is reported.
		if r.err != nil {
			return h, r.err
		}
		return h, nil
	}

	// The stream may reserve bits here, and how many is the picture set's business.
	for i := uint8(0); i < pps.ExtraSliceHeaderBits; i++ {
		r.bit()
	}
	t := r.ue()
	if t > uint32(SliceI) {
		return h, fmt.Errorf("%w: slice type %d", ErrSliceHeader, t)
	}
	h.Type, h.TypeRead = SliceType(t), true

	if r.err != nil {
		return h, r.err
	}
	return h, nil
}

// Picture is one coded picture: the units that carry it, and what it is.
//
// Units are in the order they arrived, parameter sets and all, so a caller writing
// a sample table has the bytes of a sample and nothing else to gather.
type Picture struct {
	Units []Unit
	Type  SliceType
	// TypeKnown is false when no segment of this picture stated a type, which
	// happens when a stream is joined part way through a picture.
	TypeKnown bool
	Sync      bool
	Bytes     int
}

// SplitPictures groups a stream's units into the pictures they carry.
//
// ⛔ Where H.264 needs several fields compared between neighbouring slices to find a
// boundary, HEVC states it outright: one flag per segment says whether it begins a
// picture. That is the whole rule here, and it is worth saying that the simpler
// format is simpler rather than carrying the other one's machinery over.
//
// Parameter sets and delimiters that arrive before a picture's first segment belong
// to that picture: a decoder must hold them before the segment that uses them.
func SplitPictures(units []Unit, pps PPS) ([]Picture, error) {
	var out []Picture
	var pending []Unit
	var current *Picture

	commit := func() {
		if current != nil {
			out = append(out, *current)
			current = nil
		}
	}

	for _, u := range units {
		if !u.Type.IsSliceSegment() {
			pending = append(pending, u)
			continue
		}
		h, err := ParseSliceSegmentHeader(u, pps)
		if err != nil {
			// What did read is handed back: a caller told "this went wrong" needs
			// the pictures that were whole, and the last one is the likeliest
			// reason.
			commit()
			return out, err
		}
		if h.First {
			commit()
			current = &Picture{Type: h.Type, TypeKnown: h.TypeRead, Sync: h.RandomAccess}
		}
		if current == nil {
			// Segments before any picture began: a stream joined part way through
			// one. They carry no type this can name, and dropping them silently
			// would make a count of pictures disagree with a count of segments for
			// a reason nobody could see.
			current = &Picture{Sync: h.RandomAccess}
		}
		current.Units = append(current.Units, pending...)
		for _, p := range pending {
			current.Bytes += len(p.Payload) + 2
		}
		pending = nil
		current.Units = append(current.Units, u)
		current.Bytes += len(u.Payload) + 2
	}
	commit()
	return out, nil
}
