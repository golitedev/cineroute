// Package library moves verified albums into the music library.
package library

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cineroute/internal/music/naming"
	"cineroute/internal/music/qobuz"
)

// Quality is what was actually downloaded, read from the files.
type Quality struct {
	Format     string // "flac", "mp3", ...
	BitDepth   int
	SampleRate int // Hz
}

// Values returns the placeholder values for an album folder template. q may
// be zero, in which case the best quality Qobuz offers (capped by the
// configured quality) is assumed.
func Values(a qobuz.Album, q Quality, maxQuality int) map[string]string {
	format := strings.ToUpper(q.Format)
	bitDepth, rate := q.BitDepth, float64(q.SampleRate)/1000
	if q.Format == "" {
		format, bitDepth, rate = expectedQuality(a, maxQuality)
	}
	v := map[string]string{
		"albumartist": a.Artist,
		"title":       a.FullTitle(),
		"album":       a.Title,
		"version":     a.Version,
		"year":        a.Year(),
		"date":        a.ReleaseDate,
		"label":       a.Label,
		"genre":       a.Genre,
		"format":      format,
		"id":          a.ID,
		"upc":         a.UPC,
	}
	if bitDepth > 0 {
		v["bit_depth"] = strconv.Itoa(bitDepth)
	}
	if rate > 0 {
		v["sampling_rate"] = strconv.FormatFloat(rate, 'f', -1, 64)
	}
	if v["albumartist"] == "" {
		v["albumartist"] = "Unknown Artist"
	}
	if v["title"] == "" {
		v["title"], v["album"] = "Unknown Album", "Unknown Album"
	}
	return v
}

// expectedQuality guesses the downloaded quality before downloading.
func expectedQuality(a qobuz.Album, maxQuality int) (format string, bitDepth int, rateKHz float64) {
	if maxQuality <= 1 {
		return "MP3", 0, 0
	}
	bitDepth, rateKHz = a.MaxBitDepth, a.MaxSamplingRate
	if bitDepth == 0 {
		bitDepth = 16
	}
	if rateKHz == 0 {
		rateKHz = 44.1
	}
	switch maxQuality {
	case 2:
		bitDepth, rateKHz = 16, min(rateKHz, 44.1)
	case 3:
		rateKHz = min(rateKHz, 96)
	}
	return "FLAC", bitDepth, rateKHz
}

// FolderFor renders the album folder (relative to the library).
func FolderFor(tmpl string, a qobuz.Album, q Quality, maxQuality int, restrict bool) (string, error) {
	return naming.Render(tmpl, Values(a, q, maxQuality), restrict)
}

// Exists reports whether rel exists inside the library.
func Exists(library, rel string) bool {
	_, err := os.Lstat(filepath.Join(library, filepath.FromSlash(rel)))
	return err == nil
}

// Import moves the album folder src into library/rel. The destination must not
// exist. When src is on another file system the album is first copied into a
// hidden folder next to the destination and then renamed into place, so music
// servers never see a half-copied album.
func Import(src, library, rel string) (string, error) {
	dst, err := safeJoin(library, rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(dst); err == nil {
		return "", fmt.Errorf("%s already exists", dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(dst), err)
	}
	if err := os.Rename(src, dst); err == nil {
		return dst, nil
	} else if !isCrossDevice(err) {
		return "", fmt.Errorf("moving album into the library: %w", err)
	}

	tmp := filepath.Join(filepath.Dir(dst), fmt.Sprintf(".cineroute-import-%d", time.Now().UnixNano()))
	if err := copyTree(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("copying album into the library: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.RemoveAll(tmp)
		return "", fmt.Errorf("moving album into place: %w", err)
	}
	if err := os.RemoveAll(src); err != nil {
		return dst, fmt.Errorf("album imported, but removing the staging copy failed: %w", err)
	}
	return dst, nil
}

// MoveAside moves an existing library folder out of the way (used when
// naming.if_exists = "replace"). It returns where the folder went.
func MoveAside(library, rel, trash string) (string, error) {
	src, err := safeJoin(library, rel)
	if err != nil {
		return "", err
	}
	name := time.Now().Format("20060102-150405") + " " + strings.ReplaceAll(rel, "/", " - ")
	dst := filepath.Join(trash, name)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(src, dst); err == nil {
		return dst, nil
	} else if !isCrossDevice(err) {
		return "", err
	}
	if err := copyTree(src, dst); err != nil {
		os.RemoveAll(dst)
		return "", err
	}
	return dst, os.RemoveAll(src)
}

// RemoveEmptyParents removes now-empty folders between dir and stop.
func RemoveEmptyParents(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

func safeJoin(root, rel string) (string, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid album folder %q", rel)
	}
	return filepath.Join(root, rel), nil
}

func isCrossDevice(err error) bool {
	var le *os.LinkError
	if errors.As(err, &le) {
		return errors.Is(le.Err, syscall.EXDEV)
	}
	return errors.Is(err, syscall.EXDEV)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(path, target)
		default:
			return nil // skip symlinks and special files
		}
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
