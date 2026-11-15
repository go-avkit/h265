# An HEVC decoder of our own: design

Status: design only, nothing implemented. Refs go-avkit/avkit#89.

Goal: replace `github.com/gen2brain/h265` v0.2.4 (MIT), which go-avkit/decode
uses today, with a decoder built on this package's parser and on
`go-videodsp/kernels`. It must decode Main, Main Still Picture and Main 10
(4:2:0, 8 and 10 bits), which covers the HEVC files of the library this work is
for (288 files) and the HEVC intra pictures HEIF carries. Contract:

- **The output equals the integer equations of ITU-T H.265.** HEVC
  reconstruction is integer arithmetic defined to the sample; there is no error
  bound to trade. Any instruction, order, fusion or skip is allowed as long as
  the result is that integer.
- **As fast as the hardware allows**: NEON first (Apple, Android arm64), AVX2,
  AVX-512 where measured faster, GPU only where measured faster with the data
  resident. OS decoders (VideoToolbox, MediaCodec) are an optional delegate
  behind the same interface, never the decoder.
- Algorithmic invention is wanted: §5 lists what this design does that a port
  would not, each to be kept only if it measures faster.

This is a clean-room document: clause and equation numbers, shapes and
measurements, no text of the standard and no code of another decoder. The
standard's text is not reproduced here; its equation numbers are given so an
implementer reads them there.

## 1. Bibliography and prior art (read on 2026-11-15)

| Source | What was opened | Use |
|---|---|---|
| ITU-T H.265 (V11) (01/2026), PDF | table of contents; 8.4.4.2 from the reference filtering equations 8-36 to 8-45 through 8.4.4.2.4 to 8.4.4.2.6 (planar, DC, angular; Tables 8-5, 8-6); 8.5.3.3.3.1 to 8.5.3.3.4.3 (fractional interpolation 8-215 to 8-261, Tables 8-8 and 8-9; default and explicit weighting 8-262 to 8-277); 8.6.1 end to 8.6.5 (QP 8-281 to 8-290 and Table 8-10, scaling 8-291 to 8-309, transforms 8-310 to 8-321, bypass 8-322/8-323); 8.7.2.1 and 8.7.2.5 (equations 8-344 to 8-405, Table 8-12); 8.7.3 (SAO, 8-406 to 8-415, Table 8-13); 9.3.4.3.1 to 9.3.4.3.3 (Figure 9-5, decisions 9-64 to 9-66, Tables 9-52 and 9-53, renormalisation) | the equations every kernel below computes. Sub-clauses not opened (merge/AMVP/TMVP inside 8.5.3.2, context selection inside 9.3.4.2, residual_coding() inside 7.3.8) are cited only at the level opened |
| ITU-T H.265.1 (10/2018), PDF | 6.5.1 to 6.5.5 (what a conformance bitstream zip holds; output-order conformance), the list of 6.6 and 6.7.1 (Table 1, Main/Main Still/Main 10 streams) | the oracle (§8) |
| go-avkit/h265 (this repository, v0.x on `main`) | README and file list | parsing already done (§3.1) |
| go-avkit/decode `main` | `hevc.go`, README (HEVC, Performance), `tools/jvt`; open PR #13 (`jvt bench`) | how HEVC is wired today, the conformance harness, the benchmark |
| go-videodsp/kernels `main` | README, `docs/design/design.md` | the H.264 kernels, API conventions (§4.3 there), dispatch, bounds proof |
| `priorart` on go-avkit/h265 and go-avkit/decode | 8 and 42 tracker entries | none is a pixel decoder; decode#5 is the gen2brain wrapper, decode#2 the open speed defect |

**Not opened in this session**: FFmpeg's `hevcdec`/`hevcdsp`, libde265, the HM
reference software, gen2brain/h265's source. FFmpeg and libde265 are LGPL: per
the fleet's clean-room rule, whoever reads them writes prose specifications
only, and **the implementer of any stage below must not open them**. HM is
BSD-licensed and gen2brain MIT; both may be read for structure, the equations
still come from the standard. The profile below names gen2brain functions only
as pprof prints them.

## 2. Profile of the decoder we replace

Harness: `jvt bench` from go-avkit/decode PR #13 (branch `bench-speed-2`),
`GOMAXPROCS=1` against `ffmpeg -threads 1 -benchmark -f null`, rounds
interleaved, CPU profile of our side. Raw output and `pprof -top -cum` in
[hevc-profile/](hevc-profile). Three clips of real content, Annex B, stream
copy unless said:

