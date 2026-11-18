package charts

import (
	"regexp"
	"slices"
	"strings"
	"unicode"

	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
	"golang.org/x/text/unicode/norm"
)

var (
	featureCredit       = regexp.MustCompile(`(?i)\s+(?:[(\[]\s*)?\b(?:feat\.?|ft\.?|featuring)\s+([^\])]+)[\])]?$`)
	artistDivider       = regexp.MustCompile(`(?i)\s*(?:,|;)\s*|\s+(?:&|and|x|with|feat\.?|ft\.?|featuring)\s+`)
	singleVersionSuffix = regexp.MustCompile(`(?i)\s*(?:\(single version\)|\[single version\]|[-–—]\s+single version)\s*$`)
)

func normalize(value string) string {
	value = strings.Map(func(r rune) rune {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'' || r == '’':
			return -1
		case unicode.IsLetter(r), unicode.IsNumber(r):
			return unicode.ToLower(r)
		default:
			return ' '
		}
	}, norm.NFKD.String(value))
	return strings.Join(strings.Fields(value), " ")
}

// Keep compound artist names intact when they appear among multiple credits.
func artistNames(credit string, known []string) []string {
	parts := artistDivider.Split(credit, -1)
	var names []string
	for len(parts) > 0 {
		n := len(parts)
		name := normalize(strings.Join(parts[:n], " "))
		for n > 1 && !slices.Contains(known, name) {
			n--
			name = normalize(strings.Join(parts[:n], " "))
		}
		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
		parts = parts[n:]
	}
	return names
}

// Strip a feature suffix only when every artist named there is on the chart.
func splitFeaturedCredit(title string, known []string) (string, []string) {
	if match := featureCredit.FindStringSubmatchIndex(title); len(match) == 4 && match[2] >= 0 {
		names := artistNames(title[match[2]:match[3]], known)
		if len(names) > 0 && !slices.ContainsFunc(names, func(name string) bool { return !slices.Contains(known, name) }) {
			return strings.TrimSpace(title[:match[0]]), names
		}
	}
	return title, nil
}

func titleWithoutCredit(title string, artists []string) string {
	base, _ := splitFeaturedCredit(title, normalizeArtists(artists))
	return base
}

func normalizeArtists(artists []string) []string {
	known := make([]string, len(artists))
	for i, artist := range artists {
		known[i] = normalize(artist)
	}
	return known
}

// Return the number of corroborated artist credits. Some services provide only
// the lead artist, so prefer fuller credits without requiring them everywhere.
func trackMatch(track Track, title, artist string) int {
	known := normalizeArtists(track.Artists)
	if len(known) == 0 || known[0] == "" {
		return 0
	}
	chartTitle, _ := splitFeaturedCredit(track.Title, known)
	candidateTitle, featured := splitFeaturedCredit(title, known)
	if normalized := normalize(chartTitle); normalized == "" || normalized != normalize(candidateTitle) {
		return 0
	}
	names := artistNames(artist, known)
	if !slices.Contains(names, known[0]) || slices.ContainsFunc(names, func(name string) bool { return !slices.Contains(known, name) }) {
		return 0
	}
	for _, name := range featured {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return len(names)
}

func chooseRecording(track Track, candidates []qobuz.Recording) (qobuz.Recording, string) {
	var matches []qobuz.Recording
	best := 0
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.AlbumID == "" {
			continue
		}
		score := trackMatch(track, candidate.Title, candidate.Artist)
		if score == 0 || score < best {
			continue
		}
		if score > best {
			matches, best = matches[:0], score
		}
		matches = append(matches, candidate)
	}
	var chosen qobuz.Recording
	for _, candidate := range matches {
		if candidate.Streamable && chosen.ID == "" {
			chosen = candidate
		}
	}
	if chosen.ID == "" {
		if len(matches) > 0 {
			return chosen, "The matching Qobuz track is unavailable for this account"
		}
		return chosen, "No matching Qobuz track"
	}
	for i, candidate := range matches {
		if slices.ContainsFunc(matches[:i], func(other qobuz.Recording) bool {
			return other.ID != candidate.ID && (!equalISRC(other.ISRC, candidate.ISRC) || !compatibleDurations(other.Duration, candidate.Duration))
		}) {
			return qobuz.Recording{}, "Multiple Qobuz recordings match this title and artist"
		}
	}
	return chosen, ""
}

