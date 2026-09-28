// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

// Package h265 reads an H.265/HEVC bitstream in pure Go, with no libavcodec
// linkage and no external binaries.
//
// The framing and the bit reading are go-avkit/bitstream's, shared with H.264:
// the two formats separate their units identically and differ in how the header
// bytes of a unit are read -- H.264 spends one and this spends two -- and in what
// the parameter sets hold. This package is that difference.
package h265

import "github.com/go-avkit/bitstream"

// UnitType is what a NAL unit holds. Only the ones this package acts on are
// named; the rest travel as their number.
type UnitType uint8

// The unit types a reader has to tell apart.
//
// ⛔ A picture's type is not one flag here as it is in H.264. HEVC names sixteen
// kinds of slice segment, and which of them can be decoded without anything before
// it is what a sample table needs: the ones from BLA_W_LP to CRA are exactly that
// set, and IsRandomAccess is where the boundary is written down rather than
// scattered.
const (
	UnitTrailN    UnitType = 0
	UnitTrailR    UnitType = 1
	UnitBLAWLP    UnitType = 16
	UnitIDRWRADL  UnitType = 19
	UnitIDRNLP    UnitType = 20
	UnitCRA       UnitType = 21
	UnitVPS       UnitType = 32
	UnitSPS       UnitType = 33
	UnitPPS       UnitType = 34
	UnitAUD       UnitType = 35
	UnitEOS       UnitType = 36
	UnitEOB       UnitType = 37
	UnitFD        UnitType = 38
	UnitPrefixSEI UnitType = 39
	UnitSuffixSEI UnitType = 40
)

// IsSliceSegment says whether a unit carries coded picture data.
func (t UnitType) IsSliceSegment() bool { return t <= 31 }

// IsRandomAccess says whether a picture of this type can be decoded with nothing
// before it, which is what a sample table calls a sync sample.
func (t UnitType) IsRandomAccess() bool { return t >= UnitBLAWLP && t <= UnitCRA }

// Unit is one NAL unit: its two header bytes read, and its payload still escaped.
//
// Payload is a window into the caller's bytes and is not copied.
type Unit struct {
	Type UnitType
	// LayerID is which layer of a scalable stream this belongs to. A plain
	// stream states zero throughout, and a decoder that ignored it would mix the
	// layers of one that does not.
	LayerID uint8
	// TemporalID is the sub-layer, one more than the field on the wire. A stream
	// can be thinned by dropping the higher ones, which is what it is for.
	TemporalID uint8
	Payload    []byte
}

// SplitAnnexB finds the NAL units of a byte stream and reads each one's header.
func SplitAnnexB(data []byte) ([]Unit, error) {
	raw, err := bitstream.SplitAnnexB(data)
	if err != nil {
		return nil, err
	}
	return units(raw)
}

// SplitLengthPrefixed finds the units of the form an MP4 sample carries, where
// each is preceded by its length in lengthSize bytes, and reads each one's header.
//
// lengthSize comes from the hvcC record.
func SplitLengthPrefixed(data []byte, lengthSize int) ([]Unit, error) {
	raw, err := bitstream.SplitLengthPrefixed(data, lengthSize)
	if err != nil {
		return nil, err
	}
	return units(raw)
}

// units reads the two header bytes of each raw unit.
//
// ⛔ Two bytes, and a unit of one is refused rather than read: the second byte
// carries the temporal sub-layer, and a unit that had only the first would have it
// read out of whatever followed. bitstream drops units with no bytes at all, so
// one byte is the shortest that can arrive here.
func units(raw [][]byte) ([]Unit, error) {
	out := make([]Unit, 0, len(raw))
	for i, body := range raw {
		if len(body) < 2 {
			return nil, &ShortHeaderError{Unit: i + 1, Bytes: len(body)}
		}
		// forbidden_zero_bit, then six bits of type, then six of layer, then
		// three of temporal id plus one.
		out = append(out, Unit{
			Type:       UnitType(body[0] >> 1 & 0x3F),
			LayerID:    uint8(body[0]&1)<<5 | body[1]>>3,
			TemporalID: body[1]&0x7 + 1,
			Payload:    body[2:],
		})
	}
	return out, nil
}

// ShortHeaderError means a unit is too short to hold the two bytes of header this
// format spends.
type ShortHeaderError struct {
	Unit  int
	Bytes int
}

func (e *ShortHeaderError) Error() string {
	return "h265: unit " + itoa(e.Unit) + " holds " + itoa(e.Bytes) + " bytes, and a header needs two"
}

// itoa avoids pulling in fmt for one message.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Unescape undoes the escaping inside this unit's payload.
func (u Unit) Unescape() []byte { return bitstream.Unescape(u.Payload) }
