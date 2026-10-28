package verify

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// minimalFLAC deliberately has no frames: metadata tests do not require flac.
func minimalFLAC(seconds float64, tags ...string) []byte {
	stream := make([]byte, 34)
	binary.BigEndian.PutUint16(stream, 4096)
	binary.BigEndian.PutUint16(stream[2:], 4096)
	binary.BigEndian.PutUint64(stream[10:], uint64(44100)<<44|uint64(1)<<41|uint64(15)<<36|uint64(seconds*44100))
	data := append([]byte("fLaC\x00\x00\x00\x22"), stream...)
	comments := make([]byte, 8)
	binary.LittleEndian.PutUint32(comments[4:], uint32(len(tags)))
	for _, tag := range tags {
		b := make([]byte, 4)
		binary.LittleEndian.PutUint32(b, uint32(len(tag)))
		comments = append(comments, b...)
		comments = append(comments, tag...)
	}
	data = append(data, 128|4, byte(len(comments)>>16), byte(len(comments)>>8), byte(len(comments)))
	return append(data, comments...)
}
func put(t *testing.T, root, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
func reasons(r Result) []string {
	out := []string{}
	for _, p := range r.Problems {
		out = append(out, p.Reason)
	}
	return out
}
func TestAlbum(t *testing.T) {
	one := []Track{{ID: "id", Disc: 1, Number: 1, Title: "Song", DurationSec: 100}}
	cases := []struct {
		name   string
		files  map[string][]byte
		tracks []Track
		opts   Options
		want   []string
	}{
		{name: "complete", files: map[string][]byte{"album/01. Song.flac": minimalFLAC(100, "TRACKNUMBER=01/12", "DISCNUMBER=01/02", "TITLE=Song")}, tracks: one},
		{name: "missing", files: map[string][]byte{"01. Song.flac": minimalFLAC(100, "TRACKNUMBER=1")}, tracks: append(append([]Track{}, one...), Track{Disc: 1, Number: 2, Title: "Absent"}), want: []string{"missing"}},
		{name: "preview", files: map[string][]byte{"01. Song.flac": minimalFLAC(30)}, tracks: one, want: []string{"preview sample"}},
		{name: "short", files: map[string][]byte{"01. Song.flac": minimalFLAC(60)}, tracks: one, want: []string{"too short"}},
		{name: "long", files: map[string][]byte{"01. Song.flac": minimalFLAC(110)}, tracks: one, want: []string{"too long"}},
		{name: "minimum tolerance", files: map[string][]byte{"01. Song.flac": minimalFLAC(103)}, tracks: one},
		{name: "custom tolerance", files: map[string][]byte{"01. Song.flac": minimalFLAC(110)}, tracks: one, opts: Options{TolerancePct: 10}},
		{name: "zero", files: map[string][]byte{"01. Song.flac": {}}, tracks: one, want: []string{"empty file"}},
		{name: "partial", files: map[string][]byte{"01. Song.flac": minimalFLAC(100), "02.flac.part": {}}, tracks: one, want: []string{"unexpected file"}},
		{name: "multidisc", files: map[string][]byte{"album/Disc 1/01. A.flac": minimalFLAC(100), "album/dIsC 2/01 - B.mp3": {1}}, tracks: append(append([]Track{}, one...), Track{Disc: 2, Number: 1})},
		{name: "filename fallback", files: map[string][]byte{"7. Artist - Title.flac": minimalFLAC(100)}, tracks: []Track{{Disc: 1, Number: 7, DurationSec: 100}}},
		{name: "duplicate", files: map[string][]byte{"01. Song.flac": minimalFLAC(100), "copy.flac": minimalFLAC(100, "TRACKNUMBER=1")}, tracks: one, want: []string{"duplicate"}},
		{name: "unexpected", files: map[string][]byte{"01. Song.flac": minimalFLAC(100), "02 - Extra.mp3": {1}}, tracks: one, want: []string{"unexpected file"}},
		{name: "unparseable name", files: map[string][]byte{"unknown.mp3": {1}}, tracks: one, want: []string{"unexpected file", "missing"}},
		{name: "ignored", files: map[string][]byte{"01. Song.mp3": {1}, "cover.jpg": {}, "booklet.pdf": {}, "info.txt": {}, "a.cue": {}, "a.log": {}, "a.m3u": {}, ".secret.flac": {}, ".hidden/extra.mp3": {1}}, tracks: one},
		{name: "missing binary skipped", files: map[string][]byte{"01. Song.flac": minimalFLAC(100)}, tracks: one, opts: Options{FlacTest: true, FlacBinary: "cineroute-nonexistent-flac-binary"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, data := range tc.files {
				put(t, root, name, data)
			}
			r, err := Album(context.Background(), root, tc.tracks, tc.opts)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if want == nil {
				want = []string{}
			}
			if !reflect.DeepEqual(reasons(r), want) {
				t.Fatalf("got %+v; want %v", r.Problems, want)
			}
			if r.OK() != (len(want) == 0) {
				t.Fatal("OK mismatch")
			}
			if r.AlbumDir == "" {
				t.Fatal("no album dir")
			}
			for _, f := range r.Files {
				if !filepath.IsAbs(f.Path) || filepath.IsAbs(f.Rel) {
					t.Fatalf("invalid paths: %+v", f)
				}
			}
			if tc.name == "complete" {
				if r.BitDepth != 16 || r.SampleRate != 44100 || r.Format != "flac" || r.Files[0].Title != "Song" {
					t.Fatalf("metadata %+v", r)
				}
			}
			if tc.name == "missing" && r.Problems[0].Title != "Absent" {
				t.Fatal("missing title")
			}
		})
	}
}
func TestFindAlbumDir(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string
		fail  bool
	}{
		{"direct", []string{"01.mp3", "nested/02.mp3"}, "", false},
		{"album", []string{"Album/Disc 1/01.flac", "Album/Disc 2/01.flac", "cover.jpg"}, "Album", false},
		{"multiple", []string{"A/01.mp3", "B/01.mp3"}, "", true},
		{"none", []string{"cover.jpg", ".hidden.mp3"}, "", true},
		{"hidden directory", []string{".hidden/01.mp3"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, p := range tc.files {
				put(t, root, p, []byte{1})
			}
			got, err := FindAlbumDir(root)
			if (err != nil) != tc.fail {
				t.Fatalf("%s %v", got, err)
			}
			if !tc.fail && got != filepath.Join(root, tc.want) {
				t.Fatal(got)
			}
		})
	}
	if _, err := FindAlbumDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing root accepted")
	}
	root := t.TempDir()
	path := put(t, root, "file", nil)
	if _, err := FindAlbumDir(path); err == nil {
		t.Fatal("file root accepted")
	}
}
func TestInvalidFLAC(t *testing.T) {
	valid := minimalFLAC(100, "TRACKNUMBER=1")
	zero := minimalFLAC(0)
	invalidRate := minimalFLAC(100)
	for i := 18; i < 21; i++ {
		invalidRate[i] = 0
	}
	invalidRate[20] &= 15
	cases := map[string][]byte{
		"bad magic": []byte("oops"), "short magic": []byte("fLa"), "short metadata": valid[:6], "short streaminfo": valid[:20], "zero samples": zero, "invalid rate": invalidRate,
		"not streaminfo": []byte("fLaC\x81\x00\x00\x00"), "bad stream length": []byte("fLaC\x80\x00\x00\x00"),
		"bad comment": append(append([]byte{}, valid[:42]...), 132, 0, 0, 1, 0),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "01. Broken.flac", data)
			r, err := Album(context.Background(), root, []Track{{Disc: 1, Number: 1}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reasons(r), []string{"unreadable"}) {
				t.Fatalf("%+v", r.Problems)
			}
		})
	}
}
func TestExtensions(t *testing.T) {
	for _, ext := range []string{"flac", "mp3", "m4a", "mp4", "aac", "alac", "ogg", "opus", "wav", "aiff", "aif"} {
		if audioFormat("x."+strings.ToUpper(ext)) != ext {
			t.Fatal(ext)
		}
	}
	for _, ext := range []string{"tmp", "part", "partial"} {
		if !partial("x.FLAC." + strings.ToUpper(ext)) {
			t.Fatal(ext)
		}
	}
}
func TestProblemString(t *testing.T) {
	for _, tc := range []struct {
		p    Problem
		want string
	}{
		{Problem{Disc: 1, Number: 7, Title: "Rosa Parks", Reason: "missing"}, `track 7 "Rosa Parks": missing`},
		{Problem{Disc: 2, Number: 1, Reason: "missing"}, "Disc 2 track 1: missing"},
		{Problem{Path: "left.part", Reason: "unexpected file", Detail: "partial"}, "left.part: unexpected file (partial)"},
	} {
		if got := tc.p.String(); got != tc.want {
			t.Fatalf("%q != %q", got, tc.want)
		}
	}
}

