package cabac

// Normative tables of the arithmetic decoding engine, clause 9.3.4.3.2.
//
// Provenance. Every number below is a normative value, transcribed by hand
// from ITU-T H.265 (V11) (01/2026): rangeTabLps is Table 9-52 (page 237 of the
// document, printed in two halves of 32 states), transIdxLps and transIdxMps
// are Table 9-53 (page 238). Nothing here was derived from, or compared with,
// another decoder. tables_test.go reads the same two tables a second time
// (spec_reading_test.go, extracted mechanically from the document's text layer
// by a different route) and checks facts the tables must satisfy.

// rangeTabLps is Table 9-52: the LPS range for pStateIdx (row) and
// qRangeIdx = (ivlCurrRange >> 6) & 3 (column), equation 9-65.
var rangeTabLps = [64][4]uint8{
	{128, 176, 208, 240},
	{128, 167, 197, 227},
	{128, 158, 187, 216},
	{123, 150, 178, 205},
	{116, 142, 169, 195},
	{111, 135, 160, 185},
	{105, 128, 152, 175},
	{100, 122, 144, 166},
	{95, 116, 137, 158},
	{90, 110, 130, 150},
	{85, 104, 123, 142},
	{81, 99, 117, 135},
	{77, 94, 111, 128},
	{73, 89, 105, 122},
	{69, 85, 100, 116},
	{66, 80, 95, 110},
	{62, 76, 90, 104},
	{59, 72, 86, 99},
	{56, 69, 81, 94},
	{53, 65, 77, 89},
	{51, 62, 73, 85},
	{48, 59, 69, 80},
	{46, 56, 66, 76},
	{43, 53, 63, 72},
	{41, 50, 59, 69},
	{39, 48, 56, 65},
	{37, 45, 54, 62},
	{35, 43, 51, 59},
	{33, 41, 48, 56},
	{32, 39, 46, 53},
	{30, 37, 43, 50},
	{29, 35, 41, 48},
	{27, 33, 39, 45},
	{26, 31, 37, 43},
	{24, 30, 35, 41},
	{23, 28, 33, 39},
	{22, 27, 32, 37},
	{21, 26, 30, 35},
	{20, 24, 29, 33},
	{19, 23, 27, 31},
	{18, 22, 26, 30},
	{17, 21, 25, 28},
	{16, 20, 23, 27},
	{15, 19, 22, 25},
	{14, 18, 21, 24},
	{14, 17, 20, 23},
	{13, 16, 19, 22},
	{12, 15, 18, 21},
	{12, 14, 17, 20},
	{11, 14, 16, 19},
	{11, 13, 15, 18},
	{10, 12, 15, 17},
	{10, 12, 14, 16},
	{9, 11, 13, 15},
	{9, 11, 12, 14},
	{8, 10, 12, 14},
	{8, 9, 11, 13},
	{7, 9, 11, 12},
	{7, 9, 10, 12},
	{7, 8, 10, 11},
	{6, 8, 9, 11},
	{6, 7, 9, 10},
	{6, 7, 8, 9},
	{2, 2, 2, 2},
}

// transIdxLps is the transIdxLps row of Table 9-53: the pStateIdx that follows
// a decoded LPS (equation 9-66).
var transIdxLps = [64]uint8{
	0, 0, 1, 2, 2, 4, 4, 5, 6, 7, 8, 9, 9, 11, 11, 12,
	13, 13, 15, 15, 16, 16, 18, 18, 19, 19, 21, 21, 22, 22, 23, 24,
	24, 25, 26, 26, 27, 27, 28, 29, 29, 30, 30, 30, 31, 32, 32, 33,
	33, 33, 34, 34, 35, 35, 35, 36, 36, 36, 37, 37, 37, 38, 38, 63,
}

// transIdxMps is the transIdxMps row of Table 9-53: the pStateIdx that follows
// a decoded MPS (equation 9-66).
var transIdxMps = [64]uint8{
	1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32,
	33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48,
	49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 62, 63,
}

// A context variable is one byte, pStateIdx<<1 | valMps. step folds Tables 9-52
// and 9-53 and the valMps flip of equation 9-66 into one record per
// (context byte, qRangeIdx), so that a decision is one load: the LPS range, and
// the context byte that follows an MPS and an LPS.
type step struct {
	lps     uint8
	nextMps uint8
	nextLps uint8
}

// steps is indexed by ctx<<2 | qRangeIdx.
var steps [128 * 4]step

func init() {
	for ctx := 0; ctx < 128; ctx++ {
		state, mps := ctx>>1, uint8(ctx&1)
		nextMps := transIdxMps[state]<<1 | mps
		lpsMps := mps
		if state == 0 {
			lpsMps = 1 - mps // equation 9-66: the MPS flips at state 0
		}
		nextLps := transIdxLps[state]<<1 | lpsMps
		for q := 0; q < 4; q++ {
			steps[ctx<<2|q] = step{rangeTabLps[state][q], nextMps, nextLps}
		}
	}
}
