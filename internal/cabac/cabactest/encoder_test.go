package cabactest

import (
	"math/rand/v2"
	"testing"

	"github.com/go-avkit/h265/internal/cabac"
)

// TestRoundTrip: what the encoder writes, the decoder reads back, bin for bin,
// and its bit pointer after the terminating 1 is the number of bits written.
func TestRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 200; iter++ {
		e := New()
		var ctx, start [8]uint8
		for i := range ctx {
			ctx[i] = uint8(rng.IntN(63)<<1 | rng.IntN(2))
		}
		start = ctx
		type item struct {
			kind, i int
			v       uint32
			k       int
		}
		var items []item
		for n := rng.IntN(1500); n > 0; n-- {
			var it item
			switch rng.IntN(4) {
			case 0:
				it = item{kind: 0, i: rng.IntN(8), v: uint32(rng.IntN(2))}
				e.Decision(&ctx[it.i], int(it.v))
			case 1:
				it = item{kind: 1, v: uint32(rng.IntN(2))}
				e.Bypass(int(it.v))
			case 2:
				it = item{kind: 2, k: rng.IntN(33)}
				it.v = uint32(rng.Uint64() >> uint(64-it.k))
				if it.k == 0 {
					it.v = 0
				}
				e.BypassBits(it.v, it.k)
			default:
				it = item{kind: 3}
				e.Terminate(0)
			}
			items = append(items, it)
		}
		e.Terminate(1)
		bits := e.BitCount()
		var d cabac.Decoder
		if err := d.Init(e.Bytes()); err != nil {
			t.Fatal(err)
		}
		dctx := start
		for _, it := range items {
			var got uint32
			switch it.kind {
			case 0:
				got = d.Decision(&dctx[it.i])
			case 1:
				got = d.Bypass()
			case 2:
				got = d.BypassBits(uint(it.k))
			default:
				got = d.Terminate()
			}
			if got != it.v {
				t.Fatalf("iter %d: %+v decoded %d", iter, it, got)
			}
		}
		if dctx != ctx || d.Terminate() != 1 || d.BitPos() != int64(bits) {
			t.Fatalf("iter %d: contexts %v/%v, bit pointer %d of %d", iter, dctx, ctx, d.BitPos(), bits)
		}
	}
}
