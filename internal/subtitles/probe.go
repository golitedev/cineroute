package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
}

// Prober probes video files and extracts or normalizes subtitle files.
type Prober interface {
	Probe(ctx context.Context, path string) (MediaInfo, error)
	ExtractSubtitle(ctx context.Context, path string, streamIndex int, outputPath string) error
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
		"-v", "error", "-nostdin", "-print_format", "json",
		"-show_streams", "-select_streams", "s", "-show_format", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return MediaInfo{}, fmt.Errorf("ffprobe failed: %s", firstNonEmpty(stderr.String(), err.Error()))
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		return MediaInfo{}, fmt.Errorf("decode ffprobe output: %w", err)
	}
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
	if duration, err := strconv.ParseFloat(strings.TrimSpace(parsed.Format.Duration), 64); err == nil && duration > 0 {
		info.DurationMS = int64(duration * 1000)
	}
	return info, nil
}

// ExtractSubtitle writes one embedded subtitle stream out as SRT.
func (p ExecProber) ExtractSubtitle(ctx context.Context, path string, streamIndex int, outputPath string) error {
	extractCtx, cancel := context.WithTimeout(ctx, p.extractTimeout())
	defer cancel()
	cmd := exec.CommandContext(extractCtx, p.ffmpeg(),
		"-v", "error", "-nostdin", "-y", "-i", path,
		"-map", fmt.Sprintf("0:%d", streamIndex), "-c:s", "srt", outputPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg subtitle extraction failed: %s", firstNonEmpty(stderr.String(), err.Error()))
	}
	return nil
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
	if err := cmd.Run(); err != nil {
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
)

// languageNames maps ffprobe/OpenSubtitles three-letter or alternative codes to
// the two-letter codes used everywhere else.
var languageNames = map[string]string{
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

func normalizeLanguage(tag string) string {
	value := strings.ToLower(strings.TrimSpace(tag))
	if value == "" {
		return ""
	}
	if mapped, ok := languageNames[value]; ok {
		return mapped
	}
	if len(value) > 3 {
		value = value[:3]
		if mapped, ok := languageNames[value]; ok {
			return mapped
		}
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
