package cabac

import "math"

// The binarisations of clause 9.3.3 that slice data needs first, decoded
// straight from bypass bins (and, for cu_qp_delta_abs, from decisions).
//
// Values are 32-bit. A bin string that no 32-bit value has (a unary prefix of
// ones that never stops, an escape longer than 31 bits, a sum that overflows)
// is refused with ErrBinarization, so a hostile stream cannot make a decode run
// on, wrap a shift, or return a wrapped number.

// FixedLengthBypass is the fixed-length binarization (9.3.3.5) of a value of
// k bits, k at most 32, all bins bypass: BypassBits.
func (d *Decoder) FixedLengthBypass(k uint) uint32 { return d.BypassBits(k) }

// unaryBypass counts bypass bins equal to 1, stopping at the first 0 (which is
// consumed) or after max ones (when no 0 follows): the prefix of 9.3.3.2.
func (d *Decoder) unaryBypass(max uint) uint {
	var ones uint
	for ones < max && d.Bypass() == 1 {
		ones++
	}
	return ones
}

// TruncatedUnaryBypass is the truncated Rice binarization (9.3.3.2) with
// cRiceParam 0 and the given cMax, all bins bypass: mpm_idx, and the bypass
// bins of merge_idx and ref_idx.
func (d *Decoder) TruncatedUnaryBypass(cMax uint32) uint32 {
	return uint32(d.unaryBypass(uint(cMax)))
}

// ExpGolombBypass is the k-th order Exp-Golomb binarization (9.3.3.3), all bins
// bypass: abs_mvd_minus2 (k 1), the suffix of cu_qp_delta_abs (k 0).
func (d *Decoder) ExpGolombBypass(k uint) (uint32, error) {
	if k > 31 {
		return 0, ErrBinarization
	}
	var v uint32
	for d.Bypass() == 1 {
		if k == 31 {
			return 0, ErrBinarization
		}
		v += 1 << k // equation 9-13: absV -= 1 << k, k++
		k++
	}
	return v + d.BypassBits(k), nil // below 2^32: v < 2^k and the bits < 2^k, k <= 31
}

// LimitedExpGolombBypass is the limited k-th order Exp-Golomb binarization
// (9.3.3.4, equations 9-14 to 9-16) of extended_precision_processing_flag, all
// bins bypass. log2TransformRange is equation 9-14's value, 15 to 22.
func (d *Decoder) LimitedExpGolombBypass(riceParam, log2TransformRange uint) (uint32, error) {
	if log2TransformRange > 28 || riceParam > 31 {
		return 0, ErrBinarization
	}
	maxPrefixExtensionLength := 28 - log2TransformRange // 9-15
	var pre uint
	for pre < maxPrefixExtensionLength && d.Bypass() == 1 {
		pre++
	}
	escapeLength := log2TransformRange
	if pre != maxPrefixExtensionLength {
		escapeLength = pre + riceParam // the 0 that ended the prefix was consumed above
	}
	if escapeLength > 31 {
		return 0, ErrBinarization
	}
	v := (uint64(1)<<pre-1)<<riceParam + uint64(d.BypassBits(escapeLength))
	if v > math.MaxUint32 {
		return 0, ErrBinarization
	}
	return uint32(v), nil
}

// RiceConfig is what 9.3.3.11 needs to know about the transform block whose
// coefficients are being decoded.
type RiceConfig struct {
	// Persistent is persistent_rice_adaptation_enabled_flag.
	Persistent bool
	// SbType is the sbType of equations 9-20 and 9-21; see SbType. Used only
	// when Persistent is set.
	SbType int
	// Extended is extended_precision_processing_flag; Log2TransformRange is
	// then equation 9-14's log2TransformRange (15 to 22) for the component.
	Extended           bool
	Log2TransformRange uint
}

