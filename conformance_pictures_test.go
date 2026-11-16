// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
	"testing"
)

// TestPicturesOfTheConformanceSuiteMatchTheirHashes decodes every stream of the suite
// with the Decoder, from its units to its pictures, and compares each picture with its
// decoded picture hash SEI message (D.3.20), the oracle no part of this package wrote.
// A picture that fails or does not match is counted and listed, never skipped silently.
func TestPicturesOfTheConformanceSuiteMatchTheirHashes(t *testing.T) {
	p := conformance(t)
	var total DecoderStats
	var outputs, refusedSPS int
	var listed []string
	for _, rel := range p.streams() {
		list, err := SplitAnnexB(p.read(t, rel))
		if err != nil {
			t.Fatal(err)
		}
		d := NewDecoder(Options{VerifyHash: true})
		bad, refused := 0, false
		note := func(err error) {
			if err == nil {
				return
			}
			bad++
			if bad <= 2 {
				listed = append(listed, rel+": "+err.Error())
			}
		}
		for _, u := range list {
			out, err := d.Decode(u)
			outputs += len(out)
			switch {
			case err == nil || isRefusedHeader(err):
			case errors.Is(err, ErrUnsupportedSPS):
				refusedSPS++ // cabac_bypass_alignment, which the engine refuses
				refused = true
			case errors.Is(err, ErrMissingParameterSet) && refused:
				// its slices follow the refused set
			default:
				note(err)
			}
		}
		out, err := d.Flush()
		outputs += len(out)
		note(err)
		s := d.Stats()
		total.Pictures += s.Pictures
		total.SkippedRASL += s.SkippedRASL
		total.Failed += s.Failed
		total.Verified += s.Verified
		total.VerifiedInter += s.VerifiedInter
		total.Mismatched += s.Mismatched
		total.Unhashed += s.Unhashed
	}
	for _, s := range listed {
		t.Log(s)
	}
	t.Log(fmt.Sprintf("conformance-count: picture-decode verified=%d inter-verified=%d mismatched=%d failed=%d unhashed=%d skipped-rasl=%d",
		total.Verified, total.VerifiedInter, total.Mismatched, total.Failed, total.Unhashed, total.SkippedRASL))
	t.Log(fmt.Sprintf("conformance-count: decoder-output pictures=%d refused-sps=%d unexplained-errors=%d", outputs, refusedSPS, len(listed)))
	if total.Verified == 0 || total.Mismatched != 0 || total.Failed != 0 || len(listed) != 0 {
		t.Fatalf("%d pictures match their hash, %d do not, %d could not be decoded", total.Verified, total.Mismatched, total.Failed)
	}
}

// isRefusedHeader says err is a slice segment header the package refuses (56 of the
// suite's, counted by the header suite).
func isRefusedHeader(err error) bool { return errors.Is(err, ErrSliceHeader) }
