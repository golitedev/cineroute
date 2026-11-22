package charts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	spotifyTrackURL   = "https://open.spotify.com/track/"
	musicBrainzURL    = "https://musicbrainz.org/ws/2"
	metadataUserAgent = "CineRoute/1.0 (https://github.com/golitedev/cineroute)"
)

var (
	validSpotifyID     = regexp.MustCompile(`^[A-Za-z0-9]{22}$`)
	validRecordingMBID = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	validISRC          = regexp.MustCompile(`^[A-Z]{2}[A-Z0-9]{3}[0-9]{7}$`)
)

type identityClient struct {
	client                  *http.Client
	spotify, musicBrainz    string
	nextMusicBrainz         time.Time // Only the chart worker uses this client.
	musicBrainzBlockedUntil time.Time
}

func (c *identityClient) lookup(ctx context.Context, id string) (trackIdentity, error) {
	if !validSpotifyID.MatchString(id) {
		return trackIdentity{}, fmt.Errorf("invalid Spotify track ID")
	}
	trackURL := spotifyTrackURL + id
	identity, spotifyErr := c.spotifyMetadata(ctx, id, trackURL)
	code, duration, musicBrainzErr := c.musicBrainzRecording(ctx, trackURL)
	if duration != 0 && !compatibleDurations(identity.Duration, duration) {
		musicBrainzErr = fmt.Errorf("MusicBrainz duration differs from the Spotify track")
	} else if code != "" {
		identity.ISRC = code
		if identity.Duration == 0 {
			identity.Duration = duration
		}
	}
	return identity, errors.Join(spotifyErr, musicBrainzErr)
}

func (c *identityClient) get(ctx context.Context, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", metadataUserAgent)
	return c.client.Do(req)
}

func (c *identityClient) spotifyMetadata(ctx context.Context, id, trackURL string) (trackIdentity, error) {
	resp, err := c.get(ctx, c.spotify+id)
	if err != nil {
		return trackIdentity{}, fmt.Errorf("Spotify metadata: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return trackIdentity{}, fmt.Errorf("Spotify metadata returned HTTP %d", resp.StatusCode)
	}
	return parseSpotifyMetadata(resp.Body, trackURL)
}

func parseSpotifyMetadata(body io.Reader, trackURL string) (trackIdentity, error) {
	tokenizer := html.NewTokenizer(io.LimitReader(body, 512<<10))
	tags := make(map[string]string)
	for {
		kind := tokenizer.Next()
		if kind == html.ErrorToken {
			break
		}
		if kind == html.EndTagToken {
			name, _ := tokenizer.TagName()
			if string(name) == "head" {
				break
			}
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		name, more := tokenizer.TagName()
		if string(name) != "meta" {
			continue
		}
		var key, content string
		for more {
			var attribute, value []byte
			attribute, value, more = tokenizer.TagAttr()
			switch string(attribute) {
			case "name", "property":
				key = string(value)
			case "content":
				content = string(value)
			}
		}
		if key != "" {
			tags[key] = content
		}
	}
	if tags["og:type"] != "music.song" || tags["og:url"] != trackURL {
		return trackIdentity{}, fmt.Errorf("Spotify metadata did not identify the requested track")
	}
	duration, _ := strconv.Atoi(tags["music:duration"])
	number, _ := strconv.Atoi(tags["music:album:track"])
	var album string
	// Spotify's track description is formatted as "Artist · Album · Song · Year".
	fields := strings.Split(tags["og:description"], " · ")
	if len(fields) >= 4 && fields[len(fields)-2] == "Song" && tags["music:album"] != "" {
		album = strings.Join(fields[1:len(fields)-2], " · ")
	}
	if duration <= 0 && album == "" {
		return trackIdentity{}, fmt.Errorf("Spotify track metadata has no duration or album")
	}
	return trackIdentity{Album: album, Duration: max(0, duration), Number: max(0, number)}, nil
}

func (c *identityClient) musicBrainzRecording(ctx context.Context, trackURL string) (string, int, error) {
	var link struct {
		Resource  string
		Relations []struct {
			Recording struct{ ID string }
		}
	}
	found, err := c.musicBrainzGet(ctx, "/url", url.Values{"resource": {trackURL}, "inc": {"recording-rels"}, "fmt": {"json"}}, &link)
	if err != nil || !found || link.Resource != trackURL {
		return "", 0, err
	}
	var id string
	for _, relation := range link.Relations {
		related := relation.Recording.ID
		if related == "" {
			continue
		}
		if !validRecordingMBID.MatchString(related) || (id != "" && related != id) {
			return "", 0, nil
		}
		id = related
	}
	if id == "" {
		return "", 0, nil
	}

	var recording struct {
		ID     string
		Length int
		ISRCs  []string
	}
	found, err = c.musicBrainzGet(ctx, "/recording/"+id, url.Values{"inc": {"isrcs"}, "fmt": {"json"}}, &recording)
	if err != nil || !found || recording.ID != id {
		return "", 0, err
	}
	// A recording linked to multiple ISRCs does not identify a unique code.
	var code string
	for _, value := range recording.ISRCs {
		value = normalizeISRC(value)
		if !validISRC.MatchString(value) || (code != "" && value != code) {
			return "", 0, nil
		}
		code = value
	}
	return code, max(0, (recording.Length+500)/1000), nil
}

func (c *identityClient) musicBrainzGet(ctx context.Context, path string, query url.Values, target any) (bool, error) {
	if time.Now().Before(c.musicBrainzBlockedUntil) {
		return false, fmt.Errorf("MusicBrainz is backing off after a busy response")
	}
	if wait := time.Until(c.nextMusicBrainz); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	c.nextMusicBrainz = time.Now().Add(time.Second)
	resp, err := c.get(ctx, c.musicBrainz+path+"?"+query.Encode())
	if err != nil {
		return false, fmt.Errorf("MusicBrainz: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return false, nil
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		c.musicBrainzBlockedUntil = time.Now().Add(time.Minute)
	case http.StatusOK:
		if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(target); err != nil {
			return false, fmt.Errorf("MusicBrainz metadata: %w", err)
		}
		return true, nil
	}
	return false, fmt.Errorf("MusicBrainz returned HTTP %d", resp.StatusCode)
}