| Clip | Picture | Profile | Pictures | Bitrate | Notes |
|---|---|---|---|---|---|
| a | 1920x1080 | Main | 240 | ~2.6 Mbit/s | WEB H.264 source re-encoded with x265 (`-preset medium -crf 22`): the library's 8-bit HEVC files were not reachable today |
| b | 3840x1606 | Main 10 | 120 | ~9 Mbit/s | UHD x265 release, stream copy |
| c | 3840x1606 | Main 10 | 96 | ~38 Mbit/s | UHD Blu-ray x265 encode, stream copy, SAO off |

**Speed** (frames per second, median):

| Host | Clip | ours | ffmpeg | ours / ffmpeg |
|---|---|---|---|---|
| Ryzen 9 7950X (Zen 4), ffmpeg 6.1.1, quiet (`uptime` 0.10 to 0.46) | a | 98.0 | 434.0 | **0.23** |
| | b | 6.4 | 52.4 | **0.12** |
| | c | 8.0 | 32.7 | **0.24** |
| Apple M4 Max, ffmpeg 9.0.2 (NEON), 1-min load 5.1 to 5.8 but 5-min 15 to 17: **indicative** | a | 104.9 | 497.9 | 0.21 |
| | b | 7.7 | 21.1 | 0.37 |

The harness pinned to one core with `taskset -c 4` on Zen 4. Its printed load
reads `0.00` there because the host's locale writes `0,10` and the harness
keeps only the integer part of a comma decimal (reported on decode#13; the
gate at an integer `--max-load` still decides right). FFmpeg 9.0.2 on
the M4 decodes clip b 2.5 times slower than FFmpeg 6.1.1 on Zen 4 while clip a
is about equal: its arm64 10-bit HEVC path is the weaker one (not
investigated). Our allocations: **2454 per picture and 3.3 MiB per picture on
clip a**, 30 and 19 MiB on the 10-bit clips.

**Where the time goes** (share of our CPU samples, cumulative; Zen 4 unless
said):

| Stage | a (8-bit) | b (10-bit) | c (10-bit, 38 Mbit/s) | M4 a | M4 b |
|---|---|---|---|---|---|
| Inter prediction, all (`motionCompensate`) | 21.4 % | **69.2 %** | **58.9 %** | 8.5 % | 64.6 % |
| — luma interpolation | 5.1 | 44.1 | 36.5 | | 41.5 |
| — chroma interpolation | 3.5 | 17.8 | 14.5 | | 15.8 |
| — final rounding / averaging / weighting (`combine`) | 11.5 | 7.0 | 7.5 | 5.4 | 7.2 |
| SAO | **46.2** | 11.7 | 0 (off) | 30.6 | 13.1 |
| Deblocking incl. bS | 12.5 | 4.6 | 6.6 | 7.0 | 4.9 |
| Residual: CABAC levels, dequant, inverse transform, add | 7.5 | 6.0 | **24.2** | 6.3 | 9.5 |
| — of which `decodeResidual` (CABAC residual syntax) | 3.6 | 3.7 | 16.1 | 2.9 | 5.1 |
| — of which inverse transform | 2.2 | 1.3 | 3.9 | | |
| CABAC `decodeBin` alone | 1.6 | 1.2 | 5.7 | 1.2 | 1.4 |
| Intra prediction | <1 | <1 | 1.3 | | |
| Output copy and crop (go-avkit/decode `hevcFrame`) | 4.3 | 4.9 | 5.4 | 7.0 | 3.1 |
| Memory: `madvise`/`mmap`/span init (allocation) | ~2 | ~1 | ~1 | **~36** | |

Readings:

1. **10-bit motion compensation is portable Go** (`mcLuma[go.shape.uint16]`,
   a generic instantiation) and is half to two thirds of the time. gen2brain has
   assembly only at 8 bits (`mcTap8AVX2`, `mcTap8NEON`).
2. **SAO is the first cost at 8 bits** (46 %), scalar per sample with a `sign`
   helper per neighbour.
3. **The final rounding of a uni-predicted block is a separate pass**
   (`weightUni`, 10.6 % of clip a): the 14-bit intermediate is written, read
   back and rounded. It should not exist (§5.3).
4. **CABAC grows with bitrate**: 16 % residual syntax plus 6 % bins at
   38 Mbit/s. It is the part SIMD cannot help.