// realFLAC encodes standard-library-generated PCM WAV using the optional binary.
func realFLAC(t *testing.T, root string) string {
	t.Helper()
	bin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("flac is unavailable")
	}
	pcm := make([]byte, 44100*2)
	// Deterministic noise prevents the fixture from collapsing to a tiny frame.
	var x uint32 = 1
	for i := 0; i < len(pcm); i += 2 {
		x = x*1664525 + 1013904223
		binary.LittleEndian.PutUint16(pcm[i:], uint16(x>>16))
	}
	wav := make([]byte, 44)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(36+len(pcm)))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 44100)
	binary.LittleEndian.PutUint32(wav[28:], 88200)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(len(pcm)))
	in := put(t, root, "source.wav", append(wav, pcm...))
	out := filepath.Join(root, "01. Song.flac")
	cmd := exec.Command(bin, "--silent", "-f", "--tag=TRACKNUMBER=1", "-o", out, in)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("flac: %s %v", b, err)
	}
	if err := os.Remove(in); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestRealFLAC(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			root := t.TempDir()
			path := realFLAC(t, root)
			if corrupt {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Truncate(path, info.Size()-1000); err != nil {
					t.Fatal(err)
				}
			}
			r, err := Album(context.Background(), root, []Track{{Disc: 1, Number: 1, Title: "Song", DurationSec: 1}}, Options{FlacTest: true})
			if err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if !reflect.DeepEqual(reasons(r), []string{"decode failed"}) || r.Problems[0].Detail == "" {
					t.Fatalf("%+v", r)
				}
			} else if !r.OK() {
				t.Fatalf("%+v", r.Problems)
			}
		})
	}
}
func TestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Album(ctx, t.TempDir(), nil, Options{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// A controlled executable makes cancellation during a decode deterministic.
	root := t.TempDir()
	put(t, root, "01. Song.flac", minimalFLAC(100))
	marker := filepath.Join(root, "started")
	bin := put(t, root, "fake-flac", []byte("#!/bin/sh\nprintf started > '"+marker+"'\nexec sleep 30\n"))
	if err := os.Chmod(bin, 0755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Album(ctx, root, []Track{{Disc: 1, Number: 1}}, Options{FlacTest: true, FlacBinary: bin})
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
wait:
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				break wait
			}
		case <-deadline:
			t.Fatal("decoder never started")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not stop decode promptly")
	}
}

