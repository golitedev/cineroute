package flacfix

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func need(t *testing.T, bins ...string) {
	t.Helper()
	for _, b := range bins {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("%s not installed", b)
		}
	}
}

// encode writes a FLAC with a sine sweep. The flac encoder stores the
// correct MD5, which the tests use as the reference.
func encode(t *testing.T, dir string, bits, channels, rate int, seconds float64, tags ...string) string {
	t.Helper()
	n := int(seconds * float64(rate))
	bps := bits / 8
	data := make([]byte, 0, n*channels*bps)
	for i := 0; i < n; i++ {
		for c := 0; c < channels; c++ {
			v := math.Sin(float64(i)*(0.01+0.003*float64(c))) * 0.6 * float64(int64(1)<<(bits-1)-1)
			s := int32(v)
			switch bps {
			case 2:
				data = binary.LittleEndian.AppendUint16(data, uint16(int16(s)))
			case 3:
				data = append(data, byte(s), byte(s>>8), byte(s>>16))
			}
		}
	}
	raw := filepath.Join(t.TempDir(), "in.raw")
	if err := os.WriteFile(raw, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, fmt.Sprintf("%dbit-%dch.flac", bits, channels))
	args := []string{"--silent", "-f", "-o", out, "--force-raw-format", "--endian=little", "--sign=signed",
		fmt.Sprintf("--channels=%d", channels), fmt.Sprintf("--bps=%d", bits), fmt.Sprintf("--sample-rate=%d", rate)}
	for _, tag := range tags {
		args = append(args, "--tag="+tag)
	}
	if b, err := exec.Command("flac", append(args, raw)...).CombinedOutput(); err != nil {
		t.Fatalf("flac: %v %s", err, b)
	}
	return out
}

// wipeMD5 zeroes the signature like Qobuz files have it and returns the
// original.
func wipeMD5(t *testing.T, path string) [16]byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var orig [16]byte
	copy(orig[:], b[md5Offset:headerSize])
	if orig == ([16]byte{}) {
		t.Fatal("encoder did not write an MD5")
	}
	copy(b[md5Offset:headerSize], make([]byte, 16))
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return orig
}

func TestFixMD5MatchesEncoderAndChangesNothingElse(t *testing.T) {
	need(t, "flac")
	for _, tc := range []struct{ bits, ch, rate int }{{16, 2, 44100}, {24, 2, 96000}, {16, 1, 8000}, {24, 6, 48000}} {
		t.Run(fmt.Sprintf("%dbit-%dch", tc.bits, tc.ch), func(t *testing.T) {
			path := encode(t, t.TempDir(), tc.bits, tc.ch, tc.rate, 1.5)
			want := wipeMD5(t, path)
			before, _ := os.ReadFile(path)

			res, err := FixMD5(context.Background(), path, "flac", true)
			if err != nil {
				t.Fatal(err)
			}
			if res != MD5Written {
				t.Fatalf("result %v", res)
			}
			after, _ := os.ReadFile(path)
			if len(after) != len(before) {
				t.Fatalf("file size changed: %d -> %d", len(before), len(after))
			}
			var got [16]byte
			copy(got[:], after[md5Offset:headerSize])
			if got != want {
				t.Fatalf("MD5 %x, encoder wrote %x", got, want)
			}
			if !bytes.Equal(after[:md5Offset], before[:md5Offset]) || !bytes.Equal(after[headerSize:], before[headerSize:]) {
				t.Fatal("bytes other than the MD5 signature changed")
			}

			// Running again leaves the file alone.
			res, err = FixMD5(context.Background(), path, "flac", false)
			if err != nil || res != MD5AlreadySet {
				t.Fatalf("second run: %v %v", res, err)
			}
		})
	}
}

func TestFixMD5RefusesDamagedFiles(t *testing.T) {
	need(t, "flac")
	path := encode(t, t.TempDir(), 16, 2, 44100, 2)
	wipeMD5(t, path)
	b, _ := os.ReadFile(path)
	// Flip bytes in the middle of the audio.
	for i := len(b) / 2; i < len(b)/2+64; i++ {
		b[i] ^= 0xff
	}
	os.WriteFile(path, b, 0o644)

	if _, err := FixMD5(context.Background(), path, "flac", true); err == nil {
		t.Fatal("expected an error for a damaged file")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, b) {
		t.Fatal("a damaged file must not be modified")
	}
}