5. **Allocation**: on darwin the runtime returns freed spans with `madvise`,
   36 % of clip a on the M4. A decoder must not allocate per picture.
6. The output copy (5 %) disappears if the frame is handed out without copying
   (§6.7).

## 3. Stages

### 3.1 Parsing (exists, to be completed)

This package already reads the NAL layer (Annex B and length-prefixed), VPS
skip, SPS, PPS, the slice segment header up to `pred_weight_table()`, POC
(8.3.1), the RPS (8.3.2), the reference lists with modification (8.3.4) and the
weights (7.3.6.3). Missing for decoding:

- the rest of the slice header: `slice_qp_delta`, chroma QP offsets,
  deblocking overrides, `slice_loop_filter_across_slices_enabled_flag`, entry
  points (tiles, WPP);
- SPS/PPS fields the pixel stages need and that `ParseSPS`/`ParsePPS` skip or
  do not keep: scaling lists as values (7.3.4, default lists of 7.4.5), PCM,
  `amp`, `strong_intra_smoothing`, `transform_skip`, `cu_qp_delta`, tiles
  geometry, `sign_data_hiding`, `cabac_init_present`, loop-filter flags; the SPS
  range extension (`high_precision_offsets_enabled_flag`, which the README
  already says is not read) is read and refused when set, Main/Main 10 never
  set it;
- 8.3.3 (generating unavailable reference pictures) and 8.3.5 (collocated
  picture).

Allocation discipline: parameter sets parsed into fixed-size arrays keyed by id
(16 SPS, 64 PPS), slice headers into one reused struct.

### 3.2 CABAC and slice data (9.3, 7.3.8)

The serial core. Everything else waits on it, and nothing vectorises a bin
whose range depends on the previous bin. The design aims at the fewest
instructions and mispredictions per bin, then at taking bins out of the engine
altogether.

**Engine (9.3.4.3).**

- Offset held in a 64-bit window left-aligned with the range scaled by the same
  shift, so renormalisation (9.3.4.3.3, one bit per loop iteration in the text)
  is one count-leading-zeros and one shift, and the window is refilled several
  bytes at a time, not per bin.
- **Emulation prevention removed before CABAC starts**, in one pass over the
  slice data (searching `00 00 03` with a vector byte search from go-simd), into
  a reused buffer. The refill then never tests for it. The cost is one copy of
  the slice payload, which is a few percent of a picture's bytes moved.
- A context is one byte, `pStateIdx << 1 | valMps`; Table 9-52 and Table 9-53
  are folded into one table indexed by (context byte, `qRangeIdx`) returning
  the LPS range and both next context bytes, so a decision is one load plus a
  conditional select. The MPS path is the fall-through.
- Engine state (`range`, window, bits left, read pointer) lives in locals of
  the function that decodes a whole syntax structure (a CU, a residual block)
  and is written back once, not per bin through a struct pointer.

**Bypass bins in batches (new here).** A run of k bypass bins (DecodeBypass,
9.3.4.3) is
one step of binary long division: the offset with the next k bits appended,
divided by the range, gives the k bins as the quotient and the new offset as the
remainder. HEVC has long bypass runs: the sign bits of up to 16 coefficients of
a 4x4 sub-block (`coeff_sign_flag`), the prefix and suffix of
`coeff_abs_level_remaining`, MVD suffixes, `cu_qp_delta_abs` and the SAO
offsets. Decoding the signs of a sub-block in one integer division replaces up
to 16 iterations with a data-dependent branch each. To be measured against the
plain loop on both architectures; kept only if faster.

**Residual coding (`residual_coding()` in 7.3.8, context selection in 9.3.4.2).**

- `sig_coeff_flag` context increments for a 4x4 sub-block depend on
  (`log2TrafoSize`, `scanIdx`, colour component, position of the sub-block,
  the coded-sub-block flags of its right and lower neighbours). Precompute, for
  each such combination, the 16 increments of the sub-block in scan order: the
  inner loop becomes one table read per bin.
- `last_sig_coeff_x/y` give the region beyond which every coefficient is zero;
  the decoder keeps it and hands it to the transform (§3.5).
- Dequantisation (8-309) is applied to each non-zero level as it is decoded,
  in the residual loop, writing straight into the `int16` coefficient block;
  only non-zero positions are touched, and the block is cleared afterwards by
  the same sparse positions rather than by a full memset.