func chooseSong(track Track, recording qobuz.Recording, songs []navidrome.Song) (navidrome.Song, bool) {
	var matches []navidrome.Song
	best := 0
	verified := false
	for _, song := range songs {
		if song.ID == "" || (recording.ID != "" && !matchesRecording(recording, song)) {
			continue
		}
		score := trackMatch(track, song.Title, song.Artist)
		identified := sharesISRC([]string{recording.ISRC}, song.ISRC)
		if score == 0 || (verified && !identified) {
			continue
		}
		if identified && !verified {
			matches, best, verified = matches[:0], 0, true
		}
		if score < best {
			continue
		}
		if score > best {
			matches, best = matches[:0], score
		}
		matches = append(matches, song)
	}
	if ambiguousSongs(matches) {
		return navidrome.Song{}, true
	}
	var chosen navidrome.Song
	if len(matches) > 0 {
		chosen = matches[0]
	}

	// Prefer the single release when its recording matches either the catalog
	// result or an unambiguous library match for the chart title.
	ids, duration := chosen.ISRC, chosen.Duration
	if normalizeISRC(recording.ISRC) != "" {
		ids, duration = []string{recording.ISRC}, recording.Duration
	}
	if len(ids) == 0 {
		return chosen, false
	}
	var singles []navidrome.Song
	for _, song := range songs {
		title := singleVersionSuffix.ReplaceAllString(song.Title, "")
		if song.ID == "" || title == song.Title || !compatibleDurations(duration, song.Duration) || !sharesISRC(ids, song.ISRC) {
			continue
		}
		score := trackMatch(track, title, song.Artist)
		if score == 0 || score < best {
			continue
		}
		if score > best {
			singles, best = singles[:0], score
		}
		singles = append(singles, song)
	}
	if ambiguousSongs(singles) {
		return chosen, chosen.ID == ""
	}
	if len(singles) > 0 {
		return singles[0], false
	}
	return chosen, false
}

func ambiguousSongs(songs []navidrome.Song) bool {
	for i, song := range songs {
		if slices.ContainsFunc(songs[:i], func(other navidrome.Song) bool {
			return other.ID != song.ID && (!sharesISRC(other.ISRC, song.ISRC) || !compatibleDurations(other.Duration, song.Duration))
		}) {
			return true
		}
	}
	return false
}

func normalizeISRC(isrc string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.Join(strings.Fields(isrc), ""), "-", ""))
}

func equalISRC(a, b string) bool {
	code := normalizeISRC(a)
	return code != "" && code == normalizeISRC(b)
}

func sharesISRC(a, b []string) bool {
	return slices.ContainsFunc(a, func(isrc string) bool {
		return slices.ContainsFunc(b, func(other string) bool { return equalISRC(isrc, other) })
	})
}

func compatibleDurations(a, b int) bool {
	return a <= 0 || b <= 0 || (a-b <= 2 && b-a <= 2)
}

func matchesRecording(recording qobuz.Recording, song navidrome.Song) bool {
	if !compatibleDurations(recording.Duration, song.Duration) {
		return false
	}
	if normalizeISRC(recording.ISRC) != "" && slices.ContainsFunc(song.ISRC, func(isrc string) bool { return normalizeISRC(isrc) != "" }) {
		return slices.ContainsFunc(song.ISRC, func(isrc string) bool { return equalISRC(isrc, recording.ISRC) })
	}
	return recording.Album != "" && normalize(recording.Album) == normalize(song.Album) &&
		(recording.Number == 0 || song.Track == 0 || recording.Number == song.Track) &&
		(song.Disc == 0 || recording.Disc == song.Disc)
}
