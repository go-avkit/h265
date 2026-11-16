package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// pins are the SHA-256 of the sources and of what ffmpeg 6.1.1 (libx265, Ubuntu 24.04
// build) made from them. The encoders are deterministic for one build; another build
// may differ, which gen reports (the speed is then not comparable with earlier tables).
var pins = map[string]string{
	"alien1080.264":    "f7e587e227230810bdb7a46f938d278c249ddea58baf14bd2c0f5d296eeb1704",
	"fade1080.264":     "fd08902e5b0972c9fd34be9708f9e3b0fea7c1bb51555ca2d0a7133eb1adaec1",
	"alien-main.265":   "988679a34ce4e86bcdcb1974d52d1b44a63fb9277d8e49e1d23199906540f78d",
	"fade-main.265":    "1baf43a752e0351f7aa0792fc5df3e208e117c223287ebc81d175e032aad49f0",
	"alien-main10.265": "d1487440051f231922eb8264503a8adb3295cecf4cc952dc5f54e52266f11f54",
	"alien-main.mp4":   "dd870ce8d295df3283f48220d44e1e46af0325f8e54986b1bb3640f95773fd7c",
	"fade-main.mp4":    "b1961db8b639b5826892fceb253f84efdd4b215f9a0349fc1d53d1c046451bf9",
	"alien-main10.mp4": "2811748f7431b74907460d6d00ba8409caea6513f58f850178b9685e79c8f53c",
	"alien8.265":       "498ad8aa3454308ee9023a3055acc576b63d242920b04524b246d0410e2d5642",
	"alien8.mp4":       "da295cc4053e3f271f90a9960a7c7e0a6cc1ce0506fd8741050c2b5e31c15779",
}

func shaOf(path string) string {
	b, err := os.ReadFile(path)
	must(err)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func report(path string) {
	name := filepath.Base(path)
	got := shaOf(path)
	status := "no pin"
	if want, ok := pins[name]; ok {
		status = "pinned, same"
		if want != got {
			status = "DIFFERS from the pin " + want
		}
	}
	fmt.Printf("%s  %s  (%s)\n", got, name, status)
}

func ffmpeg(args ...string) {
	cmd := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	fmt.Println("ffmpeg", args)
	must(cmd.Run())
}

// gen makes the streams of the tables from the two H.264 sources and checks the pins:
//
//	hevcbench gen <outdir> <alien1080.264> <fade1080.264> [alien8.265]
//
// alien8.265 is a clip made earlier (its encoder command is not known): it is copied
// and muxed, and its hash is pinned.
func gen(args []string) {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hevcbench gen <outdir> <alien1080.264> <fade1080.264> [alien8.265]")
		os.Exit(2)
	}
	out, alien, fade := args[0], args[1], args[2]
	must(os.MkdirAll(out, 0o755))
	report(alien)
	report(fade)
	enc := func(src, name, pixFmt, profile string) {
		raw := filepath.Join(out, name+".265")
		ffmpeg("-i", src, "-c:v", "libx265", "-preset", "medium", "-crf", "23", "-pix_fmt", pixFmt,
			"-x265-params", "profile="+profile+":log-level=error", "-f", "hevc", raw)
		report(raw)
	}
	enc(alien, "alien-main", "yuv420p", "main")
	enc(fade, "fade-main", "yuv420p", "main")
	enc(alien, "alien-main10", "yuv420p10le", "main10")
	names := []string{"alien-main", "fade-main", "alien-main10"}
	if len(args) > 3 {
		data, err := os.ReadFile(args[3])
		must(err)
		must(os.WriteFile(filepath.Join(out, "alien8.265"), data, 0o644))
		report(filepath.Join(out, "alien8.265"))
		names = append(names, "alien8")
	}
	for _, n := range names {
		mp4 := filepath.Join(out, n+".mp4")
		ffmpeg("-i", filepath.Join(out, n+".265"), "-c", "copy", "-tag:v", "hvc1", mp4)
		report(mp4)
	}
}
