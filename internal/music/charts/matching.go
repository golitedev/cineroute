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
	featureCredit = regexp.MustCompile(`(?i)\s+(?:[(\[]\s*)?\b(?:feat\.?|ft\.?|featuring)\s+([^\])]+)[\])]?$`)
	artistDivider = regexp.MustCompile(`(?i)\s*(?:,|;)\s*|\s+(?:&|and|x|with|feat\.?|ft\.?|featuring)\s+`)
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

// Match featured credits independently of the title while keeping qualifiers
// such as remix, live, clean, and sped up in the title comparison.
func titleWithoutCredit(title string, artists []string) string {
	if match := featureCredit.FindStringSubmatchIndex(title); len(match) == 4 && match[2] >= 0 {
		for _, artist := range artists {
			if strings.Contains(" "+normalize(title[match[2]:match[3]])+" ", " "+normalize(artist)+" ") {
				return strings.TrimSpace(title[:match[0]])
			}
		}
	}
	return title
}

func matchesTrack(track Track, title, artist string) bool {
	if normalize(titleWithoutCredit(track.Title, track.Artists)) != normalize(titleWithoutCredit(title, track.Artists)) {
		return false
	}
	lead := normalize(track.Artists[0])
	if normalize(artist) == lead {
		return true
	}
	for _, name := range artistDivider.Split(artist, -1) {
		if normalize(name) == lead {
			return true
		}
	}
	return false
}

func chooseRecording(track Track, candidates []qobuz.Recording) (qobuz.Recording, string) {
	var chosen qobuz.Recording
	found := false
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.AlbumID == "" || !matchesTrack(track, candidate.Title, candidate.Artist) {
			continue
		}
		found = true
		if !candidate.Streamable {
			continue
		}
		if chosen.ID == "" {
			chosen = candidate
		} else if candidate.ID != chosen.ID && (chosen.ISRC == "" || !strings.EqualFold(chosen.ISRC, candidate.ISRC)) {
			return qobuz.Recording{}, "Multiple Qobuz recordings match this title and artist"
		}
	}
	if chosen.ID == "" {
		if found {
			return chosen, "The matching Qobuz track is unavailable for this account"
		}
		return chosen, "No matching Qobuz track"
	}
	return chosen, ""
}

func chooseSong(track Track, recording qobuz.Recording, songs []navidrome.Song) (navidrome.Song, bool) {
	var chosen navidrome.Song
	for _, song := range songs {
		if song.ID == "" || !matchesTrack(track, song.Title, song.Artist) {
			continue
		}
		if recording.ID != "" && !matchesRecording(recording, song) {
			continue
		}
		if chosen.ID == "" {
			chosen = song
		} else if song.ID != chosen.ID && !slices.ContainsFunc(chosen.ISRC, func(isrc string) bool {
			return isrc != "" && slices.Contains(song.ISRC, isrc)
		}) {
			return navidrome.Song{}, true
		}
	}
	return chosen, false
}

func matchesRecording(recording qobuz.Recording, song navidrome.Song) bool {
	if recording.Duration > 0 && song.Duration > 0 && (song.Duration-recording.Duration > 2 || recording.Duration-song.Duration > 2) {
		return false
	}
	if recording.ISRC != "" && len(song.ISRC) > 0 {
		return slices.ContainsFunc(song.ISRC, func(isrc string) bool { return strings.EqualFold(isrc, recording.ISRC) })
	}
	return recording.Album != "" && normalize(recording.Album) == normalize(song.Album) &&
		(recording.Number == 0 || song.Track == 0 || recording.Number == song.Track) &&
		(song.Disc == 0 || recording.Disc == song.Disc)
}
