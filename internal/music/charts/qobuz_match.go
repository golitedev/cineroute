package charts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

var (
	ErrQobuzLookup = errors.New("Could not load tracks from Qobuz. Check the link or your Qobuz account configuration")
	qobuzAlbumID   = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
	qobuzTrackID   = regexp.MustCompile(`^[0-9]{1,20}$`)
	qobuzLocale    = regexp.MustCompile(`^[a-z]{2}-[a-z]{2}$`)
)

type qobuzRecordings interface {
	Album(context.Context, string) (qobuz.Album, error)
	TrackRecording(context.Context, string) (qobuz.Recording, error)
}

type QobuzChoice struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Disc     int    `json:"disc"`
	Number   int    `json:"number"`
	Duration int    `json:"duration"`
}

func qobuzChoice(r qobuz.Recording) QobuzChoice {
	return QobuzChoice{ID: r.ID, Title: r.Title, Artist: r.Artist, Album: r.Album, Disc: r.Disc, Number: r.Number, Duration: r.Duration}
}

func parseQobuzReference(reference string) (id string, isTrack bool, err error) {
	reference = strings.TrimSpace(reference)
	if qobuzAlbumID.MatchString(reference) {
		return reference, false, nil
	}
	invalid := fmt.Errorf("%w: paste a Qobuz album link, album ID, or track link", ErrSettings)
	u, err := url.Parse(reference)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" {
		return "", false, invalid
	}
	switch strings.ToLower(u.Hostname()) {
	case "qobuz.com", "www.qobuz.com", "play.qobuz.com", "open.qobuz.com":
	default:
		return "", false, invalid
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) > 1 && qobuzLocale.MatchString(parts[0]) {
		parts = parts[1:]
	}
	switch {
	case (len(parts) == 2 || len(parts) == 3) && parts[0] == "album" && qobuzAlbumID.MatchString(parts[len(parts)-1]):
		return parts[len(parts)-1], false, nil
	case len(parts) == 2 && parts[0] == "track" && qobuzTrackID.MatchString(parts[1]):
		return parts[1], true, nil
	default:
		return "", false, invalid
	}
}

func (m *Manager) qobuzChoices(ctx context.Context, playlistID, spotifyID, reference string) ([]qobuz.Recording, error) {
	id, isTrack, err := parseQobuzReference(reference)
	if err != nil {
		return nil, err
	}
	p, err := m.playlist(ctx, playlistID)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(p.Entries, func(e entry) bool { return e.SpotifyID == spotifyID }) {
		return nil, ErrNotFound
	}
	catalog, ok := m.catalog.(qobuzRecordings)
	if !ok {
		return nil, ErrQobuzLookup
	}
	if isTrack {
		recording, err := catalog.TrackRecording(ctx, id)
		if err != nil {
			return nil, qobuzLookupError(err)
		}
		return []qobuz.Recording{recording}, nil
	}
	album, err := catalog.Album(ctx, id)
	if err != nil {
		return nil, qobuzLookupError(err)
	}
	recordings := make([]qobuz.Recording, 0, len(album.Tracks))
	for _, track := range album.Tracks {
		if !qobuzTrackID.MatchString(track.ID) || track.Title == "" {
			continue
		}
		artist := track.Artist
		if artist == "" {
			artist = album.Artist
		}
		recordings = append(recordings, qobuz.Recording{ID: track.ID, AlbumID: album.ID, Title: track.Title, Album: album.FullTitle(),
			Artist: artist, ISRC: track.ISRC, Disc: track.Disc, Number: track.Number, Duration: track.DurationSec, Streamable: track.Streamable})
	}
	if len(recordings) == 0 {
		return nil, ErrQobuzLookup
	}
	return recordings, nil
}

func qobuzLookupError(err error) error {
	var response *qobuz.APIError
	if errors.As(err, &response) && response.Status == http.StatusNotFound {
		return fmt.Errorf("%w: Qobuz could not find that album or track", ErrSettings)
	}
	return ErrQobuzLookup
}

func (m *Manager) QobuzChoices(ctx context.Context, playlistID, spotifyID, reference string) ([]QobuzChoice, error) {
	recordings, err := m.qobuzChoices(ctx, playlistID, spotifyID, reference)
	if err != nil {
		return nil, err
	}
	choices := make([]QobuzChoice, 0, len(recordings))
	for _, recording := range recordings {
		choices = append(choices, qobuzChoice(recording))
	}
	return choices, nil
}

func (m *Manager) SetQobuzMatch(ctx context.Context, playlistID, spotifyID, reference, trackID string) error {
	recordings, err := m.qobuzChoices(ctx, playlistID, spotifyID, reference)
	if err != nil {
		return err
	}
	if trackID == "" && len(recordings) == 1 {
		trackID = recordings[0].ID
	}
	for _, recording := range recordings {
		if recording.ID == trackID {
			return m.setTrackMatch(ctx, playlistID, spotifyID, &recording)
		}
	}
	return fmt.Errorf("%w: choose a track from that Qobuz release", ErrSettings)
}

func (m *Manager) ClearTrackMatch(ctx context.Context, playlistID, spotifyID string) error {
	return m.setTrackMatch(ctx, playlistID, spotifyID, nil)
}

// Match the selected release by its position, or reuse an equivalent local
// recording with the same ISRC and compatible duration.
func chooseQobuzMatch(recording qobuz.Recording, songs []navidrome.Song) navidrome.Song {
	var equivalent navidrome.Song
	for _, song := range songs {
		if song.ID == "" || !compatibleDurations(recording.Duration, song.Duration) {
			continue
		}
		sameISRC := slices.ContainsFunc(song.ISRC, func(id string) bool { return equalISRC(id, recording.ISRC) })
		samePosition := recording.Album != "" && normalize(recording.Album) == normalize(song.Album) &&
			recording.Number > 0 && recording.Number == song.Track && recording.Disc == max(song.Disc, 1)
		// With no comparable ISRCs, the title also has to agree before trusting the position.
		missingISRC := normalizeISRC(recording.ISRC) == "" || !slices.ContainsFunc(song.ISRC, func(id string) bool { return normalizeISRC(id) != "" })
		if samePosition && (sameISRC || (missingISRC && normalize(recording.Title) == normalize(song.Title))) {
			return song
		}
		if sameISRC && equivalent.ID == "" {
			equivalent = song
		}
	}
	return equivalent
}
