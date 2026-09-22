package subtitles

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// MediaInfo is the probe result for one video file. Streams use the durable
// EmbeddedSubtitle shape so the probe cache can be stored with the item.
type MediaInfo struct {
	Streams    []EmbeddedSubtitle
	DurationMS int64
	// Probed is true when the video was actually read, even if it contains no
	// subtitle streams at all.
	Probed bool
}

// ExtractProgress is one progress sample of an embedded-subtitle extraction.
// Extracting one stream demuxes the whole container, so PositionMS — the
// timestamp ffmpeg has read up to — doubles as a measure of how much of the
// video file is left. The callback may be called from any goroutine and with
// PositionMS 0 while ffmpeg has not reported a timestamp yet.
type ExtractProgress struct {
	PositionMS int64
	Elapsed    time.Duration
}

// Prober probes video files and extracts or normalizes subtitle files.
type Prober interface {
	Probe(ctx context.Context, path string) (MediaInfo, error)
	// ExtractSubtitle writes one embedded subtitle stream out as SRT. progress
	// may be nil; when set it receives throttled samples while ffmpeg demuxes.
	ExtractSubtitle(ctx context.Context, path string, streamIndex int, outputPath string, progress func(ExtractProgress)) error
	ConvertToSRT(ctx context.Context, inputPath, outputPath string) error
	Version(ctx context.Context) (string, error)
}

// ExecProber runs the configured ffmpeg/ffprobe binaries.
type ExecProber struct {
	FFmpegPath     string
	FFprobePath    string
	ProbeTimeout   time.Duration
	ExtractTimeout time.Duration
}

func (p ExecProber) probeTimeout() time.Duration {
	if p.ProbeTimeout > 0 {
		return p.ProbeTimeout
	}
	return probeTimeout
}

func (p ExecProber) extractTimeout() time.Duration {
	if p.ExtractTimeout > 0 {
		return p.ExtractTimeout
	}
	return extractTimeout
}

func (p ExecProber) ffmpeg() string {
	if strings.TrimSpace(p.FFmpegPath) == "" {
		return "ffmpeg"
	}
	return p.FFmpegPath
}

func (p ExecProber) ffprobe() string {
	if strings.TrimSpace(p.FFprobePath) == "" {
		return "ffprobe"
	}
	return p.FFprobePath
}

