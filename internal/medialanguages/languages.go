// Package medialanguages reads tracker-supplied MediaInfo and BDInfo reports.
// Reports are preference hints, not verification of the downloaded video.
package medialanguages

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

type Option struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var Options = []Option{{"en", "English"}, {"es", "Spanish"}, {"sv", "Swedish"}, {"da", "Danish"}, {"no", "Norwegian"}, {"fi", "Finnish"}, {"is", "Icelandic"}, {"fr", "French"}, {"de", "German"}, {"it", "Italian"}, {"pt", "Portuguese"}, {"nl", "Dutch"}, {"pl", "Polish"}, {"cs", "Czech"}, {"sk", "Slovak"}, {"hu", "Hungarian"}, {"ro", "Romanian"}, {"el", "Greek"}, {"tr", "Turkish"}, {"ru", "Russian"}, {"uk", "Ukrainian"}, {"ar", "Arabic"}, {"he", "Hebrew"}, {"fa", "Persian"}, {"hi", "Hindi"}, {"bn", "Bengali"}, {"ta", "Tamil"}, {"te", "Telugu"}, {"ja", "Japanese"}, {"ko", "Korean"}, {"zh", "Chinese"}, {"th", "Thai"}, {"vi", "Vietnamese"}, {"id", "Indonesian"}, {"ms", "Malay"}, {"ca", "Catalan"}, {"hr", "Croatian"}, {"sr", "Serbian"}, {"sl", "Slovenian"}, {"bg", "Bulgarian"}, {"et", "Estonian"}, {"lv", "Latvian"}, {"lt", "Lithuanian"}}
var aliases = map[string]string{}

func init() {
	aliases["norwegian bokmal"] = "no"
	aliases["norwegian nynorsk"] = "no"
	for _, option := range Options {
		aliases[option.ID] = option.ID
		aliases[strings.ToLower(option.Label)] = option.ID
	}
	for code, words := range map[string]string{"en": "eng", "es": "spa español espanol castellano", "sv": "swe svenska", "da": "dan dansk", "no": "nor nob nno nb nn norsk", "fi": "fin suomi", "is": "isl ice", "fr": "fra fre français francais", "de": "deu ger deutsch", "it": "ita italiano", "pt": "por português portugues", "nl": "nld dut", "pl": "pol", "cs": "ces cze", "sk": "slk slo", "hu": "hun", "ro": "ron rum", "el": "ell gre", "tr": "tur", "ru": "rus", "uk": "ukr", "ar": "ara", "he": "heb", "fa": "fas per farsi", "hi": "hin", "bn": "ben", "ta": "tam", "te": "tel", "ja": "jpn", "ko": "kor", "zh": "zho chi cmn yue", "th": "tha", "vi": "vie", "id": "ind", "ms": "msa may", "ca": "cat", "hr": "hrv", "sr": "srp", "sl": "slv", "bg": "bul", "et": "est", "lv": "lav", "lt": "lit"} {
		for _, word := range strings.Fields(words) {
			aliases[word] = code
		}
	}
}
func Supported(code string) bool {
	return slices.ContainsFunc(Options, func(o Option) bool { return o.ID == code })
}
func Normalize(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if code := aliases[value]; code != "" {
		return code
	}
	if first, _, ok := strings.Cut(value, "-"); ok {
		if code := aliases[first]; code != "" {
			return code
		}
	}
	// MediaInfo can append a region, a display name, or another representation.
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '(' || r == ')' || r == '_' }) {
		if code := aliases[strings.TrimSpace(part)]; code != "" {
			return code
		}
	}
	return ""
}

type Metadata struct {
	// VideoBitrateBPS is the actual reported video stream rate, never overall,
	// nominal, maximum or audio bitrate. Multiple video streams remain unknown.
	VideoBitrateBPS int64 `json:"video_bitrate_bps,omitempty"`
	videoTracks     int

	Audio             []string `json:"audio,omitempty"`
	Subtitles         []string `json:"subtitles,omitempty"`
	ForcedSubtitles   []string `json:"forced_subtitles,omitempty"`
	TextSubtitles     []string `json:"text_subtitles,omitempty"`
	ImageSubtitles    []string `json:"image_subtitles,omitempty"`
	HasTextSubtitles  bool     `json:"has_text_subtitles,omitempty"`
	HasImageSubtitles bool     `json:"has_image_subtitles,omitempty"`
	Status            string   `json:"status"` // reported, unknown, unsupported or unavailable
}

func (m Metadata) PreferenceScore(audio, subtitles []string, original string) int {
	score := 0
	seen := map[string]bool{}
	for _, code := range audio {
		if code == "original" {
			code = Normalize(original)
		}
		if code != "" && !seen[code] && slices.Contains(m.Audio, code) {
			seen[code] = true
			score++
		}
	}
	for _, code := range subtitles {
		if slices.Contains(m.Subtitles, code) {
			score++
		}
	}
	return score
}

func (m Metadata) TextPreferenceScore(preferred []string) int {
	if len(preferred) == 0 {
		if m.HasTextSubtitles || len(m.TextSubtitles) > 0 {
			return 1
		}
		return 0
	}
	score := 0
	for _, code := range preferred {
		if slices.Contains(m.TextSubtitles, code) {
			score++
		}
	}
	return score
}

type track struct{ kind, language, title, forced, format, codecID, bitrate string }

