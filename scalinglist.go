// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// ErrScalingList means scaling_list_data( ) states what 7.4.5 forbids.
var ErrScalingList = errors.New("h265: scaling list data is not valid")

// ScalingLists is scaling_list_data( ) (7.3.4) with the inference of 7.4.5 done:
// ScalingList[ sizeId ][ matrixId ][ i ] and the DC coefficients of the 16x16 and
// 32x32 lists. Entries of sizeId 3 are filled for matrixId 0 and 3 only, as
// stated; Factors derives the chroma ones.
type ScalingLists struct {
	List [4][6][64]uint8
	// DC is scaling_list_dc_coef_minus8 + 8 of [ sizeId - 2 ][ matrixId ].
	DC [2][6]uint8
}

// Table 7-5 and Table 7-6, from the Rec. ITU-T H.265 (01/2026) text. The 4x4
// default is flat; the 8x8 one is stated for matrixId 0 to 2 (intra) and 3 to 5
// (inter), 16 values to a row of the table.
var (
	defaultIntra8 = [64]uint8{
		16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 16, 17, 16, 17, 18,
		17, 18, 18, 17, 18, 21, 19, 20, 21, 20, 19, 21, 24, 22, 22, 24,
		24, 22, 22, 24, 25, 25, 27, 30, 27, 25, 25, 29, 31, 35, 35, 31,
		29, 36, 41, 44, 41, 36, 47, 54, 54, 47, 65, 70, 65, 88, 88, 115,
	}
	defaultInter8 = [64]uint8{
		16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 17, 17, 17, 17, 17, 18,
		18, 18, 18, 18, 18, 20, 20, 20, 20, 20, 20, 20, 24, 24, 24, 24,
		24, 24, 24, 24, 25, 25, 25, 25, 25, 25, 25, 28, 28, 28, 28, 28,
		28, 33, 33, 33, 33, 33, 41, 41, 41, 41, 54, 54, 54, 71, 71, 91,
	}
)

// defaultList is the default ScalingList[ sizeId ][ matrixId ] (7.4.5).
func defaultList(sizeID, matrixID int) (l [64]uint8) {
	switch {
	case sizeID == 0:
		for i := 0; i < 16; i++ {
			l[i] = 16
		}
	case matrixID < 3:
		l = defaultIntra8
	default:
		l = defaultInter8
	}
	return l
}

// DefaultScalingLists is what scaling_list_enabled_flag means with no scaling_list_data( ).
func DefaultScalingLists() *ScalingLists {
	s := new(ScalingLists)
	for sizeID := 0; sizeID < 4; sizeID++ {
		for m := 0; m < 6; m++ {
			s.List[sizeID][m] = defaultList(sizeID, m)
		}
	}
	for i := range s.DC {
		for m := range s.DC[i] {
			s.DC[i][m] = 16
		}
	}
	return s
}

// readScalingListData is scaling_list_data( ) with the semantics of 7.4.5. The
// reader's error is left for the caller; an invalid value is returned as an error.
func readScalingListData(r *sticky) (*ScalingLists, error) {
	s := new(ScalingLists)
	for sizeID := 0; sizeID < 4; sizeID++ {
		step := 1
		if sizeID == 3 {
			step = 3
		}
		coefNum := min(64, 1<<(4+uint(sizeID)<<1))
		for m := 0; m < 6; m += step {
			if !r.flag() { // scaling_list_pred_mode_flag
				delta := r.ue()
				if r.err != nil {
					return nil, nil
				}
				if int64(delta) > int64(m/step) {
					return nil, fmt.Errorf("%w: scaling_list_pred_matrix_id_delta %d for matrix %d of size %d", ErrScalingList, delta, m, sizeID)
				}
				if delta == 0 {
					s.List[sizeID][m] = defaultList(sizeID, m)
					if sizeID > 1 {
						s.DC[sizeID-2][m] = 16
					}
					continue
				}
				ref := m - int(delta)*step
				s.List[sizeID][m] = s.List[sizeID][ref]
				if sizeID > 1 {
					s.DC[sizeID-2][m] = s.DC[sizeID-2][ref]
				}
				continue
			}
			next := 8
			if sizeID > 1 {
				dc := r.se()
				if r.err == nil && (dc < -7 || dc > 247) {
					return nil, fmt.Errorf("%w: scaling_list_dc_coef_minus8 %d", ErrScalingList, dc)
				}
				next = int(dc) + 8
				s.DC[sizeID-2][m] = uint8(next)
			}
			for i := 0; i < coefNum; i++ {
				d := r.se()
				if r.err != nil {
					return nil, nil
				}
				if d < -128 || d > 127 {
					return nil, fmt.Errorf("%w: scaling_list_delta_coef %d", ErrScalingList, d)
				}
				next = (next + int(d) + 256) % 256
				if next == 0 {
					return nil, fmt.Errorf("%w: a scaling list value of 0", ErrScalingList)
				}
				s.List[sizeID][m][i] = uint8(next)
			}
		}
	}
	return s, nil
}

// ScalingFactors is ScalingFactor[ sizeId ][ matrixId ] (7-44 to 7-51): the
// nTbS x nTbS matrix m[ x ][ y ] of 8.6.3 at index y * nTbS + x.
type ScalingFactors [4][6][]uint8

// Factors derives the scaling factors of the lists. The 32x32 chroma matrices
// (matrixId 1, 2, 4, 5) come from the 16x16 lists (7-50, 7-51).
func (s *ScalingLists) Factors() *ScalingFactors {
	var f ScalingFactors
	scan4, scan8 := DiagScan(4), DiagScan(8)
	for sizeID := 0; sizeID < 4; sizeID++ {
		n := 4 << uint(sizeID)
		for m := 0; m < 6; m++ {
			out := make([]uint8, n*n)
			src, dc, hasDC := &s.List[sizeID][m], uint8(0), false
			if sizeID == 3 && m%3 != 0 {
				src, dc, hasDC = &s.List[2][m], s.DC[0][m], true
			} else if sizeID >= 2 {
				dc, hasDC = s.DC[sizeID-2][m], true
			}
			switch {
			case sizeID == 0:
				for i, p := range scan4 {
					out[int(p[1])*4+int(p[0])] = src[i]
				}
			default:
				rep := n / 8
				for i, p := range scan8 {
					for j := 0; j < rep; j++ {
						for k := 0; k < rep; k++ {
							out[(int(p[1])*rep+j)*n+int(p[0])*rep+k] = src[i]
						}
					}
				}
			}
			if hasDC {
				out[0] = dc
			}
			f[sizeID][m] = out
		}
	}
	return &f
}

// ScalingFactorsOf is the scaling factors a picture uses: nil for the flat 16 of
// scaling_list_enabled_flag equal to 0, the PPS's lists when it states them, the
// SPS's, or the default lists (7.4.3.3.1).
func ScalingFactorsOf(sps SPS, pps PPS) *ScalingFactors {
	switch {
	case !sps.ScalingListEnabled:
		return nil
	case pps.ScalingList != nil:
		return pps.ScalingList.Factors()
	case sps.ScalingList != nil:
		return sps.ScalingList.Factors()
	}
	return DefaultScalingLists().Factors()
}
