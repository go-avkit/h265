// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"errors"
	"fmt"
)

// ErrSEI means an SEI RBSP is not what 7.3.2.4 and 7.3.5 allow: a message runs
// past the end of the unit, or a payload is not the size its syntax needs.
var ErrSEI = errors.New("h265: SEI message is not valid")

// SEIPayloadDecodedPictureHash is payloadType of the decoded picture hash SEI
// message (D.2.1, D.2.20).
const SEIPayloadDecodedPictureHash = 132

// SEIMessage is one sei_message( ) (7.3.5): its payloadType and the payloadSize
// bytes of its payload, unescaped.
type SEIMessage struct {
	Type    uint32
	Payload []byte
}

// ParseSEI reads the messages of a prefix or suffix SEI unit: sei_rbsp( ) of 7.3.2.4.
func ParseSEI(u Unit) ([]SEIMessage, error) {
	if u.Type != UnitPrefixSEI && u.Type != UnitSuffixSEI {
		return nil, fmt.Errorf("%w: unit type %d is not an SEI unit", ErrSEI, u.Type)
	}
	rbsp := u.Unescape()
	// more_rbsp_data( ): the last 1 bit is rbsp_stop_one_bit; messages are whole
	// bytes, so there is more data while a byte before the last nonzero one is left,
	// or that byte is not the stop bit alone.
	last := len(rbsp) - 1
	for last >= 0 && rbsp[last] == 0 {
		last--
	}
	if last < 0 {
		return nil, fmt.Errorf("%w: no rbsp_stop_one_bit", ErrSEI)
	}
	var out []SEIMessage
	pos := 0
	for pos < last || (pos == last && rbsp[pos] != 0x80) {
		var typ, size uint64
		for pos < len(rbsp) && rbsp[pos] == 0xFF {
			typ += 255
			pos++
		}
		if pos >= len(rbsp) {
			return nil, fmt.Errorf("%w: payloadType runs past the unit", ErrSEI)
		}
		typ += uint64(rbsp[pos])
		pos++
		for pos < len(rbsp) && rbsp[pos] == 0xFF {
			size += 255
			pos++
		}
		if pos >= len(rbsp) {
			return nil, fmt.Errorf("%w: payloadSize runs past the unit", ErrSEI)
		}
		size += uint64(rbsp[pos])
		pos++
		// The payload ends before the byte with rbsp_stop_one_bit, or there is no
		// rbsp_trailing_bits( ).
		if pos > last || size > uint64(last-pos) || typ > 1<<31 {
			return nil, fmt.Errorf("%w: payload type %d of %d bytes with %d left before the stop bit", ErrSEI, typ, size, last-pos)
		}
		out = append(out, SEIMessage{Type: uint32(typ), Payload: rbsp[pos : pos+int(size)]})
		pos += int(size)
	}
	return out, nil
}

// The hash_type values of Table D.10.
const (
	HashMD5 = iota
	HashCRC
	HashChecksum
)

// PictureHash is decoded_picture_hash( ) (D.2.20) of one decoded picture: the hash
// of each of its Components colour components, in the field its Type says.
type PictureHash struct {
	Type       uint8
	Components int
	MD5        [3][16]byte
	CRC        [3]uint16
	Checksum   [3]uint32
}

// ParsePictureHash reads a decoded picture hash payload of a picture whose
// chroma_format_idc is given (it decides how many components are hashed). A
// reserved hash_type is not an error but is to be ignored (D.3.20): ok is false.
func ParsePictureHash(payload []byte, chromaFormatIdc int) (h PictureHash, ok bool, err error) {
	if len(payload) < 1 {
		return h, false, fmt.Errorf("%w: an empty decoded picture hash", ErrSEI)
	}
	h.Type = payload[0]
	h.Components = 3
	if chromaFormatIdc == 0 {
		h.Components = 1
	}
	each := [3]int{16, 2, 4}
	if h.Type > HashChecksum {
		return h, false, nil
	}
	if want := 1 + h.Components*each[h.Type]; len(payload) != want {
		return h, false, fmt.Errorf("%w: a decoded picture hash of %d bytes, %d needed for hash_type %d", ErrSEI, len(payload), want, h.Type)
	}
	body := payload[1:]
	for c := 0; c < h.Components; c++ {
		switch h.Type {
		case HashMD5:
			copy(h.MD5[c][:], body[c*16:])
		case HashCRC:
			h.CRC[c] = uint16(body[c*2])<<8 | uint16(body[c*2+1])
		default:
			b := body[c*4:]
			h.Checksum[c] = uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		}
	}
	return h, true, nil
}

