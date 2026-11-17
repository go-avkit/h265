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
					if samePlace(&kept[i].Planes[0], 0, &kept[j].Planes[0], 0) {
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
	for _, depth := range []int{8, 10} { // byte planes and 16-bit planes
		sps := mustSPS(t, set{chroma: 1, width: 32, height: 16, window: [4]uint32{1, 2, 1, 0}})
		sps.BitDepthLuma, sps.BitDepthChroma = uint8(depth), uint8(depth)
		f := NewDecodedPicture(sps)
		got := f.Crop(sps)
		for c, first := range []int{2*32 + 2, 1*16 + 1} {
			if !samePlace(&got.Planes[c], 0, &f.Planes[c], first) {
				t.Errorf("depth %d plane %d is a copy", depth, c)
			}
			if got.Planes[c].Stride != f.Planes[c].Stride {
				t.Errorf("depth %d plane %d stride %d, the picture's is %d", depth, c, got.Planes[c].Stride, f.Planes[c].Stride)
			}
			pl := got.Planes[c]
			n, capacity := len(pl.Pix16), cap(pl.Pix16)
			if depth == 8 {
				n, capacity = len(pl.Pix8), cap(pl.Pix8)
			}
			if n != (pl.Height-1)*pl.Stride+pl.Width || capacity != n || pl.Is8() != (depth == 8) {
				t.Errorf("depth %d plane %d: %d samples, capacity %d, Is8 %v", depth, c, n, capacity, pl.Is8())
			}
		}
	}
}

// TestCloneIsAnOwnedWritableCopy: the copy equals the picture, is contiguous, and writing to it
// leaves the decoder's picture (and so the later pictures predicted from it) untouched.
func TestCloneIsAnOwnedWritableCopy(t *testing.T) {
	for _, depth := range []int{8, 10} {
		sps := mustSPS(t, set{chroma: 1, width: 32, height: 16, window: [4]uint32{1, 2, 1, 0}})
		sps.BitDepthLuma, sps.BitDepthChroma = uint8(depth), uint8(depth)
		f := NewDecodedPicture(sps)
		for c := 0; c < 3; c++ {
			for i := 0; i < f.Planes[c].Len(); i++ {
				f.Planes[c].set(i, i%251)
			}
		}
		view := f.Crop(sps)
		want := pictureMD5(view)
		cl := view.Clone()
		if got := pictureMD5(cl); got != want {
			t.Fatalf("depth %d: the clone hashes %s, the view %s", depth, got, want)
		}
		for c := 0; c < cl.Components; c++ {
			pl := &cl.Planes[c]
			if pl.Stride != pl.Width || pl.Len() != pl.Width*pl.Height || pl.Is8() != (depth == 8) {
				t.Errorf("depth %d plane %d is not contiguous or not of its kind: stride %d width %d length %d", depth, c, pl.Stride, pl.Width, pl.Len())
			}
			for i := 0; i < pl.Len(); i++ {
				pl.set(i, pl.get(i)^0xff)
			}
		}
		if got := pictureMD5(view); got != want {
			t.Errorf("depth %d: writing to the clone changed the picture: %s, was %s", depth, got, want)
		}
	}
}