- Sign data hiding (`sign_data_hiding_enabled_flag`) applied there too.
- Separate specialisations per TU size and scan order (diagonal, horizontal,
  vertical) generated, not branched per bin.

**Syntax above residuals.** Coding quadtree, CU, PU, TU, SAO syntax, merge and
AMVP indices. Neighbour information (depths, skip flags, intra modes, motion)
lives in per-CTU-row line buffers at 4x4 (motion: 4x4 granularity, then 16x16
compressed for the temporal candidate of 8.5.3.2), not in picture-sized arrays
touched at random.

**Across cores (not part of the single-core target):** WPP entry points
(x265 enables WPP by default) allow one CTU row per core with a two-CTU lag;
tiles and slices are independent; frame threading as FFmpeg's `-threads N`.
Section 6 keeps the data structures compatible with it.

### 3.3 Intra prediction (8.4.4.2)

35 modes for blocks 4 to 32. Steps: reference sample availability and
substitution, filtering ([1 2 1] smoothing, 8-41 to 8-45, or the bi-linear
strong smoothing of 32x32 luma, 8-36 to 8-40), then
planar (8-46), DC with its edge filter for luma under 32 (8-47 to 8-52), or
angular (8-53 to 8-68, Tables 8-5 and 8-6) with the vertical/horizontal edge
filters (8-60, 8-68).

- Angular modes 18 to 34: each output row is the two-tap interpolation
  `((32 − iFact)·ref[x+iIdx+1] + iFact·ref[x+iIdx+2] + 16) >> 5` with `iIdx`,
  `iFact` constant along the row (8-56 to 8-58): one row is one vector
  multiply-add from two unaligned loads. Modes 2 to 17 are the same computation
  on the left column, transposed (8-61 to 8-67): compute as a vertical mode
  into a scratch block and transpose on store (8x8 and 16x16 transposes are
  shared with the transforms).
- `iFact == 0` rows (pure copies, modes 2, 10, 18, 26, 34 every row; others at
  some rows) take a copy path.
- Intra is 1 % of time on these clips but the whole cost of HEIF and of
  intra-only streams.

### 3.4 Inter prediction (8.5.3)

Motion data derivation (merge, AMVP and the temporal candidate, all in 8.5.3.2)
is scalar control logic. Sample prediction:

- **Luma, 8.5.3.3.3.2**: 8-tap filters (8-224 to 8-238), positions per
  Table 8-8. `shift1 = Min(4, BitDepthY − 8)`, `shift2 = 6`,
  `shift3 = Max(2, 14 − BitDepthY)`. Integer position: `A << shift3`.
  Reference coordinates clamped into the picture (8-222, 8-223).
- **Chroma, 8.5.3.3.3.3**: 4-tap filters (8-248 to 8-261), eighth positions,
  Table 8-9.
- **Weighting, 8.5.3.3.4**: default uni `Clip((p + offset1) >> shift1)`
  (8-262/8-263), default bi `Clip((p0 + p1 + offset2) >> shift2)` (8-264),
  `shift1 = 14 − bitDepth`, `shift2 = 15 − bitDepth`; explicit uni/bi (8-265 to
  8-277).

**Intermediate ranges** (the reason `int16` lanes are legal): the luma taps'
positive sum is 88 and negative sum 24. First pass, 8 bits (shift1 = 0):
−6120 … 22440; 10 bits (shift1 = 2): −6138 … 22506. Both fit `int16`; the
second (vertical) pass accumulates in 32 bits and shifts by 6. The 14-bit
"prediction" domain is therefore `int16` at 8 and 10 bits alike.

**Exact shortcuts** (each a theorem on the equations above, tested
exhaustively):

- Integer vector, uni, default weights: `(A << s + 2^(s−1)) >> s` with
  `s = 14 − bitDepth` is `A`: **a copy**, no arithmetic.
- Integer vectors, bi, default weights: `(A << s + B << s + 2^s) >> (s + 1)` is
  `(A + B + 1) >> 1`: a rounding average (`URHADD`, `PAVGB/PAVGW`), no clip
  needed.
- One fractional and one integer vector: one interpolation, one rounding
  average against the shifted integer samples, fused.

**Fused output (§5.3)**: the filter that produces the last prediction of a
block also applies the rounding, the clip and (for bi) the average with the
first prediction, and stores samples. The `int16` intermediate block exists only
for the first list of a bi-predicted block.

