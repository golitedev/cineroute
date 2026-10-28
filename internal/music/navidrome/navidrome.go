// Package navidrome asks a Navidrome (or other Subsonic compatible) server
// to scan its library.
package navidrome

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	URL, Username, Password string
	HTTP                    *http.Client
}

func New(baseURL, user, pass string) *Client {
	return &Client{URL: baseURL, Username: user, Password: pass, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// StartScan triggers a library scan through the Subsonic API.
func (c *Client) StartScan(ctx context.Context) error {
	var salt [8]byte
	_, _ = rand.Read(salt[:])
	s := hex.EncodeToString(salt[:])
	sum := md5.Sum([]byte(c.Password + s))
	q := url.Values{
		"u": {c.Username},
		"t": {hex.EncodeToString(sum[:])},
		"s": {s},
		"v": {"1.16.1"},
		"c": {"cineroute"},
		"f": {"json"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/rest/startScan?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var r struct {
		Resp struct {
			Status string `json:"status"`
			Error  struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"subsonic-response"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("unexpected response: %w", err)
	}
	if r.Resp.Status != "ok" {
		return fmt.Errorf("%s", r.Resp.Error.Message)
	}
	return nil
}