// SbType is sbType, equations 9-20 and 9-21: 2 for luma, 0 for chroma, plus 1
// when transform_skip_flag or cu_transquant_bypass_flag is set.
func SbType(cIdx int, transformSkipOrBypass bool) int {
	t := 0
	if cIdx == 0 {
		t = 2
	}
	if transformSkipOrBypass {
		t++
	}
	return t
}

// SubBlock is the state that clause 9.3.3.11 carries from one
// coeff_abs_level_remaining of a sub-block to the next: cLastAbsLevel,
// cLastRiceParam and whether the next one is the first.
type SubBlock struct {
	cfg       RiceConfig
	lastAbs   uint32
	lastRice  uint32
	firstCall bool
}

// StartSubBlock begins the coeff_abs_level_remaining of a new sub-block scan
// index i: cLastAbsLevel is 0 and cLastRiceParam is initRiceValue (equation
// 9-22: StatCoeff[ sbType ] / 4 when Persistent, else 0).
func (m *Model) StartSubBlock(cfg RiceConfig) SubBlock {
	sb := SubBlock{cfg: cfg, firstCall: true}
	if cfg.Persistent {
		sb.lastRice = uint32(m.StatCoeff[cfg.SbType]) / 4
	}
	return sb
}

// CoeffAbsLevelRemaining decodes one coeff_abs_level_remaining (9.3.3.11) of
// the sub-block, given its baseLevel, and updates StatCoeff (9-23) when it is
// the sub-block's first and Persistent is set.
func (d *Decoder) CoeffAbsLevelRemaining(m *Model, sb *SubBlock, baseLevel uint32) (uint32, error) {
	rice := sb.lastRice
	if uint64(sb.lastAbs) > 3<<sb.lastRice { // 9-24, 9-25
		rice++
	}
	if !sb.cfg.Persistent && rice > 4 {
		rice = 4
	}
	if rice > 30 {
		return 0, ErrBinarization
	}
	var v uint64
	if ones := d.unaryBypass(4); ones < 4 { // the TR prefix, cMax 4 << rice
		v = uint64(ones)<<rice + uint64(d.BypassBits(uint(rice)))
	} else {
		var suffix uint32
		var err error
		if sb.cfg.Extended {
			suffix, err = d.LimitedExpGolombBypass(uint(rice)+1, sb.cfg.Log2TransformRange)
		} else {
			suffix, err = d.ExpGolombBypass(uint(rice) + 1)
		}
		if err != nil {
			return 0, err
		}
		v = 4<<rice + uint64(suffix) // 9-26, 9-28
	}
	if v > math.MaxUint32-uint64(baseLevel) {
		return 0, ErrBinarization // cAbsLevel = baseLevel + v must fit as well
	}
	if sb.firstCall && sb.cfg.Persistent { // 9-23
		sc := &m.StatCoeff[sb.cfg.SbType]
		switch {
		case v >= 3<<(*sc/4):
			*sc++
		case 2*v < 1<<(*sc/4) && *sc > 0:
			*sc--
		}
	}
	sb.firstCall = false
	sb.lastAbs = baseLevel + uint32(v)
	sb.lastRice = rice
	return uint32(v), nil
}

// CuQpDeltaAbs decodes cu_qp_delta_abs (9.3.3.10): a prefix of up to five bins
// (TR, cMax 5, cRiceParam 0) whose first bin uses the context CtxCuQpDeltaAbs
// and the next four CtxCuQpDeltaAbs+1 (Table 9-48), then, when the prefix is
// five ones, an Exp-Golomb suffix of order 0 in bypass bins.
func (d *Decoder) CuQpDeltaAbs(m *Model) (uint32, error) {
	var ones uint32
	for ones < 5 && d.Decision(&m.Ctx[CtxCuQpDeltaAbs+min(ones, 1)]) == 1 {
		ones++
	}
	if ones < 5 {
		return ones, nil
	}
	suffix, err := d.ExpGolombBypass(0)
	if err != nil || suffix > math.MaxUint32-5 {
		return 0, ErrBinarization
	}
	return 5 + suffix, nil
}
