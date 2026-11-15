package cabac

import (
	"math/rand/v2"
	"testing"
)

const nTestCtx = 16

// pair is the engine under test and the bin-serial reference reading the same
// data, with the same context variables in both forms.
type pair struct {
	t   *testing.T
	d   *Decoder
	r   *refDecoder
	ctx [nTestCtx]uint8
	rc  [nTestCtx]refCtx
}

func newPair(t *testing.T, data []byte, rng *rand.Rand) *pair {
	t.Helper()
	p := &pair{t: t, d: new(Decoder)}
	errD := p.d.Init(data)
	r, errR := newRefDecoder(data)
	if (errD != nil) != (errR != nil) {
		t.Fatalf("Init error %v, reference %v, data % x", errD, errR, data)
	}
	if errD != nil {
		return nil
	}
	p.r = r
	for i := range p.ctx {
		p.rc[i] = refCtx{rng.IntN(63), rng.IntN(2)}
		p.ctx[i] = p.rc[i].byteForm()
	}
	p.agree("Init")
	return p
}

// agree checks everything observable: the bit pointer, the overrun, every
// context variable.
func (p *pair) agree(what string) {
	p.t.Helper()
	if got, want := p.d.BitPos(), int64(p.r.pos); got != want {
		p.t.Fatalf("%s: BitPos %d, reference %d", what, got, want)
	}
	if got, want := p.d.Overrun(), p.r.pos > 8*len(p.r.data); got != want {
		p.t.Fatalf("%s: Overrun %v, reference %v", what, got, want)
	}
	for i := range p.ctx {
		if p.ctx[i] != p.rc[i].byteForm() {
			p.t.Fatalf("%s: context %d is %d, reference %d", what, i, p.ctx[i], p.rc[i].byteForm())
		}
	}
	if p.d.rng != uint32(p.r.rng) {
		p.t.Fatalf("%s: ivlCurrRange %d, reference %d", what, p.d.rng, p.r.rng)
	}
	if got := p.d.val >> p.d.n; got != uint64(p.r.off) {
		p.t.Fatalf("%s: ivlOffset %d, reference %d", what, got, p.r.off)
	}
}

func (p *pair) decision(i int) {
	p.t.Helper()
	got, want := p.d.Decision(&p.ctx[i]), uint32(p.r.decision(&p.rc[i]))
	if got != want {
		p.t.Fatalf("Decision(ctx %d): %d, reference %d", i, got, want)
	}
	p.agree("decision")
}

func (p *pair) bypass() {
	p.t.Helper()
	if got, want := p.d.Bypass(), uint32(p.r.bypass()); got != want {
		p.t.Fatalf("Bypass: %d, reference %d", got, want)
	}
	p.agree("bypass")
}

func (p *pair) bypassBits(k uint) {
	p.t.Helper()
	v0, n0, rg0, o0, pos0 := p.d.val, p.d.n, p.d.rng, p.r.off, p.r.pos
	if got, want := p.d.BypassBits(k), p.r.bypassBits(int(k)); got != want {
		p.t.Fatalf("BypassBits(%d): %#x, reference %#x; before: val %#x n %d rng %d, ref off %d pos %d", k, got, want, v0, n0, rg0, o0, pos0)
	}
	p.agree("bypassBits")
}

// terminate returns the bin; after a 1 the ranges are not compared (the
// specification renormalises nothing, and neither does the engine).
func (p *pair) terminate() uint32 {
	p.t.Helper()
	got, want := p.d.Terminate(), uint32(p.r.terminate())
	if got != want {
		p.t.Fatalf("Terminate: %d, reference %d", got, want)
	}
	if got == 1 {
		if p.d.BitPos() != int64(p.r.pos) {
			p.t.Fatalf("after terminating bin: BitPos %d, reference %d", p.d.BitPos(), p.r.pos)
		}
		return 1
	}
	p.agree("decision") // renormalised: ranges must agree again
	return 0
}

// randomOps applies n random operations to the pair, stopping at a terminating
// bin equal to 1.
func (p *pair) randomOps(rng *rand.Rand, n int) {
	for i := 0; i < n; i++ {
		switch x := rng.IntN(100); {
		case x < 55:
			p.decision(rng.IntN(nTestCtx))
		case x < 70:
			p.bypass()
		case x < 92:
			p.bypassBits(uint(rng.IntN(33)))
		default:
			if p.terminate() == 1 {
				return
			}
		}
	}
}