func (m *Metadata) add(t track) {
	if t.kind == "video" {
		m.videoTracks++
		if m.videoTracks == 1 {
			m.VideoBitrateBPS = parseVideoBitrate(t.bitrate)
		} else {
			m.VideoBitrateBPS = 0
		}
		return
	}

	code := Normalize(t.language)
	title := strings.ToLower(t.title)
	for _, word := range []string{"commentary", "comentario", "commentaire", "kommentar"} {
		if strings.Contains(title, word) {
			return
		}
	}
	if t.kind == "audio" {
		if code != "" {
			m.Audio = append(m.Audio, code)
		}
	} else if t.kind == "text" || t.kind == "subtitle" {
		forced := strings.ToLower(t.forced)
		title := strings.ToLower(t.title)
		if forced == "yes" || forced == "true" || forced == "1" || strings.Contains(title, "forced") || strings.Contains(title, "signs") || strings.Contains(title, "songs") {
			if code != "" {
				m.ForcedSubtitles = append(m.ForcedSubtitles, code)
			}
		} else {
			if code != "" {
				m.Subtitles = append(m.Subtitles, code)
			}
			switch subtitleFormat(t.format, t.codecID) {
			case "text":
				m.HasTextSubtitles = true
				if code != "" {
					m.TextSubtitles = append(m.TextSubtitles, code)
				}
			case "image":
				m.HasImageSubtitles = true
				if code != "" {
					m.ImageSubtitles = append(m.ImageSubtitles, code)
				}
			}
		}
	}
}

var sectionPattern = regexp.MustCompile(`(?i)^(general|video|audio|text|menu|image)(?:\s*#?\s*\d+)?\s*$`)

func Parse(mediaInfo, bdInfo string) Metadata {
	m := Metadata{Status: "unknown"}
	if len(mediaInfo) > 512<<10 || len(bdInfo) > 512<<10 {
		return m
	}
	if strings.HasPrefix(strings.TrimSpace(mediaInfo), "{") {
		parseJSON(&m, mediaInfo)
	} else {
		current := track{}
		for _, line := range strings.Split(mediaInfo, "\n") {
			line = strings.TrimSpace(line)
			if match := sectionPattern.FindStringSubmatch(line); match != nil {
				m.add(current)
				current = track{kind: strings.ToLower(match[1])}
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.TrimSpace(value)
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "bit rate":
				current.bitrate = value
			case "language":
				current.language = value
			case "title":
				current.title = value
			case "forced":
				current.forced = value
			case "format":
				current.format = value
			case "codec id":
				current.codecID = value
			}
		}
		m.add(current)
	}
	parseBDInfo(&m, bdInfo)
	m.Audio = unique(m.Audio)
	m.Subtitles = unique(m.Subtitles)
	m.ForcedSubtitles = unique(m.ForcedSubtitles)
	m.TextSubtitles = unique(m.TextSubtitles)
	m.ImageSubtitles = unique(m.ImageSubtitles)
	if len(m.Audio)+len(m.Subtitles)+len(m.ForcedSubtitles) > 0 || m.HasTextSubtitles || m.HasImageSubtitles || m.VideoBitrateBPS > 0 {
		m.Status = "reported"
	}
	return m
}
func unique(values []string) []string { slices.Sort(values); return slices.Compact(values) }
func parseJSON(m *Metadata, raw string) {
	var report struct {
		Media struct {
			Track []struct {
				Type     string `json:"@type"`
				Language string
				Title    string
				Forced   json.RawMessage
				Format   string
				CodecID  string
				BitRate  json.RawMessage
			}
		}
	}
	if json.Unmarshal([]byte(raw), &report) != nil {
		return
	}
	for _, t := range report.Media.Track {
		forced := strings.Trim(string(t.Forced), `"`)
		m.add(track{kind: strings.ToLower(t.Type), language: t.Language, title: t.Title, forced: forced, format: t.Format, codecID: t.CodecID, bitrate: jsonBitrate(t.BitRate)})
	}
}

var bdSummary = regexp.MustCompile(`(?i)^(?:\*\s*)?(audio|subtitles?)\s*:\s*(.*)$`)

func parseBDInfo(m *Metadata, raw string) {
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if match := bdSummary.FindStringSubmatch(line); match != nil {
			section = strings.ToLower(match[1])
			if section == "subtitles" {
				section = "subtitle"
			}
			if match[2] != "" {
				addBDLine(m, section, match[2])
			}
			continue
		}
		if strings.HasSuffix(line, ":") {
			section = ""
			continue
		}
		if section != "" {
			addBDLine(m, section, line)
		}
	}
}
func addBDLine(m *Metadata, kind, line string) {
	// Table rows identify languages after the codec; summary rows put them first.
	for _, word := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' || r == '/' || r == '(' || r == ')' }) {
		if code := Normalize(word); code != "" {
			m.add(track{kind: kind, language: code, title: line, format: line})
			return
		}
	}
}

func subtitleFormat(values ...string) string {
	text := false
	expanded := []string{}
	for _, value := range values {
		expanded = append(expanded, value)
		expanded = append(expanded, strings.Split(value, "/")...)
	}
	for _, value := range expanded {
		value = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, strings.ToLower(value))
		for _, prefix := range []string{"pgs", "presentationgraphics", "vobsub", "svobsub", "shdmvpgs", "hdmvpgs", "dvdsubtitle", "dvbsubtitle", "dvdsub", "xsub"} {
			if strings.HasPrefix(value, prefix) {
				return "image"
			}
		}
		for _, prefix := range []string{"utf8", "subrip", "srt", "ass", "ssa", "webvtt", "vtt", "timedtext", "movtext", "ttml", "tx3g", "wvtt", "stpp", "usf", "sami", "microdvd", "stextutf8", "stextass", "stextssa", "stextwebvtt", "stextusf"} {
			if strings.HasPrefix(value, prefix) {
				text = true
			}
		}
	}
	if text {
		return "text"
	}
	return ""
}
