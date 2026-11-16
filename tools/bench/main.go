// hevcbench measures three HEVC decoders on one core, interleaved: the h265 package of
// go-avkit, go-avkit/decode (the public API users have today, over a third-party
// decoder) on an MP4, and ffmpeg -threads 1. CPU time (user + system) is what is
// compared; the 1-minute load is checked before each round.
//
//	hevcbench gen <outdir> <alien1080.264> <fade1080.264> [alien8.265]   make the streams
//	hevcbench run <rounds> <cpu> <name=file.265:file.mp4>...   orchestrator
//	hevcbench child h265 <file.265> [cpuprofile]               one decode, prints a line
//	hevcbench child decode <file.mp4>
//	hevcbench child md5 <file.265>
package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-avkit/avkit/container"
	"github.com/go-avkit/decode"
	"github.com/go-avkit/h265"
)

func cpuMS() float64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return float64(ru.Utime.Sec*1000+ru.Stime.Sec*1000) + float64(ru.Utime.Usec+ru.Stime.Usec)/1000
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func child(args []string) {
	mode, file := args[0], args[1]
	data, err := os.ReadFile(file)
	must(err)
	switch mode {
	case "h265", "md5":
		units, err := h265.SplitAnnexB(data)
		must(err)
		prof := mode == "h265" && len(args) > 2
		if prof {
			f, err := os.Create(args[2])
			must(err)
			must(pprof.StartCPUProfile(f))
			defer pprof.StopCPUProfile()
		}
		loops := 1
		if prof {
			loops = 3
		}
		sum := md5.New()
		frames := 0
		c0, w0 := cpuMS(), time.Now()
		for l := 0; l < loops; l++ {
			d := h265.NewDecoder(h265.Options{})
			emit := func(outs []h265.Output) {
				for _, o := range outs {
					frames++
					if mode == "md5" {
						writePicture(sum, o.Picture)
					}
				}
			}
			for _, u := range units {
				outs, err := d.Decode(u)
				must(err)
				emit(outs)
			}
			outs, err := d.Flush()
			must(err)
			emit(outs)
		}
		if mode == "md5" {
			fmt.Printf("frames=%d md5=%s\n", frames, hex.EncodeToString(sum.Sum(nil)))
			return
		}
		fmt.Printf("frames=%d cpu_ms=%.1f wall_ms=%.1f\n", frames/loops, (cpuMS()-c0)/float64(loops), float64(time.Since(w0).Milliseconds())/float64(loops))
	case "decode":
		r, err := container.NewReader(data)
		must(err)
		id, err := decode.FirstVideoTrack(r)
		must(err)
		c0, w0 := cpuMS(), time.Now()
		frames, err := decode.DecodeTrack(r, id, decode.Config{})
		must(err)
		fmt.Printf("frames=%d cpu_ms=%.1f wall_ms=%.1f\n", len(frames), cpuMS()-c0, float64(time.Since(w0).Milliseconds()))
	}
}

type writer interface{ Write([]byte) (int, error) }

func writePicture(h writer, pic *h265.DecodedPicture) {
	wide := false
	for c := 0; c < pic.Components; c++ {
		wide = wide || pic.BitDepth[c] > 8
	}
	for c := 0; c < pic.Components; c++ {
		pl := &pic.Planes[c]
		row := make([]byte, 0, 2*pl.Width)
		for y := 0; y < pl.Height; y++ {
			row = row[:0]
			for _, v := range pl.Pix[y*pl.Stride : y*pl.Stride+pl.Width] {
				row = append(row, byte(v))
				if wide {
					row = append(row, byte(v>>8))
				}
			}
			h.Write(row)
		}
	}
}

var (
	loadRe  = regexp.MustCompile(`^([0-9.]+)`)
	kvRe    = regexp.MustCompile(`frames=(\d+) cpu_ms=([0-9.]+) wall_ms=([0-9.]+)`)
	ffRe    = regexp.MustCompile(`utime=([0-9.]+)s stime=([0-9.]+)s rtime=([0-9.]+)s`)
	ffFrame = regexp.MustCompile(`frame=\s*(\d+)`)
)

