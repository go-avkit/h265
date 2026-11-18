// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"crypto/md5"
	"encoding/hex"
	"path"
	"sort"
	"strings"
	"testing"
)

// TestAlignedBypassStreamsOfTheConformanceSuiteMatchTheirHashes is the witness for
// SPS.Range.CabacBypassAlignment (h265#54): the seven streams whose SPS sets
// cabac_bypass_alignment_enabled_flag decode without an error, every picture
// matches its decoded picture hash SEI (or, for a stream without one, the md5 of
// the yuv the suite ships), and no stream is gated by neither. The alignment of
// 9.3.4.3.6 changes ivlCurrRange, so a wrong alignment corrupts the first
// sub-block with escape data, the hash of the first picture fails, and it does so
// in every one of the seven.
func TestAlignedBypassStreamsOfTheConformanceSuiteMatchTheirHashes(t *testing.T) {
	p := conformance(t)
	var dirs []string
	for dir := range alignedStreams {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	if len(dirs) != 7 {
		t.Fatalf("%d streams in alignedStreams, want 7", len(dirs))
	}
	var pictures, verified int
	for _, dir := range dirs {
		var rel string
		for _, s := range p.streams() {
			if path.Dir(s) == dir {
				rel = s
			}
		}
		if rel == "" {
			t.Fatalf("%s: not in the manifests", dir)
		}
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
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
		if len(errs) != 0 || s.Failed != 0 || s.Mismatched != 0 || n == 0 {
			t.Errorf("%s: %d pictures out, stats %+v, errors %v", rel, n, s, errs)
			continue
		}
		got := hex.EncodeToString(sum.Sum(nil))
		matchedMD5 := false
		for k := range p.hash {
			if path.Dir(k) == dir && strings.HasSuffix(k, ".md5") && shippedMD5(p.read(t, k)) == got {
				matchedMD5 = true
			}
		}
		if s.Verified == 0 && !matchedMD5 {
			t.Errorf("%s: %d pictures decoded, none verified by a hash SEI and the output md5 %s is not a shipped one", rel, n, got)
		}
		pictures += n
		verified += s.Verified
		t.Logf("%s: pictures=%d verified=%d unhashed=%d shipped-md5-matched=%v", dir, n, s.Verified, s.Unhashed, matchedMD5)
	}
	t.Logf("conformance-count: bypass-aligned streams=%d pictures=%d verified=%d", len(dirs), pictures, verified)
}
