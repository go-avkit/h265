// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// The MV-HEVC conformance streams (JCT-3V / JCT-VC, draft_conformance/MV-HEVC)
// are the witnesses for Unit.LayerID (h265#54): every one has units with
// nuh_layer_id above 0, which a base-layer decoder must ignore, and each ships the
// md5 of its base layer's yuv (<name>_0.yuv). They are pinned in their own
// manifest so that the 196-stream counts of the single-layer suite stay what they
// are; CI fetches them into the same directory as the others.

// layerStreams reads the manifest of the multi-layer streams: stream directory
// -> relative path of its bitstream and of its md5 file.
func layerStreams(t *testing.T) (dir string, hashes map[string]string, streams []string) {
	t.Helper()
	p := conformance(t) // skips, or fails in CI, when the corpus is missing
	manifest, err := os.ReadFile(filepath.Join("testdata", "conformance-mvhevc.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	hashes = map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(manifest))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) == 2 {
			hashes[f[1]] = f[0]
			if strings.HasSuffix(f[1], ".bit") {
				streams = append(streams, f[1])
			}
		}
	}
	sortStrings(streams)
	return p.dir, hashes, streams
}

func readLayerFile(t *testing.T, dir string, hashes map[string]string, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want, ok := hashes[rel]; !ok || hex.EncodeToString(sum[:]) != want {
		t.Fatalf("%s: SHA-256 %x is not the pinned %q", rel, sum, want)
	}
	return data
}

// TestMultiLayerStreamsOfTheConformanceSuiteDecodeTheirBaseLayer decodes each
// MV-HEVC stream with the Decoder, which must skip every unit of a layer above 0
// and output exactly the base layer: the md5 of its pictures is the shipped
// <name>_0.yuv md5. The units' LayerID is read from real bytes here, which no
// single-layer stream does.
func TestMultiLayerStreamsOfTheConformanceSuiteDecodeTheirBaseLayer(t *testing.T) {
	dir, hashes, streams := layerStreams(t)
	if len(streams) != 9 {
		t.Fatalf("%d streams in the manifest, want 9", len(streams))
	}
	var units, nonBase, pictures int
	maxLayer := uint8(0)
	for _, rel := range streams {
		list, err := SplitAnnexB(readLayerFile(t, dir, hashes, rel))
		if err != nil {
			t.Fatal(err)
		}
		var streamNonBase int
		for _, u := range list {
			units++
			if u.LayerID != 0 {
				streamNonBase++
				maxLayer = max(maxLayer, u.LayerID)
			}
		}
		if streamNonBase == 0 {
			t.Errorf("%s: no unit of a layer above 0", rel)
		}
		nonBase += streamNonBase

		want := ""
		for _, l := range strings.Split(string(readLayerFile(t, dir, hashes, path.Dir(rel)+"/"+strings.TrimSuffix(path.Base(rel), ".bit")+".md5")), "\n") {
			if f := strings.Fields(l); len(f) == 2 && strings.HasSuffix(f[1], "_0.yuv") {
				want = strings.ToLower(f[0])
			}
		}
		if want == "" {
			t.Fatalf("%s: no md5 of the base layer in its md5 file", rel)
		}

		d := NewDecoder(Options{VerifyHash: true})
		sum := md5.New()
		var errs []error
		n := 0
		for _, u := range list {
			out, err := d.Decode(u)
			if err != nil {
				errs = append(errs, err)
			}
			for _, o := range out {
				writePicture(sum, o.Picture)
				n++
			}
		}
		out, err := d.Flush()
		if err != nil {
			errs = append(errs, err)
		}
		for _, o := range out {
			writePicture(sum, o.Picture)
			n++
		}
		s := d.Stats()
		if got := hex.EncodeToString(sum.Sum(nil)); got != want || len(errs) != 0 || s.Failed != 0 || s.Mismatched != 0 || n == 0 {
			t.Errorf("%s: output md5 %s, want %s; %d pictures, stats %+v, errors %v", rel, got, want, n, s, errs)
		}
		pictures += n
	}
	t.Logf("conformance-count: multi-layer streams=%d units=%d non-base-layer=%d max-layer-id=%d base-pictures=%d", len(streams), units, nonBase, maxLayer, pictures)
}
