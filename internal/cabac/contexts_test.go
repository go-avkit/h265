package cabac

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// table94 is the test's own reading of Table 9-4 (pages 207 and 208), in the
// shape the document prints it: for each syntax element, its table and the
// ctxIdx of each initType as text ("0..2", "0..3 12", "-" for none).
var table94 = []struct {
	name, table string
	types       [3]string
}{
	{"sao_merge_left_flag, sao_merge_up_flag", "Table 9-5", [3]string{"0", "1", "2"}},
	{"sao_type_idx_luma, sao_type_idx_chroma", "Table 9-6", [3]string{"0", "1", "2"}},
	{"split_cu_flag", "Table 9-7", [3]string{"0..2", "3..5", "6..8"}},
	{"cu_transquant_bypass_flag", "Table 9-8", [3]string{"0", "1", "2"}},
	{"cu_skip_flag", "Table 9-9", [3]string{"-", "0..2", "3..5"}},
	{"palette_mode_flag", "Table 9-38", [3]string{"0", "1", "2"}},
	{"pred_mode_flag", "Table 9-10", [3]string{"-", "0", "1"}},
	{"part_mode", "Table 9-11", [3]string{"0", "1..4", "5..8"}},
	{"prev_intra_luma_pred_flag", "Table 9-12", [3]string{"0", "1", "2"}},
	{"intra_chroma_pred_mode", "Table 9-13", [3]string{"0", "1", "2"}},
	{"rqt_root_cbf", "Table 9-14", [3]string{"-", "0", "1"}},
	{"merge_flag", "Table 9-15", [3]string{"-", "0", "1"}},
	{"merge_idx", "Table 9-16", [3]string{"-", "0", "1"}},
	{"inter_pred_idc", "Table 9-17", [3]string{"-", "0..4", "5..9"}},
	{"ref_idx_l0, ref_idx_l1", "Table 9-18", [3]string{"-", "0..1", "2..3"}},
	{"mvp_l0_flag, mvp_l1_flag", "Table 9-19", [3]string{"-", "0", "1"}},
	{"split_transform_flag", "Table 9-20", [3]string{"0..2", "3..5", "6..8"}},
	{"cbf_luma", "Table 9-21", [3]string{"0..1", "2..3", "4..5"}},
	{"cbf_cb, cbf_cr", "Table 9-22", [3]string{"0..3 12", "4..7 13", "8..11 14"}},
	{"abs_mvd_greater0_flag", "Table 9-23", [3]string{"-", "0", "2"}},
	{"abs_mvd_greater1_flag", "Table 9-23", [3]string{"-", "1", "3"}},
	{"tu_residual_act_flag", "Table 9-39", [3]string{"0", "1", "2"}},
	{"log2_res_scale_abs_plus1", "Table 9-36", [3]string{"0..7", "8..15", "16..23"}},
	{"res_scale_sign_flag", "Table 9-37", [3]string{"0..1", "2..3", "4..5"}},
	{"transform_skip_flag[ ][ ][ 0 ]", "Table 9-25", [3]string{"0", "1", "2"}},
	{"transform_skip_flag[ ][ ][ 1 ], [ 2 ]", "Table 9-25", [3]string{"3", "4", "5"}},
	{"explicit_rdpcm_flag[ ][ ][ 0 ]", "Table 9-32", [3]string{"-", "0", "1"}},
	{"explicit_rdpcm_flag[ ][ ][ 1 ], [ 2 ]", "Table 9-32", [3]string{"-", "2", "3"}},
	{"explicit_rdpcm_dir_flag[ ][ ][ 0 ]", "Table 9-33", [3]string{"-", "0", "1"}},
	{"explicit_rdpcm_dir_flag[ ][ ][ 1 ], [ 2 ]", "Table 9-33", [3]string{"-", "2", "3"}},
	{"last_sig_coeff_x_prefix", "Table 9-26", [3]string{"0..17", "18..35", "36..53"}},
	{"last_sig_coeff_y_prefix", "Table 9-27", [3]string{"0..17", "18..35", "36..53"}},
	{"coded_sub_block_flag", "Table 9-28", [3]string{"0..3", "4..7", "8..11"}},
	{"sig_coeff_flag", "Table 9-29", [3]string{"0..41 126..127", "42..83 128..129", "84..125 130..131"}},
	{"coeff_abs_level_greater1_flag", "Table 9-30", [3]string{"0..23", "24..47", "48..71"}},
	{"coeff_abs_level_greater2_flag", "Table 9-31", [3]string{"0..5", "6..11", "12..17"}},
	{"palette_run_prefix", "Table 9-40", [3]string{"0..7", "8..15", "16..23"}},
	{"copy_above_palette_indices_flag, copy_above_indices_for_final_run_flag", "Table 9-41", [3]string{"0", "1", "2"}},
	{"palette_transpose_flag", "Table 9-42", [3]string{"0", "1", "2"}},
	{"cu_qp_delta_abs", "Table 9-24", [3]string{"0..1", "2..3", "4..5"}},
	{"cu_chroma_qp_offset_flag", "Table 9-34", [3]string{"0", "1", "2"}},
	{"cu_chroma_qp_offset_idx", "Table 9-35", [3]string{"0", "1", "2"}},
}

