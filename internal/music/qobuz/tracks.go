package qobuz

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
)

// Recording is a search result with the metadata needed to distinguish versions.
type Recording struct {
	ID, AlbumID, Title, Artist, Album, ISRC string
	Disc, Number, Duration                  int
	Streamable                              bool
}

// SearchRecordings returns track results from the same catalog used by the portal.
func (c *Client) SearchRecordings(ctx context.Context, query string) ([]Recording, error) {
	raw, err := c.Get(ctx, "catalog/search", url.Values{"query": {query}, "limit": {"50"}})
	if err != nil {
		return nil, err
	}
	var response struct {
		Tracks struct {
			Items []struct {
				ID        json.RawMessage `json:"id"`
				Title     string          `json:"title"`
				Version   string          `json:"version"`
				ISRC      string          `json:"isrc"`
				Disc      int             `json:"media_number"`
				Number    int             `json:"track_number"`
				Duration  int             `json:"duration"`
				Available bool            `json:"streamable"`
				Performer struct {
					Name string `json:"name"`
				} `json:"performer"`
				Album albumResp `json:"album"`
			} `json:"items"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	tracks := make([]Recording, 0, len(response.Tracks.Items))
	for _, item := range response.Tracks.Items {
		album := albumMetadata(item.Album)
		tracks = append(tracks, Recording{
			ID: rawID(item.ID), AlbumID: album.ID, Album: album.FullTitle(), Title: recordingTitle(item.Title, item.Version),
			Artist: firstNonEmpty(item.Performer.Name, album.Artist), ISRC: item.ISRC,
			Disc: max(item.Disc, 1), Number: item.Number, Duration: item.Duration, Streamable: item.Available,
		})
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