func TestFixMD5Truncated(t *testing.T) {
	need(t, "flac")
	path := encode(t, t.TempDir(), 16, 2, 44100, 2)
	wipeMD5(t, path)
	b, _ := os.ReadFile(path)
	os.WriteFile(path, b[:len(b)*2/3], 0o644)
	if _, err := FixMD5(context.Background(), path, "flac", false); err == nil {
		t.Fatal("expected an error for a truncated file")
	}
}

func TestNotFLAC(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.flac")
	os.WriteFile(p, []byte("ID3 not a flac file at all, padding padding padding"), 0o644)
	if _, err := FixMD5(context.Background(), p, "flac", false); err == nil {
		t.Fatal("expected an error")
	}
}

type upper struct{}

func (upper) TranslateAll(v []string) []string {
	out := make([]string, len(v))
	for i, s := range v {
		out[i] = strings.ReplaceAll(s, "Électronique", "Electronic")
	}
	return out
}
func (upper) Unknown(v []string) []string {
	var out []string
	for _, s := range v {
		if strings.Contains(s, "Inconnu") {
			out = append(out, s)
		}
	}
	return out
}

// audioFrames returns the bytes after the metadata blocks.
func audioFrames(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i := 4
	for {
		last := b[i]&0x80 != 0
		n := int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
		i += 4 + n
		if last {
			return b[i:]
		}
	}
}

func TestDirFixesMD5AndGenresWithoutTouchingAudio(t *testing.T) {
	need(t, "flac", "metaflac")
	dir := t.TempDir()
	a := encode(t, filepath.Join(dir, "Disc 1"), 16, 2, 44100, 1, "GENRE=Électronique", "TITLE=One")
	b := encode(t, filepath.Join(dir, "Disc 2"), 24, 2, 48000, 1, "GENRE=Rock", "GENRE=Inconnu")
	wipeMD5(t, a)
	wipeMD5(t, b)
	framesA, framesB := audioFrames(t, a), audioFrames(t, b)

	o := Options{FixMD5: true, Genres: upper{}, Confirm: true, FlacBin: "flac", MetaflacBin: "metaflac"}

	dry := o
	dry.DryRun = true
	rep, err := Dir(context.Background(), dir, dry)
	if err != nil {
		t.Fatal(err)
	}
	if md5, genres, errs := rep.Counts(); md5 != 2 || genres != 1 || errs != 0 {
		t.Fatalf("dry run counts md5=%d genres=%d errs=%d", md5, genres, errs)
	}
	if info, _ := ReadStreamInfo(a); info.MD5Set() {
		t.Fatal("dry run modified a file")
	}

	rep, err = Dir(context.Background(), dir, o)
	if err != nil {
		t.Fatal(err)
	}
	if md5, genres, errs := rep.Counts(); md5 != 2 || genres != 1 || errs != 0 {
		t.Fatalf("counts md5=%d genres=%d errs=%d: %+v", md5, genres, errs, rep.Changes)
	}
	if !slices.Equal(rep.UnknownGenres, []string{"Inconnu"}) {
		t.Fatalf("unknown genres %v", rep.UnknownGenres)
	}
	if g, _ := ReadTag(a, "GENRE"); !slices.Equal(g, []string{"Electronic"}) {
		t.Fatalf("genre %v", g)
	}
	if g, _ := ReadTag(a, "TITLE"); !slices.Equal(g, []string{"One"}) {
		t.Fatalf("other tags must survive, TITLE=%v", g)
	}
	if !bytes.Equal(audioFrames(t, a), framesA) || !bytes.Equal(audioFrames(t, b), framesB) {
		t.Fatal("audio frames changed")
	}
	for _, p := range []string{a, b} {
		if err := Test(context.Background(), p, "flac"); err != nil {
			t.Fatalf("flac -t %s: %v", p, err)
		}
	}
}
