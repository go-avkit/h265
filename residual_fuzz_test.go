// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"testing"

	"github.com/go-avkit/h265/internal/cabac"
)

// FuzzDecodeResidual reads residual_coding( ) from arbitrary bytes under any
// combination of tools: it must return, never panic, refuse only with
// ErrResidual, and leave every level within CoeffMin..CoeffMax.
func FuzzDecodeResidual(f *testing.F) {
	f.Add([]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x11, 0x22, 0x33}, uint16(0x1234), uint8(0x41), false)
	f.Add([]byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, uint16(0xffff), uint8(0xff), true)
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8}, uint16(0), uint8(0), true)
	f.Add([]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x11, 0x22, 0x33}, uint16(0x1234), uint8(0x41), true)
	f.Fuzz(func(t *testing.T, data []byte, toolBits uint16, shape uint8, align bool) {
		tools := &ResidualTools{
			BypassAlignment: align,
			ChromaArrayType: 1 + int(toolBits&1)*2, SignDataHiding: toolBits&2 != 0, TransformSkip: toolBits&4 != 0,
			Log2MaxTransformSkipSize: 2 + int(toolBits>>3&3), TransformSkipContext: toolBits&32 != 0,
			ImplicitRDPCM: toolBits&64 != 0, ExplicitRDPCM: toolBits&128 != 0, PersistentRiceAdaptation: toolBits&256 != 0,
			ExtendedPrecision: toolBits&512 != 0, BitDepthY: 8 + int(toolBits>>10&7), BitDepthC: 8 + int(toolBits>>13&7),
		}
		in := ResidualInput{
			Log2Size: 2 + int(shape&3), CIdx: int(shape >> 2 & 3 % 3), Intra: shape&16 != 0,
			PredModeIntra: int(shape) % 35, CuTransquantBypass: shape&32 != 0,
		}
		var d cabac.Decoder
		if d.Init(data) != nil {
			return
		}
		var m cabac.Model
		m.Init(int(shape>>6)%3, int(toolBits)%52)
		out := ResidualBlock{Levels: make([]int32, 1<<(2*in.Log2Size))}
		err := DecodeResidual(&d, &m, tools, in, &out)
		if err != nil {
			if !errors.Is(err, ErrResidual) {
				t.Fatalf("unexpected error %v", err)
			}
			return
		}
		lo, hi := coeffLimits(tools, in.CIdx)
		for _, v := range out.Levels {
			if int64(v) < lo || int64(v) > hi {
				t.Fatalf("level %d outside %d..%d", v, lo, hi)
			}
		}
	})
}
