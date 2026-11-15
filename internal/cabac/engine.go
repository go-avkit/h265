// Package cabac is the arithmetic decoding engine of clause 9.3 of ITU-T H.265
// (V11) (01/2026): initialisation of the engine (9.3.2.6), of the context
// variables (9.3.2.2), the decoding of a decision (9.3.4.3.2), of a bypass bin
// (9.3.4.3.4) and before termination (9.3.4.3.5), and the binarisations the
// slice data needs first (9.3.3).
//
// Clean room: written from the specification alone. The specification is the
// only oracle; engine_test.go keeps a bin-by-bin transcription of 9.3.4.3 as
// the test's own reading, and the engine's output must equal it exactly.
//
// The engine keeps the specification's two registers, ivlCurrRange (rng) and
// ivlOffset, but not one bit at a time. ivlOffset sits at the top of a 64-bit
// window, followed by n bits that the specification would read later:
//
//	val = ivlOffset << n | next n bits of the slice data
//
// A comparison of ivlOffset with a range r is then a comparison of val with
// r << n, since the n lower bits are below any multiple of 1 << n. Taking a
// bit into ivlOffset (the shift of the renormalisation, the first step of a
// bypass bin) leaves val alone and lowers n by one. The window is refilled
// several bytes at a time, so a decision costs one table load, one comparison
// and, when the range drops below 256, one count of leading zeros.
//
// The input is the slice segment data with the emulation prevention bytes
// already removed (bitstream.Unescape): the refill never tests for 00 00 03.
package cabac

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// ErrOffset is returned by Init when the first nine bits are 510 or 511, which
// clause 9.3.2.6 forbids ("The bitstream shall not contain data that result in
// a value of ivlOffset being equal to 510 or 511"). Every later offset is then
// below the range, as the specification requires, whatever the bits are, which
// is also what lets a run of bypass bins be one division.
var ErrOffset = errors.New("cabac: initial offset is 510 or 511")

// ErrBinarization is returned when a bin string is longer than any value the
// syntax element can have (a unary prefix that never ends, an Exp-Golomb order
// that no 32-bit value needs).
var ErrBinarization = errors.New("cabac: bin string out of range")

// Decoder is the arithmetic decoding engine over one run of slice segment data.
// The zero value is not ready: call Init.
type Decoder struct {
	data []byte
	pos  int    // bytes taken into val, counting the zero bytes read past the end
	val  uint64 // ivlOffset << n | the next n bits
	n    uint   // bits below ivlOffset in val
	rng  uint32 // ivlCurrRange, 256..510 between bins
}

// ErrBypassAlignment is returned by Start when cabac_bypass_alignment_enabled_flag
// is set: the alignment of 9.3.4.3.6 (ivlCurrRange forced to 256) can leave
// ivlOffset at or above ivlCurrRange, which the one-division bypass run does not
// allow for, so the stream is refused rather than decoded wrongly. The flag must
// be 1 in the High Throughput 4:4:4 16 Intra profile of Annex A.
var ErrBypassAlignment = errors.New("cabac: cabac_bypass_alignment_enabled_flag is not supported")

// Config is what the SPS tells the engine about the entropy coding.
type Config struct {
	// BypassAlignment is cabac_bypass_alignment_enabled_flag
	// (sps_range_extension).
	BypassAlignment bool
}

// Start is Init for a stream whose SPS says how the entropy coding is set up:
// it refuses what the engine cannot decode exactly.
func (d *Decoder) Start(data []byte, cfg Config) error {
	if cfg.BypassAlignment {
		return ErrBypassAlignment
	}
	return d.Init(data)
}

// Init is the initialisation of the arithmetic decoding engine, clause 9.3.2.6:
// ivlCurrRange is 510 and ivlOffset the first nine bits of data. Data that ends
// early reads as zero bits and is reported by Overrun.
func (d *Decoder) Init(data []byte) error {
	d.data = data
	var first [2]byte
	copy(first[:], data)
	d.val = uint64(first[0])<<8 | uint64(first[1]) // 16 bits: 9 of offset, 7 ahead
	d.n = 7
	d.pos = 2
	d.rng = 510
	d.refill()
	if d.val>>d.n >= 510 {
		return ErrOffset
	}
	return nil
}

// refill takes whole bytes into val until n is at least 48; val never needs
// more than 9+55 bits. It is called only with n below 32.
func (d *Decoder) refill() {
	if d.pos+8 <= len(d.data) {
		m := (55 - d.n) / 8 // whole bytes that fit: n < 48 so 1 to 8
		w := binary.BigEndian.Uint64(d.data[d.pos:]) >> (64 - 8*m)
		d.val = d.val<<(8*m) | w
		d.pos += int(m)
		d.n += 8 * m
		return
	}
	for d.n < 48 {
		var b byte
		if d.pos < len(d.data) {
			b = d.data[d.pos]
		}
		d.val = d.val<<8 | uint64(b)
		d.pos++
		d.n += 8
	}
}

