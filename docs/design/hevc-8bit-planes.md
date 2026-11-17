# Native 8-bit planes

Status: design, no code. Tracks go-avkit/decode#101 (8-bit HEVC through decode is 7 to 12 % slower
on x86 than with gen2brain/h265, whose 8-bit path works on bytes end to end).

## Why

Every plane is `[]uint16`, whatever the bit depth. For an 8-bit stream that costs:

1. **The narrowing out of the decoder.** `decode` packs each picture into a one-byte-a-sample
   `Frame`: 0.62 ms per 1080p frame cache-hot on Zen 4, 0.68 to 0.75 ms when the frames are kept
   (measured, decode#101). At 77 to 89 fps that is 5 to 6 % of a frame.
2. **Twice the memory traffic** in every stage that reads or writes a picture: 6 MB instead of 3 MB
   per 1080p 4:2:0 picture, so the reference pictures of a B pyramid (up to 6 in the bench
   streams) no longer fit the caches the way bytes would. This is the rest of the gap to v0.9.0
   and has not been measured on its own; the plan measures it stage by stage (see Order).

The decoder alone does 91 to 104 fps on the bench streams; v0.9.0 through decode does 87 to 96.
The aim: native 8-bit h265 at or above 110 fps, and decode's 8-bit rows at or above v0.9.0's.

## The type

Decided (coordinator, 2026-11-17): **two typed fields and no field that is nil depending on the
depth.** `Plane` has `Pix8 []uint8` and `Pix16 []uint16`, exactly one non-nil, documented by bit
depth: `Pix8` for a plane of bit depth 8, `Pix16` for every other depth. `Plane` gets `Is8() bool`,
`BitDepth() int` (the plane knows its depth; the picture's `BitDepth` array stays) and
`At(x, y int) uint16` (any depth, for tests and tools, not for the hot paths). A `DecodedPicture` is
a byte picture when luma and chroma are both 8 bits (the only case a byte picture makes sense for);
every other picture is `Pix16` as today. `Stride`, `Width`, `Height` keep their meaning.

`Pix` is **renamed** `Pix16`, with no deprecated alias: the only importer of `go-avkit/h265` in our
organisations is go-avkit/decode (checked: no other go.mod of the go-avkit organisation, nor any
clone on the machine, requires it; GitHub code search finds none), and decode moves in the same
step as the type. Below v1 this is a minor release with a "Behaviour change" note: `Pix` no longer
exists, and an 8-bit picture has `Pix8` set.

Rejected alternatives:

* *`Plane[T]` generic in the public API*: infects `DecodedPicture`, `Output` and every caller.
* *A second picture type*: duplicates the DPB, `Crop`, `Clone`, the hash check and the API.
* *Keep filling `Pix` at 8 bits as well*: no caller breaks, but it is exactly the copy this removes.
* *`Pix` kept and nil at 8 bits*: a trap that compiles and fails at run time.

`Clone` keeps the depth of its source. decode takes `Pix8` straight: its 8-bit `Frame` is a
`memmove` of a plane per row (and, if its contract is relaxed later to a read-only view, nothing).

## Where the code goes: one decision per block, generic bodies

The stages' Go loops are generic over the sample type (`func f[T uint8 | uint16](...)`): the
instantiations for `uint8` and `uint16` have different GC shapes, so each gets its own compiled
body, with no dictionary indirection, and the source stays one function. The choice is made once
per block (`if pl.Is8()`) where a kernel is called, and at the call of the generic body
elsewhere. `interpfast.go` already does this for `hpass`/`vpass` (`[S uint16|int16]`).

## Stage by stage

What reads or writes samples of the picture, and what it needs at 8 bits (file, today's type):

| Stage | Reads | Writes | At 8 bits |
|---|---|---|---|
| Interpolation sources, `interpolateFast` (`interpfast.go`) | reference plane `[]uint16`; the edge-emulation scratch `sc.edge [71*71]uint16` | int16 mid, int32 pred | source `[]uint8`; `sc.edge` becomes `[71*71]uint8`; the clamp copy copies bytes. Intermediates stay int16/int32 |
| Kernels `HPass8/4`, `HPassW8/4`, `VPassU8/4`, `Widen` (kernels) | `[]uint16` source | int16 / int32 | `[]uint8` source variant of each: load 8 bytes and zero-extend (`VPMOVZXBD` / `VPMOVZXBW` on AVX2, `UXTL` after an 8-byte `LDR` on NEON), then the same arithmetic. The accumulator and the output are unchanged, so the bit-exactness argument is |
| `VPass8/4` second stage | int16 mid | int32 | unchanged |
| Weighted prediction, `WeightUni/Bi` (kernels) | int32 pred | `[]uint16` dst (clipped to 2^bd-1) | `[]uint8` dst variant: the last step is `VPACKUSDW` then `VPACKUSWB` (AVX2), `SQXTN`+`SQXTUN` (NEON), the clip to 255 being the saturation; the refusals for the bit depth stay |
| `Average` (kernels) | two `[]uint16` | `[]uint16` | byte variant: `VPAVGB` / `URHADD .16B`, twice as wide per instruction |
| Full-sample copy of a uni-predicted block (`inter.go`) | ref rows | dst rows | `copy` of bytes: the Go `copy` of the rows already is a `memmove` |
| Residual add, `reconstruct` and intra `block` (`recon.go`) | plane samples, int32 residual | plane | generic loop at first (a kernel after: it is 4.5 % of the profile on one line today) |
| Intra reference samples (`recon.go`, `refs[i] = int32(pl.Pix[...])`) | plane | none | generic read; 8-bit values widen on load |
| PCM (`recon.go`) | PCM sample arrays `[]uint16` (`codingunit.go`) | plane | the shift by `BitDepth - PCM.BitDepth` stays; the store narrows |
| Deblocking (`deblock.go`: `filterLumaEdge`, chroma) | plane | plane | `filterLumaEdge[T]` generic; 8-bit filters read 4 bytes a side |
| SAO (`sao.go`) | the deblocked copy `src []uint16`, plane | plane | `src` of bytes (the pool of scratch buffers becomes two, one per type); `SAOBand`/`SAOEdge` byte variants (`src` and `dst` both bytes): the same arithmetic on zero-extended lanes, the clip to 255 on the way back |
| DPB: missing-reference fill (`dpb.go`) | none | plane | fill with `1 << (bd-1)` bytes |
| `Crop`, `Clone` (`dpb.go`) | plane | plane | `Pix8` windows and copies, same stride rules |
| Picture hash SEI (`sei.go`) | plane | none | the MD5/CRC/checksum loops already branch on `deep := BitDepth > 8` and write bytes: at 8 bits they read `Pix8` and skip the narrowing |
| `PicParams`/scaling, transform, CABAC, motion | none | none | untouched: no sample of the picture |

Everything that touches `Pix` is in the table (`git grep '\.Pix\b'` lists the 9 non-test files:
deblock, dpb, inter, interpfast, recon, sao, sei, plus the tools). The bit depths above 8 keep
the 16-bit code and kernels as they are.

## What the kernels need

New in go-videodsp/kernels, generated by `internal/genhevc` from the same loop nests (a flag
`bytes bool` on the emitters, like `horiz`): `hpass8`/`hpass4` byte-source (NEON and AVX2, the
shift variants), `HPassW8/4`, `VPassU8/4`, `Widen`, `WeightUni/Bi` and `Average` byte-destination,
`SAOBand`/`SAOEdge` byte, `TryXxx8` entry points beside the 16-bit ones (the `Try` names get a `U8`
suffix: `TryHPass8U8`, ...). Same discipline as every kernel so far: bounds and overlap checks,
the text's formulas as the oracle in 64 bits, guard pages, a committed fuzz corpus run on both
architectures from the package directory, 100 % coverage on arm64, amd64 and purego, a Zen 4 and
an M4 bench file each. Bit depth is fixed at 8, so the bit-depth argument of the 16-bit kernels
goes (the shift is always 0 for the filters, `shift3` is 6).

## Order, and how each step is measured

Each step is one h265 PR (and one kernels PR where it needs kernels), measured on fractal with
the 4-stream ABBA table against the previous main, the refusal gate at zero, md5 equal to ffmpeg's.

1. **The type and the Go path, no kernels**: `Pix8`, the generic loops for every stage in the
   table, the 8-bit pictures stored as bytes, the 16-bit kernels not called for them (their
   fallbacks are the Go loops, which the refusal counter reports as refusals: the gate is relaxed
   for byte pictures in this step only, and the counter names the reason). Expected: a loss in
   the interpolation and weighting (the kernels are gone) against a gain in the copy and the
   memory traffic. Decided: **step 1 lands on the Go path alone only if no stream gets slower on
   either machine** (x86 and M4, the whole 4-stream table); otherwise it stays on its branch until
   the byte kernels of steps 2 and 3 catch up, and lands with them. Either way it is the
   measurement that separates "the narrowing" from "the traffic" in the gap, which today is only
   estimated.
2. **Byte-source interpolation kernels** (`HPass`, `HPassW`, `VPassU`, `Widen`) and the edge
   scratch: the largest block of the profile (the interpolation passes, 20 % cum).
3. **Byte-destination kernels** (`Weight`, `Average`, then the full-sample copy path).
4. **SAO and deblocking at bytes** (SAO is 20 % cum, deblocking 10 %).
5. **decode**: take `Pix8` into the 8-bit `Frame` with `memmove` rows; remove the narrowing;
   decode#101 closes when its rows are at or above v0.9.0's.

Stopping rule: after step 1 and again after step 2 the table is read; if the 8-bit rows are not
moving, the design is revisited before the next step, not after the last.

## Risks

* **API break**: `Plane.Pix` becomes `Pix16` and 8-bit pictures have `Pix8`. decode is the only
  importer; the release carries a "Behaviour change" line.
* **Code size**: every kernel gets a second set of assembly (about +40 % of the `hevc` package's
  assembly). The generator carries it; the tests are the same table with a second column.
* **A single wrong lane in a byte kernel** is what the oracle, the guard pages and both-architecture
  fuzzing exist for. The lesson of kernels#26 (an AVX2 pack that saturates where the reference
  truncates) applies to the byte pack: the clip to [0, 255] is done in 32 bits first, so the
  saturating packs (`VPACKUSDW`, `VPACKUSWB`) never see a value outside their range and agree with
  the reference; the test that pins it feeds values far beyond the range to the kernels and to the
  reference.
* **Cold-cache cost of retained frames** (the 0.68 to 0.75 ms vs 0.62 ms of decode#101) goes away
  with the narrowing only if decode takes the planes without a pass over them; it still pays one
  `memmove`.
