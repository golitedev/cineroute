package requests

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"cineroute/internal/medialanguages"
	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type Candidate struct {
	RequestedAudioScore int
	Release             prowlarr.Release
	Quality             string
	Rank                int
	Score               int
	Priority            int
	Pack                bool
	LanguageScore       int
	SubtitleFormatScore int
}

func ReleaseKey(r prowlarr.Release) string {
	identity := r.Guid
	if identity == "" {
		identity = fmt.Sprintf("%s\x00%d\x00%s", r.Title, r.Size, r.InfoURL)
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", r.IndexerID, identity)))
	return hex.EncodeToString(sum[:])
}
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}
func tokens(s string) string { return " " + strings.Join(words(s), " ") + " " }

func resolution(title string) string {
	t := strings.ToLower(title)
	for _, r := range []string{"2160p", "1080p", "720p"} {
		if strings.Contains(t, r) {
			return r
		}
	}
	return ""
}

func Quality(title string) string {
	source := qualitySource(title)
	if source == "cam" || source == "telesync" || source == "dvd" {
		return source
	}
	if res := resolution(title); source != "" && res != "" {
		return source + "-" + res
	}
	return ""
}

func qualitySource(title string) string {
	t := strings.ToLower(title)
	labels := tokens(title)
	if strings.Contains(labels, " remux ") {
		return "remux"
	}
	if strings.Contains(labels, " web dl ") || strings.Contains(labels, " webdl ") {
		return "webdl"
	}
	if strings.Contains(labels, " webrip ") || strings.Contains(labels, " web rip ") {
		return "webrip"
	}
	if strings.Contains(labels, " bluray ") || strings.Contains(labels, " blu ray ") || strings.Contains(labels, " bdrip ") || strings.Contains(labels, " bd rip ") || strings.Contains(labels, " brrip ") || strings.Contains(labels, " br rip ") {
		return "bluray"
	}
	if strings.Contains(labels, " hdtv ") {
		return "hdtv"
	}
	for _, word := range words(t) {
		if word == "cam" || word == "hdcam" {
			return "cam"
		}
		if word == "telesync" || word == "hdts" {
			return "telesync"
		}
		if word == "dvd" || word == "dvdrip" {
			return "dvd"
		}
	}
	return ""
}
func Codec(title string) string {
	t := tokens(title)
	compact := strings.Join(words(title), "")
	switch {
	case strings.Contains(t, " av1 "):
		return "av1"
	case strings.Contains(t, " hevc ") || strings.Contains(compact, "x265") || strings.Contains(compact, "h265"):
		return "hevc"
	case strings.Contains(t, " avc ") || strings.Contains(compact, "x264") || strings.Contains(compact, "h264"):
		return "h264"
	case strings.Contains(compact, "mpeg2"):
		return "mpeg2"
	}
	return ""
}
func KeywordScore(title string, p Profile) (int, error) {
	score := 0
	for _, rule := range p.Keywords {
		matched := strings.Contains(strings.ToLower(title), strings.ToLower(rule.Pattern))
		if rule.Match == "regex" {
			r, err := regexp.Compile(rule.Pattern)
			if err != nil {
				return 0, err
			}
			matched = r.MatchString(title)
		}
		if rule.Mode == "required" && !matched {
			return 0, fmt.Errorf("required keyword did not match")
		}
		if rule.Mode == "excluded" && matched {
			return 0, fmt.Errorf("blocked keyword matched")
		}
		if rule.Mode == "preferred" && matched {
			score += rule.Score
		}
	}
	if score < p.MinimumScore {
		return score, fmt.Errorf("keyword score below minimum")
	}
	return score, nil
}

var languageWords = map[string][]string{"en": {"eng", "english"}, "es": {"spa", "spanish", "castellano", "latino", "latina", "espanol"}, "sv": {"swe", "swedish", "svenska"}, "fr": {"fre", "fra", "french"}, "de": {"ger", "deu", "german"}, "it": {"ita", "italian"}, "ja": {"jpn", "japanese"}, "ko": {"kor", "korean"}, "pt": {"por", "portuguese"}, "zh": {"chi", "zho", "chinese"}, "ru": {"rus", "russian"}}