**Edge emulation** only when a block's footprint (block plus 3 before, 4 after
in luma; 1 before, 2 after in chroma) leaves the picture, built into a small
stack block, instead of padded reference pictures.

### 3.5 Transforms (8.6.2 to 8.6.5)

- Scaling (8.6.3, 8-300 to 8-309): done sparse in the residual loop (§3.2).
- Two-stage transform (8.6.4.1): columns, then `Clip3(coeffMin, coeffMax,
  (e + 64) >> 7)` (8-314), then rows, then `(r + (1 << (bdShift − 1))) >>
  bdShift` with `bdShift = Max(20 − bitDepth, 0)` for Main/Main 10 (8-295,
  8-299). The intermediate clip is normative and stays.
- DCT 4/8/16/32 from the matrix of 8-317 to 8-321 as even/odd partial
  butterflies: exact, because the matrix product is a sum of integer products
  that cannot overflow 32 bits for `int16` inputs, and an exact sum does not
  depend on its order. DST 4x4 for intra luma 4x4 (8-315, 8-316).
- **Zero-region aware**: the last significant position bounds the non-zero
  columns and rows. The column pass runs only over non-zero columns (zero
  columns give zero), the row pass reads only the non-zero part of each row.
  For 32x32 blocks with a few low-frequency coefficients, the usual case, this
  removes most of the work.
- **DC only** (one non-zero coefficient at (0,0), very frequent): the residual
  is one constant, obtained by running the two stages on the scalar (the same
  equations, so exact); the add-and-clip is then a broadcast add.
- **Add fused into the row pass**: the second stage writes prediction plus
  residual, clipped to the bit depth, straight into the picture (`[]byte` or
  `[]uint16`).
- Transform skip (8-298), and `cu_transquant_bypass` (8.6.2, 8.6.5) as plain
  paths.

### 3.6 Deblocking (8.7.2)

- Edges on the 8x8 grid only (8.7.2.1), luma bS 0..2 from 8.7.2.4, chroma only
  where bS = 2.
- **bS computed during parsing**, when the CU's prediction and `cbf` are
  known, into per-CTU 2-bit edge maps for vertical and horizontal edges. A
  four-line segment with bS 0 is never visited.
- Luma decisions per four-line segment (8-347 to 8-369; `β`, `tC` from Table
  8-12 scaled by `1 << (BitDepthY − 8)`, 8-349, 8-351), strong/normal filters
  (8-389 to 8-402); chroma 8-403 to 8-405 with `QpC` from Table 8-10 via
  8-382/8-383.
- Vectorised as the H.264 kernels already are: one line per 16-bit lane, every
  condition a lane mask, vertical edges transposed in and out. A call takes a
  **run of segments** (a whole CTU edge, up to 64 lines, with per-segment bS,
  β, tC) so the call and bounds proof are paid once per run, not per 4 lines
  (the kernels README notes that small calls gain little).
- PCM / bypass samples not modified: a per-segment flag; the rare segment that
  has one takes the portable path.

### 3.7 SAO (8.7.3)

- Per CTB, after deblocking (8.7.3.1). Band offset (8-414, 8-415): the band of
  a sample is `sample >> (bitDepth − 5)`; only four consecutive bands carry an
  offset, so the whole operation is a 32-entry table lookup and a saturating
  add: `TBL`/`VQTBL2` on NEON, `PSHUFB` on 8-bit AVX2, `VPERMW` (AVX-512BW) or
  two `PSHUFB` at 10 bits.
- Edge offset (8-409 to 8-413, Table 8-13): `edgeIdx = 2 + Sign(c − a) +
  Sign(c − b)` from two comparisons per neighbour (each Sign is the difference
  of two compare masks), remapped by 8-412 and turned into the offset by a
  5-entry table lookup, then add and clip: about ten vector instructions per 16
  samples, against the scalar loop that is 46 % of clip a.
- SAO reads deblocked, not yet SAO-filtered, neighbours. The decoder keeps
  one line above and one column left of each CTB pre-SAO (a few KiB) instead
  of a full copy of the picture.
- The "not modified" conditions (PCM with loop filter disabled, bypass,
  `SaoTypeIdx` 0) and the picture/slice/tile boundary rule of 8.7.3.2 are
  handled per CTB: an interior CTB with no exception (the usual case) runs the
  kernel on the whole CTB; a border CTB runs it on its interior and the
  portable code on its border lines.

### 3.8 DPB and output (8.3.2, C.5.2)