func randomData(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Uint32())
	}
	return b
}

func TestAgainstBitSerialReferenceRandom(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for iter := 0; iter < 4000; iter++ {
		// Lengths around the refill's whole-word fast path and the byte tail.
		data := randomData(rng, rng.IntN(90))
		if p := newPair(t, data, rng); p != nil {
			p.randomOps(rng, 300)
		}
	}
}

// TestEveryFirstOffsetAndEveryContext: for each of the 510 initial offsets and
// each of the 128 context bytes, the first operation (every kind of it), then a
// few random ones.
func TestEveryFirstOffsetAndEveryContext(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for off := 0; off < 510; off++ {
		for kind := 0; kind < 4; kind++ {
			for c := 0; c < 128; c++ {
				if kind != 0 && c > 0 {
					break
				}
				data := append([]byte{byte(off >> 1), byte(off&1) << 7}, randomData(rng, 12)...)
				data[1] |= data[2] & 0x7f // the 7 bits after the offset are random too
				p := newPair(t, data, rng)
				if p == nil {
					t.Fatalf("offset %d refused", off)
				}
				switch kind {
				case 0:
					p.rc[0], p.ctx[0] = refCtxOf(uint8(c)), uint8(c)
					p.decision(0)
				case 1:
					if p.terminate() == 1 {
						continue // decoding ends at a terminating bin of 1
					}
				case 2:
					p.bypass()
				default:
					p.bypassBits(uint(1 + off%32))
				}
				p.randomOps(rng, 6)
			}
		}
	}
}

func TestInitRefusesOffsets510And511(t *testing.T) {
	for _, data := range [][]byte{{0xFF, 0x80}, {0xFF, 0x00}, {0xFF, 0xFF, 0xFF}} {
		var d Decoder
		if err := d.Init(data); err != ErrOffset {
			t.Errorf("Init(% x) = %v, want ErrOffset", data, err)
		}
		if _, err := newRefDecoder(data); err == nil {
			t.Errorf("the reference accepted % x", data)
		}
	}
	var d Decoder
	if err := d.Init([]byte{0xFE, 0x80}); err != nil { // 509
		t.Errorf("Init of offset 509: %v", err)
	}
}

// TestAllRangesAllRunLengths puts the engine in every ivlCurrRange of 256 to
// 510 with random offsets below it, and decodes a bypass run of every length 0
// to 32, then more operations.
func TestAllRangesAllRunLengths(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	for r := 256; r <= 510; r++ {
		for k := uint(0); k <= 32; k++ {
			for rep := 0; rep < 4; rep++ {
				data := randomData(rng, 40)
				data[0] &= 0x7F // a valid first nine bits; the state is replaced below
				p := newPair(t, data, rng)
				off := rng.IntN(r)
				if rep == 0 {
					off = r - 1 // the largest offset the range allows
				} else if rep == 1 {
					off = 0
				}
				p.d.rng = uint32(r)
				p.d.val = uint64(off)<<p.d.n | p.d.val&(1<<p.d.n-1)
				p.r.rng, p.r.off = r, off
				p.bypassBits(k)
				p.randomOps(rng, 8)
			}
		}
	}
}

func TestAdversarialStreams(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	fill := func(n int, b byte) []byte {
		out := make([]byte, n)
		for i := range out {
			out[i] = b
		}
		return out
	}
	for _, data := range [][]byte{
		nil, {}, {0}, {0x55}, {0, 0}, fill(64, 0x00), fill(64, 0xAA), fill(64, 0x55), fill(64, 0xFE),
		append([]byte{0xFE}, fill(63, 0xFF)...), // offset 509, then all ones
		append([]byte{0xFD}, fill(63, 0xFF)...),
	} {
		p := newPair(t, data, rng)
		if p == nil {
			continue
		}
		// A long run of 32-bin bypass runs, then decisions on the most skewed
		// contexts (state 62 has the smallest LPS range: the widest renormalisation).
		for i := 0; i < 40; i++ {
			p.bypassBits(32)
		}
		for i := range p.ctx {
			p.rc[i] = refCtx{62 - i%3, i % 2}
			p.ctx[i] = p.rc[i].byteForm()
		}
		for i := 0; i < 400; i++ {
			p.decision(i % nTestCtx)
			if i%50 == 0 {
				p.bypassBits(uint(i % 33))
			}
		}
		p.randomOps(rng, 200)
	}
}

