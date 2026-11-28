package audiobooks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/sys/unix"
)

var (
	errCollision = errors.New("an audiobook already exists at that title")
	errSize      = errors.New("attachment is empty or exceeds the file size limit")
	errAudio     = errors.New("attachment could not be read as supported audio")
)

type book struct {
	Filename  string `json:"filename"`
	Extension string `json:"extension"`
	Digest    string `json:"sha256"`
	Author    string `json:"author"`
	Title     string `json:"title"`
}

var formats = map[string]string{".m4b": "mov", ".m4a": "mov", ".mp3": "mp3"}

func (b book) ready() bool { return b.Author != "" && b.Title != "" }

func cleanComponent(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return -1
		}
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, value)
	value = strings.Trim(value, " .\t")
	if len(value) > 180 {
		return ""
	}
	switch strings.ToLower(value) {
	case "unknown", "unknown author", "unknown artist", "unknown title", "unknown album", "untitled":
		return ""
	}
	return value
}

func details(text string) (author, title string) {
	left, right, ok := strings.Cut(text, "|")
	if !ok || strings.Contains(right, "|") {
		return "", ""
	}
	return cleanComponent(left), cleanComponent(right)
}

func attachmentExtension(item *attachment) string {
	if item.Name != "" {
		return strings.ToLower(filepath.Ext(item.Name))
	}
	switch item.MIMEType {
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/x-m4b":
		return ".m4b"
	default:
		return ""
	}
}

// Local Bot API file paths can include the bot token in a directory name.
func withoutPath(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

func (w *worker) loadBook(job string) (book, error) {
	var b book
	data, err := w.staging.ReadFile(filepath.Join(job, "book.json"))
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return b, err
	}
	if formats[b.Extension] == "" || len(b.Digest) != 2*sha256.Size ||
		cleanComponent(b.Author) != b.Author || cleanComponent(b.Title) != b.Title {
		return b, errors.New("invalid staged audiobook record")
	}
	return b, nil
}

func (w *worker) saveBook(job string, b book) error {
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	pending := filepath.Join(job, "book.json.tmp")
	if err := w.staging.WriteFile(pending, data, 0o600); err != nil {
		return err
	}
	return w.staging.Rename(pending, filepath.Join(job, "book.json"))
}

// The Bot API returns an absolute local path. Only read regular files beneath
// the configured shared mount. O_NONBLOCK also avoids opening a FIFO for read.
func (w *worker) stage(ctx context.Context, job string, msg *message, item *attachment, ext string) (book, error) {
	path, err := w.api.getFile(ctx, item.ID)
	if err != nil {
		return book{}, err
	}
	if !filepath.IsAbs(path) {
		return book{}, errors.New("getFile did not return a local path; check TELEGRAM_LOCAL")
	}
	rel, err := filepath.Rel(w.telegramPath, path)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return book{}, errors.New("getFile returned a path outside the Telegram download mount")
	}
	src, err := w.telegramRoot.OpenFile(rel, os.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		return book{}, fmt.Errorf("open Telegram download: %w", withoutPath(err))
	}
	defer src.Close()
	stat, err := src.Stat()
	if err != nil {
		return book{}, fmt.Errorf("stat Telegram download: %w", withoutPath(err))
	}
	if !stat.Mode().IsRegular() {
		return book{}, errAudio
	}
	if stat.Size() <= 0 || stat.Size() > w.maxFileBytes {
		return book{}, errSize
	}
	if item.Size > 0 && stat.Size() != item.Size {
		return book{}, errors.New("Telegram download size does not match the attachment")
	}

	// An interrupted copy has no record. It can be discarded on replay.
	if err := w.staging.RemoveAll(job); err != nil {
		return book{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = w.staging.RemoveAll(job)
		}
	}()
	if err := w.staging.Mkdir(job, 0o700); err != nil {
		return book{}, err
	}
	if err := w.staging.Mkdir(filepath.Join(job, "files"), 0o755); err != nil {
		return book{}, err
	}
	name := filepath.Join(job, "files", "audio"+ext)
	dst, err := w.staging.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return book{}, err
	}
	stop := context.AfterFunc(ctx, func() { src.Close() })
	hash := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(dst, hash), io.LimitReader(src, w.maxFileBytes+1))
	stop()
	syncErr := dst.Sync()
	closeErr := dst.Close()
	if err := errors.Join(ctx.Err(), withoutPath(copyErr), syncErr, closeErr); err != nil {
		return book{}, err
	}
	if n != stat.Size() {
		return book{}, errors.New("Telegram download changed during staging")
	}
	author, title, err := w.probe(ctx, filepath.Join(w.staging.Name(), name), ext)
	if err != nil {
		return book{}, err
	}
	if caption, ok := strings.CutPrefix(strings.TrimSpace(msg.Caption), "Book:"); ok {
		author, title = details(caption)
	}
	b := book{Filename: item.Name, Extension: ext, Digest: hex.EncodeToString(hash.Sum(nil)), Author: author, Title: title}
	if err := w.saveBook(job, b); err != nil {
		return book{}, err
	}
	keep = true
	return b, nil
}