- A fixed pool of pictures sized by `sps_max_dec_pic_buffering_minus1 + 2`,
  allocated at SPS activation within `Config.MaxFrameMBs`, recycled; **zero
  allocations per picture in steady state** (today: 2454).
- Planes padded by the alignment the kernels want on the right (not by an
  emulation border), stride a multiple of 64 bytes.
- Output order and bumping per C.5.2.2 to C.5.2.4 (already read for decode's
  wrapper), cropping to the conformance window.
- Motion field of each reference kept at 16x16 granularity for TMVP.

## 4. Kernels for go-videodsp/kernels

Conventions are those of that repository's design §4.3: `[]byte` planes for 8
bits, `[]uint16` for 9 to 16 with a `bitDepth` argument; bounds proved once per
call in Go; portable Go written from the equations is the oracle; NEON first,
then AVX2; zero allocations. HEVC block widths are 4, 8, 12, 16, 24, 32, 48, 64;
assembly per width class 4, 8, ≥16 (16-column strips).

Expected share is of today's single-core time on Zen 4, (a / b / c).

| # | Package | Kernel | Element types | Clauses | Share today | New or shared |
|---|---|---|---|---|---|---|
| 1 | `mc` | Luma 8-tap: `prep` (to `int16` 14-bit), `put` (uni, rounding fused), `avg` (bi with an `int16` block, fused), `wput`/`wavg` (explicit weights fused); integer-position copy/average shortcuts | `byte`, `uint16` | 8.5.3.3.3.2, 8.5.3.3.4 | 5 / 51 / 43 % | **new** (H.264's six-tap differs); shares the generator, transposes, bounds proof and benchmark harness |
| 2 | `mc` | Chroma 4-tap, same five output modes | `byte`, `uint16` | 8.5.3.3.3.3 | 3.5 / 18 / 15 % | new |
| 3 | `lf` | SAO band and edge (4 classes), whole CTB | `byte`, `uint16` | 8.7.3 | 46 / 12 / 0 % | new |
| 4 | `lf` | HEVC luma deblock (decision + strong/normal, one run of segments per call), chroma deblock | `byte`, `uint16` | 8.7.2.5 | 12.5 / 4.6 / 6.6 % | new arithmetic; reuses the H.264 kernels' transpose and lane-mask structure |
| 5 | `itx` | DCT 4/8/16/32, DST 4, zero-region variants (non-zero column count), DC-only, add-and-clip fused, transform skip | coefficients `int16`, destination `byte`/`uint16` | 8.6.2, 8.6.4 | 2.2 / 1.3 / 3.9 % (plus the add) | new (`itx` is empty) |
| 6 | `ipred` | Planar, DC (+ edge filter), angular vertical family, horizontal via transpose, reference smoothing and strong smoothing | `byte`, `uint16` | 8.4.4.2 | <1 / <1 / 1.3 % | new (`ipred` is empty) |
| 7 | `pix` | Edge-emulation block, copy, rounding average (`uint16` too) | `byte`, `uint16` | 8-222/8-223 | in MC today | extends the planned `pix` |

Order of work: 1 and 2 (the 10-bit gap), 3 (the 8-bit gap), 4, 5, 6, 7. Every
kernel ships portable + NEON in its first PR, AVX2 in the next; AVX-512 only
with a benchmark that beats AVX2 on Zen 4 (which executes 512-bit operations as
two halves) and on a native-512 core if one is reachable.

Not a kernel: CABAC (§3.2) stays in this repository as Go; a residual-loop
assembly specialisation is considered only if the Go version, after §3.2,
still exceeds 15 % at 38 Mbit/s.

## 5. What this design does that a port would not

Each item is kept only if it measures faster; dropped ones are listed in the
PR that dropped them.

1. **Bypass runs by one division** (§3.2).
2. **Emulation prevention removed up front** so the CABAC refill has no test.
3. **Fused prediction output**: interpolation, rounding, clip, bi-average and
   explicit weights in one kernel and one store; the separate 14-bit pass that
   costs 10.6 % today disappears; integer vectors become copies and averages
   (§3.4).
4. **Sparse residual**: dequantisation only at decoded positions, zero-region
   and DC-only transforms, add fused (§3.5).
5. **bS during parsing**, deblock runs per CTU edge, bS-0 segments never
   visited (§3.6).
6. **SAO by table lookup** at both bit depths, with line buffers instead of a
   picture copy (§3.7).
7. **CTU-lagged in-loop filters**: deblocking and SAO of CTU (x−1, y−1) run
   right after CTU (x, y) is reconstructed (the dependency 8.7.2.1's note
   allows), while its samples are still in L1/L2, instead of two whole-picture
   passes after the last slice (`finishPicture` today).
8. **Zero-copy output** (§6.7).
9. **Self-checking decode**: the decoded picture hash SEI (D.2.20, D.3.20),
   wherever a stream carries one, is verified in tests
   for every picture, so a wrong sample is caught where it happens, not by a
   frame MD5 at the end.

## 6. Architecture notes

1. **No generics in hot paths.** gen2brain's hottest functions are
   `go.shape` instantiations; per-element-type code is generated or written
   twice, and a decoder chooses its function table once at SPS activation.
2. **Two-phase CTU (parse, then reconstruct)** with a compact per-CTU record
   (modes, motion, coefficient blocks with their last position). On one core
   it groups kernels by type within a CTU; on several cores it is the seam for
   entropy/reconstruction pipelining and WPP.
3. **Line buffers, not picture maps**, for neighbour state.
4. **Planes allocated once** per SPS (§3.8).
5. **Bounds**: picture size checked against `Config.MaxFrameMBs` before any
   plane exists (as decode does today); every slice-data index is bounded by
   construction (CTU address < `PicSizeInCtbsY`); fuzzing of CABAC, residual
   and the whole decoder, corpus committed; bombs under `GOMEMLIMIT` and
   `ulimit -v`.
6. **GPU**: not on the critical path. MC and the loop filters of picture N
   feed picture N+1's prediction, so a discrete GPU (the Zen 4 host's sits
   behind a PCIe x4 link) only pays if pictures stay resident there; on Apple's unified
   memory a Metal deblock+SAO overlapping the next picture's entropy decoding
   is a measured experiment after the CPU target, not before. CABAC never
   leaves the CPU.
