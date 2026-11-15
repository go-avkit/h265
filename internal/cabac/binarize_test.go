package cabac

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// The encoder side of the binarisations of 9.3.3, written from the equations as
// they stand (bin strings out of values), over the informative encoder of 9.3.5.
// The decoder under test reads the inverse; a stream written here and read back
// must give the values that went in.

func refEncEGk(v uint64, k uint) []int { // 9-13
	var bins []int
	absV := v
	for {
		if absV >= 1<<k {
			bins = append(bins, 1)
			absV -= 1 << k
			k++
		} else {
			bins = append(bins, 0)
			for k > 0 {
				k--
				bins = append(bins, int(absV>>k)&1)
			}
			return bins
		}
	}
}

func refEncLimitedEGk(symbolVal uint64, riceParam, log2TransformRange uint) []int { // 9-14 to 9-16
	var bins []int
	maxPre := 28 - log2TransformRange
	codeValue := symbolVal >> riceParam
	var pre uint
	for pre < maxPre && codeValue > (2<<pre)-2 {
		pre++
		bins = append(bins, 1)
	}
	var escapeLength uint
	if pre == maxPre {
		escapeLength = log2TransformRange
	} else {
		escapeLength = pre + riceParam
		bins = append(bins, 0)
	}
	symbolVal -= ((1 << pre) - 1) << riceParam
	for escapeLength > 0 {
		escapeLength--
		bins = append(bins, int(symbolVal>>escapeLength)&1)
	}
	return bins
}

// refRice is the encoder's copy of the state 9.3.3.11 carries.
type refRice struct {
	cfg        RiceConfig
	statCoeff  [4]uint8
	lastAbs    uint64
	lastRice   uint64
	notFirst   bool
	riceParams []uint64 // cRiceParam of each value, for the tests that look
}

func (s *refRice) start() {
	s.lastAbs, s.notFirst = 0, false
	s.lastRice = 0
	if s.cfg.Persistent {
		s.lastRice = uint64(s.statCoeff[s.cfg.SbType]) / 4 // 9-22
	}
}

func (s *refRice) encode(value, baseLevel uint64) []int {
	var cRice uint64
	if !s.cfg.Persistent { // 9-24
		inc := uint64(0)
		if s.lastAbs > 3*(1<<s.lastRice) {
			inc = 1
		}
		cRice = min(s.lastRice+inc, 4)
	} else { // 9-25
		cRice = s.lastRice
		if s.lastAbs > 3*(1<<s.lastRice) {
			cRice++
		}
	}
	s.riceParams = append(s.riceParams, cRice)
	cMax := uint64(4) << cRice
	prefixVal := min(cMax, value) // 9-27
	var bins []int
	if prefixVal < cMax { // TR prefix, then the FL suffix of 9.3.3.2
		for i := uint64(0); i < prefixVal>>cRice; i++ {
			bins = append(bins, 1)
		}
		bins = append(bins, 0)
		for b := int(cRice) - 1; b >= 0; b-- {
			bins = append(bins, int(prefixVal>>uint(b))&1)
		}
	} else {
		bins = []int{1, 1, 1, 1}
		if s.cfg.Extended {
			bins = append(bins, refEncLimitedEGk(value-cMax, uint(cRice)+1, s.cfg.Log2TransformRange)...)
		} else {
			bins = append(bins, refEncEGk(value-cMax, uint(cRice)+1)...)
		}
	}
	if s.cfg.Persistent && !s.notFirst { // 9-23
		sc := &s.statCoeff[s.cfg.SbType]
		if value >= 3<<(*sc/4) {
			*sc++
		} else if 2*value < 1<<(*sc/4) && *sc > 0 {
			*sc--
		}
	}
	s.notFirst = true
	s.lastAbs, s.lastRice = baseLevel+value, cRice
	return bins
}

func bypassAll(e *refEncoder, bins []int) {
	for _, b := range bins {
		e.bypass(b)
	}
}

func bitString(bins []int) string {
	s := make([]byte, len(bins))
	for i, b := range bins {
		s[i] = byte('0' + b)
	}
	return string(s)
}

