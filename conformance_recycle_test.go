// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

// TestRecycledPicturesOfTheConformanceSuiteEqualThePlainDecode decodes every stream of the
// suite twice, with and without Options.RecyclePictures, releasing each output picture the
// moment it is hashed in the second: the md5 of the output, the number of pictures and the
// hash SEI counts must be the same, so no stream, with reordering, long-term references,
// IRAP restarts or a resolution change in it, ever reads a recycled picture. The pool must also
// have been used.
func TestRecycledPicturesOfTheConformanceSuiteEqualThePlainDecode(t *testing.T) {
	p := conformance(t)
	var streams, pictures, reused int
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		run := func(recycle bool) (string, int, DecoderStats, *Decoder) {
			d := NewDecoder(Options{VerifyHash: true, RecyclePictures: recycle})
			h := md5.New()
			n := 0
			emit := func(outs []Output) {
				for _, o := range outs {
					writePicture(h, o.Picture)
					n++
					if recycle {
						o.Picture.Release()
					}
				}
			}
			for _, u := range list {
				outs, _ := d.Decode(u)
				emit(outs)
			}
			outs, _ := d.Flush()
			emit(outs)
			return hex.EncodeToString(h.Sum(nil)), n, d.Stats(), d
		}
		wantSum, wantN, wantStats, _ := run(false)
		gotSum, gotN, gotStats, d := run(true)
		if gotSum != wantSum || gotN != wantN || gotStats != wantStats {
			t.Errorf("%s: recycled %s, %d pictures, %+v; plain %s, %d, %+v", rel, gotSum, gotN, gotStats, wantSum, wantN, wantStats)
		}
		streams++
		pictures += gotN
		reused += d.pool.reused
	}
	t.Logf("conformance-count: recycle streams=%d pictures=%d reused>0=%v", streams, pictures, reused > 0)
	if reused == 0 {
		t.Fatal("no picture was ever reused")
	}
}
