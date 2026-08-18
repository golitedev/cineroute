// Package prowlarr is the small slice of the Prowlarr API needed by the
// optional companion-copy workflow.
package prowlarr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxJSONBytes = 4 << 20

type Client struct {
	baseURL          string
	apiKey           string
	httpc            *http.Client
	maxDownloadBytes int64
}

// New creates a Prowlarr client. The optional maximum is used for torrent
// downloads; when omitted it defaults to 64 MiB, matching CineRoute's normal
// upload limit.
func New(baseURL, apiKey string, timeout time.Duration, maxDownloadBytes ...int64) *Client {
	max := int64(64 << 20)
	if len(maxDownloadBytes) > 0 && maxDownloadBytes[0] > 0 {
		max = maxDownloadBytes[0]
	}
	return &Client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		apiKey:           apiKey,
		httpc:            &http.Client{Timeout: timeout},
		maxDownloadBytes: max,
	}
}

func (c *Client) Configured() bool {
	return c != nil && c.baseURL != "" && strings.TrimSpace(c.apiKey) != ""
}

type Indexer struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Enable bool   `json:"enable"`
}

type Release struct {
	Guid        string    `json:"guid"`
	Title       string    `json:"title"`
	Size        int64     `json:"size"`
	IndexerID   int       `json:"indexerId"`
	Indexer     string    `json:"indexer"`
	TmdbID      int       `json:"tmdbId"`
	ImdbID      int       `json:"imdbId"`
	Seeders     *int      `json:"seeders"`
	Leechers    *int      `json:"leechers"`
	PublishDate time.Time `json:"publishDate"`
	InfoURL     string    `json:"infoUrl"`
	DownloadURL string    `json:"downloadUrl"`
}

func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("Prowlarr not configured (set prowlarr.api_key or CINEROUTE_PROWLARR_API_KEY)")
	}
	var out []Indexer
	if err := c.getJSON(ctx, "/api/v1/indexer", nil, &out); err != nil {
		return nil, fmt.Errorf("Prowlarr indexer lookup failed: %w", err)
	}
	return out, nil
}

func (c *Client) Search(ctx context.Context, indexerID int, query string, limit int) ([]Release, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("Prowlarr not configured (set prowlarr.api_key or CINEROUTE_PROWLARR_API_KEY)")
	}
	if indexerID <= 0 {
		return nil, fmt.Errorf("Prowlarr indexer ID must be positive")
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("Prowlarr search query must not be empty")
	}
	if limit <= 0 {
		limit = 50
	}
	q := url.Values{}
	q.Set("query", query)
	q.Set("type", "search")
	q.Set("indexerIds", strconv.Itoa(indexerID))
	q.Set("limit", strconv.Itoa(limit))
	var out []Release
	if err := c.getJSON(ctx, "/api/v1/search", q, &out); err != nil {
		return nil, fmt.Errorf("Prowlarr search failed: %w", err)
	}
	slog.Info("companion search diagnostics",
		"stage", "prowlarr_raw",
		"query", query,
		"indexer_id", indexerID,
		"limit", limit,
		"result_count", len(out),
	)
	for i, release := range out {
		seeders := any("unknown")
		if release.Seeders != nil {
			seeders = *release.Seeders
		}
		slog.Info("companion search result",
			"stage", "prowlarr_raw",
			"query", query,
			"result_index", i,
			"title", release.Title,
			"size_bytes", release.Size,
			"seeders", seeders,
			"indexer_id", release.IndexerID,
			"indexer", release.Indexer,
		)
	}
	return out, nil
}

// DownloadTorrent fetches a selected Prowlarr proxy URL. The URL is supplied
// by a server-side search result, never directly by the browser. Prowlarr
// proxy URLs can contain the API key, so errors deliberately do not include
// the URL.
func (c *Client) DownloadTorrent(ctx context.Context, downloadURL string) ([]byte, error) {
	if !c.Configured() {
		return nil, fmt.Errorf("Prowlarr not configured (set prowlarr.api_key or CINEROUTE_PROWLARR_API_KEY)")
	}
	downloadURL = strings.TrimSpace(downloadURL)
	if downloadURL == "" {
		return nil, fmt.Errorf("Prowlarr result has no download URL")
	}
	base, err := url.Parse(c.baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid Prowlarr base URL")
	}
	u, err := url.Parse(downloadURL)
	if err != nil {
		return nil, fmt.Errorf("invalid Prowlarr download URL")
	}
	if u.IsAbs() {
		if !sameOrigin(u, base) {
			return nil, fmt.Errorf("Prowlarr download URL is not on the configured server")
		}
	} else {
		if u.Host != "" {
			return nil, fmt.Errorf("invalid Prowlarr download URL")
		}
		u, err = url.Parse(c.baseURL + "/" + strings.TrimLeft(downloadURL, "/"))
		if err != nil {
			return nil, fmt.Errorf("invalid Prowlarr download URL")
		}
	}
	if (u.Scheme != "http" && u.Scheme != "https") || !sameOrigin(u, base) {
		return nil, fmt.Errorf("Prowlarr download URL must use HTTP or HTTPS")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating Prowlarr torrent download request")
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/x-bittorrent, application/octet-stream")
	httpc := *c.httpc
	httpc.CheckRedirect = func(next *http.Request, _ []*http.Request) error {
		if !sameOrigin(next.URL, base) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	resp, err := httpc.Do(req)
	if err != nil {
		// Do not wrap the transport error: net/http may include the full
		// proxy URL, which can contain Prowlarr's API key.
		return nil, fmt.Errorf("Prowlarr torrent download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prowlarr torrent download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > c.maxDownloadBytes {
		return nil, fmt.Errorf("Prowlarr torrent exceeds the %d-byte download limit", c.maxDownloadBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, c.maxDownloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading Prowlarr torrent: %w", err)
	}
	if int64(len(data)) > c.maxDownloadBytes {
		return nil, fmt.Errorf("Prowlarr torrent exceeds the %d-byte download limit", c.maxDownloadBytes)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("Prowlarr returned an empty torrent")
	}
	return data, nil
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	u := c.baseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxJSONBytes {
		return fmt.Errorf("response exceeds the %d-byte limit", maxJSONBytes)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("invalid JSON response")
	}
	return nil
}
