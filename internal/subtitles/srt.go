package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// This file ports the subtitle-file handling from the proven
// `~/Projects/subs` pipeline: strict SRT timestamp validation, the download
// payload decoders (zip/gzip/utf-8-sig/utf-16/cp1252), the promo-cue cleanup and
// the malformed-cue repair, with the same safety limits.

// TimeSpan is one subtitle cue in milliseconds.
type TimeSpan struct {
	Start int64
	End   int64
}

// ParsedCue is a single SRT block with its raw timing line preserved.
type ParsedCue struct {
	Timing string
	Body   string
	Start  int64
	End    int64
}

var (
	srtTimestampRe = regexp.MustCompile(`(?m)^\s*(\d{1,2}):(\d{2}):(\d{2})[,.](\d{3})\s*-->\s*(\d{1,2}):(\d{2}):(\d{2})[,.](\d{3})`)
	timingLineRe   = regexp.MustCompile(`^\d{1,2}:\d{2}:\d{2}[,.]\d{3}\s*-->\s*\d{1,2}:\d{2}:\d{2}[,.]\d{3}$`)
	promoRe        = regexp.MustCompile(`(?i)(?:https?\s*://|www\s*\.|thepiratebay|subscene\s*\.\s*com|divxsweden\s*\.\s*net|swe\s*sub\s*\.\s*nu|undertext(?:er)?\s*\.\s*se)`)

	// ErrNoSRTTimestamps means the payload is not an SRT file at all.
	ErrNoSRTTimestamps = errors.New("file contains no SRT timestamps")
	// ErrCueEndBeforeStart means the file is structurally broken.
	ErrCueEndBeforeStart = errors.New("file contains a cue whose end precedes its start")
)

// CueTimesFromText returns every timestamp pair in the text, mirroring
// `sync-swedish-subs.py::cue_times`.
func CueTimesFromText(text string) ([]TimeSpan, error) {
	matches := srtTimestampRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil, ErrNoSRTTimestamps
	}
	cues := make([]TimeSpan, 0, len(matches))
	for _, match := range matches {
		start := timestampMS(match[1], match[2], match[3], match[4])
		end := timestampMS(match[5], match[6], match[7], match[8])
		if end < start {
			return nil, ErrCueEndBeforeStart
		}
		cues = append(cues, TimeSpan{Start: start, End: end})
	}
	return cues, nil
}

// CueTimesFile reads a subtitle file as UTF-8 and validates its cues.
func CueTimesFile(path string) ([]TimeSpan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read subtitle: %w", err)
	}
	text := decodeSubtitle(data)
	return CueTimesFromText(text)
}

func timestampMS(hours, minutes, seconds, milliseconds string) int64 {
	h, _ := strconv.Atoi(hours)
	m, _ := strconv.Atoi(minutes)
	s, _ := strconv.Atoi(seconds)
	ms, _ := strconv.Atoi(milliseconds)
	return ((int64(h)*60+int64(m))*60+int64(s))*1000 + int64(ms)
}

// DecodeSubtitlePayload turns an OpenSubtitles download payload into normalized
// UTF-8 SRT text. It ports `download-swedish-subs.py::subtitle_text`: zip and
// gzip containers are unpacked, several encodings are tried in the same order,
// line endings are normalized and the result must contain SRT timestamps.
func DecodeSubtitlePayload(data []byte) (string, error) {
	payload, err := unpackSubtitleArchive(data)
	if err != nil {
		return "", err
	}
	text := decodeSubtitle(payload)
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimPrefix(text, "\ufeff")
	if !srtTimestampRe.MatchString(text) {
		return "", errors.New("downloaded file does not contain valid SRT timestamps")
	}
	return strings.TrimRight(text, "\n") + "\n", nil
}

