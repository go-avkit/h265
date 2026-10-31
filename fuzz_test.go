// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzStream drives the whole reader over a fuzzed elementary stream, the way a
// demuxer of a downloaded file does: split it into units, keep the latest
// parameter sets, and for every slice segment read its header, its order count,
// its references, the reference picture set they derive and the lists built
// from it. It holds the package to one thing: no stream, however malformed,
// panics. Every refusal along the way is an acceptable answer.
//
// The seed is a short x265 encode (testdata/fuzzseed) with B-frames, four
// references and weighted prediction. The first byte of an input picks the
// framing, so both the Annex B and the length-prefixed reader are reached.
//
//	go test -run='^$' -fuzz='^FuzzStream$' -fuzztime=60s
func FuzzStream(f *testing.F) {
	paths, err := filepath.Glob("testdata/fuzzseed/*")
	if err != nil || len(paths) == 0 {
		f.Fatalf("no seed: %v", err)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatalf("seed: %v", err)
		}
		f.Add(append([]byte{0}, data...))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		var units []Unit
		if data[0]%4 == 0 {
			units, err = SplitAnnexB(data[1:])
		} else {
			units, err = SplitLengthPrefixed(data[1:], int(data[0]%4))
		}
		if err != nil {
			return
		}
		var (
			sps     SPS
			pps     PPS
			haveSPS bool
			havePPS bool
			poc     POCCounter
		)
		for _, u := range units {
			_ = u.Unescape()
			_, _, _, _ = u.Type.IsRandomAccess(), u.Type.IsBLA(), u.Type.IsLeading(), u.Type.IsSubLayerNonReference()
			switch {
			case u.Type == UnitSPS:
				if s, err := ParseSPS(u); err == nil {
					sps, haveSPS = s, true
				}
			case u.Type == UnitPPS:
				if p, err := ParsePPS(u); err == nil {
					pps, havePPS = p, true
				}
			case u.Type.IsSliceSegment():
				if !haveSPS || !havePPS {
					continue
				}
				h, err := ParseSliceSegmentHeader(u, pps)
				if err != nil {
					continue
				}
				_ = h.Type.String()
				p, err := poc.Next(u, sps, pps)
				if err != nil {
					continue
				}
				refs, err := ReferencesOf(u, sps, pps)
				if err != nil {
					continue
				}
				set := Derive(refs, p.Value, sps)
				_ = set.Count()
				_, _, _ = Lists(set, refs)
			}
		}
		if havePPS {
			_, _ = SplitPictures(units, pps)
		}
	})
}