// BitPos is the number of bits of the slice segment data that the engine has
// consumed in the specification's sense: nine at Init and one for each bit
// shifted into ivlOffset since. After a terminating bin equal to 1 it is where
// the data that follows (pcm_alignment_zero_bit, pcm samples) starts.
func (d *Decoder) BitPos() int64 { return int64(d.pos)*8 - int64(d.n) }

// Overrun reports that the engine has consumed more bits than the data holds,
// that is, that the data was cut short.
func (d *Decoder) Overrun() bool { return d.BitPos() > int64(len(d.data))*8 }

// Decision is DecodeDecision, clause 9.3.4.3.2, on the context variable *c
// (pStateIdx<<1 | valMps), including its state transition (9.3.4.3.2.2) and
// the renormalisation (9.3.4.3.3).
func (d *Decoder) Decision(c *uint8) uint32 {
	ctx := *c
	s := &steps[uint32(ctx&127)<<2|(d.rng>>6)&3] // equations 9-64, 9-65
	rng := d.rng - uint32(s.lps)
	scaled := uint64(rng) << d.n
	bin := uint32(ctx & 1)
	if d.val >= scaled { // ivlOffset >= ivlCurrRange: the LPS
		d.val -= scaled
		rng = uint32(s.lps) // 2 to 240: renormalised by a count of leading zeros
		sh := uint(bits.LeadingZeros32(rng) - 23)
		rng <<= sh
		d.n -= sh
		bin ^= 1
		*c = s.nextLps
	} else {
		// After an MPS ivlCurrRange is at least 128 (Table 9-52 never takes more
		// than half of it), so renormalisation is at most one bit, and a branch
		// the predictor gets right keeps the next bin's range off the critical
		// path of this one's.
		if rng < 256 {
			rng <<= 1
			d.n--
		}
		*c = s.nextMps
	}
	d.rng = rng
	if d.n < 8 {
		d.refill()
	}
	return bin
}

// Bypass is DecodeBypass, clause 9.3.4.3.4.
func (d *Decoder) Bypass() uint32 {
	d.n--
	scaled := uint64(d.rng) << d.n
	_, borrow := bits.Sub64(d.val, scaled, 0) // 1 when val < scaled: the bin is 0
	d.val -= scaled & (borrow - 1)            // a mask of ones when the bin is 1
	if d.n < 8 {
		d.refill()
	}
	return uint32(borrow ^ 1)
}

// BypassBits decodes k bypass bins, k at most 32, and returns them as a number,
// the first bin the most significant (the fixed-length binarization, 9.3.3.5).
//
// Each bypass bin appends one bit to ivlOffset and subtracts ivlCurrRange when
// ivlOffset has reached it, which is one step of binary long division by an
// unchanged ivlCurrRange; ivlOffset stays below ivlCurrRange, so every step
// yields one digit. k steps are therefore one division: ivlOffset with the next
// k bits appended, divided by ivlCurrRange, has the k bins as its quotient and
// the new ivlOffset as its remainder.
func (d *Decoder) BypassBits(k uint) uint32 {
	if k == 0 {
		return 0
	}
	if d.n < k {
		d.refill()
	}
	d.n -= k
	v := d.val >> d.n // ivlOffset << k | the next k bits: 9+k bits
	var q uint32
	if k <= 23 { // 9+23 bits fit 32: a 32-bit division is the cheaper one
		q = uint32(v) / d.rng
	} else {
		q = uint32(v / uint64(d.rng))
	}
	d.val -= uint64(q) * uint64(d.rng) << d.n
	if d.n < 8 {
		d.refill()
	}
	return q
}

// Terminate is DecodeTerminate, clause 9.3.4.3.5: the bin of
// end_of_slice_segment_flag, end_of_subset_one_bit and pcm_flag. After a 1
// nothing is renormalised and BitPos is the position the specification's bit
// pointer has. Decoding has then ended (ivlOffset may have reached the range,
// which every other operation relies on it not doing): the next bin can only
// come after Init on the data that follows, as after pcm samples.
func (d *Decoder) Terminate() uint32 {
	d.rng -= 2
	if d.val >= uint64(d.rng)<<d.n {
		return 1
	}
	if d.rng < 256 {
		d.rng <<= 1
		d.n--
		if d.n < 8 {
			d.refill()
		}
	}
	return 0
}
