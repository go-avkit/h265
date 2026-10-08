// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// POC is a picture's order count: where it belongs in the order a viewer sees,
// which is not the order it was coded in.
type POC struct {
	// Value is the count itself.
	Value int32
	// LSB is what the slice header stated, before the high bits were carried in.
	LSB uint32
	// Carried says this picture is the one later pictures count from. Only a
	// picture of sub-layer zero that is neither leading nor sub-layer
	// non-reference does that, and a counter that carried from every picture
	// would place everything after the first dropped one wrongly.
	Carried bool
}

// ErrNoOrder means the unit states no order of its own.
var ErrNoOrder = errors.New("h265: the unit carries no picture order count")

// POCCounter derives picture order counts across a coded video sequence.
//
// ⛔ It has to be a counter rather than a function because the format sends only
// the LOW bits of the count. The high bits are carried forward from an earlier
// picture, and the one they come from is not simply the previous picture: it is
// the previous one of sub-layer zero that is neither a leading picture nor a
// sub-layer non-reference. Those are exactly the pictures a thinned stream may
// have dropped, so counting from them would make the order depend on what was
// thrown away.
//
// Feed it every coded picture of the sequence, in decode order, one slice per
// picture: the first slice segment of each. A picture carried by several
// segments is one picture here.
type POCCounter struct {
	// The low and high parts are kept APART, as 8.3.1 states them:
	// prevPicOrderCntLsb is what that picture's slice header said, and
	// prevPicOrderCntMsb is what was carried into it. Reconstructing the two
	// from their sum with a remainder works only while the sum is positive --
	// a negative count would give a negative remainder and a high part short by
	// one step -- and an order count is signed.
	prevLSB int32
	prevMSB int32
	started bool
}

// Reset forgets every picture counted so far. It is what a seek is: the pictures
// before the new position are not the ones the counts should follow from.
func (c *POCCounter) Reset() { *c = POCCounter{} }

// Next gives the order count of one coded picture and remembers what the
// pictures after it will need.
//
// The unit is wanted whole, not just its payload: the count depends on the unit
// TYPE, which says whether the high bits restart, and on the temporal
// identifier, which says whether this picture is one later ones count from.
func (c *POCCounter) Next(u Unit, sps SPS, pps PPS) (POC, error) {
	if !u.Type.IsSliceSegment() {
		return POC{}, fmt.Errorf("%w: type %d", ErrNotSlice, u.Type)
	}
	if sps.Log2MaxPOCLSB < 4 || sps.Log2MaxPOCLSB > 16 {
		// 7.4.3.2.1 bounds log2_max_pic_order_cnt_lsb_minus4 at 12, and a
		// sequence parameter set that was never parsed leaves this at zero.
		return POC{}, fmt.Errorf("%w: log2 max poc lsb is %d", ErrSliceHeader, sps.Log2MaxPOCLSB)
	}

	// An IDR states no count: it begins a sequence, so its own is zero and the
	// pictures after it count from there.
	if u.Type.IsIDR() {
		p := POC{Value: 0, LSB: 0, Carried: carriesPOC(u)}
		if p.Carried {
			c.prevLSB, c.prevMSB, c.started = 0, 0, true
		}
		return p, nil
	}

	lsb, err := pocLSB(u, sps, pps)
	if err != nil {
		return POC{}, err
	}

	maxLSB := int32(1) << sps.Log2MaxPOCLSB
	var msb int32
	switch {
	case u.Type.IsBLA():
		// A broken link is a splice: whatever the count had reached belongs to
		// the stream before it, so the high bits restart at zero.
		msb = 0
	case !c.started:
		// Nothing to carry from yet. The first picture of a sequence that does
		// not begin with an IDR -- a stream joined at a clean random access
		// point -- counts from zero high bits.
		msb = 0
	default:
		switch {
		case int32(lsb) < c.prevLSB && c.prevLSB-int32(lsb) >= maxLSB/2:
			msb = c.prevMSB + maxLSB
		case int32(lsb) > c.prevLSB && int32(lsb)-c.prevLSB > maxLSB/2:
			msb = c.prevMSB - maxLSB
		default:
			msb = c.prevMSB
		}
	}

	p := POC{Value: msb + int32(lsb), LSB: lsb, Carried: carriesPOC(u)}
	if p.Carried {
		c.prevLSB, c.prevMSB, c.started = int32(lsb), msb, true
	}
	return p, nil
}

// carriesPOC says whether later pictures take their high bits from this one.
//
// Sub-layer zero only, and neither a leading picture nor a sub-layer
// non-reference: those are what a thinned stream drops, and an order that
// depended on them would change when they went.
func carriesPOC(u Unit) bool {
	return u.TemporalID == 0 && !u.Type.IsLeading() && !u.Type.IsSubLayerNonReference()
}

// pocLSB reads slice_pic_order_cnt_lsb out of a first slice segment.
//
// It walks the header rather than calling ParseSliceSegmentHeader because the
// field sits past slice_type and its width comes from the SEQUENCE parameter
// set, which that function does not take.
func pocLSB(u Unit, sps SPS, pps PPS) (uint32, error) {
	r := newSticky(u.Unescape())
	first := r.flag()
	if u.Type.IsRandomAccess() {
		r.bit() // no_output_of_prior_pics_flag
	}
	r.ue() // slice_pic_parameter_set_id
	if !first {
		// A dependent segment states no order of its own, and an independent
		// one that is not the first needs its address, whose width this package
		// does not work out. Either way the count belongs to the picture, and
		// the picture's first segment has it.
		return 0, fmt.Errorf("%w: not the first segment of its picture", ErrNoOrder)
	}
	for i := uint8(0); i < pps.ExtraSliceHeaderBits; i++ {
		r.bit()
	}
	r.ue() // slice_type
	if pps.OutputFlagPresent {
		r.bit() // pic_output_flag
	}
	if sps.SeparatePlanes {
		r.bits(2) // colour_plane_id
	}
	lsb := r.bits(int(sps.Log2MaxPOCLSB))
	if r.err != nil {
		return 0, r.err
	}
	return lsb, nil
}
