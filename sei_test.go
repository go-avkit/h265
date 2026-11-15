// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"errors"
	"math/rand/v2"
	"testing"
)

// refPictureData is pictureData[ cIdx ] of D-22, literally.
func refPictureData(p *DecodedPicture, c int) []byte {
	pl := p.Planes[c]
	var out []byte
	for i := 0; i < pl.Width*pl.Height; i++ {
		v := pl.Pix[(i/pl.Width)*pl.Stride+i%pl.Width]
		out = append(out, byte(v&0xFF))
		if p.BitDepth[c] > 8 {
			out = append(out, byte(v>>8))
		}
	}
	return out
}

// refCRC is D-24 literally: bit by bit, two zero bytes appended.
func refCRC(data []byte) uint16 {
	crc := 0xFFFF
	data = append(append([]byte(nil), data...), 0, 0)
	for bitIdx := 0; bitIdx < len(data)*8; bitIdx++ {
		dataByte := int(data[bitIdx>>3])
		crcMsb := (crc >> 15) & 1
		bitVal := (dataByte >> (7 - (bitIdx & 7))) & 1
		crc = (((crc << 1) + bitVal) & 0xFFFF) ^ (crcMsb * 0x1021)
	}
	return uint16(crc)
}

// refChecksum is D-25 literally.
func refChecksum(p *DecodedPicture, c int) uint32 {
	pl := p.Planes[c]
	var sum uint32
	for y := 0; y < pl.Height; y++ {
		for x := 0; x < pl.Width; x++ {
			xorMask := uint32((x & 0xFF) ^ (y & 0xFF) ^ (x >> 8) ^ (y >> 8))
			v := uint32(pl.Pix[y*pl.Stride+x])
			sum += (v & 0xFF) ^ xorMask
			if p.BitDepth[c] > 8 {
				sum += (v >> 8) ^ xorMask
			}
		}
	}
	return sum
}

func randomPicture(rng *rand.Rand) *DecodedPicture {
	p := &DecodedPicture{Components: []int{1, 3}[rng.IntN(2)]}
	for c := 0; c < p.Components; c++ {
		w, h := 1+rng.IntN(300), 1+rng.IntN(40)
		if rng.IntN(4) == 0 { // y >> 8 and x >> 8 of the checksum need 256 or more
			w, h = 250+rng.IntN(20), 250+rng.IntN(20)
		}
		stride := w + rng.IntN(5)
		p.BitDepth[c] = 8 + rng.IntN(9)
		pix := make([]uint16, stride*h)
		for i := range pix {
			pix[i] = uint16(rng.IntN(1 << p.BitDepth[c]))
		}
		p.Planes[c] = Plane{Width: w, Height: h, Stride: stride, Pix: pix}
	}
	return p
}

func TestPictureHashAgainstTheSpecificationText(t *testing.T) {
	rng := rand.New(rand.NewPCG(101, 102))
	for iter := 0; iter < 300; iter++ {
		p := randomPicture(rng)
		md, crc, sum := p.Hash(HashMD5), p.Hash(HashCRC), p.Hash(HashChecksum)
		for c := 0; c < p.Components; c++ {
			data := refPictureData(p, c)
			if md.MD5[c] != md5.Sum(data) {
				t.Fatalf("iter %d component %d: MD5 differs", iter, c)
			}
			if crc.CRC[c] != refCRC(data) {
				t.Fatalf("iter %d component %d: CRC %04x, D-24 gives %04x", iter, c, crc.CRC[c], refCRC(data))
			}
			if sum.Checksum[c] != refChecksum(p, c) {
				t.Fatalf("iter %d component %d: checksum differs", iter, c)
			}
		}
		for _, h := range []PictureHash{md, crc, sum} {
			if err := h.Verify(p); err != nil {
				t.Fatal(err)
			}
			// One changed sample is found, in the component it is in.
			c := rng.IntN(p.Components)
			pl := &p.Planes[c]
			i := rng.IntN(pl.Height)*pl.Stride + rng.IntN(pl.Width)
			pl.Pix[i] ^= 1
			if h.Verify(p) == nil {
				t.Fatalf("iter %d: a changed sample passed hash type %d", iter, h.Type)
			}
			pl.Pix[i] ^= 1
		}
	}
	if err := (PictureHash{Components: 3}).Verify(&DecodedPicture{Components: 1}); err == nil {
		t.Error("a hash of 3 components verified a picture of 1")
	}
}

