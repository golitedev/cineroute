package charts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var validISRC = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)

const creditsLookupURL = "https://api.credits.fm/v1/lookup"

type creditsClient struct {
	client   *http.Client
	requests []time.Time // used only by the chart worker
}

func (c *creditsClient) lookup(ctx context.Context, spotifyID string) (trackIdentity, error) {
	now := time.Now()
	for len(c.requests) > 0 && now.Sub(c.requests[0]) >= time.Minute {
		c.requests = c.requests[1:]
	}
	if len(c.requests) >= 15 {
		return trackIdentity{}, fmt.Errorf("Credits.fm lookup limit reached")
	}
	c.requests = append(c.requests, now)
	return lookupCreditsIdentity(ctx, c.client, creditsLookupURL, spotifyID)
}

func lookupCreditsIdentity(ctx context.Context, client *http.Client, endpoint, spotifyID string) (trackIdentity, error) {
	spotifyURL := "https://open.spotify.com/track/" + spotifyID
	query := url.Values{"url": {spotifyURL}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return trackIdentity{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "CineRoute (https://github.com/golitedev/cineroute)")
	resp, err := client.Do(req)
	if err != nil {
		return trackIdentity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return trackIdentity{}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return trackIdentity{}, fmt.Errorf("Credits.fm returned HTTP %d", resp.StatusCode)
	}
	var track struct {
		Type       string `json:"type"`
		SpotifyURL string `json:"spotify_url"`
		ISRC       string `json:"isrc"`
		Album      string `json:"album_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&track); err != nil {
		return trackIdentity{}, err
	}
	code := normalizeISRC(track.ISRC)
	if track.Type != "track" || track.SpotifyURL != spotifyURL || !validISRC.MatchString(code) {
		return trackIdentity{}, nil
	}
	return trackIdentity{ISRC: code, Album: track.Album}, nil
}
