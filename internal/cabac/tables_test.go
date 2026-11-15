package cabac

import "testing"

// The production Tables 9-52 and 9-53 were typed from the page layout; the
// second reading was parsed from the text layer. They must agree, and both
// must satisfy what the tables are built to satisfy.

func TestRangeTabLpsAgreesWithTheSecondReading(t *testing.T) {
	if rangeTabLps != specRangeTabLps {
		for s := range rangeTabLps {
			if rangeTabLps[s] != specRangeTabLps[s] {
				t.Errorf("pStateIdx %d: typed %v, parsed %v", s, rangeTabLps[s], specRangeTabLps[s])
			}
		}
	}
}

func TestTransIdxAgreesWithTheSecondReading(t *testing.T) {
	if transIdxLps != specTransIdxLps {
		t.Errorf("transIdxLps: typed %v, parsed %v", transIdxLps, specTransIdxLps)
	}
	if transIdxMps != specTransIdxMps {
		t.Errorf("transIdxMps: typed %v, parsed %v", transIdxMps, specTransIdxMps)
	}
}

// TestTable952Facts checks what the table is built to satisfy, without
// consulting either reading: the LPS range of a state is the same or larger in
// a wider range quarter, falls (never rises) as the state index rises, and
// state 63, the non-adapting state of Table 9-4's note, is 2 everywhere. The
// LPS range is always below the range it is taken from (the smallest range of
// the quarter), and state 0 splits the range about in half (probability 0.5).
func TestTable952Facts(t *testing.T) {
	for s := 0; s < 63; s++ {
		for q := 0; q < 4; q++ {
			if q > 0 && rangeTabLps[s][q] < rangeTabLps[s][q-1] {
				t.Errorf("state %d: LPS range falls from quarter %d to %d", s, q-1, q)
			}
			if s > 0 && rangeTabLps[s][q] > rangeTabLps[s-1][q] {
				t.Errorf("quarter %d: LPS range rises from state %d to %d", q, s-1, s)
			}
		}
	}
	for q := 0; q < 4; q++ {
		if rangeTabLps[63][q] != 2 {
			t.Errorf("state 63, quarter %d: %d, want 2", q, rangeTabLps[63][q])
		}
		// ivlCurrRange of quarter q lies in [256+64q, 256+64q+63].
		lo := 256 + 64*q
		for s := 0; s < 64; s++ {
			if int(rangeTabLps[s][q]) >= lo {
				t.Errorf("state %d quarter %d: LPS range %d not below the range %d", s, q, rangeTabLps[s][q], lo)
			}
		}
	}
	// State 0 is probability 0.5: the LPS range is about half the range.
	for q := 0; q < 4; q++ {
		mid := 256 + 64*q + 32
		if d := mid/2 - int(rangeTabLps[0][q]); d < -16 || d > 16 {
			t.Errorf("state 0 quarter %d: LPS range %d far from half of %d", q, rangeTabLps[0][q], mid)
		}
	}
}

// TestAnMpsRenormalisesAtMostOneBit is the fact Decision relies on to take the
// MPS path with a single conditional shift: for every state and every range of
// 256 to 510, ivlCurrRange - ivlLpsRange is at least 128. (And the LPS range is
// below 256, so the LPS path always shifts at least once.)
func TestAnMpsRenormalisesAtMostOneBit(t *testing.T) {
	for r := 256; r <= 510; r++ {
		for s := 0; s < 64; s++ {
			lps := int(specRangeTabLps[s][(r>>6)&3])
			if r-lps < 128 {
				t.Fatalf("range %d state %d: MPS leaves %d", r, s, r-lps)
			}
			if lps >= 256 {
				t.Fatalf("state %d: LPS range %d", s, lps)
			}
		}
	}
}

func TestTable953Facts(t *testing.T) {
	for s := 0; s < 63; s++ {
		if want := uint8(min(s+1, 62)); transIdxMps[s] != want {
			t.Errorf("transIdxMps[%d] = %d, want %d", s, transIdxMps[s], want)
		}
		if transIdxLps[s] > uint8(s) {
			t.Errorf("transIdxLps[%d] = %d: an LPS must not make the state more skewed", s, transIdxLps[s])
		}
		if s > 0 && transIdxLps[s] < transIdxLps[s-1] {
			t.Errorf("transIdxLps falls from state %d to %d", s-1, s)
		}
	}
	if transIdxMps[62] != 62 || transIdxMps[63] != 63 || transIdxLps[63] != 63 {
		t.Errorf("states 62 and 63 must be absorbing for the MPS, and 63 for the LPS")
	}
}

// TestStepsFoldTheTables recomputes every folded record from equations 9-64
// to 9-66 and the second reading of the tables.
func TestStepsFoldTheTables(t *testing.T) {
	for ctx := 0; ctx < 128; ctx++ {
		for q := 0; q < 4; q++ {
			s := steps[ctx<<2|q]
			c := refCtxOf(uint8(ctx))
			if s.lps != specRangeTabLps[c.pStateIdx][q] {
				t.Errorf("ctx %d q %d: lps %d", ctx, q, s.lps)
			}
			mps := c
			mps.pStateIdx = int(specTransIdxMps[c.pStateIdx])
			lps := c
			if c.pStateIdx == 0 {
				lps.valMps = 1 - c.valMps
			}
			lps.pStateIdx = int(specTransIdxLps[c.pStateIdx])
			if s.nextMps != mps.byteForm() || s.nextLps != lps.byteForm() {
				t.Errorf("ctx %d q %d: next %d/%d, want %d/%d", ctx, q, s.nextMps, s.nextLps, mps.byteForm(), lps.byteForm())
			}
		}
	}
}