7. **Zero-copy output**: a `Frame` refers to the DPB picture's planes (offset,
   stride) and is released by the caller; at 10 bits on a little-endian host
   the `[]uint16` plane is already `yuv420p10le` and is handed out as bytes
   without the per-sample copy (`cropPlane16`, 5 % today). This changes
   go-avkit/decode's `Frame` contract and is its own PR there.
8. **Delegation**: VideoToolbox / MediaCodec behind the same decoder
   interface, off by default, outside this repository.

## 7. Conformance corpus

- **ITU-T H.265.1 bitstreams** for Main, Main Still Picture and Main 10
  (6.7.1, Table 1): the JCT-VC `HEVC_v1` directory at
  `https://www.itu.int/wftp3/av-arch/jctvc-site/bitstream_exchange/draft_conformance/HEVC_v1/`,
  which go-avkit/decode's `jvt --codec hevc fetch --all` already downloads (147
  streams). Each zip holds the bitstream, the decoded pictures or their hashes
  when present, a description and a trace file (H.265.1 6.5.2).
  **Licence**: the streams are an electronic attachment to an ITU
  Recommendation (H.265.1 Summary); no redistribution terms are stated, so they
  are **not committed** here (the policy decode already follows); tests run
  when an environment variable points at them.
- **Trace files as a syntax oracle**: the trace in each zip lists the decoded
  syntax elements. Slice-data parsing (PR 3 below) is checked against them
  before any sample is reconstructed, so a CABAC or context error is located
  to the syntax element, not to a picture MD5.
- The 5 x265 streams of decode's `testdata/hevc/streams` (CI, MD5s committed)
  and the 7 real-file hashes of `testdata/hevc-real-files.txt`.

## 8. Oracles

In order of authority:

1. **The standard's equations**, through per-kernel portable references and
   exhaustive grids (all luma 16 / chroma 64 positions × widths × bit depths;
   all β/tC indices and bS; all SAO classes and band positions; transforms on
   random and extreme coefficient blocks within CoeffMin/CoeffMax).
2. **The conformance suite's own hashes / YUV** and **the decoded picture hash
   SEI** where present (H.265.1 6.5.5: the samples shall equal the reference).
3. **`ffmpeg -f framemd5`** per picture, one thread, native pixel format
   (`yuv420p`, `yuv420p10le`), as decode's harness does. Where FFmpeg and the
   suite disagree, the suite decides: decode's README records
   `SAODBLK_A_MainConcept_4` and `SAODBLK_B_MainConcept_4`, where FFmpeg differs
   from picture 0 and gen2brain agrees with the suite.
