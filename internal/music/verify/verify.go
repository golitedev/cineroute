// Package verify checks staged albums before they are imported into a library.
package verify

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Track struct {
	ID           string
	Disc, Number int
	Title        string
	DurationSec  int
}

type Options struct {
	FlacTest                      bool
	FlacBinary                    string
	MinToleranceSec, TolerancePct float64
}

type File struct {
	Path, Rel, Format    string
	Size                 int64
	Disc, Number         int
	Title                string
	DurationSec          float64
	BitDepth, SampleRate int
}

type Problem struct {
	Disc, Number                int
	Title, Path, Reason, Detail string
}

func (p Problem) String() string {
	var s string
	if p.Disc > 1 {
		s = fmt.Sprintf("Disc %d ", p.Disc)
	}
	if p.Number > 0 {
		s += fmt.Sprintf("track %d ", p.Number)
	}
	if p.Title != "" {
		s += strconv.Quote(p.Title) + ": "
	} else if s != "" {
		s = strings.TrimSpace(s) + ": "
	} else if p.Path != "" {
		s = p.Path + ": "
	}
	s += p.Reason
	if p.Detail != "" {
		s += " (" + p.Detail + ")"
	}
	return s
}

type Result struct {
	AlbumDir             string
	Files                []File
	Problems             []Problem
	BitDepth, SampleRate int
	Format               string
}

func (r Result) OK() bool { return len(r.Problems) == 0 }

var trackName = regexp.MustCompile(`^(\d+)(?:\s*[.\-]\s*|\s+)`)
var discName = regexp.MustCompile(`(?i)^disc\s+(\d+)$`)

func audioFormat(path string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	switch ext {
	case "flac", "mp3", "m4a", "mp4", "aac", "alac", "ogg", "opus", "wav", "aiff", "aif":
		return ext
	}
	return ""
}
func partial(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tmp", ".part", ".partial":
		return true
	}
	return false
}

// FindAlbumDir finds the one top-level album containing audio, or root itself.
func FindAlbumDir(root string) (string, error) { return findAlbumDir(context.Background(), root) }
func findAlbumDir(ctx context.Context, root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("album root is not a directory: %s", root)
	}
	candidates := map[string]bool{}
	direct := false
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || audioFormat(path) == "" {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) == 1 {
			direct = true
		} else {
			candidates[filepath.Join(root, parts[0])] = true
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if direct {
		return root, nil
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no audio files under %s", root)
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("multiple album directories under %s", root)
	}
	for dir := range candidates {
		return dir, nil
	}
	panic("unreachable")
}

