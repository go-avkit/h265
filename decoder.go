// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package h265

import (
	"errors"
	"fmt"
)

// Errors of the Decoder. The errors of the parsers it calls (ErrUnsupportedSPS,
// ErrSliceHeader, ErrSliceData ...) are returned as they are, wrapped.
var (
	// ErrPictureTooLarge: a sequence parameter set describes a picture of more luma
	// samples than Limits.MaxPixels.
	ErrPictureTooLarge = errors.New("h265: the picture is larger than the decoder's limit")
	// ErrDPBTooLarge: the decoded picture buffer the stream needs would hold more than
	// Limits.MaxDPBBytes.
	ErrDPBTooLarge = errors.New("h265: the decoded picture buffer would exceed the decoder's memory limit")
	// ErrMissingParameterSet: a slice names a parameter set that was not received.
	ErrMissingParameterSet = errors.New("h265: a slice names a parameter set that was not received")
	// ErrCorruptPicture: a picture could not be decoded to its end; the picture is still
	// kept for reference and output, as far as it was decoded.
	ErrCorruptPicture = errors.New("h265: a picture could not be decoded")
	// ErrHashMismatch: a decoded picture differs from its decoded picture hash SEI message.
	ErrHashMismatch = errors.New("h265: a decoded picture does not match its picture hash")
)

// Limits bound what a stream can make the decoder allocate.
type Limits struct {
	// MaxPixels is the largest picture, in luma samples (default 8192 x 4320, the
	// largest of level 6.2).
	MaxPixels int64
	// MaxDPBBytes bounds the decoded picture buffer, estimated as the samples of its
	// pictures plus the data kept for prediction (default 2 GiB).
	MaxDPBBytes int64
}

// DefaultLimits are the limits a zero Limits stands for.
func DefaultLimits() Limits { return Limits{MaxPixels: 8192 * 4320, MaxDPBBytes: 2 << 30} }

// Options configure a Decoder.
type Options struct {
	Limits Limits
	// VerifyHash compares each decoded picture with its decoded picture hash SEI message
	// when it has one (ErrHashMismatch on a difference).
	VerifyHash bool
	// RecyclePictures lets the decoder reuse the planes of pictures the caller has given
	// back with DecodedPicture.Release, instead of allocating every picture afresh (which at
	// 1080p costs about 1500 page faults a frame). Off by default: without it nothing changes
	// and an Output picture stays valid for as long as it is referenced (see Output). With it,
	// a picture that is not released is still never reused; a released one is, once the
	// decoder no longer needs it as a reference, so the caller must not read it after Release.
	RecyclePictures bool
}

// Output is a picture the decoder outputs: cut to the conformance window, in output
// order (C.5.2.4).
//
// Lifetime. Every decoded picture is its own allocation, which the decoder never recycles
// and never writes again once it is decoded, so Picture stays valid and unchanged for as
// long as the caller keeps it (the garbage collector frees it with the last reference).
// Its planes are views of the decoder's picture (cut to the conformance window, so a plane's
// Stride can be larger than its Width, and a plane's Pix runs to its last sample): the same
// memory the decoder reads as a reference picture for later ones. The caller must therefore
// treat the planes as read-only: writing to them changes the pictures predicted from this
// one. Call Picture.Clone first to get a copy that can be modified.
//
// That is the default. With Options.RecyclePictures a caller may give a picture back with
// Picture.Release, after which its planes are the decoder's again (reused once the decoder no
// longer needs the picture as a reference) and Picture is empty; a picture that is not
// released keeps the lifetime above.
//
// Behaviour change (the release that made planes views): output planes used to be copies the
// caller could write to; callers that modify them must Clone the picture first.
type Output struct {
	Picture *DecodedPicture
	POC     int32
	// Tag is the Tag of the first slice segment of the picture (Unit.Tag).
	Tag uint64
	// Corrupt says the picture could not be decoded to its end: a slice was lost or failed, or
	// it is predicted from a reference the stream never delivered (an error came back with it
	// and Stats.Failed counts it). The picture is still output, as far as it was decoded.
	Corrupt bool
}

// DecoderStats counts what a decoder has done.
type DecoderStats struct {
	// Pictures decoded (including the ones that failed), SkippedRASL the RASL pictures
	// not decoded (8.1.3), Failed those that could not be decoded to their end.
	Pictures, SkippedRASL, Failed int
	// Verified, Mismatched and Unhashed count the decoded pictures with a hash that
	// matched, with one that did not, and without one (VerifyHash only).
	Verified, Mismatched, Unhashed int
	// VerifiedInter is the part of Verified with a P or B slice.
	VerifiedInter int
}