func languageAllowed(title, original string, p Profile) bool {
	if len(p.AudioLanguages) == 0 {
		return true
	}
	t := tokens(title)
	known := false
	matched := map[string]bool{}
	for language, aliases := range languageWords {
		for _, alias := range aliases {
			if strings.Contains(t, " "+alias+" ") {
				known = true
				matched[language] = true
			}
		}
	}
	for _, wanted := range p.AudioLanguages {
		if wanted == "original" {
			wanted = original
		}
		wanted = strings.Split(wanted, "-")[0]
		if matched[wanted] {
			return true
		}
	}
	return !known && p.AllowUnknownLanguage
}
func Evaluate(r prowlarr.Release, media tmdb.Details, p Profile, minSeeders int, priority int) (Candidate, error) {
	c := Candidate{Release: r, Quality: Quality(r.Title), Priority: priority}
	c.Rank = slices.Index(p.Qualities, c.Quality)
	c.Pack = media.MediaType == "tv" && len(EpisodeKeys(r.Title)) == 0
	if r.TmdbID > 0 && r.TmdbID != media.ID {
		return c, fmt.Errorf("different TMDB ID")
	}
	if !MatchTitle(r.Title, media, r.TmdbID != media.ID && media.MediaType == "movie") {
		return c, fmt.Errorf("title or year does not match")
	}
	if c.Rank < 0 {
		return c, fmt.Errorf("quality is not allowed or could not be identified")
	}
	if !slices.Contains(p.Codecs, Codec(r.Title)) {
		return c, fmt.Errorf("codec is not allowed or unknown")
	}
	if minSeeders > 0 && (r.Seeders == nil || *r.Seeders < minSeeders) {
		return c, fmt.Errorf("not enough confirmed seeders")
	}
	if !audioPolicyAllowed(r.Title, media.OriginalLanguage, p, r.Languages) {
		return c, fmt.Errorf("audio language is not confirmed")
	}
	score, err := KeywordScore(r.Title, p)
	if err != nil {
		return c, err
	}
	c.Score = score
	c.LanguageScore = r.Languages.PreferenceScore(p.PreferredAudioLanguages, p.PreferredSubtitleLanguages, media.OriginalLanguage)
	if p.PreferTextSubtitles {
		c.SubtitleFormatScore = r.Languages.TextPreferenceScore(p.PreferredSubtitleLanguages)
	}
	if r.Size > 0 {
		min, max := p.MinSizeGiB, p.MaxSizeGiB
		if c.Pack {
			min, max = 0, p.MaxPackSizeGiB
		}
		if err = sizeAllowed(r.Size, min, max); err != nil {
			return c, err
		}
	}
	return c, nil
}
func sizeAllowed(size int64, min, max float64) error {
	gib := float64(size) / (1 << 30)
	if gib < min || (max > 0 && gib > max) {
		return fmt.Errorf("release size is outside the configured range")
	}
	return nil
}

func audioPolicyAllowed(title, original string, p Profile, metadata medialanguages.Metadata) bool {
	if len(p.AudioLanguages) == 0 || len(metadata.Audio) == 0 {
		return languageAllowed(title, original, p)
	}
	for _, wanted := range p.AudioLanguages {
		if wanted == "original" {
			wanted = original
		}
		if slices.Contains(metadata.Audio, strings.Split(wanted, "-")[0]) {
			return true
		}
	}
	return false
}

func AssessExisting(evidence string, size int64, media tmdb.Details, p Profile, reported ...medialanguages.Metadata) Existing {
	quality := Quality(evidence)
	score, err := KeywordScore(evidence, p)
	var metadata medialanguages.Metadata
	if len(reported) > 0 {
		metadata = reported[0]
	}
	return Existing{Quality: quality, Score: score, Conforms: err == nil && slices.Contains(p.Qualities, quality) && slices.Contains(p.Codecs, Codec(evidence)) && audioPolicyAllowed(evidence, media.OriginalLanguage, p, metadata) && sizeAllowed(size, p.MinSizeGiB, p.MaxSizeGiB) == nil}
}