// seiUnit wraps an RBSP in an SEI unit (no emulation prevention needed in these).
func seiUnit(typ UnitType, rbsp ...byte) Unit { return Unit{Type: typ, Payload: rbsp} }

func TestParseSEI(t *testing.T) {
	// Two messages: type 5 of 3 bytes, then type 300 (FF 2D) of 256 bytes (FF 01).
	long := make([]byte, 256)
	long[0], long[255] = 7, 9
	rbsp := append([]byte{5, 3, 1, 2, 3, 0xFF, 0x2D, 0xFF, 0x01}, long...)
	rbsp = append(rbsp, 0x80)
	msgs, err := ParseSEI(seiUnit(UnitSuffixSEI, rbsp...))
	if err != nil || len(msgs) != 2 {
		t.Fatalf("%v %d messages", err, len(msgs))
	}
	if msgs[0].Type != 5 || len(msgs[0].Payload) != 3 || msgs[1].Type != 300 || len(msgs[1].Payload) != 256 || msgs[1].Payload[255] != 9 {
		t.Errorf("%+v", msgs)
	}
	// A message whose last byte is 0x80 is data, not the stop bit.
	msgs, err = ParseSEI(seiUnit(UnitPrefixSEI, 5, 1, 0x80, 0x80))
	if err != nil || len(msgs) != 1 || msgs[0].Payload[0] != 0x80 {
		t.Errorf("a 0x80 payload byte: %v %+v", err, msgs)
	}
	for name, u := range map[string]Unit{
		"not an SEI unit":      seiUnit(UnitSPS, 5, 0, 0x80),
		"no stop bit":          seiUnit(UnitSuffixSEI, 0, 0, 0),
		"empty":                seiUnit(UnitSuffixSEI),
		"size past the end":    seiUnit(UnitSuffixSEI, 5, 9, 1, 0x80),
		"type runs off":        seiUnit(UnitSuffixSEI, 0xFF, 0xFF),
		"size runs off":        seiUnit(UnitSuffixSEI, 5, 0xFF, 0xFF),
		"payload eats the end": seiUnit(UnitSuffixSEI, 5, 2, 1, 0x80),
	} {
		if _, err := ParseSEI(u); !errors.Is(err, ErrSEI) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A type of more than 2^31 (a million 0xFF bytes would be needed; 9 million is not).
	huge := make([]byte, 0, 9_000_000)
	for i := 0; i < 8_500_000; i++ {
		huge = append(huge, 0xFF)
	}
	huge = append(huge, 0, 0, 0x80)
	if _, err := ParseSEI(seiUnit(UnitSuffixSEI, huge...)); !errors.Is(err, ErrSEI) {
		t.Errorf("a payload type of 2 million: %v", err)
	}
}

func TestParsePictureHash(t *testing.T) {
	md := append([]byte{HashMD5}, make([]byte, 48)...)
	md[1], md[48] = 0xAB, 0xCD
	h, ok, err := ParsePictureHash(md, 1)
	if err != nil || !ok || h.Components != 3 || h.MD5[0][0] != 0xAB || h.MD5[2][15] != 0xCD {
		t.Errorf("md5: %v %v %+v", err, ok, h)
	}
	h, ok, err = ParsePictureHash(md[:17], 0)
	if err != nil || !ok || h.Components != 1 {
		t.Errorf("monochrome md5: %v %v %+v", err, ok, h)
	}
	h, _, err = ParsePictureHash([]byte{HashCRC, 1, 2, 3, 4, 5, 6}, 2)
	if err != nil || h.CRC != [3]uint16{0x0102, 0x0304, 0x0506} {
		t.Errorf("crc: %v %+v", err, h)
	}
	h, _, err = ParsePictureHash([]byte{HashChecksum, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, 3)
	if err != nil || h.Checksum != [3]uint32{0x01020304, 0x05060708, 0x090A0B0C} {
		t.Errorf("checksum: %v %+v", err, h)
	}
	// A reserved hash_type is ignored, wrong sizes and empty payloads are refused.
	if _, ok, err := ParsePictureHash([]byte{3, 1, 2}, 1); ok || err != nil {
		t.Errorf("reserved type: %v %v", ok, err)
	}
	for _, bad := range [][]byte{nil, md[:20], {HashCRC, 1, 2}, {HashChecksum, 1, 2, 3, 4}} {
		if _, _, err := ParsePictureHash(bad, 1); !errors.Is(err, ErrSEI) {
			t.Errorf("%x: %v", bad, err)
		}
	}
}
