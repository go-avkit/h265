// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path"
	"strings"
	"testing"
)

// writePicture feeds the planes of an output picture to h the way the reference decoder
// writes a yuv file: planar, one byte a sample, or two (little-endian) when either bit
// depth is above 8. A 4:0:0 picture is its luma alone.
func writePicture(h hash.Hash, pic *DecodedPicture) {
	wide := false
	for c := 0; c < pic.Components; c++ {
		wide = wide || pic.BitDepth[c] > 8
	}
	for c := 0; c < pic.Components; c++ {
		pl := &pic.Planes[c]
		row := make([]byte, 0, 2*pl.Width)
		for y := 0; y < pl.Height; y++ {
			row = row[:0]
			for x := 0; x < pl.Width; x++ {
				v := pl.At(x, y)
				row = append(row, byte(v))
				if wide {
					row = append(row, byte(v>>8))
				}
			}
			h.Write(row)
		}
	}
}

// TestStreamsOfTheConformanceSuiteMatchTheirShippedMD5 decodes every stream with the Decoder and compares
// the md5 of its output (output order, cropped to the conformance window, as a yuv file)
// with the md5 files the suite ships. The files are of two kinds: the md5 of the decoded
// yuv, which gates the decode, and the md5 of the bitstream itself (IPCM_A/B_RExt),
// which proves nothing about it and is only counted. For the streams that carry no
// decoded picture hash SEI this is the only oracle there is.
func TestStreamsOfTheConformanceSuiteMatchTheirShippedMD5(t *testing.T) {
	p := conformance(t)
	var hashless, hashed int
	var matchedHashless, matchedHashed, ofBitstream, noMD5, mismatched, refused int
	var listed, ungated []string
	for _, rel := range p.streams() {
		dirName := path.Dir(rel)
		data := p.read(t, rel)
		list, err := SplitAnnexB(data)
		if err != nil {
			t.Fatal(err)
		}
		d := NewDecoder(Options{VerifyHash: true})
		sum := md5.New()
		pictures := 0
		for _, u := range list {
			out, _ := d.Decode(u)
			for _, o := range out {
				writePicture(sum, o.Picture)
				pictures++
			}
		}
		out, _ := d.Flush()
		for _, o := range out {
			writePicture(sum, o.Picture)
			pictures++
		}
		s := d.Stats()
		if pictures == 0 {
			refused++
			continue
		}
		got := hex.EncodeToString(sum.Sum(nil))
		bit := md5.Sum(data)
		bitHex := hex.EncodeToString(bit[:])
		var files []string
		for k := range p.hash {
			if path.Dir(k) == dirName && strings.HasSuffix(k, ".md5") {
				files = append(files, k)
			}
		}
		sortStrings(files)
		kinds := map[string]bool{}
		for _, f := range files {
			want := shippedMD5(p.read(t, f))
			if want == "" {
				continue
			}
			switch {
			case want == bitHex:
				kinds["bitstream"] = true
			case want == got:
				kinds["output"] = true
			default:
				kinds["other"] = true
			}
		}
		isHashless := s.Verified+s.Mismatched == 0
		if isHashless {
			hashless++
			if !kinds["output"] { // no md5 of a decoded yuv to hold it to
				ungated = append(ungated, fmt.Sprintf("%s: md5 files %d, of the bitstream %v", rel, len(files), kinds["bitstream"]))
			}
		} else {
			hashed++
		}
		switch {
		case kinds["output"] && isHashless:
			matchedHashless++
		case kinds["output"]:
			matchedHashed++
		case kinds["bitstream"]:
			ofBitstream++
		case len(files) == 0:
			noMD5++
		default:
			mismatched++
			if len(listed) < 40 {
				listed = append(listed, fmt.Sprintf("%s (hashless %v, %d pictures): output md5 %s, files %v", rel, isHashless, pictures, got, files))
			}
		}
		_ = errors.Is
	}
	for _, l := range listed {
		t.Log(l)
	}
	for _, l := range ungated {
		t.Log("hashless and not gated:", l)
	}
	t.Log(fmt.Sprintf("conformance-count: output-md5 hashless-streams=%d matched=%d not-gated=%d hashed-streams=%d matched=%d md5-of-bitstream=%d no-md5-file=%d mismatched=%d refused=%d",
		hashless, matchedHashless, len(ungated), hashed, matchedHashed, ofBitstream, noMD5, mismatched, refused))
	if mismatched != 0 {
		t.Fatalf("%d streams whose output md5 is not the shipped one", mismatched)
	}
}

// shippedMD5 is the checksum an md5 file of the suite states: the files are
// "<md5> *<file>" under comment lines (MD5summer), or "MD5 (<file>) = <md5>" (md5), or
// the bare digest; "" when there is none.
func shippedMD5(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.LastIndex(line, "= "); strings.HasPrefix(line, "MD5 (") && i >= 0 {
			line = line[i+2:]
		}
		if f := strings.Fields(line); len(f) > 0 && len(f[0]) == 32 {
			return strings.ToLower(f[0])
		}
	}
	return ""
}