// TestBinStringsByHand: bin strings worked out on paper from 9-13 and 9-16.
func TestBinStringsByHand(t *testing.T) {
	for _, c := range []struct {
		v    uint64
		k    uint
		want string
	}{
		{0, 0, "0"}, {1, 0, "100"}, {2, 0, "101"}, {3, 0, "11000"}, {6, 0, "11011"}, {7, 0, "1110000"},
		{0, 1, "00"}, {1, 1, "01"}, {2, 1, "1000"}, {5, 1, "1011"},
		{0, 3, "0000"}, {9, 3, "100001"},
	} {
		if got := bitString(refEncEGk(c.v, c.k)); got != c.want {
			t.Errorf("EG%d(%d) = %s, want %s", c.k, c.v, got, c.want)
		}
	}
	// Limited EGk, log2TransformRange 15 (so at most 13 prefix ones), riceParam
	// 2: value 0 is "0" and two escape bits; value 12 has codeValue 3, which
	// exceeds (2<<0)-2 = 0 and (2<<1)-2 = 2: two prefix 1s, then "0", then
	// escapeLength 2+2 bits of 12-((1<<2)-1)<<2 = 0: "0000".
	if got := bitString(refEncLimitedEGk(0, 2, 15)); got != "000" {
		t.Errorf("limited(0) = %s", got)
	}
	if got := bitString(refEncLimitedEGk(12, 2, 15)); got != "11"+"0"+"0000" {
		t.Errorf("limited(12) = %s", got)
	}
}

func decodeStream(t *testing.T, enc *refEncoder) *Decoder {
	t.Helper()
	enc.terminate(1)
	d := new(Decoder)
	if err := d.Init(enc.packed()); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExpGolombRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(21, 22))
	for iter := 0; iter < 300; iter++ {
		enc := newRefEncoder()
		type item struct {
			v uint64
			k uint
		}
		var items []item
		for i := 0; i < 100; i++ {
			it := item{uint64(rng.Uint32() >> uint(rng.IntN(32))), uint(rng.IntN(5))}
			if rng.IntN(20) == 0 {
				it.v = 1<<31 - 1<<it.k // the largest value of order k that fits
			}
			bypassAll(enc, refEncEGk(it.v, it.k))
			items = append(items, it)
		}
		d := decodeStream(t, enc)
		for _, it := range items {
			got, err := d.ExpGolombBypass(it.k)
			if err != nil || uint64(got) != it.v {
				t.Fatalf("EG%d(%d): got %d, %v", it.k, it.v, got, err)
			}
		}
	}
}

func TestLimitedExpGolombRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(23, 24))
	for iter := 0; iter < 300; iter++ {
		enc := newRefEncoder()
		type item struct {
			v         uint64
			rice, log uint
		}
		var items []item
		for i := 0; i < 100; i++ {
			log := uint(15 + rng.IntN(8))
			it := item{rice: uint(1 + rng.IntN(6)), log: log}
			it.v = uint64(rng.Uint32()) >> uint(32-1-rng.IntN(int(log)))
			bypassAll(enc, refEncLimitedEGk(it.v, it.rice, it.log))
			items = append(items, it)
		}
		d := decodeStream(t, enc)
		for _, it := range items {
			got, err := d.LimitedExpGolombBypass(it.rice, it.log)
			if err != nil || uint64(got) != it.v {
				t.Fatalf("limited(%d, rice %d, range %d): got %d, %v", it.v, it.rice, it.log, got, err)
			}
		}
	}
}

func TestFixedLengthAndTruncatedUnary(t *testing.T) {
	enc := newRefEncoder()
	// FL of 5 bits (cMax 31), then TU cMax 2 of 0, 1, 2, then TU cMax 4 of 4.
	for _, b := range []int{1, 0, 1, 1, 0} {
		enc.bypass(b)
	}
	bypassAll(enc, []int{0, 1, 0, 1, 1, 1, 1, 1, 1}) // "0", "10", "11" (cMax 2: no 0 after two ones), "1111"
	d := decodeStream(t, enc)
	if got := d.FixedLengthBypass(5); got != 0b10110 {
		t.Errorf("FL = %b", got)
	}
	for _, want := range []uint32{0, 1, 2} {
		if got := d.TruncatedUnaryBypass(2); got != want {
			t.Errorf("TU(2) = %d, want %d", got, want)
		}
	}
	if got := d.TruncatedUnaryBypass(4); got != 4 {
		t.Errorf("TU(4) of four ones = %d", got)
	}
	if got := d.TruncatedUnaryBypass(0); got != 0 { // cMax 0: no bins at all
		t.Errorf("TU(0) = %d", got)
	}
}

func TestCoeffAbsLevelRemainingRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(25, 26))
	for iter := 0; iter < 600; iter++ {
		cfg := RiceConfig{
			Persistent:         rng.IntN(2) == 0,
			SbType:             rng.IntN(4),
			Extended:           rng.IntN(3) == 0,
			Log2TransformRange: uint(15 + rng.IntN(8)),
		}
		var m Model
		for i := range m.StatCoeff {
			m.StatCoeff[i] = uint8(rng.IntN(40))
		}
		es := refRice{cfg: cfg, statCoeff: m.StatCoeff}
		enc := newRefEncoder()
		type item struct{ v, base uint64 }
		var blocks [][]item
		for b := 0; b < 6; b++ {
			var items []item
			es.start()
			for i, n := 0, 1+rng.IntN(16); i < n; i++ {
				limit := 1 << (1 + rng.IntN(15))
				if cfg.Extended {
					limit = 1 << (1 + rng.IntN(int(cfg.Log2TransformRange)-1))
				}
				it := item{uint64(rng.IntN(limit)), uint64(1 + rng.IntN(3))}
				bypassAll(enc, es.encode(it.v, it.base))
				items = append(items, it)
			}
			blocks = append(blocks, items)
		}
		d := decodeStream(t, enc)
		for _, items := range blocks {
			sb := m.StartSubBlock(cfg)
			for _, it := range items {
				got, err := d.CoeffAbsLevelRemaining(&m, &sb, uint32(it.base))
				if err != nil || uint64(got) != it.v {
					t.Fatalf("cfg %+v: coeff_abs_level_remaining %d (base %d): got %d, %v", cfg, it.v, it.base, got, err)
				}
			}
		}
		if m.StatCoeff != es.statCoeff {
			t.Fatalf("cfg %+v: StatCoeff %v, encoder's %v", cfg, m.StatCoeff, es.statCoeff)
		}
	}
}

// TestRiceParameterAdaptation: the sequence of cRiceParam of equation 9-24
// without persistent adaptation (capped at 4) and with it (not capped).
func TestRiceParameterAdaptation(t *testing.T) {
	var m Model
	plain := m.StartSubBlock(RiceConfig{})
	es := refRice{}
	es.start()
	for _, v := range []uint64{0, 3, 100, 100, 100, 100, 100, 1, 0} {
		es.encode(v, 1)
	}
	// Worked from 9-24: the 4th value's lastAbs is 1+3 = 4 > 3*(1<<0), so the
	// next cRiceParam is 1; 101 > 3*2 gives 2, and so on up to the cap of 4.
	want := []uint64{0, 0, 1, 2, 3, 4, 4, 4, 4}
	if !slices.Equal(es.riceParams, want) {
		t.Errorf("cRiceParam sequence %v, want %v", es.riceParams, want)
	}
	// The same through the decoder: the stream of those values decodes to them.
	enc := newRefEncoder()
	es = refRice{}
	es.start()
	for _, v := range []uint64{0, 3, 100, 100, 100, 100, 100, 1, 0} {
		bypassAll(enc, es.encode(v, 1))
	}
	d := decodeStream(t, enc)
	for i, v := range []uint32{0, 3, 100, 100, 100, 100, 100, 1, 0} {
		if got, err := d.CoeffAbsLevelRemaining(&m, &plain, 1); err != nil || got != v {
			t.Fatalf("value %d: got %d, %v", i, got, err)
		}
	}
	// Persistent: StatCoeff 0 -> initRiceValue 0; a first value >= 3 raises it.
	pm := Model{}
	pm.StatCoeff[2] = 8 // initRiceValue 2
	sb := pm.StartSubBlock(RiceConfig{Persistent: true, SbType: 2})
	if sb.lastRice != 2 {
		t.Errorf("initRiceValue = %d, want 2", sb.lastRice)
	}
}

func TestSbType(t *testing.T) {
	for _, c := range []struct {
		cIdx int
		skip bool
		want int
	}{{0, false, 2}, {0, true, 3}, {1, false, 0}, {2, true, 1}} {
		if got := SbType(c.cIdx, c.skip); got != c.want {
			t.Errorf("SbType(%d, %v) = %d, want %d", c.cIdx, c.skip, got, c.want)
		}
	}
}

func TestCuQpDeltaAbsRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewPCG(27, 28))
	for iter := 0; iter < 200; iter++ {
		var m Model
		m.Init(rng.IntN(3), rng.IntN(52))
		rc := [2]refCtx{refCtxOf(m.Ctx[CtxCuQpDeltaAbs]), refCtxOf(m.Ctx[CtxCuQpDeltaAbs+1])}
		enc := newRefEncoder()
		var values []uint32
		for i := 0; i < 60; i++ {
			v := uint32(rng.IntN(9))
			if rng.IntN(6) == 0 {
				v = uint32(rng.IntN(1 << 20))
			}
			prefix := min(v, 5)
			for b := uint32(0); b < min(prefix+1, 5); b++ { // TR, cMax 5: five ones need no 0
				enc.decision(&rc[min(b, 1)], boolInt(b < prefix))
			}
			if v >= 5 {
				bypassAll(enc, refEncEGk(uint64(v-5), 0))
			}
			values = append(values, v)
		}
		d := decodeStream(t, enc)
		for _, v := range values {
			got, err := d.CuQpDeltaAbs(&m)
			if err != nil || got != v {
				t.Fatalf("cu_qp_delta_abs %d: got %d, %v", v, got, err)
			}
		}
		if m.Ctx[CtxCuQpDeltaAbs] != rc[0].byteForm() || m.Ctx[CtxCuQpDeltaAbs+1] != rc[1].byteForm() {
			t.Fatalf("contexts ended at %d,%d, encoder's %d,%d", m.Ctx[CtxCuQpDeltaAbs], m.Ctx[CtxCuQpDeltaAbs+1], rc[0].byteForm(), rc[1].byteForm())
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// The bit-serial reading of the same binarisations, over refDecoder, for
// random data: the engine's value, error and bit pointer must be the
// reference's. An error is expected exactly where the contract of ErrBinarization
// says.

func refEGkDecode(r *refDecoder, k int) (v uint64, endK int) {
	for r.bypass() == 1 {
		if k < 62 {
			v += 1 << uint(k)
		}
		k++
	}
	var escape uint64
	for i := 0; i < k; i++ {
		escape = escape<<1 | uint64(r.bypass()) // the low 64 bits only: a k that large is refused anyway
	}
	return v + escape, k
}

func TestExpGolombAgainstSerialReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(29, 30))
	errs, oks := 0, 0
	for iter := 0; iter < 20000; iter++ {
		data := randomData(rng, 2+rng.IntN(40))
		if iter%3 == 0 { // runs of ones, so that long prefixes happen
			for i := range data[1:] {
				data[1+i] |= byte(rng.Uint32()) | 0xF0
			}
		}
		data[0] &= 0x7F
		k := rng.IntN(32)
		var d Decoder
		d.Init(data)
		r, _ := newRefDecoder(data)
		v, endK := refEGkDecode(r, k)
		got, err := d.ExpGolombBypass(uint(k))
		if endK >= 32 { // the prefix reached order 32: refused
			if err != ErrBinarization {
				t.Fatalf("EG%d of % x: want ErrBinarization, got %d, %v", k, data, got, err)
			}
			errs++
			continue
		}
		oks++
		if err != nil || uint64(got) != v || d.BitPos() != int64(r.pos) {
			t.Fatalf("EG%d of % x: %d, %v, pos %d; reference %d, pos %d", k, data, got, err, d.BitPos(), v, r.pos)
		}
	}
	if errs == 0 || oks == 0 {
		t.Errorf("the random data reached only one outcome: %d refused, %d decoded", errs, oks)
	}
	var d Decoder
	d.Init(make([]byte, 8))
	if _, err := d.ExpGolombBypass(32); err != ErrBinarization {
		t.Errorf("EG32: %v", err)
	}
}

func TestLimitedExpGolombAgainstSerialReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(31, 32))
	errs, oks := 0, 0
	for iter := 0; iter < 20000; iter++ {
		data := randomData(rng, 4+rng.IntN(30))
		if iter%2 == 0 {
			for i := range data[1:] {
				data[1+i] |= 0xF0
			}
		}
		data[0] &= 0x7F
		rice, log := uint(1+rng.IntN(31)), uint(15+rng.IntN(8))
		var d Decoder
		d.Init(data)
		r, _ := newRefDecoder(data)
		// 9-14 to 9-16, read forwards.
		maxPre := 28 - log
		var pre uint
		for pre < maxPre && r.bypass() == 1 {
			pre++
		}
		escapeLength := log
		if pre != maxPre {
			escapeLength = pre + rice
		}
		got, err := d.LimitedExpGolombBypass(rice, log)
		if escapeLength > 31 {
			if err != ErrBinarization {
				t.Fatalf("limited(rice %d, range %d) of % x: want ErrBinarization, got %d, %v", rice, log, data, got, err)
			}
			errs++
			continue
		}
		var bitsV uint64
		for i := uint(0); i < escapeLength; i++ {
			bitsV = bitsV<<1 | uint64(r.bypass())
		}
		v := (1<<pre-1)<<rice + bitsV
		if v > math.MaxUint32 {
			if err != ErrBinarization {
				t.Fatalf("limited value %d: want ErrBinarization, got %d, %v", v, got, err)
			}
			errs++
			continue
		}
		oks++
		if err != nil || uint64(got) != v || d.BitPos() != int64(r.pos) {
			t.Fatalf("limited(rice %d, range %d) of % x: %d, %v, pos %d; reference %d, pos %d", rice, log, data, got, err, d.BitPos(), v, r.pos)
		}
	}
	if errs == 0 || oks == 0 {
		t.Errorf("only one outcome: %d refused, %d decoded", errs, oks)
	}
	var d Decoder
	d.Init(make([]byte, 8))
	if _, err := d.LimitedExpGolombBypass(1, 29); err != ErrBinarization {
		t.Errorf("log2TransformRange 29: %v", err)
	}
	if _, err := d.LimitedExpGolombBypass(32, 15); err != ErrBinarization {
		t.Errorf("riceParam 32: %v", err)
	}
}

// TestCoeffAbsLevelRemainingAgainstSerialReference reads 9.3.3.11 forwards, a
// bin at a time, from random data and compares value, error and bit pointer.
func TestCoeffAbsLevelRemainingAgainstSerialReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(33, 34))
	errs, oks := 0, 0
	for iter := 0; iter < 20000; iter++ {
		data := randomData(rng, 4+rng.IntN(30))
		switch iter % 3 {
		case 0:
			for i := range data[1:] {
				data[1+i] |= byte(rng.Uint32()) | 0xE0
			}
		case 1:
			for i := range data[1:] {
				data[1+i] &= byte(rng.Uint32())
			}
		}
		data[0] &= 0x7F
		cfg := RiceConfig{
			Persistent: rng.IntN(2) == 0, SbType: rng.IntN(4),
			Extended: rng.IntN(2) == 0, Log2TransformRange: uint(15 + rng.IntN(8)),
		}
		var m Model
		for i := range m.StatCoeff {
			m.StatCoeff[i] = uint8(rng.IntN(130))
		}
		es := refRice{cfg: cfg, statCoeff: m.StatCoeff}
		es.start()
		sb := m.StartSubBlock(cfg)
		var d Decoder
		d.Init(data)
		r, _ := newRefDecoder(data)
		for n := 0; n < 5; n++ {
			base := uint64(1 + rng.IntN(3))
			// The reference: cRiceParam from the encoder-side state (same
			// equations), then the bins read forwards.
			cRice := es.lastRice
			if es.lastAbs > 3*(1<<es.lastRice) {
				cRice++
			}
			if !cfg.Persistent {
				cRice = min(cRice, 4)
			}
			wantErr := cRice > 30
			var v uint64
			if !wantErr {
				ones := 0
				for ones < 4 && r.bypass() == 1 {
					ones++
				}
				if ones < 4 {
					v = uint64(ones)<<cRice + uint64(r.bypassBits(int(cRice)))
				} else {
					var suffix uint64
					if cfg.Extended {
						maxPre := 28 - cfg.Log2TransformRange
						var pre uint
						for pre < maxPre && r.bypass() == 1 {
							pre++
						}
						esc := cfg.Log2TransformRange
						if pre != maxPre {
							esc = pre + uint(cRice) + 1
						}
						if esc > 31 {
							wantErr = true
						} else {
							suffix = (1<<pre-1)<<(cRice+1) + uint64(r.bypassBits(int(esc)))
						}
					} else {
						k := int(cRice) + 1
						for !wantErr && r.bypass() == 1 {
							if k == 31 {
								wantErr = true
								break
							}
							suffix += 1 << uint(k)
							k++
						}
						if !wantErr {
							suffix += uint64(r.bypassBits(k))
						}
					}
					v = 4<<cRice + suffix
				}
				if !wantErr && v > math.MaxUint32-base {
					wantErr = true
				}
			}
			got, err := d.CoeffAbsLevelRemaining(&m, &sb, uint32(base))
			if wantErr {
				if err != ErrBinarization {
					t.Fatalf("cfg %+v of % x: want ErrBinarization, got %d, %v", cfg, data, got, err)
				}
				errs++
				break
			}
			oks++
			if err != nil || uint64(got) != v || d.BitPos() != int64(r.pos) {
				t.Fatalf("cfg %+v of % x: %d, %v, pos %d; reference %d, pos %d", cfg, data, got, err, d.BitPos(), v, r.pos)
			}
			es.encode(v, base) // advances the reference state (its bins are discarded)
			if m.StatCoeff != es.statCoeff {
				t.Fatalf("cfg %+v: StatCoeff %v, reference %v", cfg, m.StatCoeff, es.statCoeff)
			}
		}
	}
	if errs == 0 || oks == 0 {
		t.Errorf("only one outcome: %d refused, %d decoded", errs, oks)
	}
}

