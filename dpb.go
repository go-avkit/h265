// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// The decoded picture buffer: the application of the reference picture set (8.3.2),
// the generation of unavailable pictures (8.3.3), and the output order operation of
// C.5.2 with its "bumping" process, for a decoder that decodes every sub-layer.

// ErrMissingReference means the reference picture set of a picture names a picture
// it may predict from that the buffer does not hold.
var ErrMissingReference = errors.New("h265: a reference picture is not in the decoded picture buffer")

// RefMark is how a picture is marked for reference (8.3.2).
type RefMark uint8

// The three markings, of which a picture has exactly one.
const (
	UnusedForReference RefMark = iota
	ShortTermReference
	LongTermReference
)

// DPBPicture is one picture storage buffer: a decoded picture, its marking, and
// what output needs of it.
type DPBPicture struct {
	POC   int32
	Frame *DecodedPicture
	// State is what the walk of the picture recorded, which later pictures read
	// (the motion data for temporal prediction); nil for a generated picture.
	State *PictureState
	SPS   SPS
	Mark  RefMark
	// NeededForOutput and LatencyCount are the output state of C.5.2; Generated says
	// the picture came from 8.3.3.
	NeededForOutput bool
	LatencyCount    int
	Generated       bool
	TemporalID      uint8
}

// PictureInfo is what the buffer needs to know about the picture being started.
type PictureInfo struct {
	Type       UnitType
	TemporalID uint8
	POC        int32
	// Set is the reference picture set the slice header states, absolute (Derive).
	Set RefPicSet
	SPS SPS
	// NoOutputOfPriorPics is no_output_of_prior_pics_flag, and PicOutput
	// PicOutputFlag (8.1.3). NoRaslOutput is NoRaslOutputFlag of an IRAP picture, and
	// First says it is picture 0 of the bitstream.
	NoOutputOfPriorPics bool
	PicOutput           bool
	NoRaslOutput        bool
	First               bool
}

// RefPics are the pictures of the RPS lists that were found (or generated), in the
// order of the lists; an entry the buffer did not hold is nil.
type RefPics struct {
	StCurrBefore, StCurrAfter, StFoll []*DPBPicture
	LtCurr, LtFoll                    []*DPBPicture
}

// DPB is the decoded picture buffer. Output is called with each picture as C.5.2.4
// outputs it, in output order; the picture's frame is the uncropped decoded picture.
type DPB struct {
	Output func(*DPBPicture)
	pics   []*DPBPicture
}

// Pictures returns the pictures held, in the order they entered.
func (d *DPB) Pictures() []*DPBPicture { return d.pics }

func (d *DPB) neededCount() (n int) {
	for _, p := range d.pics {
		if p.NeededForOutput {
			n++
		}
	}
	return
}

// bump is the "bumping" process (C.5.2.4): the picture with the smallest order count
// of those needed for output is output; its buffer is emptied if it is also unused
// for reference. It reports whether there was a picture to output.
func (d *DPB) bump() bool {
	var first *DPBPicture
	for _, p := range d.pics {
		if p.NeededForOutput && (first == nil || p.POC < first.POC) {
			first = p
		}
	}
	if first == nil {
		return false
	}
	if d.Output != nil {
		d.Output(first)
	}
	first.NeededForOutput = false
	if first.Mark == UnusedForReference {
		d.remove(first)
	}
	return true
}

func (d *DPB) remove(p *DPBPicture) {
	for i, q := range d.pics {
		if q == p {
			d.pics = append(d.pics[:i], d.pics[i+1:]...)
			return
		}
	}
}

// removeDead empties the buffers of pictures neither needed for output nor used
// for reference.
func (d *DPB) removeDead() {
	kept := d.pics[:0]
	for _, p := range d.pics {
		if p.NeededForOutput || p.Mark != UnusedForReference {
			kept = append(kept, p)
		}
	}
	for i := len(kept); i < len(d.pics); i++ {
		d.pics[i] = nil
	}
	d.pics = kept
}

// latencyExceeded is the second bumping condition of C.5.2.2 and C.5.2.3.
func (d *DPB) latencyExceeded(sps SPS) bool {
	if sps.MaxLatencyIncreasePlus1 == 0 {
		return false
	}
	maxLatency := int(sps.MaxNumReorderPics) + int(sps.MaxLatencyIncreasePlus1) - 1 // SpsMaxLatencyPictures
	for _, p := range d.pics {
		if p.NeededForOutput && p.LatencyCount >= maxLatency {
			return true
		}
	}
	return false
}

