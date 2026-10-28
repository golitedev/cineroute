package flacfix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ReadTag returns the values of a Vorbis comment (e.g. "GENRE") in order.
func ReadTag(path, name string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var magic [4]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil || string(magic[:]) != "fLaC" {
		return nil, errors.New("not a FLAC file")
	}
	for {
		var h [4]byte
		if _, err := io.ReadFull(r, h[:]); err != nil {
			return nil, fmt.Errorf("reading metadata: %w", err)
		}
		last, typ := h[0]&0x80 != 0, h[0]&0x7f
		n := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		if typ == 4 {
			b := make([]byte, n)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, fmt.Errorf("reading tags: %w", err)
			}
			return parseComments(b, name)
		}
		if _, err := r.Discard(n); err != nil {
			return nil, fmt.Errorf("reading metadata: %w", err)
		}
		if last {
			return nil, nil
		}
	}
}

func parseComments(b []byte, name string) ([]string, error) {
	bad := errors.New("damaged tag block")
	next := func() ([]byte, bool) {
		if len(b) < 4 {
			return nil, false
		}
		n := binary.LittleEndian.Uint32(b)
		if uint64(n) > uint64(len(b)-4) {
			return nil, false
		}
		v := b[4 : 4+n]
		b = b[4+n:]
		return v, true
	}
	if _, ok := next(); !ok { // vendor string
		return nil, bad
	}
	if len(b) < 4 {
		return nil, bad
	}
	count := binary.LittleEndian.Uint32(b)
	b = b[4:]
	var out []string
	for i := uint32(0); i < count; i++ {
		c, ok := next()
		if !ok {
			return nil, bad
		}
		k, v, found := bytes.Cut(c, []byte("="))
		if found && strings.EqualFold(string(k), name) {
			out = append(out, string(v))
		}
	}
	return out, nil
}

// TagUpdate replaces all values of one Vorbis comment.
type TagUpdate struct {
	Name   string
	Values []string
}

// SetTag replaces all values of a Vorbis comment using metaflac.
func SetTag(ctx context.Context, path, name string, values []string, metaflacBin string) error {
	return SetTags(ctx, path, []TagUpdate{{Name: name, Values: values}}, metaflacBin)
}

// SetTags replaces several Vorbis comments in one metaflac run. metaflac
// rewrites only the metadata; the audio frames are copied unchanged.
// Values may span several lines.
func SetTags(ctx context.Context, path string, updates []TagUpdate, metaflacBin string) error {
	args := []string{"--no-utf8-convert"}
	for _, u := range updates {
		args = append(args, "--remove-tag="+u.Name)
		for _, v := range u.Values {
			if strings.ContainsRune(v, 0) {
				return fmt.Errorf("invalid tag value %q", v)
			}
			args = append(args, "--set-tag="+u.Name+"="+v)
		}
	}
	args = append(args, "--", path)
	cmd := exec.CommandContext(ctx, metaflacBin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("metaflac: %s", lastLine(stderr.String(), err))
	}
	return nil
}