func unpackSubtitleArchive(data []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(data, []byte("PK\x03\x04")):
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("read downloaded subtitle archive: %w", err)
		}
		var names []string
		for _, file := range reader.File {
			if !file.FileInfo().IsDir() {
				names = append(names, file.Name)
			}
		}
		var preferred []string
		for _, name := range names {
			if strings.HasSuffix(strings.ToLower(name), ".srt") {
				preferred = append(preferred, name)
			}
		}
		if len(preferred) == 0 {
			return nil, errors.New("downloaded archive contains no SRT file")
		}
		sort.SliceStable(preferred, func(i, j int) bool {
			leftForced := strings.Contains(strings.ToLower(preferred[i]), "forced")
			rightForced := strings.Contains(strings.ToLower(preferred[j]), "forced")
			if leftForced != rightForced {
				return !leftForced
			}
			return len(preferred[i]) < len(preferred[j])
		})
		for _, file := range reader.File {
			if file.Name != preferred[0] {
				continue
			}
			handle, err := file.Open()
			if err != nil {
				return nil, fmt.Errorf("read downloaded subtitle archive: %w", err)
			}
			defer handle.Close()
			content, err := io.ReadAll(io.LimitReader(handle, maxSubtitleBytes+1))
			if err != nil {
				return nil, fmt.Errorf("read downloaded subtitle archive: %w", err)
			}
			if len(content) > maxSubtitleBytes {
				return nil, errors.New("downloaded subtitle exceeds the size limit")
			}
			return content, nil
		}
		return nil, errors.New("downloaded archive contains no SRT file")
	case bytes.HasPrefix(data, []byte{0x1f, 0x8b}):
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("decompress downloaded subtitle: %w", err)
		}
		defer reader.Close()
		content, err := io.ReadAll(io.LimitReader(reader, maxSubtitleBytes+1))
		if err != nil {
			return nil, fmt.Errorf("decompress downloaded subtitle: %w", err)
		}
		if len(content) > maxSubtitleBytes {
			return nil, errors.New("downloaded subtitle exceeds the size limit")
		}
		return content, nil
	default:
		return data, nil
	}
}

const maxSubtitleBytes = 25 * 1024 * 1024

// decodeSubtitle ports the encoding cascade: utf-8-sig, utf-16, cp1252,
// latin-1. cp1252 decodes every byte, so it is the effective fallback.
func decodeSubtitle(data []byte) string {
	if len(data) >= 2 {
		if data[0] == 0xff && data[1] == 0xfe {
			return string(utf16.Decode(bytesToUint16(data[2:], true)))
		}
		if data[0] == 0xfe && data[1] == 0xff {
			return string(utf16.Decode(bytesToUint16(data[2:], false)))
		}
	}
	if utf8.Valid(data) {
		return string(bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf}))
	}
	return decodeCP1252(data)
}

func bytesToUint16(data []byte, littleEndian bool) []uint16 {
	out := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if littleEndian {
			out = append(out, uint16(data[i])|uint16(data[i+1])<<8)
		} else {
			out = append(out, uint16(data[i])<<8|uint16(data[i+1]))
		}
	}
	return out
}

func decodeCP1252(data []byte) string {
	var b strings.Builder
	b.Grow(len(data))
	for _, value := range data {
		if value < 0x80 || value >= 0xa0 {
			b.WriteRune(rune(value))
			continue
		}
		if mapped, ok := cp1252High[value-0x80]; ok {
			b.WriteRune(mapped)
			continue
		}
		b.WriteRune(rune(value))
	}
	return b.String()
}

var cp1252High = map[byte]rune{
	0x80: '\u20ac', 0x82: '\u201a', 0x83: '\u0192', 0x84: '\u201e',
	0x85: '\u2026', 0x86: '\u2020', 0x87: '\u2021', 0x88: '\u02c6',
	0x89: '\u2030', 0x8a: '\u0160', 0x8b: '\u2039', 0x8c: '\u0152',
	0x8e: '\u017d', 0x91: '\u2018', 0x92: '\u2019', 0x93: '\u201c',
	0x94: '\u201d', 0x95: '\u2022', 0x96: '\u2013', 0x97: '\u2014',
	0x98: '\u02dc', 0x99: '\u2122', 0x9a: '\u0161', 0x9b: '\u203a',
	0x9c: '\u0153', 0x9e: '\u017e', 0x9f: '\u0178',
}