// Begin is what happens between the slice header of the first slice of a picture
// and its decoding: the decoding process for the reference picture set (8.3.2), the
// output and removal of pictures of C.5.2.2, and the generation of unavailable
// reference pictures (8.3.3). It returns the pictures of the five lists, or
// ErrMissingReference when a picture the current one may predict from is not held.
func (d *DPB) Begin(in PictureInfo) (RefPics, error) {
	maxLSB := int32(1) << in.SPS.Log2MaxPOCLSB
	var rp RefPics

	if in.Type.IsRandomAccess() && in.NoRaslOutput {
		for _, p := range d.pics {
			p.Mark = UnusedForReference
		}
	}
	// 8.3.2 step 1: the long-term pictures, found among all reference pictures.
	findLt := func(lt LtPicture) *DPBPicture {
		for _, p := range d.pics {
			if p.Mark == UnusedForReference {
				continue
			}
			if (lt.Exact && p.POC == lt.POC) || (!lt.Exact && p.POC&(maxLSB-1) == lt.POC) {
				return p
			}
		}
		return nil
	}
	for _, lt := range in.Set.LtCurr {
		rp.LtCurr = append(rp.LtCurr, findLt(lt))
	}
	for _, lt := range in.Set.LtFoll {
		rp.LtFoll = append(rp.LtFoll, findLt(lt))
	}
	// Step 2: they are marked long-term.
	for _, ps := range [][]*DPBPicture{rp.LtCurr, rp.LtFoll} {
		for _, p := range ps {
			if p != nil {
				p.Mark = LongTermReference
			}
		}
	}
	// Step 3: the short-term pictures, found among the short-term ones.
	findSt := func(poc int32) *DPBPicture {
		for _, p := range d.pics {
			if p.Mark == ShortTermReference && p.POC == poc {
				return p
			}
		}
		return nil
	}
	for _, poc := range in.Set.StCurrBefore {
		rp.StCurrBefore = append(rp.StCurrBefore, findSt(poc))
	}
	for _, poc := range in.Set.StCurrAfter {
		rp.StCurrAfter = append(rp.StCurrAfter, findSt(poc))
	}
	for _, poc := range in.Set.StFoll {
		rp.StFoll = append(rp.StFoll, findSt(poc))
	}
	// Step 4: every other reference picture is unused.
	inSet := map[*DPBPicture]bool{}
	for _, ps := range [][]*DPBPicture{rp.StCurrBefore, rp.StCurrAfter, rp.StFoll, rp.LtCurr, rp.LtFoll} {
		for _, p := range ps {
			inSet[p] = true
		}
	}
	for _, p := range d.pics {
		if !inSet[p] {
			p.Mark = UnusedForReference
		}
	}

	// C.5.2.2.
	if in.Type.IsRandomAccess() && in.NoRaslOutput && !in.First {
		noOutput := in.NoOutputOfPriorPics
		if in.Type == UnitCRA {
			noOutput = true
		}
		if !noOutput {
			for d.bump() {
			}
		}
		d.pics = d.pics[:0]
	} else {
		d.removeDead()
		for d.neededCount() > 0 &&
			(d.neededCount() > int(in.SPS.MaxNumReorderPics) || d.latencyExceeded(in.SPS) || len(d.pics) >= int(in.SPS.MaxDecPicBufferingMinus1)+1) {
			d.bump()
		}
	}

	// 8.3.3: for a BLA picture or a CRA picture with NoRaslOutputFlag, the pictures
	// the following pictures may name are generated when the buffer lacks them.
	if in.Type.IsBLA() || (in.Type == UnitCRA && in.NoRaslOutput) {
		for i, p := range rp.StFoll {
			if p == nil {
				rp.StFoll[i] = d.generate(in, in.Set.StFoll[i], ShortTermReference)
			}
		}
		for i, p := range rp.LtFoll {
			if p == nil {
				poc := in.Set.LtFoll[i].POC
				if !in.Set.LtFoll[i].Exact {
					poc &= maxLSB - 1
				}
				rp.LtFoll[i] = d.generate(in, poc, LongTermReference)
			}
		}
	}
	for _, ps := range [][]*DPBPicture{rp.StCurrBefore, rp.StCurrAfter, rp.LtCurr} {
		for _, p := range ps {
			if p == nil {
				return rp, fmt.Errorf("%w: picture %d names a picture the buffer does not hold", ErrMissingReference, in.POC)
			}
		}
	}
	return rp, nil
}