// ctxIdxList reads "0..3 12" as [0 1 2 3 12].
func ctxIdxList(t *testing.T, s string) []int {
	t.Helper()
	var out []int
	if s == "-" {
		return nil
	}
	for _, f := range strings.Fields(s) {
		lo, hi, isRange := strings.Cut(f, "..")
		a, err := strconv.Atoi(lo)
		if err != nil {
			t.Fatalf("bad ctxIdx %q", s)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil {
				t.Fatalf("bad ctxIdx %q", s)
			}
		}
		for i := a; i <= b; i++ {
			out = append(out, i)
		}
	}
	return out
}

func spanList(spans []span) []int {
	var out []int
	for _, s := range spans {
		for i := 0; i < s.n; i++ {
			out = append(out, s.first+i)
		}
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestEveryElementIsTable94(t *testing.T) {
	if len(elements) != len(table94) {
		t.Fatalf("%d elements, Table 9-4 reading has %d rows", len(elements), len(table94))
	}
	for i, row := range table94 {
		e := elements[i]
		if e.name != row.name || e.table != row.table {
			t.Errorf("row %d: element %q (%s), Table 9-4 says %q (%s)", i, e.name, e.table, row.name, row.table)
		}
		for it := 0; it < 3; it++ {
			got, want := spanList(e.spans[it]), ctxIdxList(t, row.types[it])
			if !equalInts(got, want) {
				t.Errorf("%s, initType %d: ctxIdx %v, Table 9-4 says %v", row.name, it, got, want)
			}
		}
	}
}

// TestSpansTileEachTable: the ctxIdx that Table 9-4 gives the elements of one
// table, over the three initTypes, cover the table exactly once (each element's
// ctxIdx within one initType are distinct, and across the elements sharing a
// table nothing is listed twice and nothing is left out), and the table has
// exactly as many initValues as the largest ctxIdx plus one.
func TestSpansTileEachTable(t *testing.T) {
	seen := map[string]map[int]string{}
	for _, e := range elements {
		if seen[e.table] == nil {
			seen[e.table] = map[int]string{}
		}
		for it := 0; it < 3; it++ {
			for _, c := range spanList(e.spans[it]) {
				if prev, dup := seen[e.table][c]; dup {
					t.Errorf("%s: ctxIdx %d is listed for %q and for %q", e.table, c, prev, e.name)
				}
				seen[e.table][c] = e.name
			}
		}
	}
	for table, byIdx := range seen {
		values, ok := specInitValue[table]
		if !ok {
			t.Errorf("%s: no second reading", table)
			continue
		}
		if len(values) != len(byIdx) {
			t.Errorf("%s: %d initValues, Table 9-4 lists %d ctxIdx", table, len(values), len(byIdx))
		}
		for c := range values {
			if _, listed := byIdx[c]; !listed {
				t.Errorf("%s: ctxIdx %d is in no row of Table 9-4", table, c)
			}
		}
	}
	for table := range specInitValue {
		if _, ok := seen[table]; !ok {
			t.Errorf("%s has initValues and no row of Table 9-4", table)
		}
	}
}

// TestInitValuesAgreeWithTheSecondReading: the typed arrays equal the parsed
// ones, table by table, and every context of every initType holds the value
// Table 9-4 and the parsed table send to it.
func TestInitValuesAgreeWithTheSecondReading(t *testing.T) {
	for _, e := range elements {
		want := specInitValue[e.table]
		if len(e.init) != len(want) {
			t.Errorf("%s (%s): %d values typed, %d parsed", e.name, e.table, len(e.init), len(want))
			continue
		}
		for i := range want {
			if e.init[i] != want[i] {
				t.Errorf("%s (%s): ctxIdx %d typed %d, parsed %d", e.name, e.table, i, e.init[i], want[i])
			}
		}
	}
	for it := 0; it < 3; it++ {
		for _, row := range table94 {
			var e element
			for _, c := range elements {
				if c.name == row.name {
					e = c
				}
			}
			idx := ctxIdxList(t, row.types[it])
			for k := 0; k < e.n; k++ {
				want := uint8(unused)
				if k < len(idx) {
					want = specInitValue[row.table][idx[k]]
				}
				if got := initValues[it][e.base+k]; got != want {
					t.Errorf("%s initType %d ctxInc %d: initValue %d, want %d", row.name, it, k, got, want)
				}
			}
		}
	}
}

// TestLayoutIsContiguous: the runs of the elements, in order, are back to back
// from 0 to NumContexts, each as long as its longest initType.
func TestLayoutIsContiguous(t *testing.T) {
	next := 0
	for _, e := range elements {
		if e.base != next {
			t.Errorf("%s starts at %d, previous run ended at %d", e.name, e.base, next)
		}
		longest := 0
		for it := 0; it < 3; it++ {
			longest = max(longest, len(spanList(e.spans[it])))
		}
		if e.n != longest {
			t.Errorf("%s: run of %d, longest initType has %d", e.name, e.n, longest)
		}
		next += e.n
	}
	if next != NumContexts {
		t.Errorf("runs end at %d, NumContexts is %d", next, NumContexts)
	}
}

// TestWhichElementsHaveNoIntraContexts: the syntax elements that occur only in
// P and B slices are exactly those whose initType 0 column is empty in Table 9-4.
func TestWhichElementsHaveNoIntraContexts(t *testing.T) {
	interOnly := map[string]bool{
		"cu_skip_flag": true, "pred_mode_flag": true, "rqt_root_cbf": true, "merge_flag": true,
		"merge_idx": true, "inter_pred_idc": true, "ref_idx_l0, ref_idx_l1": true,
		"mvp_l0_flag, mvp_l1_flag": true, "abs_mvd_greater0_flag": true, "abs_mvd_greater1_flag": true,
		"explicit_rdpcm_flag[ ][ ][ 0 ]": true, "explicit_rdpcm_flag[ ][ ][ 1 ], [ 2 ]": true,
		"explicit_rdpcm_dir_flag[ ][ ][ 0 ]": true, "explicit_rdpcm_dir_flag[ ][ ][ 1 ], [ 2 ]": true,
	}
	for _, e := range elements {
		if got := e.spans[0] == nil; got != interOnly[e.name] {
			t.Errorf("%s: empty initType 0 = %v, want %v", e.name, got, interOnly[e.name])
		}
		if e.spans[1] == nil || e.spans[2] == nil {
			t.Errorf("%s: P and B slices must both have contexts", e.name)
		}
	}
}

func TestInitType(t *testing.T) {
	// Equation 9-7. slice_type: 0 B, 1 P, 2 I.
	for _, c := range []struct {
		sliceType uint8
		flag      bool
		want      int
	}{
		{2, false, 0}, {2, true, 0},
		{1, false, 1}, {1, true, 2},
		{0, false, 2}, {0, true, 1},
	} {
		if got := InitType(c.sliceType, c.flag); got != c.want {
			t.Errorf("InitType(%d, %v) = %d, want %d", c.sliceType, c.flag, got, c.want)
		}
	}
}

// refInit writes equations 9-4 to 9-6 a second time, with floor division in
// floating point for the shift and a table of the two outputs.
func refInit(initValue uint8, sliceQpY int) refCtx {
	slopeIdx, offsetIdx := int(initValue)/16, int(initValue)%16
	m := slopeIdx*5 - 45
	n := offsetIdx*8 - 16
	qp := math.Min(51, math.Max(0, float64(sliceQpY)))
	pre := int(math.Floor(float64(m)*qp/16)) + n
	pre = int(math.Min(126, math.Max(1, float64(pre))))
	if pre <= 63 {
		return refCtx{pStateIdx: 63 - pre, valMps: 0}
	}
	return refCtx{pStateIdx: pre - 64, valMps: 1}
}

func TestModelInitIsEquations94To96(t *testing.T) {
	for it := 0; it < 3; it++ {
		for qp := -10; qp <= 61; qp++ {
			var m Model
			m.StatCoeff = [4]uint8{1, 2, 3, 4}
			m.Init(it, qp)
			for i := range m.Ctx {
				if want := refInit(initValues[it][i], qp).byteForm(); m.Ctx[i] != want {
					t.Fatalf("initType %d QP %d ctx %d (initValue %d): %d, want %d", it, qp, i, initValues[it][i], m.Ctx[i], want)
				}
			}
			if m.StatCoeff != [4]uint8{} {
				t.Fatalf("StatCoeff = %v after Init", m.StatCoeff)
			}
		}
	}
}

// TestModelInitByHand: three cells worked out on paper from equations 9-4 to
// 9-6. initValue 154 has slopeIdx 9 (m = 0) and offsetIdx 10 (n = 64): state 0,
// MPS 1 at every QP. initValue 139 (split_cu_flag, ctxInc 0, initType 0): m =
// -5, n = 72, at QP 26 ( -130 >> 4 ) + 72 = 63: state 0, MPS 0. initValue 63
// (intra_chroma_pred_mode, initType 0): m = -30, n = 104; QP 0 gives 104:
// state 40, MPS 1; QP 51 gives ( -1530 >> 4 ) + 104 = 8: state 55, MPS 0.
func TestModelInitByHand(t *testing.T) {
	var m Model
	m.Init(0, 26)
	if got := m.Ctx[CtxCUTransquantBypass]; got != 1 {
		t.Errorf("initValue 154: %d, want state 0 MPS 1 = 1", got)
	}
	if got := m.Ctx[CtxSplitCU]; got != 0 {
		t.Errorf("initValue 139 at QP 26: %d, want state 0 MPS 0 = 0", got)
	}
	m.Init(0, 0)
	if got := m.Ctx[CtxIntraChromaPredMode]; got != 40<<1|1 {
		t.Errorf("initValue 63 at QP 0: %d, want %d", got, 40<<1|1)
	}
	m.Init(0, 51)
	if got := m.Ctx[CtxIntraChromaPredMode]; got != 55<<1 {
		t.Errorf("initValue 63 at QP 51: %d, want %d", got, 55<<1)
	}
	m.Init(0, 99) // SliceQpY is clipped to 51 (equation 9-6)
	if got := m.Ctx[CtxIntraChromaPredMode]; got != 55<<1 {
		t.Errorf("QP 99: %d, want %d", got, 55<<1)
	}
}

// TestStateOfAContextNeverReaches63: preCtxState is 1 to 126, so pStateIdx is
// at most 62; state 63 is the terminate bin's, which has no context here.
func TestNoInitialisedContextIsState63(t *testing.T) {
	for it := 0; it < 3; it++ {
		for qp := 0; qp <= 51; qp++ {
			var m Model
			m.Init(it, qp)
			for i, c := range m.Ctx {
				if c>>1 > 62 {
					t.Fatalf("ctx %d at QP %d: state %d", i, qp, c>>1)
				}
			}
		}
	}
}

// TestStoreAndSynchroniseAreAssignments: 9.3.2.4 and 9.3.2.5 store the whole
// Model and later restore it; copying a Model must copy every context and
// StatCoeff, and nothing may alias.
func TestStoreAndSynchroniseAreAssignments(t *testing.T) {
	var a Model
	a.Init(1, 30)
	a.StatCoeff[2] = 7
	b := a // storage
	a.Ctx[CtxSigCoeff] ^= 0xFF
	a.StatCoeff[2] = 0
	if b.Ctx[CtxSigCoeff] == a.Ctx[CtxSigCoeff] || b.StatCoeff[2] != 7 {
		t.Errorf("a stored Model shares storage with its source")
	}
}