// TestHandBuiltRefusals puts bins that no 32-bit value has through each decoder.
func TestHandBuiltRefusals(t *testing.T) {
	ones := func(n int) *refEncoder {
		e := newRefEncoder()
		for i := 0; i < n; i++ {
			e.bypass(1)
		}
		return e
	}
	d := decodeStream(t, ones(200))
	if _, err := d.ExpGolombBypass(0); err != ErrBinarization {
		t.Errorf("EG0 of 200 ones: %v", err)
	}
	// A cu_qp_delta_abs whose Exp-Golomb suffix never ends.
	var m Model
	m.Init(0, 30)
	rc := [2]refCtx{refCtxOf(m.Ctx[CtxCuQpDeltaAbs]), refCtxOf(m.Ctx[CtxCuQpDeltaAbs+1])}
	e := newRefEncoder()
	for b := 0; b < 5; b++ {
		e.decision(&rc[min(b, 1)], 1)
	}
	for i := 0; i < 100; i++ {
		e.bypass(1)
	}
	d = decodeStream(t, e)
	if _, err := d.CuQpDeltaAbs(&m); err != ErrBinarization {
		t.Errorf("cu_qp_delta_abs with an endless suffix: %v", err)
	}
	// A coeff_abs_level_remaining that cannot be added to its baseLevel.
	cfg := RiceConfig{}
	d = decodeStream(t, func() *refEncoder {
		e := newRefEncoder()
		es := refRice{cfg: cfg}
		es.start()
		bypassAll(e, es.encode(math.MaxUint32-2, 0))
		return e
	}())
	sb := m.StartSubBlock(cfg)
	if _, err := d.CoeffAbsLevelRemaining(&m, &sb, 3); err != ErrBinarization {
		t.Errorf("value + baseLevel past 2^32: %v", err)
	}
	// A Rice parameter past 30, reachable only through a hostile StatCoeff.
	d = decodeStream(t, newRefEncoder())
	sb = SubBlock{cfg: RiceConfig{Persistent: true}, lastRice: 31}
	if _, err := d.CoeffAbsLevelRemaining(&m, &sb, 1); err != ErrBinarization {
		t.Errorf("cRiceParam 31: %v", err)
	}
	// A limited Exp-Golomb suffix inside a coefficient that overflows.
	d = decodeStream(t, ones(200))
	sb = m.StartSubBlock(RiceConfig{Extended: true, Log2TransformRange: 29})
	if _, err := d.CoeffAbsLevelRemaining(&m, &sb, 1); err != ErrBinarization {
		t.Errorf("limited suffix with log2TransformRange 29: %v", err)
	}
}
