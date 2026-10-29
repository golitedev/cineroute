package qobuz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// The browse functions below return Qobuz responses unchanged, so the web UI
// (taken from Qobuz-DL) can keep parsing them the way it always did.

var (
	albumURLRe  = regexp.MustCompile(`https://(?:play|open|www)\.qobuz\.com/(?:[a-z]{2}-[a-z]{2}/)?album/(?:[^/\s]+/)?([a-zA-Z0-9]+)`)
	trackURLRe  = regexp.MustCompile(`https://(?:play|open|www)\.qobuz\.com/(?:[a-z]{2}-[a-z]{2}/)?track/(\d+)`)
	artistURLRe = regexp.MustCompile(`https://(?:play|open|www)\.qobuz\.com/(?:[a-z]{2}-[a-z]{2}/)?(?:interpreter|artist)/(?:[^/\s]+/)?(\d+)`)
)

// Search runs catalog/search. A pasted Qobuz album, track or artist link is
// searched by its id, and "switchTo" tells the UI which tab to show.
func (c *Client) Search(ctx context.Context, query string, limit, offset int) (json.RawMessage, error) {
	query = strings.TrimSpace(query)
	var switchTo any
	if m := albumURLRe.FindStringSubmatch(query); m != nil {
		query, switchTo = m[1], "albums"
	} else if m := trackURLRe.FindStringSubmatch(query); m != nil {
		query, switchTo = m[1], "tracks"
	} else if m := artistURLRe.FindStringSubmatch(query); m != nil {
		query, switchTo = m[1], "artists"
	}
	raw, err := c.Get(ctx, "catalog/search", url.Values{
		"query":  {query},
		"limit":  {strconv.Itoa(limit)},
		"offset": {strconv.Itoa(offset)},
	})
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("unexpected Qobuz search response: %w", err)
	}
	sw, _ := json.Marshal(switchTo)
	obj["switchTo"] = sw
	return json.Marshal(obj)
}

// AlbumRaw runs album/get the way the UI expects it.
func (c *Client) AlbumRaw(ctx context.Context, albumID string) (json.RawMessage, error) {
	return c.Get(ctx, "album/get", url.Values{"album_id": {albumID}, "extra": {"track_ids"}})
}

// ArtistPage runs artist/page.
func (c *Client) ArtistPage(ctx context.Context, artistID string) (json.RawMessage, error) {
	return c.Get(ctx, "artist/page", url.Values{"artist_id": {artistID}, "sort": {"release_date"}})
}

// ArtistReleases runs artist/getReleasesList.
func (c *Client) ArtistReleases(ctx context.Context, artistID, releaseType string, limit, offset, trackSize int) (json.RawMessage, error) {
	return c.Get(ctx, "artist/getReleasesList", url.Values{
		"artist_id":    {artistID},
		"release_type": {releaseType},
		"limit":        {strconv.Itoa(limit)},
		"offset":       {strconv.Itoa(offset)},
		"track_size":   {strconv.Itoa(trackSize)},
		"sort":         {"release_date"},
	})
}

// Album is the part of album/get CineRoute needs to queue, verify and name an
// album.
type Album struct {
	ID              string
	Title           string // without version
	Version         string
	Artist          string
	ReleaseDate     string // YYYY-MM-DD, may be ""
	Label           string
	Genre           string
	UPC             string
	Cover           string
	MaxBitDepth     int
	MaxSamplingRate float64 // kHz
	Streamable      bool
	Downloadable    bool
	TracksCount     int
	Tracks          []Track
}

type Track struct {
	ID          string
	Title       string // with version
	Disc        int
	Number      int
	DurationSec int
	Streamable  bool
}

// FullTitle is the title with the edition appended, e.g. "Aquemini (25th
// Anniversary Edition)".
func (a Album) FullTitle() string {
	if a.Version == "" || strings.Contains(strings.ToLower(a.Title), strings.ToLower(a.Version)) {
		return a.Title
	}
	return a.Title + " (" + a.Version + ")"
}

// Year is the release year or "".
func (a Album) Year() string {
	if len(a.ReleaseDate) >= 4 {
		return a.ReleaseDate[:4]
	}
	return ""
}

// URL is the album link handed to streamrip.
func (a Album) URL() string { return "https://play.qobuz.com/album/" + a.ID }

