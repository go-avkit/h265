// Command fetchconformance downloads the H.265.1 conformance zips that a pin
// manifest names, extracts their files, and verifies every pinned file's
// SHA-256. The streams are not committed (no redistribution terms are stated),
// so CI runs this before the conformance tests; it fails if a zip is missing or
// a hash differs, so that the oracle cannot be skipped by a broken download.
//
//	go run ./testdata/fetchconformance -dir D -base URL -manifest testdata/conformance.sha256
//
// A manifest line is "<sha256>  <stream>/<file>"; the zip of a stream is
// <base>/<stream>.zip. With -pin and -names-file (one stream name per line) the
// manifest is written from what the zips hold (bitstreams and trace files)
// instead of read.
package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxFile = 1 << 30 // a file larger than this is refused, not extracted

func main() {
	dir := flag.String("dir", "", "directory to extract into")
	base := flag.String("base", "", "URL of the directory that holds <stream>.zip")
	manifest := flag.String("manifest", "", "pin manifest to verify (or to write with -pin)")
	pin := flag.Bool("pin", false, "write the manifest from the zips named by -names-file")
	namesFile := flag.String("names-file", "", "with -pin: stream names, one per line")
	flag.Parse()
	if *dir == "" || *base == "" || *manifest == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*dir, strings.TrimRight(*base, "/"), *manifest, *pin, *namesFile); err != nil {
		fmt.Fprintln(os.Stderr, "fetchconformance:", err)
		os.Exit(1)
	}
}

func run(dir, base, manifest string, pin bool, namesFile string) error {
	var pins map[string]string // "<stream>/<file>" -> sha256
	var names []string
	if pin {
		lines, err := readLines(namesFile)
		if err != nil {
			return err
		}
		names = lines
	} else {
		var err error
		if pins, names, err = readManifest(manifest); err != nil {
			return err
		}
	}
	var written []string
	for _, name := range names {
		if !pin && cached(dir, name, pins) {
			continue
		}
		if err := fetch(dir, base, name); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if pin {
			lines, err := pinStream(dir, name)
			if err != nil {
				return err
			}
			written = append(written, lines...)
		}
	}
	if pin {
		sort.Strings(written)
		return os.WriteFile(manifest, []byte(strings.Join(written, "\n")+"\n"), 0o644)
	}
	for rel, want := range pins {
		got, err := hashFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s: SHA-256 %s, pinned %s", rel, got, want)
		}
	}
	fmt.Printf("%d files verified\n", len(pins))
	return nil
}

func readLines(file string) ([]string, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			out = append(out, s)
		}
	}
	return out, sc.Err()
}

func readManifest(file string) (map[string]string, []string, error) {
	lines, err := readLines(file)
	if err != nil {
		return nil, nil, err
	}
	pins := map[string]string{}
	seen := map[string]bool{}
	var names []string
	for _, l := range lines {
		f := strings.Fields(l)
		if len(f) != 2 || !strings.Contains(f[1], "/") {
			return nil, nil, fmt.Errorf("manifest line %q is not \"<sha256>  <stream>/<file>\"", l)
		}
		pins[f[1]] = f[0]
		if s := path.Dir(f[1]); !seen[s] {
			seen[s] = true
			names = append(names, s)
		}
	}
	sort.Strings(names)
	return pins, names, nil
}

// cached says every pinned file of the stream is already present and right.
func cached(dir, name string, pins map[string]string) bool {
	for rel, want := range pins {
		if path.Dir(rel) != name {
			continue
		}
		if got, err := hashFile(filepath.Join(dir, filepath.FromSlash(rel))); err != nil || got != want {
			return false
		}
	}
	return true
}

func hashFile(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetch(dir, base, name string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	var body []byte
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if body, err = get(client, base+"/"+name+".zip"); err == nil {
			break
		}
		time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
	}
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return err
	}
	out := filepath.Join(dir, name)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > maxFile {
			continue
		}
		// Only the file's base name is used: a zip entry cannot place a file
		// outside the stream's own directory.
		dst := filepath.Join(out, path.Base(strings.ReplaceAll(f.Name, "\\", "/")))
		rc, err := f.Open()
		if err != nil {
			return err
		}
		w, err := os.Create(dst)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, io.LimitReader(rc, maxFile))
		rc.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func get(c *http.Client, url string) ([]byte, error) {
	resp, err := c.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2*maxFile))
}

// pinStream hashes the bitstreams, trace files and md5 files of an extracted stream.
func pinStream(dir, name string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !(strings.HasSuffix(n, ".bit") || strings.HasSuffix(n, ".bin") || strings.HasSuffix(n, ".trc") || strings.HasSuffix(n, "_trace.txt") || strings.HasSuffix(n, ".md5")) {
			continue
		}
		h, err := hashFile(filepath.Join(dir, name, n))
		if err != nil {
			return nil, err
		}
		out = append(out, h+"  "+name+"/"+n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no bitstream in the zip", name)
	}
	return out, nil
}
