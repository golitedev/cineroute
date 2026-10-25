package prowlarr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cineroute/internal/medialanguages"
)

type trackerCredentials struct {
	base                 *url.URL
	key, sort, direction string
	expires              time.Time
	freeleech            bool
}
type languagePage struct {
	items        map[string]medialanguages.Metadata
	status       string
	expires      time.Time
	refreshAfter time.Time
}
type languageCache struct {
	mu               sync.Mutex
	credentials      map[int]trackerCredentials
	pages            map[[32]byte]languagePage
	unavailableUntil map[int]time.Time
}

func newLanguageCache() *languageCache {
	return &languageCache{credentials: map[int]trackerCredentials{}, pages: map[[32]byte]languagePage{}, unavailableUntil: map[int]time.Time{}}
}

// EnrichLanguageMetadata reads at most one native result page per tracker and
// search, rather than one request per release. Credentials and reports never
// enter the public Indexer resource. Only matching, same-origin torrent IDs can
// receive metadata. Errors are neutral preference hints, not download failures.
func (c *Client) EnrichLanguageMetadata(ctx context.Context, releases []Release, query string, beforeRequest func() error) []Release {
	groups := map[int][]int{}
	for i := range releases {
		releases[i].Languages = medialanguages.Metadata{Status: "unsupported"}
		if unit3dTorrentID(releases[i].InfoURL) != "" {
			groups[releases[i].IndexerID] = append(groups[releases[i].IndexerID], i)
		}
	}
	ids := []int{}
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		c.languages.mu.Lock()
		c.enrichTracker(ctx, id, query, releases, groups[id], beforeRequest)
		c.languages.mu.Unlock()
	}
	return releases
}
func (c *Client) enrichTracker(ctx context.Context, id int, query string, releases []Release, indices []int, beforeRequest func() error) {
	credentials := c.trackerCredentials(ctx, id)
	if credentials.base == nil || credentials.key == "" {
		return
	}
	eligible := []int{}
	for _, i := range indices {
		u, err := url.Parse(releases[i].InfoURL)
		if err == nil && sameOrigin(u, credentials.base) && strings.HasPrefix(u.Path, strings.TrimRight(credentials.base.Path, "/")+"/torrents/") {
			eligible = append(eligible, i)
		}
	}
	if len(eligible) == 0 {
		return
	}
	cacheKey := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t", id, credentials.base, credentials.key, query, credentials.sort, credentials.direction, credentials.freeleech)))
	page, found := c.languages.pages[cacheKey]
	missing := false
	for _, i := range eligible {
		if _, ok := page.items[unit3dTorrentID(releases[i].InfoURL)]; !ok {
			missing = true
			break
		}
	}
	if !found || time.Now().After(page.expires) || (missing && time.Now().After(page.refreshAfter)) {
		if time.Now().Before(c.languages.unavailableUntil[id]) {
			for _, i := range eligible {
				releases[i].Languages = medialanguages.Metadata{Status: "unavailable"}
			}
			return
		}
		page = languagePage{items: map[string]medialanguages.Metadata{}, status: "unavailable", expires: time.Now().Add(5 * time.Minute), refreshAfter: time.Now().Add(5 * time.Minute)}
		if beforeRequest != nil {
			if err := beforeRequest(); err != nil {
				return
			}
		}
		// Construct a fixed API path from the administrator-configured base URL.
		// The release URL supplies only a validated numeric torrent identity.
		endpoint := *credentials.base
		endpoint.Path = path.Join(endpoint.Path, "api/torrents/filter")
		endpoint.RawQuery = url.Values{"name": {query}, "perPage": {"100"}, "sortField": {credentials.sort}, "sortDirection": {credentials.direction}}.Encode()
		if credentials.freeleech {
			values := endpoint.Query()
			values.Set("free[]", "100")
			endpoint.RawQuery = values.Encode()
		}
		var response struct {
			Data []struct {
				Attributes struct {
					DetailsLink string `json:"details_link"`
					MediaInfo   string `json:"media_info"`
					BDInfo      string `json:"bd_info"`
				}
			}
		}
		if c.metadataJSON(ctx, endpoint.String(), "Authorization", "Bearer "+credentials.key, 8<<20, &response) == nil {
			page.status = "unknown"
			page.expires = time.Now().Add(6 * time.Hour)
			for index, row := range response.Data {
				if index >= 100 {
					break
				}
				torrentID := unit3dTorrentID(row.Attributes.DetailsLink)
				u, err := url.Parse(row.Attributes.DetailsLink)
				if torrentID != "" && err == nil && sameOrigin(u, credentials.base) && strings.HasPrefix(u.Path, strings.TrimRight(credentials.base.Path, "/")+"/torrents/") {
					page.items[torrentID] = medialanguages.Parse(row.Attributes.MediaInfo, row.Attributes.BDInfo)
				}
			}
		} else {
			if len(c.languages.unavailableUntil) >= 100 {
				c.languages.unavailableUntil = map[int]time.Time{}
			}
			c.languages.unavailableUntil[id] = time.Now().Add(5 * time.Minute)
		}
		if ctx.Err() != nil {
			return
		}
		// A bounded in-memory cache contains parsed language codes, never raw
		// reports, passkeys, download URLs or persistent tracker credentials.
		if len(c.languages.pages) >= 128 {
			for key, value := range c.languages.pages {
				if time.Now().After(value.expires) {
					delete(c.languages.pages, key)
				}
			}
			if len(c.languages.pages) >= 128 {
				for key := range c.languages.pages {
					delete(c.languages.pages, key)
					break
				}
			}
		}
		c.languages.pages[cacheKey] = page
	}
	for _, i := range eligible {
		metadata, ok := page.items[unit3dTorrentID(releases[i].InfoURL)]
		if !ok {
			metadata = medialanguages.Metadata{Status: page.status}
		}
		releases[i].Languages = metadata
	}
}
func (c *Client) trackerCredentials(ctx context.Context, id int) trackerCredentials {
	if cached, ok := c.languages.credentials[id]; ok && time.Now().Before(cached.expires) {
		return cached
	}
	result := trackerCredentials{expires: time.Now().Add(5 * time.Minute), sort: "created_at", direction: "desc"}
	defer func() {
		if ctx.Err() == nil {
			if len(c.languages.credentials) >= 100 {
				c.languages.credentials = map[int]trackerCredentials{}
			}
			c.languages.credentials[id] = result
		}
	}()
	if id <= 0 || !c.Configured() {
		return result
	}
	var resource struct {
		ID       int
		Enable   bool
		Protocol string
		Fields   []struct {
			Name  string
			Value json.RawMessage
		}
	}
	if c.metadataJSON(ctx, c.baseURL+"/api/v1/indexer/"+strconv.Itoa(id), "X-Api-Key", c.apiKey, 1<<20, &resource) != nil || resource.ID != id || !resource.Enable || resource.Protocol != "torrent" {
		return result
	}
	var base string
	for _, field := range resource.Fields {
		if field.Name == "freeleech" {
			_ = json.Unmarshal(field.Value, &result.freeleech)
			continue
		}
		var value string
		if json.Unmarshal(field.Value, &value) != nil {
			continue
		}
		switch strings.ToLower(field.Name) {
		case "baseurl":
			base = value
		case "apikey", "api_key", "apitoken", "api_token":
			result.key = value
		case "sort":
			if value == "created_at" || value == "seeders" || value == "size" || value == "name" {
				result.sort = value
			}
		case "type":
			if value == "asc" || value == "desc" {
				result.direction = value
			}
		}
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(result.key, "\r\n") || len(result.key) > 8192 {
		result.key = ""
		return result
	}
	// Public tracker credentials require HTTPS. HTTP is allowed only for local
	// test servers; neither search results nor API responses choose the origin.
	ip := net.ParseIP(parsed.Hostname())
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || (parsed.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()))) {
		result.key = ""
		return result
	}
	result.base = parsed
	return result
}
func unit3dTorrentID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || u.EscapedPath() != u.Path {
		return ""
	}
	before, id, ok := strings.Cut(strings.TrimRight(u.Path, "/"), "/torrents/")
	if !ok || strings.Contains(before, "/torrents/") || id == "" || len(id) > 18 {
		return ""
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if strings.Trim(id, "0") == "" {
		return ""
	}
	return id
}
func (c *Client) metadataJSON(ctx context.Context, endpoint, header, secret string, maxBytes int64, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("metadata request unavailable")
	}
	req.Header.Set(header, secret)
	req.Header.Set("Accept", "application/json")
	client := *c.httpc
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("metadata request unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metadata service returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil || int64(len(body)) > maxBytes {
		return fmt.Errorf("metadata response unavailable")
	}
	if json.Unmarshal(body, out) != nil {
		return fmt.Errorf("metadata response invalid")
	}
	return nil
}
