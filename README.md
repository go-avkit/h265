# h265

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

**It does not decode pictures.** There is no CABAC, no residual, no transform,
no prediction and no deblocking — nothing here turns a bitstream into pixels. It
reads the syntax a caller needs in order to describe a track, cut a stream on
picture boundaries, or hand slices to something that does decode.

## What is in it

| | |
|---|---|
| `SplitAnnexB`, `SplitLengthPrefixed`, `Unit`, `UnitType` | the NAL layer, both framings |
| `ParseSPS`, `ParsePPS` | the parameter sets |
| `ParseSliceSegmentHeader`, `SliceSegmentHeader`, `SliceType` | the slice segment header |
| `SplitPictures`, `Picture` | coded pictures, from `first_slice_segment_in_pic_flag` |
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
last picture -- three pictures are named in two bits -- so an index out of range
is reachable from a field of conformant width. `Lists` refuses it.

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
