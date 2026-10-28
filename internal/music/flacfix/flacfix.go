// Package flacfix tidies FLAC files after download without touching the
// audio:
//
//   - Qobuz FLACs leave the MD5 signature in STREAMINFO empty, so players and
//     `flac -t` cannot check the audio. FixMD5 decodes the file with the
//     reference decoder, hashes the decoded samples exactly as the FLAC format
//     defines, and writes the 16 byte signature in place. No other byte of
//     the file changes.
//   - SetGenres rewrites the GENRE tags with metaflac, which only touches the
//     metadata blocks and copies the audio frames unchanged.
//
// Afterwards `flac -t` decodes the file again and compares the audio with
// the MD5 written from the original download, which proves the audio is
// still bit for bit what Qobuz delivered.
package flacfix

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// File layout: "fLaC", then a 4 byte block header, then STREAMINFO (34
// bytes) whose last 16 bytes are the MD5 signature.
const (
	streamInfoOffset = 8
	md5Offset        = streamInfoOffset + 18
	headerSize       = md5Offset + 16
)

// StreamInfo is the part of STREAMINFO FixMD5 needs.
type StreamInfo struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
	TotalSamples  uint64
	MD5           [16]byte
}

// MD5Set reports whether the file carries an MD5 signature.
func (s StreamInfo) MD5Set() bool { return s.MD5 != [16]byte{} }

// ReadStreamInfo reads STREAMINFO from the start of a FLAC file.
func ReadStreamInfo(path string) (StreamInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return StreamInfo{}, err
	}
	defer f.Close()
	var b [headerSize]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return StreamInfo{}, fmt.Errorf("not a FLAC file: %w", err)
	}
	return parseStreamInfo(b[:])
}

func parseStreamInfo(b []byte) (StreamInfo, error) {
	if len(b) < headerSize || string(b[:4]) != "fLaC" {
		return StreamInfo{}, errors.New("not a FLAC file")
	}
	if b[4]&0x7f != 0 {
		return StreamInfo{}, errors.New("first metadata block is not STREAMINFO")
	}
	if n := int(b[5])<<16 | int(b[6])<<8 | int(b[7]); n != 34 {
		return StreamInfo{}, fmt.Errorf("STREAMINFO has length %d, want 34", n)
	}
	si := b[streamInfoOffset:]
	// Bytes 10..17: sample rate (20 bits), channels-1 (3), bps-1 (5),
	// total samples (36).
	v := binary.BigEndian.Uint64(si[10:18])
	var s StreamInfo
	s.SampleRate = int(v >> 44)
	s.Channels = int(v>>41&0x7) + 1
	s.BitsPerSample = int(v>>36&0x1f) + 1
	s.TotalSamples = v & (1<<36 - 1)
	copy(s.MD5[:], si[18:34])
	return s, nil
}

// MD5Result says what FixMD5 did.
type MD5Result int

const (
	MD5AlreadySet MD5Result = iota // the file already had a signature; untouched
	MD5Written                     // the signature was missing and is now set
)

// FixMD5 sets a missing MD5 signature. flacBin is the `flac` command. When
// confirm is true the file is tested with `flac -t` afterwards and the old
// (empty) signature is restored if the test fails.
func FixMD5(ctx context.Context, path, flacBin string, confirm bool) (MD5Result, error) {
	info, err := ReadStreamInfo(path)
	if err != nil {
		return 0, err
	}
	if info.MD5Set() {
		return MD5AlreadySet, nil
	}
	if info.BitsPerSample%8 != 0 {
		// The decoder's raw output only matches the MD5 layout for whole
		// bytes per sample. Qobuz only delivers 16 and 24 bit.
		return 0, fmt.Errorf("unsupported bit depth %d", info.BitsPerSample)
	}

	sum, n, err := decodedMD5(ctx, path, flacBin)
	if err != nil {
		return 0, err
	}
	if want := info.TotalSamples * uint64(info.Channels) * uint64(info.BitsPerSample/8); info.TotalSamples > 0 && n != want {
		return 0, fmt.Errorf("decoder returned %d bytes of audio, STREAMINFO says %d", n, want)
	}
	if err := patchMD5(path, [16]byte{}, sum); err != nil {
		return 0, err
	}
	if confirm {
		if err := Test(ctx, path, flacBin); err != nil {
			if rerr := patchMD5(path, sum, [16]byte{}); rerr != nil {
				return 0, fmt.Errorf("%v; restoring the old signature also failed: %v", err, rerr)
			}
			return 0, fmt.Errorf("written signature did not verify: %w", err)
		}
	}
	return MD5Written, nil
}

// decodedMD5 hashes the decoded audio in the layout STREAMINFO's MD5 uses:
// interleaved, signed, little endian samples of whole bytes.
func decodedMD5(ctx context.Context, path, flacBin string) ([16]byte, uint64, error) {
	cmd := exec.CommandContext(ctx, flacBin,
		"--decode", "--stdout", "--silent",
		"--force-raw-format", "--endian=little", "--sign=signed",
		"--", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return [16]byte{}, 0, err
	}
	if err := cmd.Start(); err != nil {
		return [16]byte{}, 0, fmt.Errorf("running %s: %w", flacBin, err)
	}
	h := md5.New()
	n, copyErr := io.Copy(h, bufio.NewReaderSize(out, 512<<10))
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return [16]byte{}, 0, ctx.Err()
	}
	if waitErr != nil {
		return [16]byte{}, 0, fmt.Errorf("decoding failed: %s", lastLine(stderr.String(), waitErr))
	}
	if copyErr != nil {
		return [16]byte{}, 0, copyErr
	}
	var sum [16]byte
	copy(sum[:], h.Sum(nil))
	return sum, uint64(n), nil
}

// patchMD5 overwrites the 16 signature bytes in place after checking that
// they still hold old.
func patchMD5(path string, old, sum [16]byte) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	var b [headerSize]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		f.Close()
		return err
	}
	if _, err := parseStreamInfo(b[:]); err != nil {
		f.Close()
		return err
	}
	if !bytes.Equal(b[md5Offset:headerSize], old[:]) {
		f.Close()
		return errors.New("the file changed while it was being fixed")
	}
	if _, err := f.WriteAt(sum[:], md5Offset); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Test runs `flac -t`, which decodes the file, checks every frame and
// compares the audio with the MD5 signature.
func Test(ctx context.Context, path, flacBin string) error {
	cmd := exec.CommandContext(ctx, flacBin, "--test", "--silent", "--", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(lastLine(stderr.String(), err))
	}
	return nil
}

func lastLine(s string, fallback error) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return fallback.Error()
}