// crcTable is the byte-wise form of the CRC of D-24 (poly 0x1021, no reflection).
var crcTable = func() (t [256]uint16) {
	for i := range t {
		c := uint16(i) << 8
		for k := 0; k < 8; k++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return
}()

// Hash computes the decoded picture hash of kind (HashMD5, HashCRC, HashChecksum)
// as D.3.20 arranges and hashes the samples.
func (p *DecodedPicture) Hash(kind uint8) PictureHash {
	h := PictureHash{Type: kind, Components: p.Components}
	for c := 0; c < p.Components; c++ {
		pl, deep := &p.Planes[c], p.BitDepth[c] > 8
		switch kind {
		case HashMD5:
			m := md5.New()
			if pl.Pix8 != nil { // the rows are the bytes the hash takes
				for y := 0; y < pl.Height; y++ {
					m.Write(pl.Pix8[y*pl.Stride : y*pl.Stride+pl.Width])
				}
			} else {
				row := make([]byte, 0, 2*pl.Width)
				for y := 0; y < pl.Height; y++ {
					row = row[:0]
					for _, v := range pl.Pix16[y*pl.Stride : y*pl.Stride+pl.Width] {
						row = append(row, byte(v))
						if deep {
							row = append(row, byte(v>>8))
						}
					}
					m.Write(row)
				}
			}
			m.Sum(h.MD5[c][:0])
		case HashCRC:
			// D-24 shifts the data through the register and then two zero bytes. That
			// is the table-driven CRC with the initial value 0xFFFF moved ahead of
			// those two bytes: 0x1D0F.
			if pl.Pix8 != nil {
				h.CRC[c] = crcPlane(pl.Pix8, pl, deep)
			} else {
				h.CRC[c] = crcPlane(pl.Pix16, pl, deep)
			}
		default:
			if pl.Pix8 != nil {
				h.Checksum[c] = checksumPlane(pl.Pix8, pl, deep)
			} else {
				h.Checksum[c] = checksumPlane(pl.Pix16, pl, deep)
			}
		}
	}
	return h
}

// crcPlane is the CRC of D-24 over the samples of a plane, a byte a sample, or two (low byte
// first) when deep.
func crcPlane[T pixel](pix []T, pl *Plane, deep bool) uint16 {
	crc := uint16(0x1D0F)
	step := func(b byte) { crc = crc<<8 ^ crcTable[byte(crc>>8)^b] }
	for y := 0; y < pl.Height; y++ {
		for _, v := range pix[y*pl.Stride : y*pl.Stride+pl.Width] {
			step(byte(v))
			if deep {
				step(byte(uint16(v) >> 8))
			}
		}
	}
	return crc
}

// checksumPlane is the checksum of D.3.20 over the samples of a plane.
func checksumPlane[T pixel](pix []T, pl *Plane, deep bool) uint32 {
	var sum uint32
	for y := 0; y < pl.Height; y++ {
		for x, v := range pix[y*pl.Stride : y*pl.Stride+pl.Width] {
			mask := uint32((x & 0xFF) ^ (y & 0xFF) ^ (x >> 8) ^ (y >> 8))
			sum += uint32(v&0xFF) ^ mask
			if deep {
				sum += uint32(uint16(v)>>8) ^ mask
			}
		}
	}
	return sum
}

// Verify reports the first component of p whose hash is not the stated one.
func (h PictureHash) Verify(p *DecodedPicture) error {
	if h.Components != p.Components {
		return fmt.Errorf("h265: the hash covers %d components, the picture has %d", h.Components, p.Components)
	}
	got := p.Hash(h.Type)
	for c := 0; c < h.Components; c++ {
		switch h.Type {
		case HashMD5:
			if got.MD5[c] != h.MD5[c] {
				return fmt.Errorf("h265: component %d: MD5 %x, the stream's %x", c, got.MD5[c], h.MD5[c])
			}
		case HashCRC:
			if got.CRC[c] != h.CRC[c] {
				return fmt.Errorf("h265: component %d: CRC %04x, the stream's %04x", c, got.CRC[c], h.CRC[c])
			}
		default:
			if got.Checksum[c] != h.Checksum[c] {
				return fmt.Errorf("h265: component %d: checksum %08x, the stream's %08x", c, got.Checksum[c], h.Checksum[c])
			}
		}
	}
	return nil
}
