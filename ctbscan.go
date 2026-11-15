// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"fmt"
)

// maxPicSizeInCtbs bounds a picture in coding tree blocks wherever a table is
// built from one: Level 6.2's largest picture (35,651,584 luma samples) is 139264
// blocks of 16x16, so 2^18 is above every level.
const maxPicSizeInCtbs = 1 << 18

// CtbLayout is the CTB raster and tile scanning conversion of clause 6.5.1 for
// one picture: the order in which the coding tree blocks of the picture are
// coded, and which tile each belongs to.
type CtbLayout struct {
	// WidthCtbs and HeightCtbs are PicWidthInCtbsY and PicHeightInCtbsY.
	WidthCtbs, HeightCtbs int
	// ColBd and RowBd are colBd and rowBd (6-5, 6-6): the tile boundaries in
	// coding tree blocks, one more than there are tiles each way.
	ColBd, RowBd []int
	// RsToTs is CtbAddrRsToTs (6-7), TsToRs CtbAddrTsToRs (6-8) and TileID the
	// TileId of each address in TILE scan (6-9).
	RsToTs, TsToRs []int32
	TileID         []int32

	log2Ctb, log2MinTb uint
}

// NewCtbLayout derives the layout from the SPS and the PPS that names it. It
// validates the pair first (PPS.Validate), so the tile grid fits the picture.
func NewCtbLayout(sps SPS, pps PPS) (*CtbLayout, error) {
	if err := pps.Validate(sps); err != nil {
		return nil, err
	}
	ctb := uint64(1) << sps.Log2CtbSize
	w, h := (uint64(sps.CodedWidth)+ctb-1)/ctb, (uint64(sps.CodedHeight)+ctb-1)/ctb
	if w*h > maxPicSizeInCtbs {
		return nil, fmt.Errorf("%w: a picture of %d coding tree blocks", ErrSliceHeader, w*h)
	}
	L := &CtbLayout{WidthCtbs: int(w), HeightCtbs: int(h), log2Ctb: uint(sps.Log2CtbSize), log2MinTb: uint(sps.Log2MinTbSize)}

	// colWidth and rowHeight (6-3, 6-4).
	cols, rows := int(pps.TileColumns), int(pps.TileRows)
	colWidth, rowHeight := make([]int, cols), make([]int, rows)
	split := func(size []int, total int, n int, uniform bool, stated []uint32) {
		if uniform {
			for i := range size {
				size[i] = (i+1)*total/n - i*total/n
			}
			return
		}
		size[n-1] = total
		for i := 0; i < n-1; i++ {
			size[i] = int(stated[i]) + 1
			size[n-1] -= size[i]
		}
	}
	uniform := pps.UniformTileSpacing || !pps.TilesEnabled
	split(colWidth, L.WidthCtbs, cols, uniform, pps.ColumnWidthMinus1)
	split(rowHeight, L.HeightCtbs, rows, uniform, pps.RowHeightMinus1)
	// (Validate has checked that the stated sizes leave the last column and row
	// at least one block, so every column and row is wider than 0, 7.4.3.3.)

	// colBd and rowBd (6-5, 6-6).
	L.ColBd, L.RowBd = make([]int, cols+1), make([]int, rows+1)
	for i := 0; i < cols; i++ {
		L.ColBd[i+1] = L.ColBd[i] + colWidth[i]
	}
	for j := 0; j < rows; j++ {
		L.RowBd[j+1] = L.RowBd[j] + rowHeight[j]
	}

	// CtbAddrRsToTs (6-7).
	n := L.WidthCtbs * L.HeightCtbs
	L.RsToTs, L.TsToRs, L.TileID = make([]int32, n), make([]int32, n), make([]int32, n)
	for rs := 0; rs < n; rs++ {
		tbX, tbY := rs%L.WidthCtbs, rs/L.WidthCtbs
		tileX, tileY := 0, 0
		for i := 0; i < cols; i++ {
			if tbX >= L.ColBd[i] {
				tileX = i
			}
		}
		for j := 0; j < rows; j++ {
			if tbY >= L.RowBd[j] {
				tileY = j
			}
		}
		ts := 0
		for i := 0; i < tileX; i++ {
			ts += rowHeight[tileY] * colWidth[i]
		}
		for j := 0; j < tileY; j++ {
			ts += L.WidthCtbs * rowHeight[j]
		}
		ts += (tbY-L.RowBd[tileY])*colWidth[tileX] + tbX - L.ColBd[tileX]
		L.RsToTs[rs] = int32(ts)
	}
	// CtbAddrTsToRs (6-8).
	for rs, ts := range L.RsToTs {
		L.TsToRs[ts] = int32(rs)
	}
	// TileId (6-9).
	tileIdx := int32(0)
	for j := 0; j < rows; j++ {
		for i := 0; i < cols; i++ {
			for y := L.RowBd[j]; y < L.RowBd[j+1]; y++ {
				for x := L.ColBd[i]; x < L.ColBd[i+1]; x++ {
					L.TileID[L.RsToTs[y*L.WidthCtbs+x]] = tileIdx
				}
			}
			tileIdx++
		}
	}
	return L, nil
}

// MinTbAddrZs is the minimum block address in z-scan order of the minimum
// transform block at ( x, y ), in units of minimum transform blocks (6-10).
// It is computed where the specification stores it in an array: the array
// would hold up to 256 entries per coding tree block.
func (l *CtbLayout) MinTbAddrZs(x, y int) int {
	k := l.log2Ctb - l.log2MinTb
	tbX, tbY := (x<<l.log2MinTb)>>l.log2Ctb, (y<<l.log2MinTb)>>l.log2Ctb
	addr := int(l.RsToTs[tbY*l.WidthCtbs+tbX]) << (k * 2)
	p := 0
	for i := uint(0); i < k; i++ {
		m := 1 << i
		if m&x != 0 {
			p += m * m
		}
		if m&y != 0 {
			p += 2 * m * m
		}
	}
	return addr + p
}

// Scan is a scan order: Scan[i] is the ( x, y ) of the i-th position.
type Scan [][2]uint8

// DiagScan is the up-right diagonal scan order of blkSize (6.5.3, 6-11).
func DiagScan(blkSize int) Scan {
	out := make(Scan, 0, blkSize*blkSize)
	x, y := 0, 0
	for len(out) < blkSize*blkSize {
		for y >= 0 {
			if x < blkSize && y < blkSize {
				out = append(out, [2]uint8{uint8(x), uint8(y)})
			}
			y--
			x++
		}
		y = x
		x = 0
	}
	return out
}

// HorScan is the horizontal scan order (6.5.4, 6-12).
func HorScan(blkSize int) Scan {
	out := make(Scan, 0, blkSize*blkSize)
	for y := 0; y < blkSize; y++ {
		for x := 0; x < blkSize; x++ {
			out = append(out, [2]uint8{uint8(x), uint8(y)})
		}
	}
	return out
}

// VerScan is the vertical scan order (6.5.5, 6-13).
func VerScan(blkSize int) Scan {
	out := make(Scan, 0, blkSize*blkSize)
	for x := 0; x < blkSize; x++ {
		for y := 0; y < blkSize; y++ {
			out = append(out, [2]uint8{uint8(x), uint8(y)})
		}
	}
	return out
}
