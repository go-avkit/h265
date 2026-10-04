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
| `ShortHeaderError` | a unit that ended inside the syntax, said as such |

⛔ **HEVC's slice types are not H.264's.** Here `B` is 0, `P` is 1 and `I` is 2 —
the reverse of H.264, where `P` is 0 and `B` is 1. A reader that shares a
constant between the two formats reads every B slice as a P slice and every P
slice as a B one, and the stream still parses.

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
