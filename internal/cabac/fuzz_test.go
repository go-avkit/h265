package cabac

import (
	"errors"
	"math/rand/v2"
	"testing"
)

// FuzzEngine drives the engine and the bin-serial reference with the same data
// and an operation script taken from the second argument; any difference in a
// bin, a context variable, the offset, the range or the bit pointer fails.
func FuzzEngine(f *testing.F) {
	f.Add([]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x11, 0x22}, []byte{0, 1, 2, 3, 130, 126, 7, 11, 255})
	f.Add([]byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, []byte{130, 130, 130, 4, 8, 12, 16, 250})
	f.Add([]byte{0xfe, 0xff, 0x00}, []byte{3, 3, 3})
	f.Add([]byte{}, []byte{0, 1, 2, 3})
	f.Fuzz(func(t *testing.T, data, ops []byte) {
		rng := rand.New(rand.NewPCG(uint64(len(data)), uint64(len(ops))))
		p := newPair(t, data, rng)
		if p == nil {
			return
		}
		for _, o := range ops {
			switch o & 3 {
			case 0:
				p.decision(int(o>>2) % nTestCtx)
			case 1:
				p.bypass()
			case 2:
				p.bypassBits(uint(o>>2) % 33)
			default:
				if p.terminate() == 1 {
					return
				}
			}
		}
	})
}

// FuzzBinarizations decodes every binarisation from arbitrary data: no panic,
// no error but ErrBinarization, and the bit pointer only moves forward.
func FuzzBinarizations(f *testing.F) {
	f.Add([]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x11, 0x22}, uint8(0))
	f.Add([]byte{0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, uint8(0xff))
	f.Add([]byte{0x00, 0x01, 0x02}, uint8(0x55))
	f.Fuzz(func(t *testing.T, data []byte, sel uint8) {
		var d Decoder
		if d.Init(data) != nil {
			return
		}
		var m Model
		m.Init(int(sel)%3, int(sel))
		cfg := RiceConfig{Persistent: sel&1 != 0, SbType: int(sel>>1) & 3, Extended: sel&8 != 0, Log2TransformRange: 15 + uint(sel>>4)%8}
		sb := m.StartSubBlock(cfg)
		last := d.BitPos()
		for i := 0; i < 64; i++ {
			var err error
			switch (int(sel) + i) % 6 {
			case 0:
				_, err = d.CoeffAbsLevelRemaining(&m, &sb, uint32(1+i%3))
			case 1:
				_, err = d.CuQpDeltaAbs(&m)
			case 2:
				_, err = d.ExpGolombBypass(uint(i % 33))
			case 3:
				_, err = d.LimitedExpGolombBypass(uint(i%34), uint(10+i%24))
			case 4:
				d.TruncatedUnaryBypass(uint32(i))
			default:
				d.FixedLengthBypass(uint(i % 33))
			}
			if err != nil && !errors.Is(err, ErrBinarization) {
				t.Fatalf("unexpected error %v", err)
			}
			if d.BitPos() < last {
				t.Fatalf("bit pointer moved back: %d after %d", d.BitPos(), last)
			}
			last = d.BitPos()
			if err != nil {
				return
			}
		}
	})
}
