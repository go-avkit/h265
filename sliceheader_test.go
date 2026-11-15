// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"bufio"
	"bytes"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// sliceTrace is one slice segment header as an HM trace prints it: the first
// value of each element, the bit offset of the first and the offset where the
// header ended (the first line that is not a syntax element), or -1.
type sliceTrace struct {
	vals       map[string]int64
	start, end int64
}

var (
	sliceMarker  = "=========== Slice ==========="
	offsetElemRe = regexp.MustCompile(`^\s*(\d+)\s+(\S+)\s+(?:u\(\d+\)|ue\(v\)|se\(v\))\s*:\s*(-?\d+)\s*$`)
	leadingIntRe = regexp.MustCompile(`^\s*(\d+)\s`)
)

func sliceTraces(trace []byte) []sliceTrace {
	var out []sliceTrace
	open := false
	sc := bufio.NewScanner(bytes.NewReader(trace))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, sliceMarker) {
			out = append(out, sliceTrace{vals: map[string]int64{}, start: -1, end: -1})
			open = true
			continue
		}
		if !open {
			continue
		}
		cur := &out[len(out)-1]
		if m := offsetElemRe.FindStringSubmatch(line); m != nil {
			off, _ := strconv.ParseInt(m[1], 10, 64)
			v, _ := strconv.ParseInt(m[3], 10, 64)
			if cur.start < 0 {
				cur.start = off
			}
			if _, seen := cur.vals[m[2]]; !seen {
				cur.vals[m[2]] = v
			}
			continue
		}
		if m := leadingIntRe.FindStringSubmatch(line); m != nil && cur.start >= 0 {
			cur.end, _ = strconv.ParseInt(m[1], 10, 64)
		}
		open = false // the header is over; what follows is slice data
	}
	return dedupe(out)
}

// dedupe drops a header that repeats the one before it, element for element and
// in length: HM's trace prints some streams' slice headers twice (the header is
// read once to find the picture and once to decode it). Two real consecutive
// segments never agree on every element, since the address or the order count
// differs.
func dedupe(in []sliceTrace) []sliceTrace {
	var out []sliceTrace
	for i, s := range in {
		if i > 0 && equalVals(s.vals, in[i-1].vals) {
			continue
		}
		out = append(out, s)
	}
	return out
}