type albumResp struct {
	AudioInfo struct {
		MaxBitDepth     float64 `json:"maximum_bit_depth"`
		MaxSamplingRate float64 `json:"maximum_sampling_rate"`
	} `json:"audio_info"`
	Dates struct {
		Original string `json:"original"`
	} `json:"dates"`
	ID      json.RawMessage `json:"id"`
	QobuzID json.RawMessage `json:"qobuz_id"`
	Title   string          `json:"title"`
	Version *string         `json:"version"`
	Artist  struct {
		Name string `json:"name"`
	} `json:"artist"`
	ReleaseDateOriginal *string `json:"release_date_original"`
	ReleaseDate         *string `json:"release_date"`
	Label               struct {
		Name string `json:"name"`
	} `json:"label"`
	Genre struct {
		Name string `json:"name"`
	} `json:"genre"`
	UPC   string `json:"upc"`
	Image struct {
		Small     string `json:"small"`
		Thumbnail string `json:"thumbnail"`
		Large     string `json:"large"`
	} `json:"image"`
	MaxBitDepth     float64 `json:"maximum_bit_depth"`
	MaxSamplingRate float64 `json:"maximum_sampling_rate"`
	Streamable      bool    `json:"streamable"`
	Downloadable    bool    `json:"downloadable"`
	TracksCount     int     `json:"tracks_count"`
	Tracks          struct {
		Total int `json:"total"`
		Items []struct {
			ID          json.RawMessage `json:"id"`
			Title       string          `json:"title"`
			Version     *string         `json:"version"`
			TrackNumber int             `json:"track_number"`
			MediaNumber int             `json:"media_number"`
			Duration    int             `json:"duration"`
			Streamable  bool            `json:"streamable"`
		} `json:"items"`
	} `json:"tracks"`
}

// Album fetches an album with its full track list.
func (c *Client) Album(ctx context.Context, albumID string) (Album, error) {
	const pageSize = 500
	var a Album
	seen := map[string]bool{}
	for offset := 0; ; offset += pageSize {
		raw, err := c.Get(ctx, "album/get", url.Values{
			"album_id": {albumID},
			"limit":    {strconv.Itoa(pageSize)},
			"offset":   {strconv.Itoa(offset)},
		})
		if err != nil {
			return Album{}, err
		}
		var r albumResp
		if err := json.Unmarshal(raw, &r); err != nil {
			return Album{}, fmt.Errorf("unexpected Qobuz album response: %w", err)
		}
		if offset == 0 {
			a = albumMetadata(r)
			a.ID = firstNonEmpty(a.ID, albumID)
		}
		added := 0
		for _, t := range r.Tracks.Items {
			id := rawID(t.ID)
			if id != "" && seen[id] {
				continue
			}
			seen[id] = true
			added++
			title := strings.TrimSpace(t.Title)
			if v := strings.TrimSpace(deref(t.Version)); v != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(v)) {
				title += " (" + v + ")"
			}
			disc := t.MediaNumber
			if disc < 1 {
				disc = 1
			}
			a.Tracks = append(a.Tracks, Track{
				ID:          id,
				Title:       title,
				Disc:        disc,
				Number:      t.TrackNumber,
				DurationSec: t.Duration,
				Streamable:  t.Streamable,
			})
		}
		total := r.Tracks.Total
		if total == 0 {
			total = a.TracksCount
		}
		if added == 0 || len(a.Tracks) >= total {
			break
		}
	}
	if a.Title == "" {
		return Album{}, fmt.Errorf("Qobuz returned no album for id %s", albumID)
	}
	if len(a.Tracks) == 0 {
		return Album{}, fmt.Errorf("Qobuz returned no tracks for album %q", a.Title)
	}
	return a, nil
}

func rawID(r json.RawMessage) string {
	s := strings.TrimSpace(string(r))
	if s == "" || s == "null" {
		return ""
	}
	var str string
	if json.Unmarshal(r, &str) == nil {
		return str
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ParseAlbumMetadata decodes catalog album fields without fetching its tracks.
func ParseAlbumMetadata(raw json.RawMessage) (Album, error) {
	var r albumResp
	if err := json.Unmarshal(raw, &r); err != nil {
		return Album{}, err
	}
	return albumMetadata(r), nil
}

func albumMetadata(r albumResp) Album {
	if r.MaxBitDepth == 0 {
		r.MaxBitDepth = r.AudioInfo.MaxBitDepth
	}
	if r.MaxSamplingRate == 0 {
		r.MaxSamplingRate = r.AudioInfo.MaxSamplingRate
	}
	if r.ReleaseDateOriginal == nil && r.Dates.Original != "" {
		r.ReleaseDateOriginal = &r.Dates.Original
	}
	return Album{
		ID:              rawID(r.ID),
		Title:           strings.TrimSpace(r.Title),
		Version:         strings.TrimSpace(deref(r.Version)),
		Artist:          strings.TrimSpace(r.Artist.Name),
		ReleaseDate:     firstNonEmpty(deref(r.ReleaseDateOriginal), deref(r.ReleaseDate)),
		Label:           strings.TrimSpace(r.Label.Name),
		Genre:           strings.TrimSpace(r.Genre.Name),
		UPC:             r.UPC,
		Cover:           firstNonEmpty(r.Image.Small, r.Image.Thumbnail, r.Image.Large),
		MaxBitDepth:     int(r.MaxBitDepth),
		MaxSamplingRate: r.MaxSamplingRate,
		Streamable:      r.Streamable,
		Downloadable:    r.Downloadable,
		TracksCount:     r.TracksCount,
	}
}