// Decoder decodes the base layer of an HEVC bitstream. It is not safe for concurrent use.
type Decoder struct {
	opt  Options
	spsM map[uint32]SPS
	ppsM map[uint32]PPS
	// epoch changes with every parameter set, which invalidates the layouts.
	epoch   int
	layouts map[[2]int]*CtbLayout
	counter POCCounter
	seq     Sequence
	prev    *SliceHeader
	dpb     DPB
	cur     *decPicture
	skip    bool
	out     []Output
	errs    []error
	stats   DecoderStats
	// pool is the pool of pictures when Options.RecyclePictures is on, else nil.
	pool   *picPool
	motion *motionPool
}

type decPicture struct {
	state  *PictureState
	frame  *DecodedPicture
	sps    SPS
	pps    PPS
	poc    int32
	set    RefPicSet
	rp     RefPics
	output bool
	tid    uint8
	failed bool
	hash   *PictureHash
	intra  bool
	tag    uint64
}

// NewDecoder starts a decoder; zero limits stand for DefaultLimits.
func NewDecoder(opt Options) *Decoder {
	def := DefaultLimits()
	if opt.Limits.MaxPixels <= 0 {
		opt.Limits.MaxPixels = def.MaxPixels
	}
	if opt.Limits.MaxDPBBytes <= 0 {
		opt.Limits.MaxDPBBytes = def.MaxDPBBytes
	}
	d := &Decoder{opt: opt, spsM: map[uint32]SPS{}, ppsM: map[uint32]PPS{}, layouts: map[[2]int]*CtbLayout{}}
	d.dpb.Output = func(p *DPBPicture) {
		d.out = append(d.out, Output{Picture: p.Frame.Crop(p.SPS), POC: p.POC, Tag: p.Tag, Corrupt: p.Corrupt})
	}
	if opt.RecyclePictures {
		d.pool, d.motion = newPicPool(), &motionPool{}
		d.dpb.Drop = func(p *DPBPicture) {
			if r := p.Frame.rec; r != nil {
				r.pool.drop(r)
			}
			if st := p.State; st != nil && st.motion != nil {
				st.motion.put(st.Motion)
				st.Motion = nil
			}
		}
	}
	return d
}

// Stats returns the counts so far.
func (d *Decoder) Stats() DecoderStats { return d.stats }

// pictureBytes estimates what a picture of sps holds: its samples (two bytes each)
// and what is kept for prediction and the filters.
func pictureBytes(sps SPS) int64 {
	luma := int64(sps.CodedWidth) * int64(sps.CodedHeight)
	chroma := [4]int64{0, 1, 2, 2}[sps.ChromaFormat&3] // chroma samples per luma sample, rounded up
	return luma * (2 + chroma + 6)
}

func (d *Decoder) checkSPS(sps SPS) error {
	if int64(sps.CodedWidth)*int64(sps.CodedHeight) > d.opt.Limits.MaxPixels {
		return fmt.Errorf("%w: %dx%d", ErrPictureTooLarge, sps.CodedWidth, sps.CodedHeight)
	}
	if (int64(sps.MaxDecPicBufferingMinus1)+2)*pictureBytes(sps) > d.opt.Limits.MaxDPBBytes {
		return fmt.Errorf("%w: %d pictures of %dx%d", ErrDPBTooLarge, sps.MaxDecPicBufferingMinus1+2, sps.CodedWidth, sps.CodedHeight)
	}
	return nil
}

// Decode takes the next NAL unit of the stream, in decoding order, and returns the
// pictures that became ready for output because of it. A picture is complete when the
// first slice segment of the next one, an end of sequence unit, or Flush arrives, so
// the pictures returned are usually those of earlier units. Units the decoder does not
// use (VPS, AUD, prefix SEI, layers above the base) are ignored. An error does not stop
// the decoder: the stream can go on, and the pictures returned alongside it are valid.
func (d *Decoder) Decode(u Unit) ([]Output, error) {
	d.errs = d.errs[:0]
	d.decode(u)
	out := d.out
	d.out = nil
	return out, errors.Join(d.errs...)
}

