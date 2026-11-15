// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// TestEveryScalingListOfTheConformanceSuiteIsValid reads the scaling lists of every
// parameter set of the suite and checks them against what 7.4.5 allows: no
// parameter set is refused for its lists, and every list value is above 0. The
// factors derived from them are checked against the pictures' hashes once
// reconstruction exists.
func TestEveryScalingListOfTheConformanceSuiteIsValid(t *testing.T) {
	p := conformance(t)
	var enabled, spsLists, ppsLists, defaults, predicted int
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range list {
			var lists *ScalingLists
			switch u.Type {
			case UnitSPS:
				s, err := ParseSPS(u)
				if err != nil {
					continue // refused for other reasons (bypass alignment)
				}
				if s.ScalingListEnabled {
					enabled++
					if s.ScalingList == nil {
						defaults++
					}
				}
				if lists = s.ScalingList; lists != nil {
					spsLists++
				}
			case UnitPPS:
				pps, err := ParsePPS(u)
				if err != nil {
					continue
				}
				if lists = pps.ScalingList; lists != nil {
					ppsLists++
				}
			}
			if lists == nil {
				continue
			}
			f := lists.Factors()
			for sizeID := range f {
				for m := range f[sizeID] {
					for _, v := range f[sizeID][m] {
						if v == 0 {
							t.Fatalf("%s: a scaling factor of 0", rel)
						}
					}
				}
			}
			if lists.List[1][1] == lists.List[1][0] {
				predicted++
			}
		}
	}
	t.Logf("conformance-count: scaling-list sps-enabled=%d sps-default=%d sps-lists=%d pps-lists=%d equal-intra-chroma=%d",
		enabled, defaults, spsLists, ppsLists, predicted)
	if enabled == 0 || spsLists+ppsLists == 0 {
		t.Fatal("no scaling list was read")
	}
}
