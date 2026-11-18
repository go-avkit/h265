package cabac

import "errors"

// The test's own reading of clause 9.3.4.3: a bin-serial decoder that does what
// the text says, one bit at a time, in plain integers. It shares nothing with
// engine.go and reads the second transcription of Tables 9-52 and 9-53
// (spec_reading_test.go), not the production tables. Next to it, the
// informative encoder of 9.3.5 (Figures 9-10 to 9-15), so that streams exist
// that were not produced by the decoder under test.

// refCtx is one context variable as the specification names its parts.
type refCtx struct{ pStateIdx, valMps int }

func (c refCtx) byteForm() uint8 { return uint8(c.pStateIdx<<1 | c.valMps) }

func refCtxOf(b uint8) refCtx { return refCtx{int(b >> 1), int(b & 1)} }

// refDecoder is DecodeDecision, DecodeBypass and DecodeTerminate over a bit
// pointer that moves one bit per read_bits( 1 ).
type refDecoder struct {
	data []byte
	pos  int // bits read
	rng  int // ivlCurrRange
	off  int // ivlOffset
}

var errRefOffset = errors.New("offset 510 or 511")

func (r *refDecoder) readBit() int {
	i := r.pos
	r.pos++
	if i>>3 >= len(r.data) {
		return 0 // reads past the end see zero bits, as Decoder.Init documents
	}
	return int(r.data[i>>3]>>(7-uint(i&7))) & 1
}

func newRefDecoder(data []byte) (*refDecoder, error) {
	r := &refDecoder{data: data, rng: 510}
	for i := 0; i < 9; i++ { // read_bits( 9 ), most significant bit first
		r.off = r.off<<1 | r.readBit()
	}
	if r.off == 510 || r.off == 511 {
		return nil, errRefOffset
	}
	return r, nil
}

func (r *refDecoder) renorm() { // 9.3.4.3.3
	for r.rng < 256 {
		r.rng <<= 1
		r.off = r.off<<1 | r.readBit()
	}
}

func (r *refDecoder) decision(c *refCtx) int { // 9.3.4.3.2
	qRangeIdx := (r.rng >> 6) & 3
	lps := int(specRangeTabLps[c.pStateIdx][qRangeIdx])
	r.rng -= lps
	var bin int
	if r.off >= r.rng {
		bin = 1 - c.valMps
		r.off -= r.rng
		r.rng = lps
	} else {
		bin = c.valMps
	}
	if bin == c.valMps { // 9-66
		c.pStateIdx = int(specTransIdxMps[c.pStateIdx])
	} else {
		if c.pStateIdx == 0 {
			c.valMps = 1 - c.valMps
		}
		c.pStateIdx = int(specTransIdxLps[c.pStateIdx])
	}
	r.renorm()
	return bin
}

func (r *refDecoder) bypass() int { // 9.3.4.3.4
	r.off = r.off<<1 | r.readBit()
	if r.off >= r.rng {
		r.off -= r.rng
		return 1
	}
	return 0
}

func (r *refDecoder) bypassBits(k int) uint32 {
	var v uint32
	for i := 0; i < k; i++ {
		v = v<<1 | uint32(r.bypass())
	}
	return v
}

// align is 9.3.4.3.6; ok is false when the stream would then break the shall of
// 9.3.4.3.4 (the first bypass bin leaves ivlOffset at or above ivlCurrRange).
func (r *refDecoder) align() (ok bool) {
	if r.off >= 256 {
		return false
	}
	r.rng = 256
	return true
}

func (r *refDecoder) terminate() int { // 9.3.4.3.5
	r.rng -= 2
	if r.off >= r.rng {
		return 1
	}
	r.renorm()
	return 0
}

// refEncoder is 9.3.5: EncodeDecision, EncodeBypass, EncodeTerminate with
// EncodeFlush, over RenormE (Figure 9-11) and PutBit (Figure 9-12).
type refEncoder struct {
	low, rng, outstanding int
	first                 bool
	bits                  []byte // one bit per element
}

func newRefEncoder() *refEncoder { return &refEncoder{rng: 510, first: true} }

func (e *refEncoder) putBit(b int) {
	if e.first {
		e.first = false
	} else {
		e.bits = append(e.bits, byte(b))
	}
	for ; e.outstanding > 0; e.outstanding-- {
		e.bits = append(e.bits, byte(1-b))
	}
}

func (e *refEncoder) renorm() {
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

func (e *refEncoder) decision(c *refCtx, bin int) {
	qRangeIdx := (e.rng >> 6) & 3
	lps := int(specRangeTabLps[c.pStateIdx][qRangeIdx])
	e.rng -= lps
	if bin != c.valMps {
		e.low += e.rng
		e.rng = lps
		if c.pStateIdx == 0 {
			c.valMps = 1 - c.valMps
		}
		c.pStateIdx = int(specTransIdxLps[c.pStateIdx])
	} else {
		c.pStateIdx = int(specTransIdxMps[c.pStateIdx])
	}
	e.renorm()
}

func (e *refEncoder) bypass(bin int) {
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

// terminate encodes a bin before termination; a 1 flushes (Figure 9-15), the
// last bit written being the 1 that the decoder reads last.
func (e *refEncoder) terminate(bin int) {
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

// packed is the written bits as bytes, the last byte padded with zero bits.
func (e *refEncoder) packed() []byte {
	out := make([]byte, (len(e.bits)+7)/8)
	for i, b := range e.bits {
		out[i>>3] |= b << (7 - uint(i&7))
	}
	return out
}