func ImprovesExisting(quality string, score int, old Existing, p Profile) bool {
	return !old.Conforms || Improved(quality, score, old.Quality, old.Score, p)
}
func Better(a, b Candidate, preferPacks bool) bool {
	if a.Rank != b.Rank {
		return a.Rank < b.Rank
	}
	if a.RequestedAudioScore != b.RequestedAudioScore {
		return a.RequestedAudioScore > b.RequestedAudioScore
	}
	if a.LanguageScore != b.LanguageScore {
		return a.LanguageScore > b.LanguageScore
	}
	if a.SubtitleFormatScore != b.SubtitleFormatScore {
		return a.SubtitleFormatScore > b.SubtitleFormatScore
	}
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	if a.Pack != b.Pack {
		return a.Pack == preferPacks
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	as, bs := 0, 0
	if a.Release.Seeders != nil {
		as = *a.Release.Seeders
	}
	if b.Release.Seeders != nil {
		bs = *b.Release.Seeders
	}
	if as != bs {
		return as > bs
	}
	return a.Release.Size < b.Release.Size
}
func Improved(quality string, score int, oldQuality string, oldScore int, p Profile) bool {
	oldRank, newRank := slices.Index(p.Qualities, oldQuality), slices.Index(p.Qualities, quality)
	if newRank < 0 {
		return false
	}
	if oldRank < 0 {
		return true
	}
	cutoff := slices.Index(p.Qualities, p.UpgradeUntil)
	if oldRank <= cutoff && oldScore >= p.UpgradeUntilScore {
		return false
	}
	return newRank < oldRank || (newRank == oldRank && score > oldScore)
}

var episodePattern = regexp.MustCompile(`(?i)s([0-9]{1,3})[ ._-]*e([0-9]{1,4})((?:[ ._-]*e[0-9]{1,4})*)(?:[ ._-]*-[ ._-]*e?([0-9]{1,4}))?`)
var extraEpisode = regexp.MustCompile(`(?i)e([0-9]{1,4})`)
var xEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])([0-9]{1,3})x([0-9]{1,4})(?:[^0-9]|$)`)

func EpisodeKey(season, episode int) string { return fmt.Sprintf("s%de%d", season, episode) }
func EpisodeKeys(path string) []string {
	out := []string{}
	for _, m := range episodePattern.FindAllStringSubmatch(path, -1) {
		season, _ := strconv.Atoi(m[1])
		first, _ := strconv.Atoi(m[2])
		out = append(out, EpisodeKey(season, first))
		for _, e := range extraEpisode.FindAllStringSubmatch(m[3], -1) {
			n, _ := strconv.Atoi(e[1])
			out = append(out, EpisodeKey(season, n))
		}
		if m[4] != "" {
			last, _ := strconv.Atoi(m[4])
			if last >= first && last-first <= 100 {
				for n := first + 1; n <= last; n++ {
					out = append(out, EpisodeKey(season, n))
				}
			}
		}
	}
	if len(out) == 0 {
		for _, m := range xEpisode.FindAllStringSubmatch(path, -1) {
			s, _ := strconv.Atoi(m[1])
			e, _ := strconv.Atoi(m[2])
			out = append(out, EpisodeKey(s, e))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
func IsVideo(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".webm", ".ts", ".m2ts", ".mov":
		return true
	}
	return false
}
func IsExtra(path string) bool {
	parts := strings.FieldsFunc(strings.ToLower(path), func(r rune) bool { return r == '/' || r == '\\' })
	for i, part := range parts {
		label := strings.TrimSpace(tokens(part))
		if i == len(parts)-1 {
			label = strings.TrimSpace(tokens(strings.TrimSuffix(part, filepath.Ext(part))))
		}
		for _, word := range []string{"sample", "samples", "trailer", "trailers", "featurettes", "extras"} {
			if label == word || (i == len(parts)-1 && strings.HasSuffix(label, " "+word)) {
				return true
			}
		}
	}
	return false
}

// ValidateTorrent checks actual video files and sizes, rather than trusting
// a tracker title or reported size. Packs must cover only requested episodes.
func ValidateTorrent(meta *torrentmeta.MetaInfo, c Candidate, job Job, p Profile, wanted map[string]bool, covered map[string]bool) ([]string, error) {
	identity := MatchTitle(meta.Name, job.Media, false)
	keys := []string{}
	seenEpisodes := map[string]bool{}
	videos := 0
	for _, file := range meta.Files {
		path := strings.Join(file.FullPath, "/")
		if !IsVideo(path) || IsExtra(path) {
			continue
		}
		videos++
		if MatchTitle(filepath.Base(path), job.Media, false) {
			identity = true
		}
		// A pack's folder labels must not override contradictory file labels.
		// Bare episode filenames can still inherit metadata from their folder.
		filename := filepath.Base(path)
		if actual := resolution(filename); actual != "" && actual != resolution(c.Quality) {
			return nil, fmt.Errorf("actual video quality contradicts tracker title")
		}
		if source := qualitySource(filename); source != "" && source != qualitySource(c.Quality) {
			return nil, fmt.Errorf("actual video quality contradicts tracker title")
		}
		actual := Quality(filename)
		if actual == "" {
			actual = Quality(path)
		}
		if actual != "" && actual != c.Quality {
			return nil, fmt.Errorf("actual video quality contradicts tracker title")
		}
		codec := Codec(filename)
		if codec == "" {
			codec = Codec(path)
		}
		if codec != "" && !slices.Contains(p.Codecs, codec) {
			return nil, fmt.Errorf("actual video codec is not allowed")
		}
		if job.Media.MediaType == "tv" {
			episodes := EpisodeKeys(path)
			if len(episodes) == 0 {
				return nil, fmt.Errorf("video has no unambiguous season/episode numbering")
			}
			for _, key := range episodes {
				if !wanted[key] || covered[key] || seenEpisodes[key] {
					return nil, fmt.Errorf("pack contains unrequested or already covered episodes")
				}
				keys = append(keys, key)
				seenEpisodes[key] = true
			}
			if err := sizeAllowed(file.Length/int64(len(episodes)), p.MinSizeGiB, p.MaxSizeGiB); err != nil {
				return nil, err
			}
		}
	}
	if !identity || videos == 0 {
		return nil, fmt.Errorf("torrent identity could not be verified")
	}
	if job.Media.MediaType == "movie" {
		if videos != 1 {
			return nil, fmt.Errorf("movie torrent must contain one unambiguous feature video")
		}
		return nil, sizeAllowed(meta.Size, p.MinSizeGiB, p.MaxSizeGiB)
	}
	if len(keys) > 1 || c.Pack {
		if err := sizeAllowed(meta.Size, 0, p.MaxPackSizeGiB); err != nil {
			return nil, err
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys), nil
}
