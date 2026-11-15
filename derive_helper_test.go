// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import "testing"

// derive is Derive for a test that expects the set to be derivable.
func derive(t testing.TB, refs PictureRefs, poc int32, sps SPS) RefPicSet {
	t.Helper()
	got, err := Derive(refs, poc, sps)
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	return got
}
