package subtitles

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// OpenSubtitlesHash computes the well-known OpenSubtitles hash: the file size
// plus the 64-bit little-endian sum of the first and last 64 KiB. CineRoute uses
// it as an extra search query and ranking signal, never as a hard filter, since
// the proven pipeline matches releases by title/feature and validates with alass
// timing instead.
func OpenSubtitlesHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open video for hashing: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat video for hashing: %w", err)
	}
	size := uint64(info.Size())
	const chunkSize = 64 * 1024

	hash := size
	buffer := make([]byte, chunkSize)

	readSum := func(offset int64) error {
		length := chunkSize
		if offset < 0 {
			offset = 0
		}
		remaining := int64(size) - offset
		if remaining <= 0 {
			return nil
		}
		if remaining < int64(length) {
			length = int(remaining)
		}
		if _, err := file.ReadAt(buffer[:length], offset); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for i := 0; i+8 <= length; i += 8 {
			hash += binary.LittleEndian.Uint64(buffer[i : i+8])
		}
		return nil
	}

	if err := readSum(0); err != nil {
		return "", fmt.Errorf("hash video head: %w", err)
	}
	if size > chunkSize {
		if err := readSum(int64(size) - chunkSize); err != nil {
			return "", fmt.Errorf("hash video tail: %w", err)
		}
	}
	return fmt.Sprintf("%016x", hash), nil
}