// Flush ends the stream: the last picture is completed and every picture still waiting
// is output, in order. The decoder can then be used for another stream.
func (d *Decoder) Flush() ([]Output, error) {
	d.errs = d.errs[:0]
	d.finish()
	d.dpb.Flush()
	out := d.out
	d.out = nil
	d.counter.Reset()
	d.seq = Sequence{}
	d.prev = nil
	return out, errors.Join(d.errs...)
}

func (d *Decoder) fail(err error) { d.errs = append(d.errs, err) }

// finish completes the picture being decoded: the in-loop filters, the hash check, and
// its entry into the decoded picture buffer.
func (d *Decoder) finish() {
	c := d.cur
	if c == nil {
		return
	}
	d.cur = nil
	d.stats.Pictures++
	if c.failed {
		d.stats.Failed++
	} else {
		c.state.Deblock(c.frame, c.sps, c.pps)
		c.state.SAO(c.frame, c.sps, c.pps)
		if d.opt.VerifyHash {
			switch {
			case c.hash == nil:
				d.stats.Unhashed++
			default:
				if err := c.hash.Verify(c.frame); err != nil {
					d.stats.Mismatched++
					d.fail(fmt.Errorf("%w: POC %d: %v", ErrHashMismatch, c.poc, err))
				} else {
					d.stats.Verified++
					if !c.intra {
						d.stats.VerifiedInter++
					}
				}
			}
		}
	}
	d.dpb.Finish(&DPBPicture{POC: c.poc, Frame: c.frame, State: c.state, SPS: c.sps, TemporalID: c.tid, Tag: c.tag, Corrupt: c.failed}, c.output)
}

func (d *Decoder) decode(u Unit) {
	if u.LayerID != 0 { // only the base layer is decoded: the others' parameter sets are not read either
		return
	}
	switch {
	case u.Type == UnitSPS:
		d.epoch++
		s, err := ParseSPS(u)
		if err != nil {
			d.fail(err)
			return
		}
		if err := d.checkSPS(s); err != nil {
			d.fail(err)
			delete(d.spsM, s.ID)
			return
		}
		d.spsM[s.ID] = s
	case u.Type == UnitPPS:
		d.epoch++
		pps, err := ParsePPS(u)
		if err != nil {
			d.fail(err)
			return
		}
		d.ppsM[pps.ID] = pps
	case u.Type == UnitEOS:
		d.finish()
		d.seq.EndOfSequence()
		d.counter.Reset()
	case u.Type <= UnitRASLR || (u.Type >= UnitBLAWLP && u.Type <= UnitCRA):
		d.slice(u)
	case u.Type == UnitSuffixSEI:
		if !d.opt.VerifyHash || d.cur == nil {
			return
		}
		msgs, err := ParseSEI(u)
		if err != nil {
			d.fail(err)
			return
		}
		for _, m := range msgs {
			if m.Type != SEIPayloadDecodedPictureHash {
				continue
			}
			if hash, valid, err := ParsePictureHash(m.Payload, int(d.cur.sps.ChromaFormat)); err == nil && valid {
				d.cur.hash = &hash
			}
		}
	}
}

func (d *Decoder) slice(u Unit) {
	r := newSticky(u.Unescape())
	first := r.bit() == 1
	if u.Type.IsRandomAccess() {
		r.bit()
	}
	ppsID := r.ue()
	if r.err != nil {
		d.fail(fmt.Errorf("%w: %v", ErrSliceHeader, r.err))
		return
	}
	pps, ok := d.ppsM[ppsID]
	if !ok {
		d.fail(fmt.Errorf("%w: picture parameter set %d", ErrMissingParameterSet, ppsID))
		return
	}
	sps, ok := d.spsM[pps.SPSID]
	if !ok {
		d.fail(fmt.Errorf("%w: sequence parameter set %d", ErrMissingParameterSet, pps.SPSID))
		return
	}
	h, err := ParseSliceSegmentHeaderFull(u, sps, pps, d.prev)
	if err != nil {
		d.fail(err)
		return
	}
	if !h.Dependent {
		hh := h
		d.prev = &hh
	}
	if first {
		d.finish()
		d.skip = true
		if !d.begin(u, h, sps, pps) {
			return
		}
		d.skip = false
	}
	if d.skip || d.cur == nil || d.cur.failed {
		return
	}
	c := d.cur
	rec := NewReconstructor(c.frame, sps, pps)
	c.state.Inter = nil
	err = nil
	if h.Type != SliceI {
		c.intra = false
		var sl *InterSlice
		if sl, err = d.interSlice(c, h, pps); err == nil {
			c.state.Inter, rec.Inter = sl, sl
		}
	}
	if err == nil {
		err = DecodeSliceData(c.state, u, h, pps, rec.Decoder())
	}
	if err == nil {
		err = rec.Err()
	}
	if err != nil {
		c.failed = true
		d.fail(fmt.Errorf("%w: POC %d: %w", ErrCorruptPicture, c.poc, err))
	}
}