func load() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	must(err)
	v, _ := strconv.ParseFloat(loadRe.FindString(string(b)), 64)
	return v
}

func med(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func run(args []string) {
	rounds, _ := strconv.Atoi(args[0])
	cpu := args[1]
	self, _ := os.Executable()
	type clip struct{ name, raw, mp4 string }
	var clips []clip
	for _, a := range args[2:] {
		kv := strings.SplitN(a, "=", 2)
		fs := strings.SplitN(kv[1], ":", 2)
		clips = append(clips, clip{kv[0], fs[0], fs[1]})
	}
	cpuT := map[string][]float64{}
	frames := map[string]int{}
	var loads []float64
	child := func(c clip, mode, file string) (float64, int) {
		out, err := exec.Command("taskset", "-c", cpu, self, "child", mode, file).CombinedOutput()
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %s: %v\n%s", mode, c.name, err, out)
			os.Exit(1)
		}
		m := kvRe.FindStringSubmatch(string(out))
		if m == nil {
			fmt.Fprintf(os.Stderr, "%s %s: no result in %q\n", mode, c.name, out)
			os.Exit(1)
		}
		n, _ := strconv.Atoi(m[1])
		v, _ := strconv.ParseFloat(m[2], 64)
		return v, n
	}
	for r := 1; r <= rounds; r++ {
		for load() >= 8 {
			time.Sleep(5 * time.Second)
		}
		l := load()
		loads = append(loads, l)
		fmt.Printf("round %d load %.2f\n", r, l)
		for _, c := range clips {
			v, n := child(c, "h265", c.raw)
			cpuT["h265 "+c.name] = append(cpuT["h265 "+c.name], v)
			frames["h265 "+c.name] = n
			v, n = child(c, "decode", c.mp4)
			cpuT["decode "+c.name] = append(cpuT["decode "+c.name], v)
			frames["decode "+c.name] = n
			out, err := exec.Command("taskset", "-c", cpu, "ffmpeg", "-threads", "1", "-benchmark", "-i", c.raw, "-f", "null", "-").CombinedOutput()
			m := ffRe.FindStringSubmatch(string(out))
			if err != nil || m == nil {
				fmt.Fprintf(os.Stderr, "ffmpeg %s: %v %q\n", c.name, err, out)
				os.Exit(1)
			}
			u, _ := strconv.ParseFloat(m[1], 64)
			s, _ := strconv.ParseFloat(m[2], 64)
			cpuT["ffmpeg "+c.name] = append(cpuT["ffmpeg "+c.name], (u+s)*1000)
			all := ffFrame.FindAllStringSubmatch(string(out), -1)
			if len(all) > 0 {
				frames["ffmpeg "+c.name], _ = strconv.Atoi(all[len(all)-1][1])
			}
		}
	}
	fmt.Printf("\nGo %s, GOMAXPROCS %d, taskset -c %s, loads %v\n\n", runtime.Version(), runtime.GOMAXPROCS(0), cpu, loads)
	fmt.Println("| clip | frames | h265.Decoder fps (min-max) | decode (third-party) fps | ffmpeg -threads 1 fps | h265 / ffmpeg | h265 / decode |")
	fmt.Println("|---|---|---|---|---|---|---|")
	fps := func(key string) (float64, float64, float64) {
		v := cpuT[key]
		n := float64(frames[key])
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		return n / (med(v) / 1000), n / (s[len(s)-1] / 1000), n / (s[0] / 1000)
	}
	for _, c := range clips {
		a, alo, ahi := fps("h265 " + c.name)
		b, _, _ := fps("decode " + c.name)
		f, _, _ := fps("ffmpeg " + c.name)
		fmt.Printf("| %s | %d/%d/%d | %.1f (%.1f-%.1f) | %.1f | %.1f | %.2f | %.2f |\n", c.name,
			frames["h265 "+c.name], frames["decode "+c.name], frames["ffmpeg "+c.name], a, alo, ahi, b, f, a/f, a/b)
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hevcbench run|child ...")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "child":
		child(os.Args[2:])
		return
	case "gen":
		gen(os.Args[2:])
		return
	}
	run(os.Args[2:])
}
