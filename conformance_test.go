// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The H.265.1 conformance bitstreams are an electronic attachment to an ITU
// Recommendation with no redistribution terms stated, so they are not committed.
// Tests that need them read the directory named by H265_CONFORMANCE_DIR (one
// subdirectory per stream, as `jvt --codec hevc fetch` lays them out) and skip
// without it. testdata/conformance.sha256 pins every bitstream and every trace
// file by SHA-256: a file that differs is a failure, not a different corpus.

type pinned struct {
	dir  string
	hash map[string]string // relative path -> hex SHA-256
}

func conformance(t *testing.T) *pinned {
	t.Helper()
	dir := os.Getenv("H265_CONFORMANCE_DIR")
	if dir == "" {
		// ⛔ In CI an oracle that is not there is a failure, never a skip: a
		// green run must mean the streams were read.
		if os.Getenv("CI") == "true" && os.Getenv("H265_CONFORMANCE_SKIP_OK") != "1" {
			t.Fatal("H265_CONFORMANCE_DIR is not set in CI")
		}
		t.Skip("H265_CONFORMANCE_DIR is not set")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("H265_CONFORMANCE_DIR %q is not a directory: %v", dir, err)
	}
	p := &pinned{dir: dir, hash: map[string]string{}}
	for _, name := range []string{"conformance.sha256", "conformance-rext.sha256"} {
		manifest, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(bytes.NewReader(manifest))
		for sc.Scan() {
			if f := strings.Fields(sc.Text()); len(f) == 2 {
				p.hash[f[1]] = f[0]
			}
		}
	}
	return p
}