// TestLongBypassRunsOfRandomData: 200000 bypass bins through BypassBits against
// the bit-serial reference, with the run lengths varying.
func TestLongBypassRunsOfRandomData(t *testing.T) {
	rng := rand.New(rand.NewPCG(9, 10))
	data := randomData(rng, 40000)
	data[0] &= 0x7F
	p := newPair(t, data, rng)
	for i := 0; i < 20000; i++ {
		p.bypassBits(uint(rng.IntN(33)))
	}
}

// TestRefillBoundaries: data of every length from 0 to 40 bytes, decoded to and
// past its end.
func TestRefillBoundaries(t *testing.T) {
	rng := rand.New(rand.NewPCG(11, 12))
	for n := 0; n <= 40; n++ {
		for rep := 0; rep < 20; rep++ {
			data := randomData(rng, n)
			if len(data) > 0 {
				data[0] &= 0x7F
			}
			p := newPair(t, data, rng)
			for i := 0; i < 80; i++ {
				switch rng.IntN(3) {
				case 0:
					p.decision(rng.IntN(nTestCtx))
				case 1:
					p.bypass()
				default:
					p.bypassBits(uint(rng.IntN(33)))
				}
			}
		}
	}
}

// TestRoundTripAgainstTheInformativeEncoder: streams written by the encoder of
// 9.3.5 (not by anything in this package) decode to the bins that went in, and
// the bit pointer after the terminating bin is exactly the number of bits the
// encoder wrote, the last of them the 1 that the decoder reads last.
func TestRoundTripAgainstTheInformativeEncoder(t *testing.T) {
	rng := rand.New(rand.NewPCG(13, 14))
	type item struct {
		kind  int // 0 decision, 1 bypass, 2 run, 3 terminate(0)
		ctx   int
		k     uint
		value uint32
	}
	for iter := 0; iter < 300; iter++ {
		enc := newRefEncoder()
		var rc, start [nTestCtx]refCtx
		for i := range rc {
			rc[i] = refCtx{rng.IntN(63), rng.IntN(2)}
		}
		start = rc
		var items []item
		for i, n := 0, rng.IntN(2000); i < n; i++ {
			var it item
			switch x := rng.IntN(100); {
			case x < 55:
				it = item{kind: 0, ctx: rng.IntN(nTestCtx)}
				// Mostly the MPS, so the states climb and the ranges stay long.
				it.value = uint32(rc[it.ctx].valMps)
				if rng.IntN(8) == 0 {
					it.value ^= 1
				}
				enc.decision(&rc[it.ctx], int(it.value))
			case x < 70:
				it = item{kind: 1, value: uint32(rng.IntN(2))}
				enc.bypass(int(it.value))
			case x < 95:
				it = item{kind: 2, k: uint(rng.IntN(33))}
				it.value = uint32(rng.Uint64() >> (64 - it.k))
				if it.k == 0 {
					it.value = 0
				}
				for b := int(it.k) - 1; b >= 0; b-- {
					enc.bypass(int(it.value>>uint(b)) & 1)
				}
			default:
				it = item{kind: 3}
				enc.terminate(0)
			}
			items = append(items, it)
		}
		enc.terminate(1)
		data := enc.packed()

		var d Decoder
		if err := d.Init(data); err != nil {
			t.Fatalf("iter %d: Init: %v", iter, err)
		}
		var ctx [nTestCtx]uint8
		for i := range ctx {
			ctx[i] = start[i].byteForm()
		}
		for n, it := range items {
			var got uint32
			switch it.kind {
			case 0:
				got = d.Decision(&ctx[it.ctx])
			case 1:
				got = d.Bypass()
			case 2:
				got = d.BypassBits(it.k)
			default:
				got = d.Terminate()
			}
			if got != it.value {
				t.Fatalf("iter %d item %d (%+v): decoded %#x", iter, n, it, got)
			}
		}
		for i := range ctx {
			if ctx[i] != rc[i].byteForm() {
				t.Fatalf("iter %d: context %d ends at %d, encoder's at %d", iter, i, ctx[i], rc[i].byteForm())
			}
		}
		if d.Terminate() != 1 {
			t.Fatalf("iter %d: the terminating 1 decoded as 0", iter)
		}
		if got := d.BitPos(); got != int64(len(enc.bits)) {
			t.Fatalf("iter %d: BitPos after termination %d, the encoder wrote %d bits", iter, got, len(enc.bits))
		}
		if d.Overrun() {
			t.Fatalf("iter %d: overrun", iter)
		}
	}
}
