package cabac

// Context variables and their initialisation, clause 9.3.2.2.
//
// Provenance. The initValue arrays below are normative values, transcribed by
// hand from ITU-T H.265 (V11) (01/2026): Tables 9-5 to 9-42 (pages 209 to 216)
// give them per ctxIdx, and Table 9-4 (pages 207 and 208) says which ctxIdx of
// which table belongs to which syntax element for each initType. The spans in
// elements record Table 9-4 and nothing else. Nothing was derived from, or
// compared with, another decoder. spec_reading_test.go holds a second reading
// of every table (extracted mechanically from the document's text layer, not
// typed), and contexts_test.go checks what the tables must satisfy: that the
// spans of each table tile it exactly, that a table has as many values as its
// last ctxIdx says, and equations 9-4 to 9-6 against a second writing of them.
//
// Layout. Every syntax element that has a context set of its own gets a
// contiguous run in Model.Ctx, as many contexts as its largest initType needs:
// element e's ctxInc c is Model.Ctx[CtxE+c]. Syntax elements that share a
// table and a ctxIdx share the run (sao_merge_left_flag and sao_merge_up_flag;
// ref_idx_l0 and ref_idx_l1; and so on, as Table 9-4 lists them).

// The first context of each element's run in Model.Ctx, and its length.
const (
	CtxSAOMerge               = 0                             // sao_merge_left_flag, sao_merge_up_flag; 1
	CtxSAOTypeIdx             = CtxSAOMerge + 1               // sao_type_idx_luma, sao_type_idx_chroma; 1
	CtxSplitCU                = CtxSAOTypeIdx + 1             // split_cu_flag; 3
	CtxCUTransquantBypass     = CtxSplitCU + 3                // cu_transquant_bypass_flag; 1
	CtxCUSkip                 = CtxCUTransquantBypass + 1     // cu_skip_flag; 3
	CtxPaletteMode            = CtxCUSkip + 3                 // palette_mode_flag; 1
	CtxPredMode               = CtxPaletteMode + 1            // pred_mode_flag; 1
	CtxPartMode               = CtxPredMode + 1               // part_mode; 4
	CtxPrevIntraLumaPred      = CtxPartMode + 4               // prev_intra_luma_pred_flag; 1
	CtxIntraChromaPredMode    = CtxPrevIntraLumaPred + 1      // intra_chroma_pred_mode; 1
	CtxRqtRootCbf             = CtxIntraChromaPredMode + 1    // rqt_root_cbf; 1
	CtxMergeFlag              = CtxRqtRootCbf + 1             // merge_flag; 1
	CtxMergeIdx               = CtxMergeFlag + 1              // merge_idx; 1
	CtxInterPredIdc           = CtxMergeIdx + 1               // inter_pred_idc; 5
	CtxRefIdx                 = CtxInterPredIdc + 5           // ref_idx_l0, ref_idx_l1; 2
	CtxMvpFlag                = CtxRefIdx + 2                 // mvp_l0_flag, mvp_l1_flag; 1
	CtxSplitTransform         = CtxMvpFlag + 1                // split_transform_flag; 3
	CtxCbfLuma                = CtxSplitTransform + 3         // cbf_luma; 2
	CtxCbfChroma              = CtxCbfLuma + 2                // cbf_cb, cbf_cr; 5
	CtxAbsMvdGreater0         = CtxCbfChroma + 5              // abs_mvd_greater0_flag; 1
	CtxAbsMvdGreater1         = CtxAbsMvdGreater0 + 1         // abs_mvd_greater1_flag; 1
	CtxTuResidualAct          = CtxAbsMvdGreater1 + 1         // tu_residual_act_flag; 1
	CtxLog2ResScaleAbsPlus1   = CtxTuResidualAct + 1          // log2_res_scale_abs_plus1; 8
	CtxResScaleSign           = CtxLog2ResScaleAbsPlus1 + 8   // res_scale_sign_flag; 2
	CtxTransformSkipLuma      = CtxResScaleSign + 2           // transform_skip_flag[ ][ ][ 0 ]; 1
	CtxTransformSkipChroma    = CtxTransformSkipLuma + 1      // transform_skip_flag[ ][ ][ 1 and 2 ]; 1
	CtxExplicitRdpcmLuma      = CtxTransformSkipChroma + 1    // explicit_rdpcm_flag[ ][ ][ 0 ]; 1
	CtxExplicitRdpcmChroma    = CtxExplicitRdpcmLuma + 1      // explicit_rdpcm_flag[ ][ ][ 1 and 2 ]; 1
	CtxExplicitRdpcmDirLuma   = CtxExplicitRdpcmChroma + 1    // explicit_rdpcm_dir_flag[ ][ ][ 0 ]; 1
	CtxExplicitRdpcmDirChroma = CtxExplicitRdpcmDirLuma + 1   // explicit_rdpcm_dir_flag[ ][ ][ 1 and 2 ]; 1
	CtxLastSigCoeffXPrefix    = CtxExplicitRdpcmDirChroma + 1 // last_sig_coeff_x_prefix; 18
	CtxLastSigCoeffYPrefix    = CtxLastSigCoeffXPrefix + 18   // last_sig_coeff_y_prefix; 18
	CtxCodedSubBlock          = CtxLastSigCoeffYPrefix + 18   // coded_sub_block_flag; 4
	CtxSigCoeff               = CtxCodedSubBlock + 4          // sig_coeff_flag; 44 (42, then the 2 of the transform-skip context)
	CtxCoeffAbsGreater1       = CtxSigCoeff + 44              // coeff_abs_level_greater1_flag; 24
	CtxCoeffAbsGreater2       = CtxCoeffAbsGreater1 + 24      // coeff_abs_level_greater2_flag; 6
	CtxPaletteRunPrefix       = CtxCoeffAbsGreater2 + 6       // palette_run_prefix; 8
	CtxCopyAbovePalette       = CtxPaletteRunPrefix + 8       // copy_above_palette_indices_flag, copy_above_indices_for_final_run_flag; 1
	CtxPaletteTranspose       = CtxCopyAbovePalette + 1       // palette_transpose_flag; 1
	CtxCuQpDeltaAbs           = CtxPaletteTranspose + 1       // cu_qp_delta_abs; 2
	CtxCuChromaQpOffsetFlag   = CtxCuQpDeltaAbs + 2           // cu_chroma_qp_offset_flag; 1
	CtxCuChromaQpOffsetIdx    = CtxCuChromaQpOffsetFlag + 1   // cu_chroma_qp_offset_idx; 1
	NumContexts               = CtxCuChromaQpOffsetIdx + 1    // contexts in a Model
)