// IsValidSRTFile mirrors `download-swedish-subs.py::valid_srt`: non-empty and at
// least one timestamp line.
func IsValidSRTFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return srtTimestampRe.MatchString(decodeSubtitle(data))
}

// ParseCues parses SRT blocks, preserving the raw timing line so a rewritten
// file keeps identical timestamps.
func ParseCues(text string) ([]ParsedCue, error) {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimPrefix(normalized, "\ufeff")
	var cues []ParsedCue
	for _, block := range regexp.MustCompile(`\n\s*\n`).Split(strings.TrimSpace(normalized), -1) {
		lines := strings.Split(block, "\n")
		timingIndex := -1
		for i, line := range lines {
			if timingLineRe.MatchString(strings.TrimSpace(line)) {
				timingIndex = i
				break
			}
		}
		if timingIndex < 0 {
			continue
		}
		body := strings.TrimSpace(strings.Join(lines[timingIndex+1:], "\n"))
		if body == "" {
			continue
		}
		timing := strings.TrimSpace(lines[timingIndex])
		spans, err := cueSpansFromTiming(timing)
		if err != nil {
			continue
		}
		cues = append(cues, ParsedCue{Timing: timing, Body: body, Start: spans.Start, End: spans.End})
	}
	if len(cues) == 0 {
		return nil, ErrNoSRTTimestamps
	}
	return cues, nil
}

func cueSpansFromTiming(timing string) (TimeSpan, error) {
	match := srtTimestampRe.FindStringSubmatch(timing)
	if match == nil {
		return TimeSpan{}, ErrNoSRTTimestamps
	}
	return TimeSpan{
		Start: timestampMS(match[1], match[2], match[3], match[4]),
		End:   timestampMS(match[5], match[6], match[7], match[8]),
	}, nil
}

// RenderCues writes cues back as canonical SRT with renumbered indexes.
func RenderCues(cues []ParsedCue) string {
	blocks := make([]string, 0, len(cues))
	for i, cue := range cues {
		blocks = append(blocks, fmt.Sprintf("%d\n%s\n%s", i+1, cue.Timing, cue.Body))
	}
	return strings.Join(blocks, "\n\n") + "\n"
}

// RemovePromoCues drops uploader/site promotion cues, mirroring
// `cleanup-second-pass.py`.
func RemovePromoCues(text string) (string, int) {
	cues, err := ParseCues(text)
	if err != nil {
		return text, 0
	}
	kept := make([]ParsedCue, 0, len(cues))
	for _, cue := range cues {
		if promoRe.MatchString(cue.Body) {
			continue
		}
		kept = append(kept, cue)
	}
	removed := len(cues) - len(kept)
	if removed == 0 || len(kept) == 0 {
		return text, 0
	}
	return RenderCues(kept), removed
}

// maxCueMillis is the malformed-cue cutoff used by `repair_malformed_cues`:
// a single cue longer than ten minutes is treated as broken.
const maxCueMillis = 10 * 60 * 1000

// RepairMalformedCues removes isolated over-10-minute cues, but only when the
// number removed is small (at most max(3, cues/100)), exactly like the Python
// implementation.
func RepairMalformedCues(text string) (string, int) {
	cues, err := ParseCues(text)
	if err != nil {
		return text, 0
	}
	kept := make([]ParsedCue, 0, len(cues))
	for _, cue := range cues {
		if cue.End-cue.Start > maxCueMillis {
			continue
		}
		kept = append(kept, cue)
	}
	removed := len(cues) - len(kept)
	limit := len(cues) / 100
	if limit < 3 {
		limit = 3
	}
	if removed == 0 || removed > limit || len(kept) == 0 {
		return text, 0
	}
	repaired := RenderCues(kept)
	if _, err := CueTimesFromText(repaired); err != nil {
		return text, 0
	}
	return repaired, removed
}

// StripPromoAndRepair applies both cleanups in the order the Python pipeline
// used, returning the new text and the counts removed.
func StripPromoAndRepair(text string) (string, int, int) {
	cleaned, promos := RemovePromoCues(text)
	repaired, malformed := RepairMalformedCues(cleaned)
	return repaired, promos, malformed
}