// read returns a pinned file, refusing one whose hash is not the pinned one.
func (p *pinned) read(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(p.dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if want, ok := p.hash[rel]; !ok || hex.EncodeToString(sum[:]) != want {
		t.Fatalf("%s: SHA-256 %x is not the pinned %q", rel, sum, want)
	}
	return data
}

// streams lists the bitstreams of the manifest, sorted by the manifest's order.
func (p *pinned) streams() []string {
	var out []string
	for rel := range p.hash {
		if strings.HasSuffix(rel, ".bit") || strings.HasSuffix(rel, ".bin") {
			out = append(out, rel)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// traceRe is a header line of an HM trace: bit offset, element name,
// descriptor, ": ", value.
var traceRe = regexp.MustCompile(`^\s*\d+\s+(\S+)\s+(?:u\(\d+\)|ue\(v\)|se\(v\))\s*:\s*(-?\d+)\s*$`)

// firstValues reads the first value each syntax element of a trace takes.
func firstValues(trace []byte) map[string]int64 {
	out := map[string]int64{}
	sc := bufio.NewScanner(bytes.NewReader(trace))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		if m := traceRe.FindStringSubmatch(sc.Text()); m != nil {
			if _, seen := out[m[1]]; !seen {
				v, _ := strconv.ParseInt(m[2], 10, 64)
				out[m[1]] = v
			}
		}
	}
	return out
}

func TestEverySPSOfTheConformanceSuiteParses(t *testing.T) {
	p := conformance(t)
	n := 0
	for _, rel := range p.streams() {
		data := p.read(t, rel)
		units, err := SplitAnnexB(data)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		found, refused := 0, 0
		for _, u := range units {
			if u.Type != UnitSPS {
				continue
			}
			found++
			_, err := ParseSPS(u)
			switch {
			case errors.Is(err, ErrBypassAlignment):
				refused++
			case err != nil:
				t.Errorf("%s: %v", rel, err)
			}
		}
		if found == 0 {
			t.Errorf("%s: no sequence parameter set", rel)
		}
		// Exactly the streams that enable cabac_bypass_alignment are refused,
		// and for that reason alone.
		if want := alignedStreams[filepath.Dir(rel)]; want != (refused > 0) {
			t.Errorf("%s: refused for bypass alignment = %v, want %v", rel, refused > 0, want)
		}
		n++
	}
	t.Logf("%d streams", n)
}

// alignedStreams are the conformance streams whose SPS enables
// cabac_bypass_alignment_enabled_flag (the High Throughput 4:4:4 16 Intra
// streams, and others that turn it on), found by reading their SPS and pinned
// here so that a change in what is refused is noticed.
var alignedStreams = map[string]bool{
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_8BIT_RExt_Sony_1":                 true,
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_10BIT_RExt_Sony_1":                true,
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_12BIT_RExt_Sony_1":                true,
	"EXTPREC_HIGHTHROUGHPUT_444_16_INTRA_16BIT_RExt_Sony_1":                true,
	"GENERAL_16b_444_highThroughput_RExt_Sony_2":                           true,
	"WAVETILES_RExt_Sony_2":                                                true,
	"WPP_AND_TILE_AND_CABAC_BYPASS_ALIGN_1_HIGH_TP_444_14BIT_RExt_Apple_2": true,
}

// spsAliases name each SPS field as the traces do. HM changed its names over
// the years, so a field is checked against whichever name a trace has.
var spsAliases = []struct {
	names []string
	get   func(SPS) int64
}{
	{[]string{"pic_width_in_luma_samples"}, func(s SPS) int64 { return int64(s.CodedWidth) }},
	{[]string{"pic_height_in_luma_samples"}, func(s SPS) int64 { return int64(s.CodedHeight) }},
	{[]string{"bit_depth_luma_minus8"}, func(s SPS) int64 { return int64(s.BitDepthLuma) - 8 }},
	{[]string{"bit_depth_chroma_minus8"}, func(s SPS) int64 { return int64(s.BitDepthChroma) - 8 }},
	{[]string{"log2_min_coding_block_size_minus3", "log2_min_luma_coding_block_size_minus3"},
		func(s SPS) int64 { return int64(s.Log2MinCbSize) - 3 }},
	{[]string{"log2_diff_max_min_coding_block_size", "log2_diff_max_min_luma_coding_block_size"},
		func(s SPS) int64 { return int64(s.Log2CtbSize - s.Log2MinCbSize) }},
	{[]string{"log2_min_transform_block_size_minus2", "log2_min_luma_transform_block_size_minus2"},
		func(s SPS) int64 { return int64(s.Log2MinTbSize) - 2 }},
	{[]string{"log2_diff_max_min_transform_block_size", "log2_diff_max_min_luma_transform_block_size"},
		func(s SPS) int64 { return int64(s.Log2MaxTbSize - s.Log2MinTbSize) }},
	{[]string{"max_transform_hierarchy_depth_inter"}, func(s SPS) int64 { return int64(s.MaxTransformHierarchyDepthInter) }},
	{[]string{"max_transform_hierarchy_depth_intra"}, func(s SPS) int64 { return int64(s.MaxTransformHierarchyDepthIntra) }},
	{[]string{"scaling_list_enabled_flag"}, func(s SPS) int64 { return b2i(s.ScalingListEnabled) }},
	{[]string{"amp_enabled_flag"}, func(s SPS) int64 { return b2i(s.AMPEnabled) }},
	{[]string{"sample_adaptive_offset_enabled_flag"}, func(s SPS) int64 { return b2i(s.SAOEnabled) }},
	{[]string{"pcm_enabled_flag"}, func(s SPS) int64 { return b2i(s.PCM.Enabled) }},
	{[]string{"sps_temporal_mvp_enable_flag", "sps_temporal_mvp_enabled_flag"}, func(s SPS) int64 { return b2i(s.TemporalMVPEnabled) }},
	{[]string{"sps_strong_intra_smoothing_enable_flag", "strong_intra_smoothing_enabled_flag"}, func(s SPS) int64 { return b2i(s.StrongIntraSmoothing) }},
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// TestSPSAgainstTheTraces checks the parsed first SPS of each stream that
// carries a trace file against the values the trace file reports, field by field.
func TestSPSAgainstTheTraces(t *testing.T) {
	p := conformance(t)
	checked, fields, skipped := 0, 0, 0
	for _, rel := range p.streams() {
		dir := filepath.Dir(rel)
		var traceRel string
		for cand := range p.hash {
			if filepath.Dir(cand) == dir && (strings.HasSuffix(cand, ".trc") || strings.HasSuffix(cand, "_trace.txt")) {
				traceRel = cand
			}
		}
		if traceRel == "" {
			continue
		}
		units, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		var sps SPS
		for _, u := range units {
			if u.Type == UnitSPS {
				if sps, err = ParseSPS(u); err != nil {
					t.Fatalf("%s: %v", rel, err)
				}
				break
			}
		}
		want := firstValues(p.read(t, traceRel))
		if _, ok := want["pic_width_in_luma_samples"]; !ok {
			// DELTAQP_A and SAO_E/F/G carry traces of another kind (slice data
			// only, or no header): nothing to compare a parameter set with.
			skipped++
			continue
		}
		for _, f := range spsAliases {
			var got int64 = f.get(sps)
			ok := false
			for _, name := range f.names {
				if v, present := want[name]; present {
					ok = true
					fields++
					if v != got {
						t.Errorf("%s: %s: trace %d, parsed %d", rel, name, v, got)
					}
				}
			}
			if !ok {
				t.Errorf("%s: the trace has none of %v", rel, f.names)
			}
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d streams with a header trace", checked)
	}
	t.Logf("%d streams compared (%d field values), %d traces without a header", checked, fields, skipped)
}