func TestTagPrecedenceAndSummary(t *testing.T) {
	root := t.TempDir()
	put(t, root, "album/Disc 9/99. Misleading.flac", minimalFLAC(200, "tracknumber=007/12", "discnumber=02/03", "title=Tagged"))
	high := minimalFLAC(200, "TRACKNUMBER=8", "DISCNUMBER=2")
	binary.BigEndian.PutUint64(high[18:26], uint64(96000)<<44|uint64(1)<<41|uint64(23)<<36|uint64(200*96000))
	put(t, root, "album/second.flac", high)
	put(t, root, "album/Disc 2/09 - Fallback.flac", minimalFLAC(200, "TRACKNUMBER=garbage"))
	r, err := Album(context.Background(), root, []Track{{Disc: 2, Number: 7, DurationSec: 200}, {Disc: 2, Number: 8, DurationSec: 200}, {Disc: 2, Number: 9, DurationSec: 200}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || r.BitDepth != 24 || r.SampleRate != 96000 || r.Format != "flac" {
		t.Fatalf("%+v", r)
	}
	for _, f := range r.Files {
		if f.Title == "Tagged" && (f.Number != 7 || f.Disc != 2) {
			t.Fatalf("%+v", f)
		}
	}
}
func TestDurationBoundaries(t *testing.T) {
	for _, tc := range []struct {
		actual   float64
		expected int
		reason   string
	}{
		{25, 100, "preview sample"}, {35, 100, "preview sample"}, {24, 100, "too short"}, {36, 100, "too short"},
		{30, 40, "too short"}, {490, 500, ""}, {489, 500, "too short"}, {510, 500, ""}, {511, 500, "too long"}, {1, 0, ""},
	} {
		t.Run(fmt.Sprintf("%g-of-%d", tc.actual, tc.expected), func(t *testing.T) {
			root := t.TempDir()
			put(t, root, "01. Song.flac", minimalFLAC(tc.actual))
			r, err := Album(context.Background(), root, []Track{{Disc: 1, Number: 1, DurationSec: tc.expected}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.reason == "" {
				if !r.OK() {
					t.Fatal(r.Problems)
				}
			} else if len(r.Problems) != 1 || r.Problems[0].Reason != tc.reason {
				t.Fatal(r.Problems)
			}
		})
	}
}
func TestProblemOrder(t *testing.T) {
	root := t.TempDir()
	put(t, root, "01. Song.mp3", []byte{1})
	expected := []Track{{Disc: 3, Number: 1}, {Disc: 2, Number: 9}, {Disc: 1, Number: 1}, {Disc: 2, Number: 2}, {Disc: 1, Number: 4}}
	r, err := Album(context.Background(), root, expected, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, p := range r.Problems {
		got = append(got, fmt.Sprintf("%d/%d", p.Disc, p.Number))
	}
	if !reflect.DeepEqual(got, []string{"1/4", "2/2", "2/9", "3/1"}) {
		t.Fatal(got)
	}
}
func TestMalformedCommentLengths(t *testing.T) {
	for _, payload := range [][]byte{
		{}, {255, 255, 255, 255},
		{0, 0, 0, 0, 1, 0, 0, 0},
		{0, 0, 0, 0, 1, 0, 0, 0, 255, 255, 255, 255},
	} {
		var f File
		if err := readComments(payload, &f); err == nil {
			t.Fatalf("accepted %v", payload)
		}
	}
}
func TestOperationalError(t *testing.T) {
	if _, err := Album(context.Background(), filepath.Join(t.TempDir(), "missing"), nil, Options{}); err == nil {
		t.Fatal("missing root accepted")
	}
}