func (w *worker) probe(ctx context.Context, path, ext string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, w.ffprobe, "-v", "error", "-protocol_whitelist", "file",
		"-f", formats[ext], "-select_streams", "a:0",
		"-show_entries", "stream=codec_type:format_tags=author,artist,album_artist,album,title", "-of", "json", "-i", path)
	cmd.Env = []string{"LANG=C"}
	var output cappedBuffer
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return "", "", errAudio
	}
	var parsed struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		}
		Format struct {
			Tags map[string]string
		}
	}
	if err := json.Unmarshal(output.Bytes(), &parsed); err != nil || len(parsed.Streams) == 0 || parsed.Streams[0].CodecType != "audio" {
		return "", "", errAudio
	}
	tags := make(map[string]string, len(parsed.Format.Tags))
	for key, value := range parsed.Format.Tags {
		tags[strings.ToLower(key)] = value
	}
	return firstTag(tags, "author", "artist", "album_artist"), firstTag(tags, "album", "title"), nil
}

type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 256<<10 {
		return 0, errors.New("ffprobe output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func firstTag(tags map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := cleanComponent(tags[key]); value != "" {
			return value
		}
	}
	return ""
}

// Folder publication is a single no-replace rename. After a crash following
// the rename, or on a repeat submission, verify an existing file by digest.
func (w *worker) publish(ctx context.Context, job string, b book) error {
	if err := w.library.Mkdir(b.Author, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	author, err := w.library.OpenFile(b.Author, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer author.Close()
	work, err := w.staging.OpenFile(job, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer work.Close()

	filename := b.Title + b.Extension
	folder := filepath.Join(job, "files")
	files, err := fs.ReadDir(w.staging.FS(), folder)
	if err == nil {
		if len(files) != 1 || !files[0].Type().IsRegular() {
			return errors.New("staged audiobook folder must contain one audio file")
		}
		if files[0].Name() != filename {
			if err := w.staging.Rename(filepath.Join(folder, files[0].Name()), filepath.Join(folder, filename)); err != nil {
				return err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err = renameNoReplace(int(work.Fd()), "files", int(author.Fd()), b.Title)
	if err == nil {
		return nil
	}
	if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	dest, openErr := w.library.OpenFile(filepath.Join(b.Author, b.Title, filename), os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if openErr == nil {
		defer dest.Close()
		stat, statErr := dest.Stat()
		if statErr != nil {
			return statErr
		}
		if stat.Mode().IsRegular() && stat.Size() <= w.maxFileBytes {
			stop := context.AfterFunc(ctx, func() { dest.Close() })
			defer stop()
			hash := sha256.New()
			if _, hashErr := io.Copy(hash, io.LimitReader(dest, w.maxFileBytes+1)); hashErr != nil {
				return hashErr
			}
			if hex.EncodeToString(hash.Sum(nil)) == b.Digest {
				return nil
			}
		}
	}
	if errors.Is(err, os.ErrExist) {
		return errCollision
	}
	return errors.New("staged audiobook is missing")
}
