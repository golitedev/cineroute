// Package qbittorrent is a minimal client for the qBittorrent WebUI API.
// It only touches the endpoints CineRoute needs: auth, add (stopped), verify,
// start, categories, preferences, versions, and the torrent list.
package qbittorrent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// StoppedStates are the states qBittorrent reports for a torrent that is not
// transferring (the names differ across API generations).
var StoppedStates = map[string]bool{
	"stoppedDL": true, "stoppedUP": true,
	"pausedDL": true, "pausedUP": true,
	"stopped": true, "paused": true,
}

type Client struct {
	baseURL  string
	username string
	password string
	httpc    *http.Client
	mu       sync.Mutex
	authed   bool
}

func New(baseURL, username, password string, timeout time.Duration) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		httpc:    &http.Client{Jar: jar, Timeout: timeout},
	}, nil
}

func (c *Client) login(ctx context.Context) error {
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", c.baseURL)
	req.Header.Set("Origin", c.baseURL)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Ok.") {
		return fmt.Errorf("qBittorrent login failed (status %d): %s", resp.StatusCode, truncate(string(body), 200))
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		u := c.baseURL + path
		if len(q) > 0 {
			u += "?" + q.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Referer", c.baseURL)
		req.Header.Set("Origin", c.baseURL)
		resp, err := c.httpc.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			// The session cookie (SID) may have expired or qBittorrent may
			// have restarted; always re-authenticate and retry regardless of
			// the cached authed flag.
			c.authed = false
			if err := c.login(ctx); err != nil {
				return nil, err
			}
			c.authed = true
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("qBittorrent GET %s returned %d: %s", path, resp.StatusCode, truncate(string(body), 300))
		}
		c.authed = true
		return body, nil
	}
	return nil, fmt.Errorf("qBittorrent authentication failed")
}

func (c *Client) post(ctx context.Context, path string, q url.Values) ([]byte, error) {
	encoded := q.Encode()
	return c.postBody(ctx, path, func() (io.Reader, string, error) {
		return strings.NewReader(encoded), "application/x-www-form-urlencoded", nil
	})
}

func (c *Client) postBody(ctx context.Context, path string, rebuild func() (io.Reader, string, error)) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		b, ct, err := rebuild()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, b)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Referer", c.baseURL)
		req.Header.Set("Origin", c.baseURL)
		resp, err := c.httpc.Do(req)
		if err != nil {
			return nil, err
		}
		body2, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusForbidden {
			// The session cookie (SID) may have expired or qBittorrent may
			// have restarted; always re-authenticate and retry regardless of
			// the cached authed flag.
			c.authed = false
			if err := c.login(ctx); err != nil {
				return nil, err
			}
			c.authed = true
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("qBittorrent POST %s returned %d: %s", path, resp.StatusCode, truncate(string(body2), 300))
		}
		c.authed = true
		return body2, nil
	}
	return nil, fmt.Errorf("qBittorrent authentication failed")
}

type Torrent struct {
	Hash        string `json:"hash"`
	Name        string `json:"name"`
	SavePath    string `json:"save_path"`
	ContentPath string `json:"content_path"`
	Category    string `json:"category"`
	Tags        string `json:"tags"`
	AutoTMM     bool   `json:"auto_tmm"`
	TotalSize   int64  `json:"total_size"`
	AmountLeft  int64  `json:"amount_left"`
	State       string `json:"state"`
}

type TorrentFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type Category struct {
	SavePath string `json:"savePath"`
}

type Preferences struct {
	PreallocateAll  bool `json:"preallocate_all"`
	TempPathEnabled bool `json:"temp_path_enabled"`
}

func (c *Client) AppVersion(ctx context.Context) (string, error) {
	b, err := c.get(ctx, "/api/v2/app/version", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *Client) WebAPIVersion(ctx context.Context) (string, error) {
	b, err := c.get(ctx, "/api/v2/app/webapiVersion", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (c *Client) Preferences(ctx context.Context) (*Preferences, error) {
	b, err := c.get(ctx, "/api/v2/app/preferences", nil)
	if err != nil {
		return nil, err
	}
	var p Preferences
	_ = json.Unmarshal(b, &p)
	return &p, nil
}

func (c *Client) Categories(ctx context.Context) (map[string]Category, error) {
	b, err := c.get(ctx, "/api/v2/torrents/categories", nil)
	if err != nil {
		return nil, err
	}
	var m map[string]Category
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// EnsureCategory creates the category with an empty save path if missing.
func (c *Client) EnsureCategory(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	cats, err := c.Categories(ctx)
	if err != nil {
		return err
	}
	if _, ok := cats[name]; ok {
		return nil
	}
	_, err = c.post(ctx, "/api/v2/torrents/createCategory", url.Values{
		"category": {name},
		"savePath": {""},
	})
	return err
}

func (c *Client) Torrents(ctx context.Context, q url.Values) ([]Torrent, error) {
	b, err := c.get(ctx, "/api/v2/torrents/info", q)
	if err != nil {
		return nil, err
	}
	var out []Torrent
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Files(ctx context.Context, hash string) ([]TorrentFile, error) {
	b, err := c.get(ctx, "/api/v2/torrents/files", url.Values{"hash": {hash}})
	if err != nil {
		return nil, err
	}
	var out []TorrentFile
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type AddOptions struct {
	SavePath   string
	Category   string
	Tags       string
	RootFolder bool
	Stopped    bool
	Filename   string
}

func (c *Client) AddTorrent(ctx context.Context, data []byte, opts AddOptions) error {
	rebuild := func() (io.Reader, string, error) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("torrents", opts.Filename)
		if err != nil {
			return nil, "", err
		}
		if _, err := fw.Write(data); err != nil {
			return nil, "", err
		}
		fields := map[string]string{
			"savepath":    opts.SavePath,
			"category":    opts.Category,
			"tags":        opts.Tags,
			"autoTMM":     "false",
			"paused":      strconv.FormatBool(opts.Stopped),
			"stopped":     strconv.FormatBool(opts.Stopped),
			"root_folder": strconv.FormatBool(opts.RootFolder),
		}
		for k, v := range fields {
			if err := mw.WriteField(k, v); err != nil {
				return nil, "", err
			}
		}
		if err := mw.Close(); err != nil {
			return nil, "", err
		}
		return &buf, mw.FormDataContentType(), nil
	}
	_, err := c.postBody(ctx, "/api/v2/torrents/add", rebuild)
	return err
}

func (c *Client) Start(ctx context.Context, hash string) error {
	_, err := c.post(ctx, "/api/v2/torrents/start", url.Values{"hashes": {hash}})
	return err
}

func (t *Torrent) Stopped() bool {
	return StoppedStates[t.State]
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
