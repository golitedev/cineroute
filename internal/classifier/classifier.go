// Package classifier detects whether a torrent is a movie or a TV show and
// derives a search title, year and season from the torrent name and file list.
package classifier

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Result struct {
	MediaType  string // "movie" | "tv"
	Title      string
	AltTitle   string // title including a trailing year token ("Blade Runner 2049")
	Year       int
	Season     int
	Confidence string // "high" | "medium" | "low"
}

var (
	reEpisode    = regexp.MustCompile(`(?i)\bS(\d{1,3})E(\d{1,4})\b`)
	reSeason     = regexp.MustCompile(`(?i)\bS(\d{1,3})\b`)
	reXEpisode   = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{1,3})\b`)
	reSeasonWord = regexp.MustCompile(`(?i)\bSeason[\s._-]*(\d{1,3})\b`)
	reYear       = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reResolution = regexp.MustCompile(`(?i)^\d{3,4}p$`)
	reSceneNoise = regexp.MustCompile(`(?i)^(h\.?26[45]|x26[45]|ddp?5?\.?1?|dts[-a-z]*|ac3|aac|flac|10bit|8bit)$`)
)

// stopTokens mark the boundary between title and release information.
var stopTokens = map[string]bool{
	"s0": true, "s1": true, "s2": true, "s3": true, "s4": true, "s5": true,
	"season": true, "episode": true, "series": true, "miniseries": true,
	"complete": true, "part": true,
}

// noiseTokens are release-metadata tokens removed from the search title.
var noiseTokens = map[string]bool{
	"2160p": true, "1080p": true, "720p": true, "480p": true, "4k": true, "8k": true,
	"uhd": true, "hdr": true, "hdr10": true, "dv": true, "dolby": true, "vision": true,
	"atmos": true, "truehd": true, "dts": true, "dtshd": true, "dtsx": true,
	"ddp": true, "dd": true, "5.1": true, "7.1": true, "2.0": true,
	"ac3": true, "aac": true, "flac": true,
	"web-dl": true, "webdl": true, "webrip": true, "hdtv": true,
	"bluray": true, "blu-ray": true, "brrip": true, "bdrip": true, "remux": true,
	"h264": true, "x264": true, "h265": true, "x265": true, "hevc": true, "avc": true,
	"av1": true, "10bit": true, "8bit": true,
	"repack": true, "proper": true, "imax": true, "extended": true, "internal": true,
	"subbed": true, "dubbed": true, "multi": true, "dual": true,
	"eng": true, "english": true, "ger": true, "german": true, "fre": true, "french": true,
	"spa": true, "spanish": true, "ita": true, "italian": true, "jpn": true, "japanese": true,
	"kor": true, "korean": true, "chi": true, "chinese": true, "theatrical": true,
	"unrated": true, "directors": true, "cut": true, "web": true,
	"1080": true, "2160": true,
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".ts": true, ".m2ts": true,
	".mov": true, ".webm": true, ".wmv": true, ".flv": true, ".m4v": true,
}

// Classify derives the media type and search terms from the torrent name and
// its file list (relative paths).
func Classify(name string, relPaths []string) Result {
	res := Result{}
	base := strings.TrimSuffix(name, ".torrent")
	base = strings.TrimSpace(base)
	if ext := filepath.Ext(base); ext != "" && videoExts[strings.ToLower(ext)] {
		base = strings.TrimSuffix(base, ext)
	}
	lower := strings.ToLower(base)

	season := 0
	var seasonTok string
	if m := reEpisode.FindStringSubmatch(lower); m != nil {
		season, _ = strconv.Atoi(m[1])
		seasonTok = m[0]
	} else if m := reSeason.FindStringSubmatch(lower); m != nil {
		season, _ = strconv.Atoi(m[1])
		seasonTok = m[0]
	} else if m := reXEpisode.FindStringSubmatch(lower); m != nil {
		season, _ = strconv.Atoi(m[1])
		seasonTok = m[0]
	} else if m := reSeasonWord.FindStringSubmatch(lower); m != nil {
		season, _ = strconv.Atoi(m[1])
		seasonTok = m[0]
	}

	spaced := strings.NewReplacer(".", " ", "_", " ", "-", " ").Replace(lower)
	series := strings.Contains(spaced, "complete series") ||
		strings.Contains(spaced, "complete season") ||
		strings.Contains(spaced, "miniseries") ||
		strings.Contains(spaced, "mini series") ||
		strings.Contains(spaced, "collection")

	isTV := season > 0 || series

	title, altTitle, year := extractTitleAndYear(base, seasonTok)
	res.Title = title
	res.AltTitle = altTitle
	res.Year = year
	if res.Title == "" {
		res.Title = base
	}

	if isTV {
		res.MediaType = "tv"
		res.Season = season
	} else {
		res.MediaType = "movie"
	}

	// File-based signals: multiple video files with episode numbers indicate TV.
	if !isTV && len(relPaths) > 1 {
		vids := 0
		eps := 0
		for _, p := range relPaths {
			pl := strings.ToLower(p)
			if videoExts[filepath.Ext(pl)] {
				vids++
				if reEpisode.MatchString(pl) || reXEpisode.MatchString(pl) {
					eps++
				}
			}
		}
		if vids >= 2 && eps >= 2 {
			res.MediaType = "tv"
			isTV = true
		} else if vids == 1 {
			res.MediaType = "movie"
		}
	}

	switch {
	case res.MediaType == "tv" && (season > 0 || series):
		res.Confidence = "high"
	case res.MediaType == "movie" && res.Year > 0:
		res.Confidence = "high"
	case res.MediaType == "movie" && len(relPaths) == 1:
		res.Confidence = "medium"
	default:
		res.Confidence = "low"
	}

	return res
}

// extractTitleAndYear splits the release name into tokens and derives the
// search title, an alternate title (title + trailing year token, for movies
// like "Blade Runner 2049") and the release year.
//
// The release year is the LAST year-like token immediately followed by a
// stop/noise token or the end of the name. Year-like tokens before it are
// part of the title: "2012.2009.1080p" → title "2012", year 2009;
// "2001.A.Space.Odyssey.1968.2160p" → title "2001 A Space Odyssey", year 1968.
func extractTitleAndYear(name, seasonTok string) (title, altTitle string, year int) {
	tokens := []string{}
	for _, t := range splitTokens(name) {
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	yearIdx := releaseYearIndex(tokens)
	if yearIdx >= 0 {
		year, _ = strconv.Atoi(tokens[yearIdx])
	}

	end := len(tokens)
	for i, t := range tokens {
		if seasonTok != "" && strings.EqualFold(t, seasonTok) {
			end = i
			break
		}
		if i == yearIdx {
			end = i
			break
		}
		lt := strings.ToLower(t)
		if isStopToken(lt) {
			end = i
			break
		}
		// Year-like tokens other than the chosen release year belong to the
		// title; other noise ends it.
		if isNoiseToken(lt) && !reYear.MatchString(t) {
			end = i
			break
		}
	}
	if end > 0 {
		title = strings.Join(tokens[:end], " ")
	}
	// When the chosen year directly follows the title, the year may actually
	// be part of the title ("Blade Runner 2049"): offer it as an alternative.
	if title != "" && yearIdx == end && yearIdx < len(tokens) {
		altTitle = title + " " + tokens[yearIdx]
	}
	return title, altTitle, year
}

// releaseYearIndex picks the most plausible release-year token: the last
// year-like token followed by a stop/noise token or the end of the name.
// Falls back to the first year-like token.
func releaseYearIndex(tokens []string) int {
	chosen := -1
	first := -1
	for i, t := range tokens {
		if !reYear.MatchString(t) {
			continue
		}
		if first < 0 {
			first = i
		}
		if i+1 >= len(tokens) {
			chosen = i
			continue
		}
		nt := strings.ToLower(tokens[i+1])
		if isStopToken(nt) || isNoiseToken(nt) {
			chosen = i
		}
	}
	if chosen < 0 {
		chosen = first
	}
	return chosen
}

func splitTokens(name string) []string {
	re := regexp.MustCompile(`[._\-\[\]()/ ]+`)
	return re.Split(name, -1)
}

func isStopToken(lt string) bool {
	if stopTokens[lt] {
		return true
	}
	if reSeason.MatchString(lt) || reEpisode.MatchString(lt) || reXEpisode.MatchString(lt) {
		return true
	}
	if reResolution.MatchString(lt) {
		return true
	}
	if reSeasonWord.MatchString(lt) {
		return true
	}
	return false
}

func isNoiseToken(lt string) bool {
	if noiseTokens[lt] {
		return true
	}
	if reSceneNoise.MatchString(lt) {
		return true
	}
	if reYear.MatchString(lt) {
		return true
	}
	return false
}
