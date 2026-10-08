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

A picture may be carried by several slice segments, and only the one with
`first_slice_segment_in_pic_flag` set begins a new picture. That is what
`SplitPictures` counts, which is how a stream's pictures are counted without
decoding any of them.

## Sibling

[`go-avkit/h264`](https://github.com/go-avkit/h264) is the same layer for
H.264/AVC, and goes further: it also carries the derivations of clause 8.2
(picture order counts, the reference set, the reference lists, the prediction
weights). HEVC's equivalents are not here yet.

**Windows, macOS, Linux; six 64-bit architectures.** 100% statement coverage,
gated in CI.

## Licence

BSD-3-Clause.