// generate makes an unavailable picture (8.3.3.2): every sample at mid-grey, intra
// prediction mode, not for output.
func (d *DPB) generate(in PictureInfo, poc int32, mark RefMark) *DPBPicture {
	frame := NewDecodedPicture(in.SPS)
	for c := 0; c < frame.Components; c++ {
		for i := range frame.Planes[c].Pix {
			frame.Planes[c].Pix[i] = 1 << uint(frame.BitDepth[c]-1)
		}
	}
	p := &DPBPicture{POC: poc, Frame: frame, SPS: in.SPS, Mark: mark, Generated: true, TemporalID: in.TemporalID}
	d.pics = append(d.pics, p)
	return p
}

// Finish stores the decoded current picture (C.3.4, 8.1.3) and does the additional
// bumping of C.5.2.3. cur's POC, Frame, State and SPS are the caller's.
func (d *DPB) Finish(cur *DPBPicture, picOutput bool) {
	if picOutput {
		for _, p := range d.pics {
			if p.NeededForOutput && p.POC > cur.POC {
				p.LatencyCount++
			}
		}
		cur.NeededForOutput, cur.LatencyCount = true, 0
	} else {
		cur.NeededForOutput = false
	}
	cur.Mark = ShortTermReference
	d.pics = append(d.pics, cur)
	// Either condition needs a picture waiting for output, so a bump always finds one.
	for d.neededCount() > int(cur.SPS.MaxNumReorderPics) || d.latencyExceeded(cur.SPS) {
		d.bump()
	}
}

// Flush outputs every picture still needed for output, in order, and empties the
// buffer: the end of the bitstream.
func (d *DPB) Flush() {
	for d.bump() {
	}
	d.pics = d.pics[:0]
}

// Crop returns the picture cut to the conformance window of sps (C.5.2.4). It is a view:
// each plane of the result is the window of the plane of p, the same memory with p's stride,
// so nothing is copied. See Output for what that means for the caller.
func (p *DecodedPicture) Crop(sps SPS) *DecodedPicture {
	out := &DecodedPicture{Components: p.Components, BitDepth: p.BitDepth}
	subW, subH := sps.chromaUnits()
	for c := 0; c < p.Components; c++ {
		pl := &p.Planes[c]
		x0, y0, w, h := int(sps.WinLeft), int(sps.WinTop), int(sps.Width), int(sps.Height)
		if c > 0 {
			sw, sh := int(subW), int(subH)
			x0, y0, w, h = int(sps.WinLeft), int(sps.WinTop), int(sps.Width)/sw, int(sps.Height)/sh
		} else {
			x0, y0 = int(subW)*x0, int(subH)*y0
		}
		first := y0*pl.Stride + x0
		end := first + (h-1)*pl.Stride + w
		out.Planes[c] = Plane{Width: w, Height: h, Stride: pl.Stride, Pix: pl.Pix[first:end:end]}
	}
	return out
}

// Sequence tracks what 8.1.3 needs across pictures: whether a picture is the first
// of the bitstream or follows an end of sequence unit, and so what NoRaslOutputFlag
// is, and whether the RASL pictures that follow an IRAP picture are to be skipped.
type Sequence struct {
	started, afterEOS, skipRASL bool
}

// EndOfSequence is called with an end of sequence NAL unit.
func (s *Sequence) EndOfSequence() { s.afterEOS = true }

// Next classifies the first slice segment of a picture: whether it is the first picture of
// the bitstream, its NoRaslOutputFlag (meaningful for IRAP pictures), and whether it is
// a RASL picture associated with an IRAP picture that has NoRaslOutputFlag 1, which
// is not decoded and not output (8.1.3).
func (s *Sequence) Next(t UnitType) (first, noRaslOutput, skip bool) {
	first = !s.started
	s.started = true
	if t.IsRandomAccess() {
		noRaslOutput = t.IsIDR() || t.IsBLA() || first || s.afterEOS
		s.skipRASL = noRaslOutput
		s.afterEOS = false
		return first, noRaslOutput, false
	}
	s.afterEOS = false
	return first, false, (t == UnitRASLN || t == UnitRASLR) && s.skipRASL
}