4. **gen2brain/h265 as a differential oracle** while it is still a
   dependency: same streams, every picture compared, until the switch.

## 9. Target

Per-picture budget on Zen 4 for clip b (10-bit): ours 156 ms, FFmpeg 6.1.1
19 ms. The H.264 kernels measured 4 to 47 × over portable Go on luma MC
(4x4 to 16x16) and 2 to 7 × on deblocking (kernels README, Zen 4). Assuming
10 × on HEVC MC and SAO, 2 × on residual and 3 × on deblocking: inter
prediction ~108 ms → ~11, SAO ~18 → ~2, residual ~9 → ~4, deblocking ~7 → ~2,
the output copy ~8 → 0, the rest ~6 → ~4: about **23 ms, 0.8 × FFmpeg**. Clip
a (8-bit, 10.2 ms against 2.3) comes out near 1.0 × by the same arithmetic,
and clip c (38 Mbit/s, 125 ms against 31) near 0.9 × if the CABAC work of
§3.2 makes residual syntax 1.7 × faster. The targets below keep a margin over
that arithmetic: the assumed speed-ups are not yet measured on HEVC shapes, and
FFmpeg's 8-bit paths are its most tuned.

**Targets, single core, `ffmpeg -threads 1` as reference:**

| | Zen 4 (FFmpeg 6.1.1 AVX2) | M4 Max (FFmpeg 9.0.2 NEON) |
|---|---|---|
| Main 10, ≤ 10 Mbit/s | 0.75 × | ≥ 1.0 × (FFmpeg's arm64 10-bit is the weaker path) |
| Main, ≤ 10 Mbit/s | 0.75 to 0.8 × | 0.75 × |
| Main 10, 38 Mbit/s | 0.7 × | 0.8 × |
| Stretch, after §5 items prove out | parity | above parity |

Today: 0.12 to 0.24 × on Zen 4. Parity is not promised: FFmpeg's entropy path
is hand-tuned C with branch-free tricks the Go compiler does not always
reproduce, and bounds checks remain in the parser. Multi-core (WPP, frame
threads) is a separate target, measured against `-threads N`.

## 10. Pull requests, in order

go-avkit/h265 (this repository):

1. This document.
2. `internal/cabac`: engine, context tables (normative values of the
   initialisation tables of 9.3.2 and of Tables 9-52/9-53, declared as such),
   bypass batches, fuzz, benchmark in bins per second.
3. Slice data syntax (7.3.8) to a per-CTU record, no samples; checked against
   the H.265.1 trace files.
4. Intra reconstruction, portable: reference substitution and filtering,
   35 modes, dequant, DCT/DST, add. Intra-only conformance streams with loop
   filters disabled.
5. Deblocking and SAO, portable, CTU-lagged. All intra streams; **Main Still
   Picture complete: HEIF unblocked here.**
6. Inter: merge, AMVP, TMVP, MC through `go-videodsp/kernels` (portable
   first), weighted prediction. All `HEVC_v1` Main streams.
7. Main 10 (`uint16` planes end to end). All Main 10 streams.
8. DPB pool, output order, zero allocations per picture (allocation test).
9. A `Decoder` API mirroring what go-avkit/decode uses from gen2brain, and the
   differential test against it.

go-videodsp/kernels, in parallel from PR 6: kernel 1 (portable + NEON), 1
(AVX2), 2, 3, 4, 5, 6, 7, then AVX-512 where measured. Each with its README
table against FFmpeg's figure for the same stage.

go-avkit/decode: switch HEVC to go-avkit/h265 once (a) every stream gen2brain
passes today passes, and (b) `jvt bench` shows us faster than gen2brain on all
three clips on both hosts; then drop the dependency, then the zero-copy `Frame`
(§6.7). README, landing and docs pages of each repository updated in the PR
that changes what a user sees.

## 11. Measurement protocol for every PR above

`jvt bench` (decode#13) on the three clips, `GOMAXPROCS=1`, interleaved with
`ffmpeg -threads 1`, at a 1-minute load below 8 (the Zen 4 host under
`taskset` for amd64; the M4 when quiet, otherwise marked indicative), raw output committed
next to the README table. Per-kernel Go benchmarks at HEVC block sizes. The
clips themselves are not committed (they are cut from copyrighted files); their
description above and the hashes of their decoded pictures are.
