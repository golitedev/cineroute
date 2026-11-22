package charts

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
	"golang.org/x/text/unicode/norm"
)

const releaseLabel = `(?:(?:single|original|album|lp)\s+version|bonus\s+track)`
const soundtrackLabel = `from\s+[^()\[\]]+:\s*(?:original score|original soundtrack|the album)`
const ambiguousCatalogRecording = "Multiple Qobuz recordings match this title and artist"
const conflictingCandidate = "conflicting eligible recording"

var (
	featureCredit        = regexp.MustCompile(`(?i)\s+(?:[(\[]\s*)?\b(?:feat\.?|ft\.?|featuring)\s+([^\])]+)[\])]?$`)
	artistDivider        = regexp.MustCompile(`(?i)\s*(?:,|;)\s*|\s+(?:&|and|x|with|feat\.?|ft\.?|featuring)\s+`)
	releaseVersionSuffix = regexp.MustCompile(`(?i)\s*(?:\(` + releaseLabel + `\)|\[` + releaseLabel + `\]|[-–—]\s+` + releaseLabel + `)\s*$`)
	soundtrackSuffix     = regexp.MustCompile(`(?i)\s*(?:\(` + soundtrackLabel + `\)|\[` + soundtrackLabel + `\])\s*$`)
	acousticLabel        = regexp.MustCompile(`\bacoustic(?: version| sessions?)?$`)
	remixSuffix          = regexp.MustCompile(`(?i)(?:(?:\s+|\s*[-–—]\s*)(?:rmx|remix)|\s*\(\s*(?:rmx|remix)\s*\)|\s*\[\s*(?:rmx|remix)\s*\])+\s*$`)
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

func canonicalRemixTitle(title string) string {
	match := remixSuffix.FindStringIndex(title)
	if match == nil || match[0] == 0 {
		return title
	}
	return strings.TrimSpace(title[:match[0]]) + " (Remix)"
}

func trackSearchTitle(track Track) string {
	base, _ := splitSoundtrackLabel(track.Title)
	title := titleWithoutCredit(base, track.Artists)
	remix := canonicalRemixTitle(title)
	if normalize(remix) != normalize(title) {
		return remix
	}
	return title
}

func splitSoundtrackLabel(title string) (string, string) {
	if suffix := soundtrackSuffix.FindStringIndex(title); suffix != nil && suffix[0] > 0 {
		return strings.TrimSpace(title[:suffix[0]]), normalize(title[suffix[0]:])
	}
	return title, ""
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
	return trackMatchWithContributors(track, title, artist, nil)
}

func trackMatchWithContributors(track Track, title, artist string, contributors []navidrome.Contributor) int {
	known := normalizeArtists(track.Artists)
	if len(known) == 0 || known[0] == "" {
		return 0
	}
	chartTitle, chartSoundtrack := splitSoundtrackLabel(track.Title)
	candidateTitle, candidateSoundtrack := splitSoundtrackLabel(title)
	if chartSoundtrack != "" && candidateSoundtrack != "" && chartSoundtrack != candidateSoundtrack {
		return 0
	}
	chartTitle, _ = splitFeaturedCredit(chartTitle, known)
	candidateTitle, featured := splitFeaturedCredit(candidateTitle, known)
	if normalized := normalize(canonicalRemixTitle(chartTitle)); normalized == "" || normalized != normalize(canonicalRemixTitle(candidateTitle)) {
		return 0
	}
	var producers []string
	for _, contributor := range contributors {
		if strings.EqualFold(contributor.Role, "producer") {
			producers = append(producers, artistKey(contributor.Artist.Name))
		}
	}
	names := artistNames(artist, append(slices.Clone(known), producers...))
	if !slices.Contains(names, known[0]) {
		return 0
	}
	for _, name := range featured {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	score := 0
	for _, name := range names {
		if slices.Contains(known, name) {
			score++
		} else if !slices.Contains(producers, name) {
			return 0
		}
	}
	// Providers sometimes include a producer in the artist field. Accept that
	// extra credit only when the library separately identifies it and also
	// credits all chart artists.
	if score != len(names) && slices.ContainsFunc(known, func(name string) bool { return !slices.Contains(names, name) }) {
		return 0
	}
	return score
}

func acousticConflict(track Track, title, album string) bool {
	chartTitle, _ := splitSoundtrackLabel(track.Title)
	title, _ = splitSoundtrackLabel(title)
	album, _ = splitSoundtrackLabel(album)
	return !acousticLabel.MatchString(normalize(titleWithoutCredit(chartTitle, track.Artists))) &&
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

func matchesLead(track Track, title, artist string) bool {
	known := normalizeArtists(track.Artists)
	names := artistNames(artist, known)
	if len(known) == 0 || len(names) == 0 || names[0] != known[0] {
		return false
	}
	return catalogMatch(track, qobuz.Recording{Title: title, Artist: track.Artists[0]}) != 0
}

func (identity trackIdentity) matchesDuration(duration int) bool {
	return identity.Duration > 0 && duration > 0 && compatibleDurations(identity.Duration, duration)
}

func (identity trackIdentity) matchesAlbum(album string) bool {
	return identity.Album != "" && normalize(identity.Album) == normalize(album)
}

func (identity trackIdentity) matchesPosition(album string, number, duration int) bool {
	return identity.matchesAlbum(album) && identity.Number > 0 && identity.Number == number && identity.matchesDuration(duration)
}

func catalogScore(track Track, recording qobuz.Recording, identity trackIdentity) int {
	if score := catalogMatch(track, recording); score != 0 {
		return score
	}
	// Services can add orchestra or other performer credits to the same recording.
	// Require a compatible duration plus a verified ISRC or the album track position.
	if (identity.matchesPosition(recording.Album, recording.Number, recording.Duration) ||
		(equalISRC(recording.ISRC, identity.ISRC) && identity.matchesDuration(recording.Duration))) &&
		!acousticConflict(track, recording.Title, recording.Album) && matchesLead(track, recording.Title, recording.Artist) {
		return 1
	}
	return 0
}

func chooseRecording(track Track, candidates []qobuz.Recording) (qobuz.Recording, string) {
	return chooseRecordingWithISRC(track, candidates, trackIdentity{})
}

func chooseRecordingWithISRC(track Track, candidates []qobuz.Recording, identity trackIdentity) (qobuz.Recording, string) {
	recording, reason, _ := evaluateRecordings(track, candidates, identity)
	return recording, reason
}

type catalogCandidate struct {
	TrackID    string `json:"track_id"`
	AlbumID    string `json:"album_id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	ISRC       string `json:"isrc"`
	Duration   int    `json:"duration_seconds"`
	Number     int    `json:"track_number,omitempty"`
	Streamable bool   `json:"streamable"`
	Score      int    `json:"score"`
	Decision   string `json:"decision"`
	Navidrome  bool   `json:"navidrome,omitempty"`
}

type catalogEvaluation struct {
	AlbumFilter string             `json:"album_filter,omitempty"`
	Candidates  []catalogCandidate `json:"candidates"`
	Conflict    string             `json:"conflict,omitempty"`
}

func evaluateRecordings(track Track, candidates []qobuz.Recording, identity trackIdentity) (qobuz.Recording, string, catalogEvaluation) {
	album := normalize(identity.Album)
	if album != "" && (identity.ISRC != "" || identity.Duration > 0) {
		// Prefer the chart album when a recording ID or duration corroborates it.
		recording, reason, evaluation := evaluateRecordingsOnAlbum(track, candidates, identity, album)
		if recording.ID != "" || reason == ambiguousCatalogRecording {
			return recording, reason, evaluation
		}
	}
	recording, reason, evaluation := evaluateRecordingsOnAlbum(track, candidates, identity, "")
	if identity.ISRC != "" {
		switch {
		case reason == "No matching Qobuz track":
			reason = "No library or Qobuz match for ISRC " + identity.ISRC
		case reason == ambiguousCatalogRecording && album == "":
			reason = "Qobuz editions with this ISRC differ in duration. Use Qobuz link to select a release."
		}
	}
	return recording, reason, evaluation
}

func evaluateRecordingsOnAlbum(track Track, candidates []qobuz.Recording, identity trackIdentity, album string) (qobuz.Recording, string, catalogEvaluation) {
	// Qobuz can list the same acoustic recording with and without its version.
	acousticCodes := make(map[string]bool)
	for _, candidate := range candidates {
		if code := normalizeISRC(candidate.ISRC); code != "" && acousticConflict(track, candidate.Title, candidate.Album) {
			acousticCodes[code] = true
		}
	}
	evaluation := catalogEvaluation{AlbumFilter: album, Candidates: make([]catalogCandidate, len(candidates))}
	best := 0
	for i, candidate := range candidates {
		d := &evaluation.Candidates[i]
		*d = catalogCandidate{TrackID: candidate.ID, AlbumID: candidate.AlbumID, Title: candidate.Title,
			Artist: candidate.Artist, Album: candidate.Album, ISRC: candidate.ISRC, Duration: candidate.Duration,
			Number: candidate.Number, Streamable: candidate.Streamable}
		switch {
		case candidate.ID == "" || candidate.AlbumID == "":
			d.Decision = "missing Qobuz track or album ID"
		case identity.ISRC != "" && !equalISRC(candidate.ISRC, identity.ISRC):
			d.Decision = "does not match verified ISRC"
		case acousticCodes[normalizeISRC(candidate.ISRC)] || acousticConflict(track, candidate.Title, candidate.Album):
			d.Decision = "acoustic recording does not match the chart version"
		case !compatibleDurations(identity.Duration, candidate.Duration):
			d.Decision = "duration differs from chart recording"
		case identity.ISRC == "" && identity.matchesAlbum(candidate.Album) && identity.Number > 0 &&
			candidate.Number > 0 && candidate.Number != identity.Number:
			d.Decision = "different track position on the chart album"
		case album != "" && (!identity.matchesAlbum(candidate.Album) ||
			(identity.ISRC == "" && !identity.matchesDuration(candidate.Duration))):
			d.Decision = "does not match chart album and recording evidence"
		default:
			d.Score = catalogScore(track, candidate, identity)
			if d.Score == 0 {
				d.Decision = "title or artist credits do not match"
			}
			best = max(best, d.Score)
		}
	}
	var available []int
	for i := range evaluation.Candidates {
		d := &evaluation.Candidates[i]
		if d.Decision != "" {
			continue
		}
		switch {
		case d.Score < best:
			d.Decision = "fewer matching artist credits"
		case !d.Streamable:
			d.Decision = "not streamable on this account"
		default:
			d.Decision = "eligible"
			available = append(available, i)
		}
	}
	if best == 0 {
		return qobuz.Recording{}, "No matching Qobuz track", evaluation
	}
	if len(available) == 0 {
		return qobuz.Recording{}, "The matching Qobuz track is unavailable for this account", evaluation
	}
	// Unavailable listings cannot create ambiguity among the eligible releases.
	for i, index := range available {
		candidate := candidates[index]
		for _, previous := range available[:i] {
			other := candidates[previous]
			if candidate.ID == other.ID {
				continue
			}
			if !equalISRC(other.ISRC, candidate.ISRC) {
				evaluation.Conflict = fmt.Sprintf("tracks %s and %s have different or missing ISRCs", other.ID, candidate.ID)
			} else if !compatibleDurations(other.Duration, candidate.Duration) {
				evaluation.Conflict = fmt.Sprintf("tracks %s and %s differ in duration by more than two seconds", other.ID, candidate.ID)
			} else {
				continue
			}
			evaluation.Candidates[previous].Decision = conflictingCandidate
			evaluation.Candidates[index].Decision = conflictingCandidate
			return qobuz.Recording{}, ambiguousCatalogRecording, evaluation
		}
	}
	winner := available[0]
	evaluation.Candidates[winner].Decision = "selected"
	return candidates[winner], "", evaluation
}

func (evaluation catalogEvaluation) reviewCandidates(track Track) []catalogCandidate {
	var choices []catalogCandidate
	for _, candidate := range evaluation.Candidates {
		if candidate.TrackID != "" && (candidate.Score > 0 || matchesLead(track, candidate.Title, candidate.Artist)) {
			choices = append(choices, candidate)
		}
	}
	slices.SortStableFunc(choices, func(a, b catalogCandidate) int {
		switch {
		case a.Decision == conflictingCandidate && b.Decision != conflictingCandidate:
			return -1
		case b.Decision == conflictingCandidate && a.Decision != conflictingCandidate:
			return 1
		default:
			return b.Score - a.Score
		}
	})
	return choices[:min(len(choices), 8)]
}

func libraryReviewCandidates(track Track, songs []navidrome.Song) []catalogCandidate {
	var candidates []catalogCandidate
	for _, song := range songs {
		if song.ID != "" && (matchesLead(track, song.Title, song.Artist) ||
			trackMatchWithContributors(track, song.Title, song.Artist, song.Contributors) > 0) {
			candidates = append(candidates, catalogCandidate{TrackID: song.ID, Title: song.Title,
				Artist: song.Artist, Album: song.Album, Duration: song.Duration, ISRC: strings.Join(song.ISRC, ", "), Navidrome: true})
		}
	}
	return candidates[:min(len(candidates), 8)]
}

func chooseSongWithIdentity(track Track, recording qobuz.Recording, songs []navidrome.Song, identity trackIdentity) (navidrome.Song, bool) {
	if identity.ISRC == "" && identity.Duration == 0 {
		return chooseSong(track, recording, songs)
	}
	songs = slices.DeleteFunc(slices.Clone(songs), func(song navidrome.Song) bool {
		if !compatibleDurations(identity.Duration, song.Duration) {
			return true
		}
		if identity.ISRC == "" {
			return identity.matchesAlbum(song.Album) && identity.Number > 0 && song.Track > 0 && song.Track != identity.Number
		}
		return !sharesISRC([]string{identity.ISRC}, song.ISRC) &&
			(len(song.ISRC) != 0 || !identity.matchesPosition(song.Album, song.Track, song.Duration))
	})
	if identity.Album != "" {
		onAlbum := slices.DeleteFunc(slices.Clone(songs), func(song navidrome.Song) bool {
			return !identity.matchesAlbum(song.Album) || (identity.ISRC == "" && !identity.matchesDuration(song.Duration))
		})
		if song, ambiguous := chooseSongWithEvidence(track, recording, onAlbum, identity); song.ID != "" || ambiguous {
			return song, ambiguous
		}
	}
	return chooseSongWithEvidence(track, recording, songs, identity)
}

func chooseSong(track Track, recording qobuz.Recording, songs []navidrome.Song) (navidrome.Song, bool) {
	return chooseSongWithEvidence(track, recording, songs, trackIdentity{})
}

func chooseSongWithEvidence(track Track, recording qobuz.Recording, songs []navidrome.Song, identity trackIdentity) (navidrome.Song, bool) {
	var matches []navidrome.Song
	best := 0
	verified := false
	for _, song := range songs {
		if song.ID == "" || acousticConflict(track, song.Title, song.Album) || (recording.ID != "" && !matchesRecording(recording, song)) {
			continue
		}
		score := trackMatchWithContributors(track, song.Title, song.Artist, song.Contributors)
		identified := sharesISRC([]string{recording.ISRC}, song.ISRC)
		if score == 0 && (identity.matchesPosition(song.Album, song.Track, song.Duration) ||
			(identity.matchesDuration(song.Duration) && sharesISRC([]string{identity.ISRC}, song.ISRC))) &&
			matchesLead(track, song.Title, song.Artist) {
			score = 1
		}
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
		score := trackMatchWithContributors(baseTrack, title, song.Artist, song.Contributors)
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
