# h265

[![ci](https://github.com/go-avkit/h265/actions/workflows/ci.yml/badge.svg)](https://github.com/go-avkit/h265/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-avkit/h265.svg)](https://pkg.go.dev/github.com/go-avkit/h265)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

Part of [go-avkit](https://go-avkit.github.io): [documentation](https://go-avkit.github.io/docs/).

Pure-Go (CGO=0) reader for the H.265/HEVC bitstream: the NAL layer, the
parameter sets, the slice segment header, and the boundaries between coded
pictures.

```go
units, err := h265.SplitAnnexB(data)
sps, err := h265.ParseSPS(spsUnit)
pps, err := h265.ParsePPS(ppsUnit)
pics, err := h265.SplitPictures(units, pps)   // without decoding any
```

## What it is not

**It is a decoder of our own, and it is slow (see Speed below).** `Decoder` takes NAL units in
decoding order and returns the pictures in output order:

```go
d := h265.NewDecoder(h265.Options{VerifyHash: true})
for _, u := range units {
	pics, err := d.Decode(u) // err does not stop the decoder; the pictures are valid
}
pics, err := d.Flush()
```

Every hashed picture of the conformance suite that it decodes (intra and inter,
7147 of them) matches its decoded picture hash SEI; 21 streams use
`cabac_bypass_alignment_enabled_flag`, which it refuses. Limits (`Limits`) bound the
picture size and the memory of the picture buffer; a fuzz target covers the whole
decoder. Speed is below. CI pins these counts. The streams that carry no
hash SEI are held to the md5 of their decoded output (output order, cropped, as a
yuv file) that the suite ships: 45 of the 52 such streams it decodes match; the other
7 ship no md5 file, so nothing verifies them. What it has:

- the CABAC engine and the full slice-data syntax (7.3.8): all 52215 slices of
  the conformance suite (v1 and RExt) walk to their last bit;
- QP, scaling lists, dequantisation and inverse transforms (8.6);
- intra prediction and picture construction (8.4, 8.6.7), with deblocking
  (8.7.2) and SAO (8.7.3);
- motion vector derivation (merge, AMVP, temporal prediction, 8.5.3.2),
  fractional sample interpolation and weighted prediction (8.5.3.3), and the
  inter residual (8.5.4);
- the decoded picture buffer: RPS marking, unavailable pictures, output and
  bumping (C.5.2), conformance-window cropping; all 10238 pictures of the suite
  run through it;
- SEI parsing and the decoded picture hash (MD5, CRC, checksum).

`decode` does not use it: it still wraps a third-party decoder for HEVC.

### Speed

**Behaviour change in v0.19.0:** the planes of a picture returned by
`Decoder.Output` are read-only views of the decoder's own pictures, not copies, and
`Stride` can be larger than `Width`. A picture you keep stays valid and unchanged,
but writing into its planes corrupts the decoding of later pictures that reference
it. A caller that modifies samples must call `(*DecodedPicture).Clone()` first,
which returns an owned, writable copy.

v0.19.0 runs more of the HEVC kernels of go-videodsp/kernels (two-list average,
inverse DCT and DST, vertical-only interpolation) and crops output as a view, with
the output unchanged bit for bit (every conformance gate is the same; the md5 of
four 1080p outputs equals ffmpeg's). Frames per second by CPU time, one core (AMD
Ryzen 9 7950X, `taskset -c 5`, four runs of five rounds interleaved, load 0.15 to
1.06, 2026-11-16). The third-party decoder is the one go-avkit/decode v0.9.0 uses
for HEVC, measured through `DecodeTrack`; ffmpeg is 6.1.1 with `-threads 1`.

| stream (1080p) | v0.18.0 | v0.19.0 | third-party | ffmpeg |
|---|---|---|---|---|
| alien, Main | 58.2 | 76.1 | 87.4 | 395 |
| fade, Main | 53.8 | 68.8 | 80.0 | 430 |
| alien, Main 10 | 60.1 | 79.3 | 30.0 | 303 |
| alien8 | 66.6 | 85.6 | 95.3 | 429 |

On 8-bit streams this decoder is still slower than the third-party one, at 0.86 to
0.90 of its speed, an open defect; on Main 10 it is 2.6 times as fast. It runs at
0.16 to 0.26 of ffmpeg's speed. No parity is claimed.

**Security fix in v0.16.0:** on a hostile SPS (a picture width or height that is
not a multiple of the minimum coding block size) v0.14.0 and v0.15.0 could panic
in intra reconstruction, found by `FuzzDecoder`; `ParseSPS` now refuses such a
size, as 7.4.3.2.1 requires. Upgrade if you call them on untrusted input.

**Behaviour change in v0.14.0:** `Unit.TemporalID` is now TemporalId (0 for the
base layer, as 7.4.2.2 defines it); every version up to v0.13.2 returned
TemporalId + 2. A unit stating `nuh_temporal_id_plus1 = 0` is refused with
`ErrTemporalIDPlus1`. `POCCounter` used the wrong value too, so its picture order
counts were wrong after the first picture on real streams.

The design of the decoder: [docs/design/hevc.md](docs/design/hevc.md) (profile
of the decoder it would replace, stages, kernels, conformance, target).

## What is in it

| | |
|---|---|
| `SplitAnnexB`, `SplitLengthPrefixed`, `Unit`, `UnitType` | the NAL layer, both framings |
| `ParseSPS`, `ParsePPS` | the parameter sets |
| `ParseSliceSegmentHeader`, `SliceSegmentHeader`, `SliceType` | the slice segment header |
| `SplitPictures`, `Picture` | coded pictures, from `first_slice_segment_in_pic_flag` |
| `NewDecoder`, `Decoder`, `Options`, `Limits`, `Output` | the decoder: units in, cropped pictures out in output order (read-only views of the decoder's pictures, each its own allocation that stays valid and unchanged for as long as it is kept; see `Output`; `Picture.Clone` gives a writable copy; a `Unit.Tag` comes back as the `Output.Tag` of the picture whose first slice segment carried it), with bounds on picture size and buffer memory and an optional check of each picture against its hash SEI |
| `POCCounter`, `POC` | picture order counts, clause 8.3.1 |
| `ShortTermRPS`, `RefPic`, `LongTermRefPic` | the reference picture sets a sequence carries |
| `ReferencesOf`, `PictureRefs`, `LongTermRef` | what one coded picture says it still needs |
| `Derive`, `RefPicSet`, `LtPicture` | the pictures a decoder must keep for it, clause 8.3.2 |
| `Lists`, `RefListEntry` | the two lists a slice predicts from, clause 8.3.4, in the order the slice asked for |
| `PredWeights`, `RefWeight` | how a slice weighs each picture, 7.3.6.3 |
| `ShortHeaderError` | a unit that ended inside the syntax, said as such |

⛔ **HEVC's slice types are not H.264's.** Here `B` is 0, `P` is 1 and `I` is 2 —
the reverse of H.264, where `P` is 0 and `B` is 1. A reader that shares a
constant between the two formats reads every B slice as a P slice and every P
slice as a B one, and the stream still parses.

### The order a viewer sees

A stream sends only the LOW bits of each picture's order count, so the count has
to be carried across pictures rather than read from one. `POCCounter` does that.

⛔ The picture the high bits come from is **not** simply the previous one. It is
the previous picture of sub-layer zero that is neither a leading picture nor a
sub-layer non-reference — which is exactly the set a thinned stream may have
dropped. A counter that carried from every picture would make the order of what
remains depend on what was thrown away.

An IDR states no count at all and is zero by definition; a BLA restarts the high
bits, because a broken link is a splice.

The derivation is held against a second implementation: FFmpeg's
`ff_hevc_compute_poc2`, transcribed into the test, over **263 168** carry-and-low
-bit pairs at two widths. The one place the two part company is a negative carry
— FFmpeg reconstructs the previous low bits as a remainder, which is negative
there, and 84 of 1024 negative cases disagree. 8.3.1 keeps the low and high parts
apart and the low part is what the slice header stated, so that is what this
keeps.

### The pictures a picture may still need

A sequence parameter set carries the short-term reference picture sets its
slices may name. `SPS.ShortTermRefPicSets` holds them in order, each as the
pictures already seen (`Before`) and those still to come (`After`), nearest
first.

⛔ **A set may be written as a DIFFERENCE from an earlier one**, and the
difference cannot even have its bits counted without the set it is a difference
from — so the sets are read in order and every one is kept. The smallest
difference the syntax can state is one: `abs_delta_rps_minus1` has no way to
say "the same set", so a predicted set always moves every entry, and an entry
that crosses zero changes which half it belongs to.

⛔ **Everything between the order-count width and the sets is conditional and of
variable length** — sub-layer buffering, block and transform geometry, the
scaling lists, the PCM fields. A reader that walked any of them wrongly would
read the sets from the wrong bits, and nothing in a set would show it. Each
shape has a test that reads a known set back out of it.

The scaling lists are skipped with the reader the picture parameter set already
had: a second copy of that walk would be a second thing to drift.

`ReferencesOf` reads what one picture states. A slice usually **names** one of
the sequence's sets rather than restating it — which is why a sequence carries
them — and may instead state one of its own, itself possibly a difference from
one of the sequence's.

⛔ The index into those sets is **as wide as the number of them needs, and one
set means no index at all**. A reader that always read bits would take the next
field as the index and misread everything after it.

Long-term pictures come from two places at once and are counted as one run: some
named out of the sequence's list, some stated by the slice. Each is named by the
LOW bits of its order count, and states the high bits only when the low ones
would be ambiguous.

An IDR says nothing, and that is not an error: it begins a coded video sequence,
so nothing before it is available and nothing after it may reach back.

`Derive` turns that into the five lists 8.3.2 names, given the picture's own
order count. The short-term entries are relative and become absolute here.

⛔ **`Curr` and `Foll` are not the same thing.** A picture in `Curr` may be
predicted from by this picture. One in `Foll` may **not** — it is listed because
a LATER picture will want it, and a decoder that dropped it would break that one
instead. Both must be kept; only one may be used.

⛔ A long-term picture's count is **exact only when the slice stated its high
bits**; otherwise it is the low bits, and the picture meant is whichever held
matches them. `LtPicture.Exact` says which, because handing a decoder low bits
as though they were a count is a wrong answer that looks like a right one.

The high bits are stated as a number of wraps that **accumulates** across the
entries — each counted from the one before, not from zero — and the count starts
afresh for the slice's own entries after the sequence's. Two entries each
stating one wrap are 256 and 512 back, not 256 twice.

### The two lists a slice predicts from

`Lists` builds `RefPicList0` and `RefPicList1` out of a derived set, clause
8.3.4. They hold the SAME pictures; the order is what distinguishes them.

```go
refs, err := h265.ReferencesOf(first, sps, pps)
set := h265.Derive(refs, poc, sps)
l0, l1, err := h265.Lists(set, refs)
```

`refs` carries how many entries each list has and, where the slice stated one,
the order they are taken in -- so the caller passes neither.

⛔ **The two lists are not interchangeable.** List 0 opens with the pictures
BEFORE this one in output order, list 1 with those after. A caller that fed a B
slice the same list twice, or swapped them, would predict from the wrong side
and get an image that is wrong without being malformed.

A list is as long as the slice says, which has nothing to do with how many
pictures are available. **Shorter** and it simply stops. **Longer** and the
candidates are repeated, from the start, as many times as it takes — that is
legal, and a list built to stop once would come out short.

An I slice gets no lists. A P slice gets list 0 only.

### The order the slice asked for

A slice may state, for each position of each list, which picture goes there --
`ref_pic_lists_modification()`, 7.3.6.2. `ReferencesOf` reads it into
`ListEntryL0` and `ListEntryL1`, and `Lists` applies it.

⛔ **An entry indexes the TEMPORARY list, which is as long as the greater of the
list's own length and the pictures available.** A slice may name a picture
beyond the end of its own list, and a reader that cut the list to length first
would refuse a legal stream.

⛔ **nil is not an empty list.** A slice that stated no modification leaves the
default order standing; a list of no entries is a different thing, and the two
must not be spelt the same way.

An entry is `Ceil(Log2(NumPicTotalCurr))` bits wide, which holds values past the
last picture — three pictures are named in two bits — so an index out of range
is reachable from a field of conformant width.

⛔ **An entry is bounded by the PICTURES, not by the temporary list it indexes**
(7.4.7.2). The temporary list is longer whenever the list has more entries than
there are pictures, and bounding on it lets an index past the last picture
through. FFmpeg bounds on the temporary list; GStreamer enforces the clause and
cites it. Nothing is lost by refusing them: the temporary list past
`NumPicTotalCurr` only repeats the candidates.

### How a slice weighs what it predicts from

`pred_weight_table()`, 7.3.6.3, comes back as `PictureRefs.Weights` -- nil where
the picture parameter set does not weight this kind of slice.

⛔ **The weights are VALUES, not the differences the syntax carries.** A weight
is stated as a difference from the neutral `1 << denominator`, and a chroma
offset as a difference that still has the weight folded into it. Handed the raw
numbers, a decoder scales by something close to nothing.

```
ChromaOffset = Clip(delta - ((128 * ChromaWeight) >> denom) + 128, -128, 127)
```

⛔ **Every luma flag comes first, as one field of n bits, then every chroma
flag, and only then the values.** H.264 interleaves a flag with its own values;
HEVC does not. A reader carrying the sibling's shape over takes the first
entry's weight out of the second entry's flag -- a wrong number, not an error.

An entry that states nothing still comes back with the neutral weight and a zero
offset, so a caller never has to ask whether a field was present.

The chroma fields follow **`ChromaArrayType`**, as the SAO flag does.

`TemporalMVP`, `CollocatedFromL0` and `CollocatedRefIdx` say which picture the
slice takes its motion from, and `MaxMergeCand` how many merge candidates it
uses. The index is stated only where the list has more than one entry.

⛔ **The offsets are bounded by the format, and two readers disagree on how
much.** 7.4.7.3 allows a luma offset in `-128..127` and a chroma offset
*difference* in `-512..511` — four times the half range. FFmpeg checks neither:
it bounds the chroma difference at ±2¹⁷, which guards its own arithmetic rather
than stating the format's range, and does not check the luma offset at all.
[libde265](https://github.com/strukturag/libde265) enforces both. This package
follows the tighter pair, and the boundaries are pinned on both sides.

**Not here:** `slice_qp_delta` and the fields after it. `ReferencesOf` stops
once it has everything about the pictures.

⛔ **No sequence range extension.** `WpOffsetHalfRange` is `1 << 7` unless
`high_precision_offsets_enabled_flag` is set, and that flag lives in a sequence
extension this package does not read — it sits past everything `ParseSPS`
consumes. A stream that sets it states its offsets over a wider range and would
be refused here, and nothing in this package can tell.

A picture may be carried by several slice segments, and only the one with
`first_slice_segment_in_pic_flag` set begins a new picture. That is what
`SplitPictures` counts, which is how a stream's pictures are counted without
decoding any of them.

## A ceiling on the sum, not on each half

⛔ **7.4.8 bounds `num_negative_pics` by `sps_max_dec_pic_buffering_minus1` and
`num_positive_pics` by what is LEFT of it**, so the ceiling is on the sum. A
bound on each half allows twice what any conforming stream may state: eight
before and eight after is well inside sixteen each way and outside the format.

The ceiling is fifteen — `sps_max_dec_pic_buffering_minus1` is at most
`MaxDpbSize - 1`, and A.4.2 puts `MaxDpbSize` at sixteen whatever the level.
GStreamer bounds `NumDeltaPocs` the same way.

## A width is read before it is narrowed

⛔ **`log2_max_pic_order_cnt_lsb_minus4` is a BIT COUNT** — a slice header reads
its order count with it, so a reader given a wrong one is misaligned from that
field onwards.

It was bounded, and the bound did not hold: the value was narrowed to `uint8`
*before* the check, and that conversion **wraps**. `1048576` becomes `0`, so the
width came out as 4 — the smallest legal one — and a value a million past the
range read as conformant. The two bit depths were narrowed the same way and not
bounded at all.

Every one of them is now read as a full `uint32`, checked against 7.4.3.2.1's
range, and narrowed afterwards. Both boundaries are pinned.

## Sibling

[`go-avkit/h264`](https://github.com/go-avkit/h264) is the same layer for
H.264/AVC. Its clause 8.2 derivations have HEVC counterparts here — order
counts, the reference set, the reference lists — with two exceptions: h264 also
reads the prediction weights and applies the list reordering, and this package
does neither yet.

**Windows, macOS, Linux; six 64-bit architectures.** 100% statement coverage,
gated in CI.

## Licence

BSD-3-Clause.