// begin starts the picture whose first slice segment is u; false when it is not decoded.
func (d *Decoder) begin(u Unit, h SliceHeader, sps SPS, pps PPS) bool {
	isFirst, noRasl, skip := d.seq.Next(u.Type)
	if skip {
		d.stats.SkippedRASL++
		return false
	}
	poc, err := d.counter.Next(u, sps, pps)
	if err != nil {
		d.fail(err)
		return false
	}
	set, err := Derive(h.Refs, poc.Value, sps)
	if err != nil {
		d.fail(err)
		return false
	}
	if int64(len(d.dpb.Pictures())+1)*pictureBytes(sps) > d.opt.Limits.MaxDPBBytes {
		d.fail(fmt.Errorf("%w: %d pictures held", ErrDPBTooLarge, len(d.dpb.Pictures())))
		return false
	}
	key := [2]int{int(pps.ID), d.epoch}
	l := d.layouts[key]
	if l == nil {
		if l, err = NewCtbLayout(sps, pps); err != nil {
			d.fail(err)
			return false
		}
		if len(d.layouts) > 64 {
			d.layouts = map[[2]int]*CtbLayout{}
		}
		d.layouts[key] = l
	}
	in := PictureInfo{Type: u.Type, TemporalID: u.TemporalID, POC: poc.Value, Set: set, SPS: sps,
		NoOutputOfPriorPics: h.NoOutputOfPriorPics, PicOutput: h.PicOutput, NoRaslOutput: noRasl, First: isFirst}
	rp, err := d.dpb.Begin(in)
	d.cur = &decPicture{state: NewPictureState(sps, l), frame: d.newFrame(sps), sps: sps, pps: pps,
		poc: poc.Value, set: set, rp: rp, output: h.PicOutput, tid: u.TemporalID, intra: true, tag: u.Tag}
	d.cur.state.motion = d.motion
	if err != nil {
		d.cur.failed = true
		d.fail(fmt.Errorf("%w: POC %d: %w", ErrCorruptPicture, poc.Value, err))
	}
	return true
}

// newFrame is the picture the next one is decoded into: from the pool of recycled pictures
// when the option is on, else a new allocation.
func (d *Decoder) newFrame(sps SPS) *DecodedPicture {
	if d.pool != nil {
		return d.pool.alloc(sps)
	}
	return NewDecodedPicture(sps)
}

// interSlice makes the InterSlice of a P or B slice: the reference lists of 8.3.4 with
// the pictures of the buffer behind each entry.
func (d *Decoder) interSlice(c *decPicture, h SliceHeader, pps PPS) (*InterSlice, error) {
	l0, l1, err := Lists(c.set, h.Refs)
	if err != nil {
		return nil, err
	}
	// The entries name their pictures by order count and marking; the buffer found them
	// in the same order the set states them. A picture the buffer did not hold would be
	// nil here, and the prediction refuses a reference that is not a decoded picture.
	type key struct {
		poc  int32
		long bool
	}
	pics := map[key]*DPBPicture{}
	for i, poc := range c.set.StCurrBefore {
		pics[key{poc, false}] = c.rp.StCurrBefore[i]
	}
	for i, poc := range c.set.StCurrAfter {
		pics[key{poc, false}] = c.rp.StCurrAfter[i]
	}
	for i, lt := range c.set.LtCurr {
		pics[key{lt.POC, true}] = c.rp.LtCurr[i]
	}
	var lists [2][]RefEntry
	for l, entries := range [][]RefListEntry{l0, l1} {
		for _, e := range entries {
			pic := pics[key{e.POC, e.LongTerm}]
			re := RefEntry{POC: e.POC, LongTerm: e.LongTerm, Pic: pic}
			if pic != nil {
				re.POC = pic.POC
			}
			lists[l] = append(lists[l], re)
		}
	}
	return NewInterSlice(h, pps, c.poc, lists[0], lists[1])
}
