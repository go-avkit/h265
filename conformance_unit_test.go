// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// TestUnitHeadersOfTheConformanceSuite checks the two header bytes of every unit
// of the suite against what the streams say elsewhere. Unit tests build their units
// by hand, so a header field read wrongly from real bytes can hide from them: this
// is the check that would have caught TemporalID being TemporalId + 2. Every slice
// segment's TemporalID is below the sps_max_sub_layers_minus1 + 1 of its sequence
// parameter set, and a stream that uses sub-layers has units above 0.
func TestUnitHeadersOfTheConformanceSuite(t *testing.T) {
	p := conformance(t)
	var units, slices, upper, maxTid, beyondLayers, nonBase int
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		spsByID, ppsByID := map[uint32]SPS{}, map[uint32]PPS{}
		for _, u := range list {
			units++
			if u.LayerID != 0 {
				nonBase++
			}
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
				r := newSticky(u.Unescape())
				r.bit()
				if u.Type.IsRandomAccess() {
					r.bit()
				}
				pps, ok1 := ppsByID[r.ue()]
				sps, ok2 := spsByID[pps.SPSID]
				if !ok1 || !ok2 {
					continue
				}
				slices++
				if u.TemporalID > 0 {
					upper++
				}
				maxTid = max(maxTid, int(u.TemporalID))
				if int(u.TemporalID) >= int(sps.MaxSubLayers) {
					beyondLayers++
					t.Errorf("%s: a slice of sub-layer %d in a sequence of %d", rel, u.TemporalID, sps.MaxSubLayers)
				}
			}
		}
	}
	t.Logf("conformance-count: unit-headers units=%d slices=%d above-base-sublayer=%d max-temporal-id=%d non-base-layer=%d", units, slices, upper, maxTid, nonBase)
	if upper == 0 || maxTid < 2 || beyondLayers != 0 {
		t.Fatalf("%d slices above the base sub-layer, highest %d, %d beyond their sequence's layers", upper, maxTid, beyondLayers)
	}
}
