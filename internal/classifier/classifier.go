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

	if m := reYear.FindString(base); m != "" {
		res.Year, _ = strconv.Atoi(m)
	}

	res.Title = extractTitle(base, seasonTok)
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

// extractTitle splits the release name into tokens and takes everything up to
// the first season marker or release-metadata token.
func extractTitle(name, seasonTok string) string {
	tokens := splitTokens(name)
	title := []string{}
	for _, t := range tokens {
		if t == "" {
			continue
		}
		if seasonTok != "" && strings.EqualFold(t, seasonTok) {
			break
		}
		lt := strings.ToLower(t)
		if isStopToken(lt) || isNoiseToken(lt) {
			break
		}
		title = append(title, t)
	}
	if len(title) == 0 {
		return ""
	}
	return strings.Join(title, " ")
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
