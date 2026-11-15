// Package cabactest is the arithmetic encoder of clause 9.3.5 (informative), for
// tests that need a bitstream the decoder did not produce: a syntax writer drives
// it with the same bins and contexts the decoder will ask for, and the decoder
// must read the bins back. It is written from the flowcharts of Figures 9-10 to
// 9-15 and shares no code with the decoder but the tables of 9.3.4.3.2.
package cabactest

import "github.com/go-avkit/h265/internal/cabac"

// Encoder is the arithmetic encoding engine: ivlLow, ivlCurrRange, firstBitFlag
// and bitsOutstanding of 9.3.5.2.
type Encoder struct {
	low, rng, outstanding int
	first                 bool
	bits                  []byte // one bit per element
}

// New starts an encoder (InitEncoder, 9.3.5.2).
func New() *Encoder { return &Encoder{rng: 510, first: true} }

func (e *Encoder) putBit(b int) {
	if e.first {
		e.first = false
	} else {
		e.bits = append(e.bits, byte(b))
	}
	for ; e.outstanding > 0; e.outstanding-- {
		e.bits = append(e.bits, byte(1-b))
	}
}

func (e *Encoder) renorm() {
	for e.rng < 256 {
		switch {
		case e.low < 256:
			e.putBit(0)
		case e.low >= 512:
			e.low -= 512
			e.putBit(1)
		default:
			e.low -= 256
			e.outstanding++
		}
		e.rng <<= 1
		e.low <<= 1
	}
}

// Decision is EncodeDecision (9.3.5.3) on the context variable *c, in the byte
// form of cabac.Model: pStateIdx<<1 | valMps.
func (e *Encoder) Decision(c *uint8, bin int) {
	state, mps := int(*c>>1), int(*c&1)
	lps := cabac.RangeTabLps(state, (e.rng>>6)&3)
	e.rng -= lps
	if bin != mps {
		e.low += e.rng
		e.rng = lps
		if state == 0 {
			mps = 1 - mps
		}
		state = cabac.TransIdxLps(state)
	} else {
		state = cabac.TransIdxMps(state)
	}
	*c = uint8(state<<1 | mps)
	e.renorm()
}

// Bypass is EncodeBypass (9.3.5.5).
func (e *Encoder) Bypass(bin int) {
	e.low <<= 1
	if bin != 0 {
		e.low += e.rng
	}
	switch {
	case e.low >= 1024:
		e.putBit(1)
		e.low -= 1024
	case e.low < 512:
		e.putBit(0)
	default:
		e.low -= 512
		e.outstanding++
	}
}

// BypassBits writes the k low bits of v as bypass bins, the most significant
// first.
func (e *Encoder) BypassBits(v uint32, k int) {
	for i := k - 1; i >= 0; i-- {
		e.Bypass(int(v>>uint(i)) & 1)
	}
}

// Terminate is EncodeTerminate (9.3.5.6); a 1 flushes (Figure 9-15), the last bit
// written being the 1 the decoder reads last.
func (e *Encoder) Terminate(bin int) {
	e.rng -= 2
	if bin == 0 {
		e.renorm()
		return
	}
	e.low += e.rng
	e.rng = 2
	e.renorm()
	e.putBit((e.low >> 9) & 1)
	e.bits = append(e.bits, byte((e.low>>8)&1), 1) // WriteBits( ( ( low >> 7 ) & 3 ) | 1, 2 )
}

// BitCount is how many bits have been written.
func (e *Encoder) BitCount() int { return len(e.bits) }

// Bytes is the written bits, the last byte padded with zero bits.
func (e *Encoder) Bytes() []byte {
	out := make([]byte, (len(e.bits)+7)/8)
	for i, b := range e.bits {
		out[i>>3] |= b << (7 - uint(i&7))
	}
	return out
}