type ffprobeOutput struct {
	Streams []struct {
		Index     int    `json:"index"`
		CodecName string `json:"codec_name"`
		CodecType string `json:"codec_type"`
		Tags      struct {
			Language string `json:"language"`
			Title    string `json:"title"`
		} `json:"tags"`
		Disposition struct {
			Default int `json:"default"`
			Forced  int `json:"forced"`
		} `json:"disposition"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// imageSubtitleCodecs cannot be used as an alass reference: they store bitmap
// images, not text. CineRoute detects them and reports no_reference instead of
// attempting OCR.
var imageSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true,
	"dvd_subtitle":      true,
	"dvb_subtitle":      true,
	"xsub":              true,
	"pgssub":            true,
}

// Probe runs ffprobe and parses subtitle streams plus the container duration.
func (p ExecProber) Probe(ctx context.Context, path string) (MediaInfo, error) {
	probeCtx, cancel := context.WithTimeout(ctx, p.probeTimeout())
	defer cancel()
	cmd := exec.CommandContext(probeCtx, p.ffprobe(),
		"-v", "error", "-print_format", "json",
		"-show_streams", "-select_streams", "s", "-show_format", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("subtitles: ffprobe failed", "video", path, "err", firstNonEmpty(stderr.String(), err.Error()))
		return MediaInfo{}, fmt.Errorf("ffprobe failed: %s", firstNonEmpty(stderr.String(), err.Error()))
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return MediaInfo{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
	slog.Debug("subtitles: ffprobe", "video", path, "streams", len(parsed.Streams))
	info := MediaInfo{}
	for _, stream := range parsed.Streams {
		codec := strings.ToLower(strings.TrimSpace(stream.CodecName))
		info.Streams = append(info.Streams, EmbeddedSubtitle{
			Index:    stream.Index,
			Codec:    codec,
			Language: normalizeLanguage(stream.Tags.Language),
			Title:    strings.TrimSpace(stream.Tags.Title),
			Forced:   stream.Disposition.Forced == 1,
			Default:  stream.Disposition.Default == 1,
			Usable:   !imageSubtitleCodecs[codec],
		})
	}
	info.Probed = true
	if duration, err := strconv.ParseFloat(strings.TrimSpace(parsed.Format.Duration), 64); err == nil && duration > 0 {
		info.DurationMS = int64(duration * 1000)
	}
	return info, nil
}

// ExtractSubtitle writes one embedded subtitle stream out as SRT.
//
// The step is I/O bound: ffmpeg has to demux the container from the start of the
// file because subtitle packets are interleaved with the video, so a large movie
// on a spinning disk can take minutes. When progress is set, ffmpeg's
// `-progress` stream is parsed so the caller can show how far the demux has got
// instead of an opaque spinner.
func (p ExecProber) ExtractSubtitle(ctx context.Context, path string, streamIndex int, outputPath string, progress func(ExtractProgress)) error {
	extractCtx, cancel := context.WithTimeout(ctx, p.extractTimeout())
	defer cancel()
	args := []string{"-v", "error", "-nostdin", "-y"}
	if progress != nil {
		args = append(args, "-progress", "pipe:1", "-nostats")
	}
	args = append(args, "-i", path, "-map", fmt.Sprintf("0:%d", streamIndex), "-c:s", "srt", outputPath)
	cmd := exec.CommandContext(extractCtx, p.ffmpeg(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	slog.Info("subtitles: extracting embedded subtitle stream",
		"video", path, "stream", streamIndex, "output", outputPath, "timeout", p.extractTimeout())

	if progress == nil {
		if err := cmd.Run(); err != nil {
			return p.extractFailure(path, streamIndex, extractCtx, stderr.String(), err)
		}
		return nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("ffmpeg subtitle extraction failed: %w", err)
	}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return p.extractFailure(path, streamIndex, extractCtx, stderr.String(), err)
	}
	position := scanExtractProgress(stdout, progress, started)
	if err := cmd.Wait(); err != nil {
		return p.extractFailure(path, streamIndex, extractCtx, stderr.String(), err)
	}
	progress(ExtractProgress{PositionMS: position, Elapsed: time.Since(started)})
	return nil
}

// extractFailure logs one failed extraction and turns a timeout into an error
// that says so, because "signal: killed" alone reads like a crash instead of a
// movie that is too slow to demux.
func (p ExecProber) extractFailure(path string, streamIndex int, ctx context.Context, stderr string, err error) error {
	slog.Warn("subtitles: subtitle extraction failed",
		"video", path, "stream", streamIndex, "err", firstNonEmpty(stderr, err.Error()))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("ffmpeg subtitle extraction timed out after %s: %w", p.extractTimeout(), context.DeadlineExceeded)
	}
	return fmt.Errorf("ffmpeg subtitle extraction failed: %s", firstNonEmpty(stderr, err.Error()))
}

// scanExtractProgress reads ffmpeg's `-progress` key=value stream and forwards
// throttled samples. It returns the last position ffmpeg reported.
func scanExtractProgress(r io.Reader, progress func(ExtractProgress), started time.Time) int64 {
	scanner := bufio.NewScanner(r)
	var position int64
	var last time.Time
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok {
			continue
		}
		switch key {
		// out_time_ms is microseconds as well, despite the name; ffmpeg has
		// reported it that way for years, so both are accepted.
		case "out_time_us", "out_time_ms":
			if micros, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && micros >= 0 {
				position = micros / 1000
			}
		}
		if time.Since(last) >= extractProgressInterval {
			last = time.Now()
			progress(ExtractProgress{PositionMS: position, Elapsed: time.Since(started)})
		}
	}
	return position
}

// ConvertToSRT normalizes a reference subtitle (for example .ass/.ssa or an
// unusual encoding) into a clean UTF-8 SRT file, mirroring the Python
// pipeline's `ffmpeg -f srt` normalization.
func (p ExecProber) ConvertToSRT(ctx context.Context, inputPath, outputPath string) error {
	convertCtx, cancel := context.WithTimeout(ctx, p.extractTimeout())
	defer cancel()
	cmd := exec.CommandContext(convertCtx, p.ffmpeg(),
		"-v", "error", "-nostdin", "-y", "-i", inputPath, "-f", "srt", outputPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	slog.Info("subtitles: normalizing reference subtitle", "input", inputPath, "output", outputPath)
	if err := cmd.Run(); err != nil {
		slog.Warn("subtitles: reference normalization failed", "input", inputPath, "err", firstNonEmpty(stderr.String(), err.Error()))
		return fmt.Errorf("ffmpeg subtitle normalization failed: %s", firstNonEmpty(stderr.String(), err.Error()))
	}
	return nil
}

// Version reports the ffmpeg build string, used by the status endpoint.
func (p ExecProber) Version(ctx context.Context) (string, error) {
	versionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(versionCtx, p.ffmpeg(), "-version")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg not available")
	}
	line := strings.SplitN(strings.TrimSpace(stdout.String()), "\n", 2)[0]
	return line, nil
}

const (
	probeTimeout   = 60 * time.Second
	extractTimeout = 300 * time.Second
	// extractProgressInterval throttles the samples taken from ffmpeg's progress
	// stream so a long extraction does not rewrite the item state constantly.
	extractProgressInterval = 2 * time.Second
)

// languageNames maps ffprobe/OpenSubtitles three-letter or alternative codes to
// the two-letter codes used everywhere else. OpenSubtitles reports Latin
// American Spanish as "ea" and European Spanish as "sp" (plain "es" is
// generic), so those two keep a regional suffix and can be preferred or avoided
// per target.
var languageNames = map[string]string{
	"ea": "es-419", "sp": "es-es",
	"eng": "en", "spa": "es", "swe": "sv", "por": "pt", "fin": "fi",
	"dan": "da", "nor": "no", "nob": "no", "nno": "no", "und": "",
	"fra": "fr", "fre": "fr", "deu": "de", "ger": "de", "ita": "it",
	"nld": "nl", "dut": "nl", "pol": "pl", "rus": "ru", "jpn": "ja",
	"zho": "zh", "chi": "zh", "kor": "ko", "ara": "ar", "tur": "tr",
	"ces": "cs", "cze": "cs", "ell": "el", "gre": "el", "heb": "he",
	"hun": "hu", "ron": "ro", "rum": "ro", "tha": "th", "ukr": "uk",
	"vie": "vi", "ind": "id", "hin": "hi", "cat": "ca", "hrv": "hr",
	"srp": "sr", "slk": "sk", "slo": "sk", "bul": "bg", "est": "et",
	"lav": "lv", "lit": "lt", "isl": "is", "ice": "is", "fas": "fa",
	"per": "fa", "msa": "ms", "may": "ms", "tam": "ta", "tel": "te",
}

// normalizeLanguage maps a subtitle language tag to the two-letter code used
// throughout CineRoute while keeping a regional suffix, so "spa" becomes "es",
// "english" becomes "en" and "es-419" stays "es-419" (a regional variant of
// Spanish rather than an unknown language).
func normalizeLanguage(tag string) string {
	value := strings.ToLower(strings.TrimSpace(tag))
	if value == "" {
		return ""
	}
	base, region, hasRegion := strings.Cut(value, "-")
	if mapped, ok := languageNames[base]; ok {
		base = mapped
	} else if len(base) > 3 {
		if mapped, ok := languageNames[base[:3]]; ok {
			base = mapped
		} else {
			base = base[:3]
		}
	}
	if base == "" {
		return ""
	}
	if hasRegion && strings.TrimSpace(region) != "" {
		return base + "-" + strings.TrimSpace(region)
	}
	return base
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
