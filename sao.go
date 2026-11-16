// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

// The sample adaptive offset process (8.7.3). It runs on the deblocked picture, one
// coding tree block at a time, reading the deblocked samples and writing the picture.

// saoHPos and saoVPos are Table 8-13: the two neighbours of an edge offset class.
var (
	saoHPos = [4][2]int{{-1, 1}, {0, 0}, {-1, 1}, {1, -1}}
	saoVPos = [4][2]int{{0, 0}, {-1, 1}, {-1, 1}, {-1, 1}}
)

// SAO applies sample adaptive offset to the deblocked picture frame (8.7.3.1) with
// the parameters the walk read for each coding tree block.
func (ps *PictureState) SAO(frame *DecodedPicture, sps SPS, pps PPS) {
	l := ps.Layout
	for c := 0; c < frame.Components; c++ {
		used := false
		for i := range ps.Sao {
			used = used || ps.Sao[i].Type[c] != 0
		}
		if !used {
			continue
		}
		pl := &frame.Planes[c]
		src := append([]uint16(nil), pl.Pix...) // recPicture: the deblocked samples
		subW, subH := int(sps.CodedWidth)/pl.Width, int(sps.CodedHeight)/pl.Height
		bitDepth := int(sps.BitDepthLuma)
		if c > 0 {
			bitDepth = int(sps.BitDepthChroma)
		}
		ctbW, ctbH := (1<<uint(sps.Log2CtbSize))/subW, (1<<uint(sps.Log2CtbSize))/subH
		for ry := 0; ry < l.HeightCtbs; ry++ {
			for rx := 0; rx < l.WidthCtbs; rx++ {
				p := &ps.Sao[ry*l.WidthCtbs+rx]
				if p.Type[c] == 0 {
					continue
				}
				ps.saoCTB(pl, src, c, p, rx, ry, ctbW, ctbH, subW, subH, bitDepth, pps)
			}
		}
	}
}

func (ps *PictureState) saoCTB(pl *Plane, src []uint16, c int, p *SaoParams, rx, ry, ctbW, ctbH, subW, subH, bitDepth int, pps PPS) {
	l := ps.Layout
	maxVal := 1<<uint(bitDepth) - 1
	rsCur := ry*l.WidthCtbs + rx
	var bandTable [32]int
	if p.Type[c] == 1 {
		for k := 0; k < 4; k++ {
			bandTable[(k+int(p.BandPos[c]))&31] = k + 1 // (8-414)
		}
	}
	for j := 0; j < ctbH; j++ {
		y := ry*ctbH + j
		if y >= pl.Height {
			break
		}
		for i := 0; i < ctbW; i++ {
			x := rx*ctbW + i
			if x >= pl.Width {
				break
			}
			if ps.NoFilter[ps.cuAt(x*subW, y*subH)] {
				continue
			}
			cur := int(src[y*pl.Stride+x])
			var idx int // index into SaoOffsetVal: 0 for none
			if p.Type[c] == 2 {
				edgeIdx, ok := 0, true
				var nb [2]int
				for k := 0; k < 2; k++ {
					xn, yn := x+saoHPos[p.EoClass[c]][k], y+saoVPos[p.EoClass[c]][k]
					if xn < 0 || yn < 0 || xn >= pl.Width || yn >= pl.Height {
						ok = false
						break
					}
					rsN := ps.ctbAt(xn*subW, yn*subH)
					if rsN != rsCur {
						if ps.SliceAddr[rsN] != ps.SliceAddr[rsCur] {
							// The neighbour is in another slice: the flag that counts is that of the
							// later of the two (8.7.3.2).
							later := rsCur
							if l.RsToTs[rsN] > l.RsToTs[rsCur] {
								later = rsN
							}
							if !ps.DbAcross[later] {
								ok = false
								break
							}
						}
						if l.TileID[l.RsToTs[rsN]] != l.TileID[l.RsToTs[rsCur]] && !pps.LoopFilterAcrossTiles {
							ok = false
							break
						}
					}
					nb[k] = int(src[yn*pl.Stride+xn])
				}
				if ok {
					edgeIdx = 2 + sign(cur-nb[0]) + sign(cur-nb[1]) // (8-411)
					if edgeIdx <= 2 {
						if edgeIdx == 2 {
							edgeIdx = 0
						} else {
							edgeIdx++
						}
					}
				}
				idx = edgeIdx
			} else {
				idx = bandTable[cur>>uint(bitDepth-5)]
			}
			if idx == 0 {
				continue
			}
			pl.Pix[y*pl.Stride+x] = uint16(clip3(0, maxVal, cur+int(p.Offset[c][idx-1])))
		}
	}
}

func sign(v int) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}
