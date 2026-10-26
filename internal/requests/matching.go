package requests

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"cineroute/internal/tmdb"
)

var releaseMarker = regexp.MustCompile(`^(s[0-9]+(e[0-9]+)?|[0-9]+x[0-9]+|[0-9]{3,4}p|season|complete|series|web|webdl|webrip|bluray|blu|bd|br|bdrip|brrip|hdtv|remux|dvd|dvdrip|extended|directors|theatrical|unrated|repack|proper|imax|multi|dual|h264|x264|h265|x265|hevc|av1|mkv|mp4)$`)
var releaseYearBoundary = regexp.MustCompile(`^([0-9]{3,4}p|web|webrip|webdl|bluray|blu|bd|br|bdrip|brrip|hdtv|remux|dvd|dvdrip)$`)

// MatchTitle requires complete TMDB title prefixes. AKA may join known titles,
// but cannot introduce an unrelated title or conceal a sequel or remake.
// Tracker titles are text, not paths: Face/Off must retain both title words.
func MatchTitle(title string, media tmdb.Details, requireYear bool) bool {
	aliases := [][]string{}
	seen := map[string]bool{}
	for _, alias := range media.Aliases() {
		prefix := words(alias)
		key := strings.Join(prefix, " ")
		if len(prefix) > 0 && !seen[key] {
			aliases = append(aliases, prefix)
			seen[key] = true
		}
	}
	return matchKnownTitle(words(title), aliases, media, requireYear, false, 0, map[titleMatchState]bool{})
}

type titleMatchState struct {
	remaining, depth int
	haveYear         bool
}

func matchKnownTitle(raw []string, aliases [][]string, media tmdb.Details, requireYear, haveYear bool, depth int, memo map[titleMatchState]bool) bool {
	// Bound work for unusual tracker titles with many repeated AKA clauses.
	if depth >= 8 {
		return false
	}
	state := titleMatchState{remaining: len(raw), depth: depth, haveYear: haveYear}
	if matched, found := memo[state]; found {
		return matched
	}
	memo[state] = false
	for _, prefix := range aliases {
		if len(raw) >= len(prefix) && slices.Equal(raw[:len(prefix)], prefix) && matchReleaseSuffix(raw[len(prefix):], aliases, media, requireYear, haveYear, depth, memo) {
			memo[state] = true
			return true
		}
	}
	return false
}

func matchReleaseSuffix(rest []string, aliases [][]string, media tmdb.Details, requireYear, haveYear bool, depth int, memo map[titleMatchState]bool) bool {
	for len(rest) > 0 {
		year := titleYear(rest[0])
		if year == 0 {
			break
		}
		if !matchesReleaseYear(year, media, requireYear) {
			return false
		}
		haveYear = true
		rest = rest[1:]
	}
	if len(rest) == 0 {
		return haveYear || !requireYear || media.MediaType == "tv"
	}
	aka := 0
	if rest[0] == "aka" {
		aka = 1
	} else if len(rest) >= 3 && slices.Equal(rest[:3], []string{"a", "k", "a"}) {
		aka = 3
	}
	if aka > 0 {
		return matchKnownTitle(rest[aka:], aliases, media, requireYear, haveYear, depth+1, memo)
	}
	if !releaseMarker.MatchString(rest[0]) {
		return false
	}
	if media.MediaType == "tv" {
		return true
	}
	// Preserve year checks for movie edition labels such as EXTENDED 2024.
	for _, word := range rest {
		if releaseYearBoundary.MatchString(word) {
			break
		}
		if year := titleYear(word); year > 0 {
			if !matchesReleaseYear(year, media, requireYear) {
				return false
			}
			haveYear = true
		}
	}
	return haveYear || !requireYear
}

func titleYear(word string) int {
	year, _ := strconv.Atoi(word)
	if year >= 1800 && year <= 2200 {
		return year
	}
	return 0
}

func matchesReleaseYear(year int, media tmdb.Details, requireYear bool) bool {
	if media.Year() == 0 {
		return false
	}
	if year == media.Year() {
		return true
	}
	// Movies can have adjacent release years in different countries. An explicit
	// TV title year identifies the series, not a later season's air date.
	return media.MediaType == "movie" && !requireYear && (year == media.Year()-1 || year == media.Year()+1)
}