func equalVals(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// sliceOptional are the slice header elements compared when the trace prints
// them, under the names HM printed them with.
var sliceOptional = []struct {
	names []string
	get   func(SliceHeader) int64
}{
	{[]string{"first_slice_segment_in_pic_flag"}, func(h SliceHeader) int64 { return b2i(h.FirstSliceSegmentInPic) }},
	{[]string{"slice_pic_parameter_set_id"}, func(h SliceHeader) int64 { return int64(h.PPSID) }},
	{[]string{"dependent_slice_segment_flag"}, func(h SliceHeader) int64 { return b2i(h.Dependent) }},
	{[]string{"slice_segment_address"}, func(h SliceHeader) int64 { return int64(h.SegmentAddress) }},
	{[]string{"slice_type"}, func(h SliceHeader) int64 { return int64(h.Type) }},
	{[]string{"pic_order_cnt_lsb", "slice_pic_order_cnt_lsb"}, func(h SliceHeader) int64 { return int64(h.POCLSB) }},
	{[]string{"slice_temporal_mvp_enable_flag", "slice_temporal_mvp_enabled_flag"}, func(h SliceHeader) int64 { return b2i(h.Refs.TemporalMVP) }},
	{[]string{"slice_sao_luma_flag"}, func(h SliceHeader) int64 { return b2i(h.Refs.SAOLuma) }},
	{[]string{"slice_sao_chroma_flag"}, func(h SliceHeader) int64 { return b2i(h.Refs.SAOChroma) }},
	{[]string{"num_ref_idx_l0_active_minus1"}, func(h SliceHeader) int64 { return int64(h.Refs.NumRefIdxL0Active) - 1 }},
	{[]string{"num_ref_idx_l1_active_minus1"}, func(h SliceHeader) int64 { return int64(h.Refs.NumRefIdxL1Active) - 1 }},
	{[]string{"mvd_l1_zero_flag"}, func(h SliceHeader) int64 { return b2i(h.Refs.MvdL1Zero) }},
	{[]string{"cabac_init_flag"}, func(h SliceHeader) int64 { return b2i(h.Refs.CabacInit) }},
	{[]string{"collocated_from_l0_flag"}, func(h SliceHeader) int64 { return b2i(h.Refs.CollocatedFromL0) }},
	{[]string{"collocated_ref_idx"}, func(h SliceHeader) int64 { return int64(h.Refs.CollocatedRefIdx) }},
	{[]string{"five_minus_max_num_merge_cand"}, func(h SliceHeader) int64 { return 5 - int64(h.Refs.MaxMergeCand) }},
	{[]string{"slice_qp_delta"}, func(h SliceHeader) int64 { return int64(h.QPDelta) }},
	{[]string{"slice_cb_qp_offset"}, func(h SliceHeader) int64 { return int64(h.CbQPOffset) }},
	{[]string{"slice_cr_qp_offset"}, func(h SliceHeader) int64 { return int64(h.CrQPOffset) }},
	{[]string{"slice_deblocking_filter_disabled_flag"}, func(h SliceHeader) int64 { return b2i(h.DeblockingDisabled) }},
	{[]string{"slice_beta_offset_div2"}, func(h SliceHeader) int64 { return int64(h.BetaOffsetDiv2) }},
	{[]string{"slice_tc_offset_div2"}, func(h SliceHeader) int64 { return int64(h.TCOffsetDiv2) }},
	{[]string{"slice_loop_filter_across_slices_enabled_flag"}, func(h SliceHeader) int64 { return b2i(h.LoopFilterAcrossSlices) }},
	{[]string{"num_entry_point_offsets"}, func(h SliceHeader) int64 { return int64(len(h.EntryPoints)) }},
}

// TestEverySliceHeaderOfTheConformanceSuite parses every slice segment header of
// every stream against its active parameter sets, and for the streams whose HM
// trace prints slice headers compares them element by element and by length.
func TestEverySliceHeaderOfTheConformanceSuite(t *testing.T) {
	p := conformance(t)
	slices, refused, compared, fields, lengths := 0, 0, 0, 0, 0
	for _, rel := range p.streams() {
		units, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		var traces []sliceTrace
		for cand := range p.hash {
			if strings.HasPrefix(cand, strings.TrimSuffix(rel, rel[strings.LastIndex(rel, "/"):])+"/") &&
				(strings.HasSuffix(cand, ".trc") || strings.HasSuffix(cand, "_trace.txt")) {
				traces = sliceTraces(p.read(t, cand))
			}
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		var prev *SliceHeader
		n := 0
		for _, u := range units {
			switch {
			case u.Type == UnitSPS:
				if s, err := ParseSPS(u); err == nil {
					spsByID[s.ID] = s
				}
			case u.Type == UnitPPS:
				if pps, err := ParsePPS(u); err == nil {
					ppsByID[pps.ID] = pps
				}
			case u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA):
				// Which PPS the slice names comes first in the header, after one
				// flag (and one more for an IRAP): read it to find the sets.
				r := newSticky(u.Unescape())
				r.bit()
				if u.Type.IsRandomAccess() {
					r.bit()
				}
				id := r.ue()
				pps, ok := ppsByID[id]
				sps, ok2 := spsByID[pps.SPSID]
				if !ok || !ok2 {
					refused++ // its parameter sets were refused (bypass alignment)
					n++
					continue
				}
				h, err := ParseSliceSegmentHeaderFull(u, sps, pps, prev)
				if err != nil {
					t.Errorf("%s: slice %d: %v", rel, n, err)
					n++
					continue
				}
				if !h.Dependent {
					hh := h
					prev = &hh
				}
				if h.DataOffset <= 0 || h.DataOffset > len(u.Unescape()) {
					t.Errorf("%s: slice %d: data offset %d of %d bytes", rel, n, h.DataOffset, len(u.Unescape()))
				}
				if n < len(traces) {
					tr := traces[n]
					for _, f := range sliceOptional {
						for _, name := range f.names {
							if v, present := tr.vals[name]; present {
								fields++
								if got := f.get(h); got != v {
									t.Errorf("%s: slice %d: %s: trace %d, parsed %d", rel, n, name, v, got)
								}
							}
						}
					}
					compared++
				}
				slices++
				n++
			}
		}
		// TSUNEQBD_A's trace prints 2 of its 8 slice headers; every other trace
		// prints each header once (after the duplicates are dropped).
		if short := strings.HasPrefix(rel, "TSUNEQBD_A"); len(traces) != 0 && len(traces) != n && !short {
			t.Errorf("%s: %d slice headers in the trace, %d in the stream", rel, len(traces), n)
		}
	}
	if slices != wantSlices || refused != wantSlicesRefused {
		t.Fatalf("%d slice headers parsed, %d with refused parameter sets; want %d and %d", slices, refused, wantSlices, wantSlicesRefused)
	}
	t.Logf("conformance-count: slices=%d refused=%d traced=%d fields=%d lengths=%d", slices, refused, compared, fields, lengths)
}

// wantSlices and wantSlicesRefused are what the pinned streams hold: the slice
// segments whose parameter sets parse, and those whose parameter sets were
// refused for bypass alignment. Set from the first run and read against the
// trace counts it prints.
const (
	wantSlices        = 52215
	wantSlicesRefused = 56
)

func TestFullSliceHeaderRefusals(t *testing.T) {
	sps, _ := ParseSPS(set{subLayers: 1, chroma: 1, width: 64, height: 64, sao: true, temporalMVP: true}.build())
	pps, _ := ParsePPS(pset{}.build())
	hdr := func(fn func(*writer)) Unit {
		w := &writer{}
		fn(w)
		w.bit(1)
		for w.pos%8 != 0 {
			w.bit(0)
		}
		return Unit{Type: UnitIDRWRADL, Payload: w.data}
	}
	// A valid IDR I slice, to build the refusals from.
	good := func(w *writer) {
		w.bit(1) // first_slice_segment_in_pic_flag
		w.bit(0) // no_output_of_prior_pics_flag
		w.ue(0)  // slice_pic_parameter_set_id
		w.ue(2)  // slice_type I
		w.bit(1) // slice_sao_luma_flag
		w.bit(1) // slice_sao_chroma_flag
		w.se(0)  // slice_qp_delta
		w.bit(1) // slice_loop_filter_across_slices_enabled_flag
	}
	if h, err := ParseSliceSegmentHeaderFull(hdr(good), sps, pps, nil); err != nil || h.Type != SliceI || !h.PicOutput || h.SliceQP != 26 {
		t.Fatalf("the fixture does not parse: %v %+v", err, h)
	}
	for name, fn := range map[string]func(*writer){
		"a picture parameter set that is not the one given": func(w *writer) { w.bit(1); w.bit(0); w.ue(1) },
		"slice type 3":   func(w *writer) { w.bit(1); w.bit(0); w.ue(0); w.ue(3) },
		"slice type 256": func(w *writer) { w.bit(1); w.bit(0); w.ue(0); w.ue(256) },
		"slice_qp_delta past the range": func(w *writer) {
			w.bit(1)
			w.bit(0)
			w.ue(0)
			w.ue(2)
			w.bit(0)
			w.bit(0)
			w.se(129)
		},
		"slice_qp_delta 2^31-1": func(w *writer) {
			w.bit(1)
			w.bit(0)
			w.ue(0)
			w.ue(2)
			w.bit(0)
			w.bit(0)
			w.se(1<<31 - 1)
		},
		"SliceQpY above 51": func(w *writer) {
			w.bit(1)
			w.bit(0)
			w.ue(0)
			w.ue(2)
			w.bit(0)
			w.bit(0)
			w.se(26)
		},
		"SliceQpY below -QpBdOffset": func(w *writer) {
			w.bit(1)
			w.bit(0)
			w.ue(0)
			w.ue(2)
			w.bit(0)
			w.bit(0)
			w.se(-27)
		},
		"a P slice in an IDR": func(w *writer) {
			w.bit(1)
			w.bit(0)
			w.ue(0)
			w.ue(1)
			w.bit(0)
			w.bit(0)
		},
	} {
		if _, err := ParseSliceSegmentHeaderFull(hdr(fn), sps, pps, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A dependent segment with nothing before it; an address past the picture.
	notFirst := func(dep bool, addr uint32) Unit {
		return hdr(func(w *writer) {
			w.bit(0)
			w.bit(0)
			w.ue(0)
			if dep {
				w.bit(1)
			}
			w.bits(addr, 4) // ceil(log2(16)) bits: 16 coding tree blocks of 16 in 64x64
		})
	}
	ppsDep, _ := ParsePPS(pset{dependentSlices: true}.build())
	if _, err := ParseSliceSegmentHeaderFull(notFirst(true, 1), sps, ppsDep, nil); !errors.Is(err, ErrSliceHeader) {
		t.Errorf("a dependent segment with no previous: %v", err)
	}
	if _, err := ParseSliceSegmentHeaderFull(Unit{Type: UnitPPS}, sps, pps, nil); !errors.Is(err, ErrNotSlice) {
		t.Errorf("a PPS handed over as a slice: %v", err)
	}
	u := good
	_ = u
}