// Album verifies the staged album. Verification failures are reported in Problems.
func Album(ctx context.Context, root string, expected []Track, opts Options) (Result, error) {
	var r Result
	if err := ctx.Err(); err != nil {
		return r, err
	}
	dir, err := findAlbumDir(ctx, root)
	if err != nil {
		return r, err
	}
	r.AlbumDir = dir
	if opts.MinToleranceSec <= 0 {
		opts.MinToleranceSec = 3
	}
	if opts.TolerancePct <= 0 {
		opts.TolerancePct = 2
	}
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		if path != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if partial(path) {
			r.Problems = append(r.Problems, Problem{Path: path, Reason: "unexpected file", Detail: "leftover temporary/partial file"})
			return nil
		}
		format := audioFormat(path)
		if format == "" {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		f := File{Path: path, Rel: rel, Format: format}
		info, e := d.Info()
		var issue string
		if e != nil {
			issue = e.Error()
		} else if !info.Mode().IsRegular() {
			issue = "not a regular file"
		} else {
			f.Size = info.Size()
		}
		if issue == "" && f.Size > 0 && format == "flac" {
			e = readFLAC(ctx, &f)
			if e != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				issue = e.Error()
			}
		}
		r.Files = append(r.Files, f)
		if issue != "" {
			r.Problems = append(r.Problems, Problem{Path: path, Reason: "unreadable", Detail: issue})
		} else if f.Size == 0 {
			r.Problems = append(r.Problems, Problem{Path: path, Reason: "empty file"})
		}
		return nil
	})
	if err != nil {
		return r, err
	}
	type key struct{ disc, number int }
	tracks := map[key]Track{}
	for _, t := range expected {
		if t.Disc <= 0 {
			t.Disc = 1
		}
		tracks[key{t.Disc, t.Number}] = t
	}
	counts := map[key]int{}
	formats := map[string]int{}
	attribution := map[string]Problem{}
	for i := range r.Files {
		f := &r.Files[i]
		formats[f.Format]++
		r.BitDepth = max(r.BitDepth, f.BitDepth)
		r.SampleRate = max(r.SampleRate, f.SampleRate)
		disc, num := f.Disc, f.Number
		if num <= 0 {
			disc = 1
			for parent := filepath.Dir(f.Rel); parent != "."; parent = filepath.Dir(parent) {
				if m := discName.FindStringSubmatch(filepath.Base(parent)); m != nil {
					disc, _ = strconv.Atoi(m[1])
					break
				}
			}
			if m := trackName.FindStringSubmatch(filepath.Base(f.Rel)); m != nil {
				num, _ = strconv.Atoi(m[1])
			}
		} else if disc <= 0 {
			disc = 1
		}
		k := key{disc, num}
		t, found := tracks[k]
		p := Problem{Disc: disc, Number: num, Path: f.Path, Title: f.Title}
		if found {
			p.Title = t.Title
		}
		attribution[f.Path] = p
		// Attribute structural errors using the same matching rules.
		for j := range r.Problems {
			if r.Problems[j].Path == f.Path {
				r.Problems[j].Disc = disc
				r.Problems[j].Number = num
				r.Problems[j].Title = p.Title
			}
		}
		if !found {
			p.Reason = "unexpected file"
			r.Problems = append(r.Problems, p)
			continue
		}
		counts[k]++
		if counts[k] > 1 {
			p.Reason = "duplicate"
			r.Problems = append(r.Problems, p)
		}
		if f.Format == "flac" && f.DurationSec > 0 && t.DurationSec > 0 {
			actual, want := f.DurationSec, float64(t.DurationSec)
			if actual >= 25 && actual <= 35 && want > 40 {
				p.Reason = "preview sample"
			} else if math.Abs(actual-want) > math.Max(opts.MinToleranceSec, want*opts.TolerancePct/100) {
				if actual < want {
					p.Reason = "too short"
				} else {
					p.Reason = "too long"
				}
			}
			if p.Reason == "preview sample" || p.Reason == "too short" || p.Reason == "too long" {
				p.Detail = duration(actual) + " of " + duration(want)
				r.Problems = append(r.Problems, p)
			}
		}
	}
	for k, t := range tracks {
		if counts[k] == 0 {
			r.Problems = append(r.Problems, Problem{Disc: k.disc, Number: k.number, Title: t.Title, Reason: "missing"})
		}
	}
	for format, count := range formats {
		if r.Format == "" || count > formats[r.Format] || count == formats[r.Format] && format < r.Format {
			r.Format = format
		}
	}
	if opts.FlacTest {
		bin := opts.FlacBinary
		if bin == "" {
			bin = "flac"
		}
		if resolved, e := exec.LookPath(bin); e == nil {
			for _, p := range decode(ctx, resolved, r.Files) {
				a := attribution[p.Path]
				p.Disc, p.Number, p.Title = a.Disc, a.Number, a.Title
				r.Problems = append(r.Problems, p)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return r, err
	}
	sort.SliceStable(r.Problems, func(i, j int) bool {
		a, b := r.Problems[i], r.Problems[j]
		if a.Disc != b.Disc {
			return a.Disc < b.Disc
		}
		if a.Number != b.Number {
			return a.Number < b.Number
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Reason < b.Reason
	})
	return r, nil
}
func duration(sec float64) string {
	n := int(math.Round(sec))
	return fmt.Sprintf("%d:%02d", n/60, n%60)
}

func readFLAC(ctx context.Context, out *File) error {
	f, err := os.Open(out.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err = io.ReadFull(f, magic); err != nil {
		return err
	}
	if string(magic) != "fLaC" {
		return fmt.Errorf("invalid FLAC magic")
	}
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h := make([]byte, 4)
		if _, err := io.ReadFull(f, h); err != nil {
			return err
		}
		kind := h[0] & 127
		n := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		if first && kind != 0 {
			return fmt.Errorf("STREAMINFO must be first")
		}
		if !first && kind == 0 {
			return fmt.Errorf("duplicate STREAMINFO")
		}
		if kind == 127 {
			return fmt.Errorf("invalid metadata type")
		}
		if kind == 0 || kind == 4 {
			data := make([]byte, n)
			if _, err := io.ReadFull(f, data); err != nil {
				return err
			}
			if kind == 0 {
				if n != 34 {
					return fmt.Errorf("invalid STREAMINFO length")
				}
				packed := binary.BigEndian.Uint64(data[10:18])
				rate := int(packed >> 44)
				channels := int(packed>>41&7) + 1
				bits := int(packed>>36&31) + 1
				samples := packed & ((1 << 36) - 1)
				if rate == 0 || samples == 0 || bits < 4 || channels < 1 {
					return fmt.Errorf("invalid STREAMINFO audio parameters")
				}
				out.SampleRate = rate
				out.BitDepth = bits
				out.DurationSec = float64(samples) / float64(rate)
			} else if err := readComments(data, out); err != nil {
				return err
			}
		} else {
			if _, err := io.CopyN(io.Discard, f, int64(n)); err != nil {
				return err
			}
		}
		first = false
		if h[0]&128 != 0 {
			return nil
		}
	}
}
func readComments(data []byte, out *File) error {
	take := func() ([]byte, error) {
		if len(data) < 4 {
			return nil, io.ErrUnexpectedEOF
		}
		n := uint64(binary.LittleEndian.Uint32(data))
		data = data[4:]
		if n > uint64(len(data)) {
			return nil, io.ErrUnexpectedEOF
		}
		s := data[:int(n)]
		data = data[int(n):]
		return s, nil
	}
	if _, err := take(); err != nil {
		return err
	}
	if len(data) < 4 {
		return io.ErrUnexpectedEOF
	}
	count := binary.LittleEndian.Uint32(data)
	data = data[4:]
	for i := uint32(0); i < count; i++ {
		s, err := take()
		if err != nil {
			return err
		}
		k, v, ok := strings.Cut(string(s), "=")
		if !ok {
			continue
		}
		switch strings.ToUpper(k) {
		case "TITLE":
			out.Title = v
		case "TRACKNUMBER":
			out.Number = tagNumber(v)
		case "DISCNUMBER":
			out.Disc = tagNumber(v)
		}
	}
	return nil
}
func tagNumber(s string) int {
	s, _, _ = strings.Cut(s, "/")
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	if n < 0 {
		return 0
	}
	return n
}

func decode(ctx context.Context, bin string, files []File) []Problem {
	jobs := make(chan File)
	var mu sync.Mutex
	var problems []Problem
	var wg sync.WaitGroup
	for i := 0; i < min(4, runtime.NumCPU()); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if ctx.Err() != nil {
					continue
				}
				testCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				cmd := exec.CommandContext(testCtx, bin, "-t", "-s", f.Path)
				var stdout, stderr strings.Builder
				cmd.Stdout = &stdout
				cmd.Stderr = &stderr
				cmd.WaitDelay = 100 * time.Millisecond
				// os/exec connects nil stdin to the null device.
				err := cmd.Run()
				timedOut := testCtx.Err()
				cancel()
				if err != nil && ctx.Err() == nil {
					detail := strings.TrimSpace(stderr.String())
					if j := strings.LastIndexByte(detail, '\n'); j >= 0 {
						detail = detail[j+1:]
					}
					if detail == "" {
						detail = err.Error()
					}
					if timedOut != nil {
						detail = timedOut.Error()
					}
					mu.Lock()
					problems = append(problems, Problem{Disc: f.Disc, Number: f.Number, Title: f.Title, Path: f.Path, Reason: "decode failed", Detail: detail})
					mu.Unlock()
				}
			}
		}()
	}
	for _, f := range files {
		if f.Format == "flac" {
			select {
			case jobs <- f:
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				return problems
			}
		}
	}
	close(jobs)
	wg.Wait()
	return problems
}