// span is a run of consecutive ctxIdx of one initValue table, as Table 9-4
// lists them for one initType.
type span struct{ first, n int }

// element ties the run [base, base+n) of Model.Ctx to the ctxIdx that Table 9-4
// gives each initType in the table named by table.
type element struct {
	name  string
	table string
	base  int
	n     int
	init  []uint8
	spans [3][]span
}

// unused is the initValue given to a context that the initType in question
// does not define (the syntax element does not occur in that slice type, or
// has fewer contexts there). 154 gives preCtxState 64 at every QP, that is
// pStateIdx 0, valMps 1: the equiprobable state. No conforming bitstream
// consults such a context.
const unused = 154

// elements is Table 9-4, with the initValue arrays of Tables 9-5 to 9-42.
var elements = [...]element{
	{"sao_merge_left_flag, sao_merge_up_flag", "Table 9-5", CtxSAOMerge, 1,
		[]uint8{153, 153, 153},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"sao_type_idx_luma, sao_type_idx_chroma", "Table 9-6", CtxSAOTypeIdx, 1,
		[]uint8{200, 185, 160},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"split_cu_flag", "Table 9-7", CtxSplitCU, 3,
		[]uint8{139, 141, 157, 107, 139, 126, 107, 139, 126},
		[3][]span{{{0, 3}}, {{3, 3}}, {{6, 3}}}},
	{"cu_transquant_bypass_flag", "Table 9-8", CtxCUTransquantBypass, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"cu_skip_flag", "Table 9-9", CtxCUSkip, 3,
		[]uint8{197, 185, 201, 197, 185, 201},
		[3][]span{nil, {{0, 3}}, {{3, 3}}}},
	{"palette_mode_flag", "Table 9-38", CtxPaletteMode, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"pred_mode_flag", "Table 9-10", CtxPredMode, 1,
		[]uint8{149, 134},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"part_mode", "Table 9-11", CtxPartMode, 4,
		[]uint8{184, 154, 139, 154, 154, 154, 139, 154, 154},
		[3][]span{{{0, 1}}, {{1, 4}}, {{5, 4}}}},
	{"prev_intra_luma_pred_flag", "Table 9-12", CtxPrevIntraLumaPred, 1,
		[]uint8{184, 154, 183},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"intra_chroma_pred_mode", "Table 9-13", CtxIntraChromaPredMode, 1,
		[]uint8{63, 152, 152},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"rqt_root_cbf", "Table 9-14", CtxRqtRootCbf, 1,
		[]uint8{79, 79},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"merge_flag", "Table 9-15", CtxMergeFlag, 1,
		[]uint8{110, 154},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"merge_idx", "Table 9-16", CtxMergeIdx, 1,
		[]uint8{122, 137},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"inter_pred_idc", "Table 9-17", CtxInterPredIdc, 5,
		[]uint8{95, 79, 63, 31, 31, 95, 79, 63, 31, 31},
		[3][]span{nil, {{0, 5}}, {{5, 5}}}},
	{"ref_idx_l0, ref_idx_l1", "Table 9-18", CtxRefIdx, 2,
		[]uint8{153, 153, 153, 153},
		[3][]span{nil, {{0, 2}}, {{2, 2}}}},
	{"mvp_l0_flag, mvp_l1_flag", "Table 9-19", CtxMvpFlag, 1,
		[]uint8{168, 168},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"split_transform_flag", "Table 9-20", CtxSplitTransform, 3,
		[]uint8{153, 138, 138, 124, 138, 94, 224, 167, 122},
		[3][]span{{{0, 3}}, {{3, 3}}, {{6, 3}}}},
	{"cbf_luma", "Table 9-21", CtxCbfLuma, 2,
		[]uint8{111, 141, 153, 111, 153, 111},
		[3][]span{{{0, 2}}, {{2, 2}}, {{4, 2}}}},
	{"cbf_cb, cbf_cr", "Table 9-22", CtxCbfChroma, 5,
		[]uint8{94, 138, 182, 154, 149, 107, 167, 154, 149, 92, 167, 154, 154, 154, 154},
		[3][]span{{{0, 4}, {12, 1}}, {{4, 4}, {13, 1}}, {{8, 4}, {14, 1}}}},
	{"abs_mvd_greater0_flag", "Table 9-23", CtxAbsMvdGreater0, 1,
		[]uint8{140, 198, 169, 198},
		[3][]span{nil, {{0, 1}}, {{2, 1}}}},
	{"abs_mvd_greater1_flag", "Table 9-23", CtxAbsMvdGreater1, 1,
		[]uint8{140, 198, 169, 198},
		[3][]span{nil, {{1, 1}}, {{3, 1}}}},
	{"tu_residual_act_flag", "Table 9-39", CtxTuResidualAct, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"log2_res_scale_abs_plus1", "Table 9-36", CtxLog2ResScaleAbsPlus1, 8,
		[]uint8{
			154, 154, 154, 154, 154, 154, 154, 154, 154,
			154, 154, 154, 154, 154, 154, 154, 154, 154,
			154, 154, 154, 154, 154, 154},
		[3][]span{{{0, 8}}, {{8, 8}}, {{16, 8}}}},
	{"res_scale_sign_flag", "Table 9-37", CtxResScaleSign, 2,
		[]uint8{154, 154, 154, 154, 154, 154},
		[3][]span{{{0, 2}}, {{2, 2}}, {{4, 2}}}},
	{"transform_skip_flag[ ][ ][ 0 ]", "Table 9-25", CtxTransformSkipLuma, 1,
		[]uint8{139, 139, 139, 139, 139, 139},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"transform_skip_flag[ ][ ][ 1 ], [ 2 ]", "Table 9-25", CtxTransformSkipChroma, 1,
		[]uint8{139, 139, 139, 139, 139, 139},
		[3][]span{{{3, 1}}, {{4, 1}}, {{5, 1}}}},
	{"explicit_rdpcm_flag[ ][ ][ 0 ]", "Table 9-32", CtxExplicitRdpcmLuma, 1,
		[]uint8{139, 139, 139, 139},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"explicit_rdpcm_flag[ ][ ][ 1 ], [ 2 ]", "Table 9-32", CtxExplicitRdpcmChroma, 1,
		[]uint8{139, 139, 139, 139},
		[3][]span{nil, {{2, 1}}, {{3, 1}}}},
	{"explicit_rdpcm_dir_flag[ ][ ][ 0 ]", "Table 9-33", CtxExplicitRdpcmDirLuma, 1,
		[]uint8{139, 139, 139, 139},
		[3][]span{nil, {{0, 1}}, {{1, 1}}}},
	{"explicit_rdpcm_dir_flag[ ][ ][ 1 ], [ 2 ]", "Table 9-33", CtxExplicitRdpcmDirChroma, 1,
		[]uint8{139, 139, 139, 139},
		[3][]span{nil, {{2, 1}}, {{3, 1}}}},
	{"last_sig_coeff_x_prefix", "Table 9-26", CtxLastSigCoeffXPrefix, 18,
		[]uint8{
			110, 110, 124, 125, 140, 153, 125, 127, 140, 109, 111, 143, 127, 111, 79, 108, 123, 63,
			125, 110, 94, 110, 95, 79, 125, 111, 110, 78, 110, 111, 111, 95, 94, 108, 123, 108,
			125, 110, 124, 110, 95, 94, 125, 111, 111, 79, 125, 126, 111, 111, 79, 108, 123, 93},
		[3][]span{{{0, 18}}, {{18, 18}}, {{36, 18}}}},
	{"last_sig_coeff_y_prefix", "Table 9-27", CtxLastSigCoeffYPrefix, 18,
		[]uint8{
			110, 110, 124, 125, 140, 153, 125, 127, 140, 109, 111, 143, 127, 111, 79, 108, 123, 63,
			125, 110, 94, 110, 95, 79, 125, 111, 110, 78, 110, 111, 111, 95, 94, 108, 123, 108,
			125, 110, 124, 110, 95, 94, 125, 111, 111, 79, 125, 126, 111, 111, 79, 108, 123, 93},
		[3][]span{{{0, 18}}, {{18, 18}}, {{36, 18}}}},
	{"coded_sub_block_flag", "Table 9-28", CtxCodedSubBlock, 4,
		[]uint8{91, 171, 134, 141, 121, 140, 61, 154, 121, 140, 61, 154},
		[3][]span{{{0, 4}}, {{4, 4}}, {{8, 4}}}},
	{"sig_coeff_flag", "Table 9-29", CtxSigCoeff, 44,
		[]uint8{
			111, 111, 125, 110, 110, 94, 124, 108, 124, 107, 125, 141, 179, 153, 125, 107,
			125, 141, 179, 153, 125, 107, 125, 141, 179, 153, 125, 140, 139, 182, 182, 152,
			136, 152, 136, 153, 136, 139, 111, 136, 139, 111, 155, 154, 139, 153, 139, 123,
			123, 63, 153, 166, 183, 140, 136, 153, 154, 166, 183, 140, 136, 153, 154, 166,
			183, 140, 136, 153, 154, 170, 153, 123, 123, 107, 121, 107, 121, 167, 151, 183,
			140, 151, 183, 140, 170, 154, 139, 153, 139, 123, 123, 63, 124, 166, 183, 140,
			136, 153, 154, 166, 183, 140, 136, 153, 154, 166, 183, 140, 136, 153, 154, 170,
			153, 138, 138, 122, 121, 122, 121, 167, 151, 183, 140, 151, 183, 140, 141, 111,
			140, 140, 140, 140},
		[3][]span{{{0, 42}, {126, 2}}, {{42, 42}, {128, 2}}, {{84, 42}, {130, 2}}}},
	{"coeff_abs_level_greater1_flag", "Table 9-30", CtxCoeffAbsGreater1, 24,
		[]uint8{
			140, 92, 137, 138, 140, 152, 138, 139, 153, 74, 149, 92, 139, 107, 122, 152,
			140, 179, 166, 182, 140, 227, 122, 197, 154, 196, 196, 167, 154, 152, 167, 182,
			182, 134, 149, 136, 153, 121, 136, 137, 169, 194, 166, 167, 154, 167, 137, 182,
			154, 196, 167, 167, 154, 152, 167, 182, 182, 134, 149, 136, 153, 121, 136, 122,
			169, 208, 166, 167, 154, 152, 167, 182},
		[3][]span{{{0, 24}}, {{24, 24}}, {{48, 24}}}},
	{"coeff_abs_level_greater2_flag", "Table 9-31", CtxCoeffAbsGreater2, 6,
		[]uint8{138, 153, 136, 167, 152, 152, 107, 167, 91, 122, 107, 167, 107, 167, 91, 107, 107, 167},
		[3][]span{{{0, 6}}, {{6, 6}}, {{12, 6}}}},
	{"palette_run_prefix", "Table 9-40", CtxPaletteRunPrefix, 8,
		[]uint8{
			154, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154,
			154, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154, 154},
		[3][]span{{{0, 8}}, {{8, 8}}, {{16, 8}}}},
	{"copy_above_palette_indices_flag, copy_above_indices_for_final_run_flag", "Table 9-41", CtxCopyAbovePalette, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"palette_transpose_flag", "Table 9-42", CtxPaletteTranspose, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"cu_qp_delta_abs", "Table 9-24", CtxCuQpDeltaAbs, 2,
		[]uint8{154, 154, 154, 154, 154, 154},
		[3][]span{{{0, 2}}, {{2, 2}}, {{4, 2}}}},
	{"cu_chroma_qp_offset_flag", "Table 9-34", CtxCuChromaQpOffsetFlag, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
	{"cu_chroma_qp_offset_idx", "Table 9-35", CtxCuChromaQpOffsetIdx, 1,
		[]uint8{154, 154, 154},
		[3][]span{{{0, 1}}, {{1, 1}}, {{2, 1}}}},
}

// initValues[initType][ctx] is the initValue of context ctx for initType,
// assembled once from elements.
var initValues [3][NumContexts]uint8

func init() {
	for t := range initValues {
		for i := range initValues[t] {
			initValues[t][i] = unused
		}
	}
	for _, e := range elements {
		for t, spans := range e.spans {
			c := e.base
			for _, s := range spans {
				copy(initValues[t][c:], e.init[s.first:s.first+s.n])
				c += s.n
			}
		}
	}
}

// InitType is equation 9-7: the initType of a slice, from its slice_type
// (2 for I, 1 for P, 0 for B, as slice_type is numbered) and cabac_init_flag.
func InitType(sliceType uint8, cabacInitFlag bool) int {
	if sliceType == 2 {
		return 0
	}
	if (sliceType == 1) == cabacInitFlag {
		return 2 // P with cabac_init_flag, B without it
	}
	return 1
}

// Model is every piece of state that clause 9.3.2 initialises, stores (9.3.2.4)
// and synchronises (9.3.2.5) besides the arithmetic decoding engine: the context
// variables and the Rice parameter statistics StatCoeff. Storing and
// synchronising are plain assignments of a Model.
type Model struct {
	// Ctx holds one context variable per byte, pStateIdx<<1 | valMps.
	Ctx [NumContexts]uint8
	// StatCoeff is StatCoeff[ 0..3 ] of clause 9.3.3.11.
	StatCoeff [4]uint8
}

func clip3(lo, hi, x int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// Init is the initialisation of clause 9.3.2.2 for every context variable, and
// of StatCoeff (clause 9.3.2.1), for initType 0, 1 or 2 and SliceQpY.
func (m *Model) Init(initType, sliceQpY int) {
	qp := clip3(0, 51, sliceQpY)
	for i, initValue := range initValues[initType] {
		slopeIdx, offsetIdx := int(initValue>>4), int(initValue&15) // 9-4
		mm := slopeIdx*5 - 45                                       // 9-5
		n := offsetIdx<<3 - 16
		pre := clip3(1, 126, (mm*qp)>>4+n) // 9-6
		if pre <= 63 {
			m.Ctx[i] = uint8(63-pre) << 1
		} else {
			m.Ctx[i] = uint8(pre-64)<<1 | 1
		}
	}
	m.StatCoeff = [4]uint8{}
}
