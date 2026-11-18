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

const releaseLabel = `(?:(?:single|original|album|lp)\s+version|bonus\s+track)`

var (
	featureCredit        = regexp.MustCompile(`(?i)\s+(?:[(\[]\s*)?\b(?:feat\.?|ft\.?|featuring)\s+([^\])]+)[\])]?$`)
	artistDivider        = regexp.MustCompile(`(?i)\s*(?:,|;)\s*|\s+(?:&|and|x|with|feat\.?|ft\.?|featuring)\s+`)
	releaseVersionSuffix = regexp.MustCompile(`(?i)\s*(?:\(` + releaseLabel + `\)|\[` + releaseLabel + `\]|[-–—]\s+` + releaseLabel + `)\s*$`)
	acousticLabel        = regexp.MustCompile(`\bacoustic(?: version| sessions?)?$`)
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

func artistKey(name string) string {
	return strings.TrimPrefix(normalize(name), "the ")
}

// Keep compound artist names intact when they appear among multiple credits.
func artistNames(credit string, known []string) []string {
	parts := artistDivider.Split(credit, -1)
	var names []string
	for len(parts) > 0 {
		n := len(parts)
		name := artistKey(strings.Join(parts[:n], " "))
		for n > 1 && !slices.Contains(known, name) {
			n--
			name = artistKey(strings.Join(parts[:n], " "))
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

func splitReleaseLabel(title string, artists []string) (string, string) {
	trimmed := strings.TrimSpace(title)
	base := titleWithoutCredit(trimmed, artists)
	match := releaseVersionSuffix.FindStringIndex(base)
	if match == nil {
		return title, ""
	}
	return strings.TrimSpace(base[:match[0]]) + trimmed[len(base):], normalize(base[match[0]:])
}

func normalizeArtists(artists []string) []string {
	known := make([]string, len(artists))
	for i, artist := range artists {
		known[i] = artistKey(artist)
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

func acousticConflict(track Track, title, album string) bool {
	return !acousticLabel.MatchString(normalize(titleWithoutCredit(track.Title, track.Artists))) &&
		(acousticLabel.MatchString(normalize(titleWithoutCredit(title, track.Artists))) || acousticLabel.MatchString(normalize(album)))
}

func catalogMatch(track Track, recording qobuz.Recording) int {
	if acousticConflict(track, recording.Title, recording.Album) {
		return 0
	}
	if score := trackMatch(track, recording.Title, recording.Artist); score != 0 {
		return score
	}
	title, _ := splitReleaseLabel(recording.Title, track.Artists)
	track.Title, _ = splitReleaseLabel(track.Title, track.Artists)
	return trackMatch(track, title, recording.Artist)
}

func chooseRecording(track Track, candidates []qobuz.Recording) (qobuz.Recording, string) {
	// Qobuz can list the same acoustic recording with and without its version.
	acousticCodes := make(map[string]bool)
	for _, candidate := range candidates {
		if code := normalizeISRC(candidate.ISRC); code != "" && acousticConflict(track, candidate.Title, candidate.Album) {
			acousticCodes[code] = true
		}
	}
	var matches []qobuz.Recording
	best := 0
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.AlbumID == "" || acousticCodes[normalizeISRC(candidate.ISRC)] {
			continue
		}
		score := catalogMatch(track, candidate)
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
		if song.ID == "" || acousticConflict(track, song.Title, song.Album) || (recording.ID != "" && !matchesRecording(recording, song)) {
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

	// Compare release labels only after identifying the recording from the
	// catalog or an unambiguous library match for the chart title.
	ids, duration := chosen.ISRC, chosen.Duration
	if normalizeISRC(recording.ISRC) != "" {
		ids = []string{recording.ISRC}
		if recording.Duration > 0 {
			duration = recording.Duration
		}
	}
	if len(ids) == 0 || duration <= 0 {
		return chosen, false
	}
	baseTrack := track
	baseTrack.Title, _ = splitReleaseLabel(track.Title, track.Artists)
	if chosen.ID != "" && baseTrack.Title != track.Title {
		return chosen, false
	}
	var versions []navidrome.Song
	for _, song := range songs {
		if song.ID == "" || song.Duration <= 0 || !compatibleDurations(duration, song.Duration) || !sharesISRC(ids, song.ISRC) {
			continue
		}
		title, label := splitReleaseLabel(song.Title, track.Artists)
		if acousticConflict(track, title, song.Album) {
			continue
		}
		if label == "" && baseTrack.Title == track.Title {
			continue
		}
		if chosen.ID != "" && label != "single version" && label != "original version" {
			continue
		}
		score := trackMatch(baseTrack, title, song.Artist)
		if score == 0 || score < best {
			continue
		}
		if score > best {
			versions, best = versions[:0], score
		}
		versions = append(versions, song)
	}
	if ambiguousSongs(versions) {
		return chosen, chosen.ID == ""
	}
	if len(versions) > 0 {
		return versions[0], false
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
