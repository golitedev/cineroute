// Package navidrome uses the Subsonic API to scan the library, look up songs,
// and maintain CineRoute's public playlists.
package navidrome

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	URL, Username, Password string
	MusicFolderID           string
	HTTP                    *http.Client
}

func New(baseURL, user, pass string) *Client {
	return &Client{URL: baseURL, Username: user, Password: pass, HTTP: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var ErrNotFound = errors.New("Navidrome item not found")

type response struct {
	Status string `json:"status"`
	Error  struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	User struct {
		Admin bool `json:"adminRole"`
	} `json:"user"`
	Scan struct {
		Scanning bool `json:"scanning"`
	} `json:"scanStatus"`
	Search struct {
		Songs []Song `json:"song"`
	} `json:"searchResult3"`
	Playlists struct {
		Items []Playlist `json:"playlist"`
	} `json:"playlists"`
	Playlist Playlist `json:"playlist"`
}

type Song struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Artist       string        `json:"artist"`
	Album        string        `json:"album"`
	ISRC         []string      `json:"isrc"`
	Track        int           `json:"track"`
	Disc         int           `json:"discNumber"`
	Duration     int           `json:"duration"`
	Contributors []Contributor `json:"contributors"`
}

type Contributor struct {
	Role   string `json:"role"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
}

type Playlist struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Comment string `json:"comment"`
	Owner   string `json:"owner"`
	Public  bool   `json:"public"`
	Songs   []Song `json:"entry"`
}

func (c *Client) call(ctx context.Context, method, endpoint string, q url.Values) (response, error) {
	var result response
	var salt [8]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return result, err
	}
	s := hex.EncodeToString(salt[:])
	sum := md5.Sum([]byte(c.Password + s))
	if q == nil {
		q = make(url.Values)
	}
	q.Set("u", c.Username)
	q.Set("t", hex.EncodeToString(sum[:]))
	q.Set("s", s)
	q.Set("v", "1.16.1")
	q.Set("c", "cineroute")
	q.Set("f", "json")
	target := c.URL + "/rest/" + endpoint
	var body io.Reader
	if method == http.MethodPost {
		body = strings.NewReader(q.Encode())
	} else {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return result, fmt.Errorf("invalid Navidrome URL")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		if urlErr, ok := errors.AsType[*url.Error](err); ok {
			err = urlErr.Err
		}
		return result, fmt.Errorf("Navidrome %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return result, fmt.Errorf("Navidrome %s: HTTP %d", endpoint, resp.StatusCode)
	}
	var envelope struct {
		Response response `json:"subsonic-response"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return result, fmt.Errorf("invalid Navidrome %s response: %w", endpoint, err)
	}
	result = envelope.Response
	if result.Status != "ok" {
		if result.Error.Code == 70 {
			return result, ErrNotFound
		}
		return result, fmt.Errorf("Navidrome %s: %s (code %d)", endpoint, result.Error.Message, result.Error.Code)
	}
	return result, nil
}

func (c *Client) StartScan(ctx context.Context) error {
	_, err := c.call(ctx, http.MethodGet, "startScan", nil)
	return err
}

func (c *Client) Scanning(ctx context.Context) (bool, error) {
	r, err := c.call(ctx, http.MethodGet, "getScanStatus", nil)
	return r.Scan.Scanning, err
}

func (c *Client) CheckAdmin(ctx context.Context) error {
	r, err := c.call(ctx, http.MethodGet, "getUser", url.Values{"username": {c.Username}})
	if err == nil && !r.User.Admin {
		return fmt.Errorf("the configured Navidrome account must be an administrator")
	}
	return err
}

const songSearchLimit = 50

func (c *Client) Search(ctx context.Context, title, artist string) ([]Song, error) {
	songs, err := c.searchSongs(ctx, title)
	if err != nil || len(songs) < songSearchLimit || artist == "" {
		return songs, err
	}
	// Broad searches can fill the result window before finding the exact title.
	// Keep both result sets so that narrowing cannot hide conflicting recordings.
	narrowed, err := c.searchSongs(ctx, title+" "+artist)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(songs)+len(narrowed))
	for _, song := range songs {
		seen[song.ID] = true
	}
	for _, song := range narrowed {
		if !seen[song.ID] {
			songs = append(songs, song)
			seen[song.ID] = true
		}
	}
	return songs, nil
}

func (c *Client) searchSongs(ctx context.Context, query string) ([]Song, error) {
	q := url.Values{"query": {query}, "artistCount": {"0"}, "albumCount": {"0"}, "songCount": {strconv.Itoa(songSearchLimit)}}
	if c.MusicFolderID != "" {
		q.Set("musicFolderId", c.MusicFolderID)
	}
	r, err := c.call(ctx, http.MethodGet, "search3", q)
	return r.Search.Songs, err
}

func (c *Client) Playlist(ctx context.Context, id string) (Playlist, error) {
	r, err := c.call(ctx, http.MethodGet, "getPlaylist", url.Values{"id": {id}})
	return r.Playlist, err
}

// FindOrCreate recovers a playlist created just before an interrupted sync.
// Only CineRoute's unique temporary name and the configured owner can match.
func (c *Client) FindOrCreate(ctx context.Context, temporaryName string) (string, error) {
	id, err := c.FindOwnedPlaylist(ctx, temporaryName)
	if err != nil || id != "" {
		return id, err
	}
	r, err := c.call(ctx, http.MethodPost, "createPlaylist", url.Values{"name": {temporaryName}})
	if err == nil && r.Playlist.ID == "" {
		return "", fmt.Errorf("Navidrome did not return a playlist ID")
	}
	return r.Playlist.ID, err
}

func (c *Client) FindOwnedPlaylist(ctx context.Context, name string) (string, error) {
	r, err := c.call(ctx, http.MethodGet, "getPlaylists", nil)
	if err != nil {
		return "", err
	}
	for _, playlist := range r.Playlists.Items {
		if playlist.Name == name && strings.EqualFold(playlist.Owner, c.Username) {
			return playlist.ID, nil
		}
	}
	return "", nil
}

func (c *Client) ReplaceSongs(ctx context.Context, id string, songs []string) error {
	_, err := c.call(ctx, http.MethodPost, "createPlaylist", url.Values{"playlistId": {id}, "songId": songs})
	return err
}

func (c *Client) MakePublic(ctx context.Context, id, name, comment string) error {
	_, err := c.call(ctx, http.MethodPost, "updatePlaylist", url.Values{
		"playlistId": {id}, "name": {name}, "public": {"true"}, "comment": {comment},
	})
	return err
}

func (c *Client) DeletePlaylist(ctx context.Context, id string) error {
	_, err := c.call(ctx, http.MethodPost, "deletePlaylist", url.Values{"id": {id}})
	return err
}
