# tools/bench

Speed of three HEVC decoders on one core, interleaved: this package's `h265.Decoder`,
`go-avkit/decode` (the public API users have today, over a third-party decoder; it is
given the MP4) and `ffmpeg -threads 1`. It is a separate module (it depends on `decode`),
so `go test ./...` of the repository does not build it.

The compared figure is CPU time (user + system), as frames per second. ffmpeg's is its
`-benchmark` utime + stime; ours and decode's are the `getrusage` delta around the decode
(the file read excluded). Our time includes the copy that crops each output picture.
The 1-minute load is read before each round and the run waits while it is 8 or more.

## Streams

The streams are regenerated, never committed. From the two H.264 sources of the
go-avkit h264 bench (`alien1080.264`, 1920x1080, 240 frames, and `fade1080.264`, 120
frames) `gen` runs, with ffmpeg 6.1.1 (Ubuntu 24.04 build, libx265):

```
ffmpeg -y -i SRC -c:v libx265 -preset medium -crf 23 -pix_fmt yuv420p   -x265-params profile=main:log-level=error   -f hevc X.265
ffmpeg -y -i SRC -c:v libx265 -preset medium -crf 23 -pix_fmt yuv420p10le -x265-params profile=main10:log-level=error -f hevc X.265
ffmpeg -y -i X.265 -c copy -tag:v hvc1 X.mp4
```

`alien8.265` is a clip made earlier whose encoder command was not kept; it is given as it
is. `gen` prints the SHA-256 of each file and says whether it equals the pin (the pins are
in `gen.go`):

| file | SHA-256 |
|---|---|
| alien1080.264 (source) | f7e587e227230810bdb7a46f938d278c249ddea58baf14bd2c0f5d296eeb1704 |
| fade1080.264 (source) | fd08902e5b0972c9fd34be9708f9e3b0fea7c1bb51555ca2d0a7133eb1adaec1 |
| alien-main.265 | 988679a34ce4e86bcdcb1974d52d1b44a63fb9277d8e49e1d23199906540f78d |
| fade-main.265 | 1baf43a752e0351f7aa0792fc5df3e208e117c223287ebc81d175e032aad49f0 |
| alien-main10.265 | d1487440051f231922eb8264503a8adb3295cecf4cc952dc5f54e52266f11f54 |
| alien8.265 | 498ad8aa3454308ee9023a3055acc576b63d242920b04524b246d0410e2d5642 |

Another libx265 build may produce other bytes; the table is then not comparable.

## Run

```
cd tools/bench
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o hevcbench .
hevcbench gen streams alien1080.264 fade1080.264 alien8.265
cd streams
taskset -c 5 true   # pick a quiet core
hevcbench run 5 5 alien-main=alien-main.265:alien-main.mp4 fade-main=fade-main.265:fade-main.mp4 \
    alien-main10=alien-main10.265:alien-main10.mp4 alien8=alien8.265:alien8.mp4
hevcbench child md5 alien-main.265     # md5 of the output as a yuv file, to compare with
                                       # ffmpeg -i X.265 -map 0:v -c:v rawvideo -f md5 -
hevcbench child h265 alien-main.265 cpu.prof   # a CPU profile (three passes)
```

## Reference table

fractal (32 cores, load about 1, `taskset -c 5`, 5 rounds), h265 at v0.15.0 plus four
commits, decode v0.9.0 over gen2brain/h265 v0.2.4, ffmpeg 6.1.1, Go 1.27.4; frames per
second of CPU time:

| clip | h265.Decoder | decode (third-party) | ffmpeg -threads 1 |
|---|---|---|---|
| alien-main (240 frames) | 13.5 | 87.5 | 394 |
| fade-main (120) | 13.8 | 79.9 | 430 |
| alien-main10 (240) | 13.3 | 30.1 | 300 |
| alien8 (240) | 15.4 | 95.7 | 430 |

All four streams decode bit-exactly: the md5 of our output equals ffmpeg's rawvideo md5.
`decode` returns 238 of 240 frames (it applies the MP4 edit list).
