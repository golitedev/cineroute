package qobuz

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Recording is a search result with the metadata needed to distinguish versions.
type Recording struct {
	ID, AlbumID, Title, Artist, Album, ISRC string
	Disc, Number, Duration                  int
	Streamable                              bool
}

type trackResp struct {
	ID          json.RawMessage `json:"id"`
	Title       string          `json:"title"`
	Version     *string         `json:"version"`
	ISRC        string          `json:"isrc"`
	MediaNumber int             `json:"media_number"`
	TrackNumber int             `json:"track_number"`
	Duration    int             `json:"duration"`
	Streamable  bool            `json:"streamable"`
	Performer   struct {
		Name string `json:"name"`
	} `json:"performer"`
}

type recordingResp struct {
	trackResp
	Album albumResp `json:"album"`
}

func (r recordingResp) recording() Recording {
	album := albumMetadata(r.Album)
	return Recording{ID: rawID(r.ID), AlbumID: album.ID, Title: recordingTitle(r.Title, deref(r.Version)),
		Artist: firstNonEmpty(r.Performer.Name, album.Artist), ISRC: r.ISRC, Album: album.FullTitle(),
		Disc: max(r.MediaNumber, 1), Number: r.TrackNumber, Duration: r.Duration, Streamable: r.Streamable}
}

// TrackRecording fetches an exact track and its release by Qobuz track ID.
func (c *Client) TrackRecording(ctx context.Context, id string) (Recording, error) {
	raw, err := c.Get(ctx, "track/get", url.Values{"track_id": {id}})
	if err != nil {
		return Recording{}, err
	}
	var response recordingResp
	if err := json.Unmarshal(raw, &response); err != nil {
		return Recording{}, err
	}
	recording := response.recording()
	if recording.ID != id || recording.AlbumID == "" || recording.Title == "" {
		return Recording{}, fmt.Errorf("Qobuz returned incomplete track details")
	}
	return recording, nil
}

// SearchRecordings returns track results from the same catalog used by the portal.
func (c *Client) SearchRecordings(ctx context.Context, query string) ([]Recording, error) {
	raw, err := c.Get(ctx, "catalog/search", url.Values{"query": {query}, "limit": {"50"}})
	if err != nil {
		return nil, err
	}
	var response struct {
		Tracks struct {
			Items []recordingResp `json:"items"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	tracks := make([]Recording, 0, len(response.Tracks.Items))
	for _, item := range response.Tracks.Items {
		tracks = append(tracks, item.recording())
	}
	return tracks, nil
}

func recordingTitle(title, version string) string {
	title, version = strings.TrimSpace(title), strings.TrimSpace(version)
	if version == "" {
		return title
	}
	lowerTitle, lowerVersion := strings.ToLower(title), strings.ToLower(version)
	if strings.Contains(lowerTitle, "("+lowerVersion+")") || strings.Contains(lowerTitle, "["+lowerVersion+"]") {
		return title
	}
	for _, suffix := range []string{" - " + lowerVersion, " – " + lowerVersion, " — " + lowerVersion} {
		if strings.HasSuffix(lowerTitle, suffix) {
			return title
		}
	}
	return title + " (" + version + ")"
}
