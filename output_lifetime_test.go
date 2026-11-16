// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"testing"
)

func pictureMD5(pic *DecodedPicture) string {
	h := md5.New()
	writePicture(h, pic)
	return hex.EncodeToString(h.Sum(nil))
}

// TestAnOutputPictureIsUnchangedWhileTheRestOfTheStreamDecodes is the aliasing witness of
// the lifetime Output states: the planes of an output picture are views of the decoder's
// pictures, so the decoder must never write one again. Every output picture is kept and
// hashed the moment it comes out; the rest of the stream is decoded, the decoder is flushed
// and runs the whole stream a second time (the same decoder, so any recycled buffer would be
// reused), and every kept picture must still hash the same.
func TestAnOutputPictureIsUnchangedWhileTheRestOfTheStreamDecodes(t *testing.T) {
	for _, s := range benchSeeds {
		t.Run(s.file, func(t *testing.T) {
			data, err := os.ReadFile("testdata/bench/" + s.file)
			if err != nil {
				t.Fatal(err)
			}
			units, err := SplitAnnexB(data)
			if err != nil {
				t.Fatal(err)
			}
			d := NewDecoder(Options{})
			var kept []*DecodedPicture
			var sums []string
			keep := func(outs []Output) {
				for _, o := range outs {
					kept = append(kept, o.Picture)
					sums = append(sums, pictureMD5(o.Picture))
				}
			}
			for pass := 0; pass < 2; pass++ {
				for _, u := range units {
					outs, err := d.Decode(u)
					if err != nil {
						t.Fatal(err)
					}
					keep(outs)
				}
				outs, err := d.Flush()
				if err != nil {
					t.Fatal(err)
				}
				keep(outs)
			}
			if len(kept) != 24 {
				t.Fatalf("%d pictures kept, want 24", len(kept))
			}
			for i, p := range kept {
				if got := pictureMD5(p); got != sums[i] {
					t.Errorf("picture %d changed after it was output: %s, was %s", i, got, sums[i])
				}
			}
			// two pictures never share memory: each is its own allocation
			for i := range kept {
				for j := i + 1; j < len(kept); j++ {
					if &kept[i].Planes[0].Pix[0] == &kept[j].Planes[0].Pix[0] {
						t.Fatalf("pictures %d and %d are the same memory", i, j)
					}
				}
			}
		})
	}
}

// TestCropIsAViewOfThePicture: no sample is copied, the stride is the picture's, and the
// view cannot grow into what follows it.
func TestCropIsAViewOfThePicture(t *testing.T) {
	sps := mustSPS(t, set{chroma: 1, width: 32, height: 16, window: [4]uint32{1, 2, 1, 0}})
	f := NewDecodedPicture(sps)
	got := f.Crop(sps)
	for c, first := range []int{2*32 + 2, 1*16 + 1} {
		if &got.Planes[c].Pix[0] != &f.Planes[c].Pix[first] {
			t.Errorf("plane %d is a copy", c)
		}
		if got.Planes[c].Stride != f.Planes[c].Stride {
			t.Errorf("plane %d stride %d, the picture's is %d", c, got.Planes[c].Stride, f.Planes[c].Stride)
		}
		if pl := got.Planes[c]; len(pl.Pix) != (pl.Height-1)*pl.Stride+pl.Width || cap(pl.Pix) != len(pl.Pix) {
			t.Errorf("plane %d: %d samples, capacity %d", c, len(pl.Pix), cap(pl.Pix))
		}
	}
}
