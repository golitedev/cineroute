package opensubtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client is a paced, authenticated OpenSubtitles.com API client. It mirrors the
// proven `download-swedish-subs.py` client: one request at a time, an explicit
// User-Agent, Api-Key on every call, an optional Bearer token, https-only
// downloads restricted to opensubtitles.com hosts and a hard size limit.
type Client struct {
	apiKey    string
	username  string
	password  string
	userAgent string
	baseURL   string
	httpc     *http.Client
	delay     time.Duration

	mu        sync.Mutex
	token     string
	tokenAt   time.Time
	lastCall  time.Time
	baseKnown bool
}

// Config configures a client.
type Config struct {
	BaseURL          string
	APIKey           string
	Username         string
	Password         string
	UserAgent        string
	RequestInterval  time.Duration
	RequestTimeout   time.Duration
	DownloadTimeout  time.Duration
	MaxDownloadBytes int64
}

// Errors returned by the client. Hard-stop errors mean the batch must stop
// immediately rather than retry: they are the codes the Python pipeline treated
// as "stop authentication or rate-limit error".
var (
	ErrUnauthorized = errors.New("opensubtitles authentication failed")
	ErrQuota        = errors.New("opensubtitles download quota exhausted")
	ErrRateLimited  = errors.New("opensubtitles rate limited")
	ErrNotFound     = errors.New("opensubtitles resource not found")
)

// New creates a client.
func New(cfg Config) *Client {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = "https://api.opensubtitles.com/api/v1"
	}
	agent := strings.TrimSpace(cfg.UserAgent)
	if agent == "" {
		agent = "CineRoute/1.0"
	}
	interval := cfg.RequestInterval
	if interval < 0 {
		interval = 0
	}
	requestTimeout := cfg.RequestTimeout
	if requestTimeout <= 0 {
		requestTimeout = 30 * time.Second
	}
	downloadTimeout := cfg.DownloadTimeout
	if downloadTimeout <= 0 {
		downloadTimeout = 60 * time.Second
	}
	maxBytes := cfg.MaxDownloadBytes
	if maxBytes <= 0 {
		maxBytes = 25 * 1024 * 1024
	}
	return &Client{
		apiKey:    strings.TrimSpace(cfg.APIKey),
		username:  strings.TrimSpace(cfg.Username),
		password:  cfg.Password,
		userAgent: agent,
		baseURL:   base,
		httpc:     &http.Client{Timeout: requestTimeout},
		delay:     interval,
	}
}

// Configured reports whether an API key is present.
func (c *Client) Configured() bool {
	return c != nil && c.apiKey != ""
}

func (c *Client) pace(ctx context.Context) error {
	if c.delay <= 0 {
		return nil
	}
	c.mu.Lock()
	wait := c.delay - time.Since(c.lastCall)
	c.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) markCall() {
	c.mu.Lock()
	c.lastCall = time.Now()
	c.mu.Unlock()
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == "" {
		return ""
	}
	if time.Since(c.tokenAt) > 23*time.Hour {
		return ""
	}
	return c.token
}

// loginToken returns a cached token or logs in. Login is skipped when no
// credentials are configured: the API key alone still allows a small number of
// anonymous downloads.
func (c *Client) loginToken(ctx context.Context) (string, error) {
	if token := c.currentToken(); token != "" {
		return token, nil
	}
	if c.username == "" || c.password == "" {
		return "", nil
	}
	var response LoginResponse
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/login", nil, map[string]string{
		"username": c.username,
		"password": c.password,
	}, &response); err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", errors.New("opensubtitles login returned no token")
	}
	c.mu.Lock()
	c.token = response.Token
	c.tokenAt = time.Now()
	if host := strings.TrimSpace(response.BaseURL); host != "" && !c.baseKnown {
		if validHostRe.MatchString(host) {
			c.baseURL = "https://" + strings.Trim(host, "/") + "/api/v1"
			c.baseKnown = true
		}
	}
	c.mu.Unlock()
	slog.Info("opensubtitles: logged in",
		"host", strings.TrimPrefix(strings.TrimSuffix(c.baseURL, "/api/v1"), "https://"),
		"level", response.User.Level,
		"vip", response.User.VIP,
		"allowed_downloads", response.User.AllowedDownloads.Int())
	return response.Token, nil
}

var validHostRe = regexp.MustCompile(`^[A-Za-z0-9.-]+$`)

// Invalidate drops the cached token so the next call logs in again.
func (c *Client) Invalidate() {
	c.mu.Lock()
	c.token = ""
	c.mu.Unlock()
}

func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, body any, out any) error {
	if !c.Configured() {
		return errors.New("opensubtitles API key is not configured")
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode opensubtitles request: %w", err)
		}
		payload = encoded
	}
	// The login request itself must not try to bootstrap a token, otherwise
	// loginToken and doJSON would recurse forever.
	token := ""
	if !strings.HasSuffix(endpoint, "/login") {
		bootstrapped, err := c.loginToken(ctx)
		if err != nil {
			return err
		}
		token = bootstrapped
	}
	if err := c.pace(ctx); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build opensubtitles request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Api-Key", c.apiKey)
	request.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	started := time.Now()
	response, err := c.httpc.Do(request)
	c.markCall()
	if err != nil {
		slog.Warn("opensubtitles: request failed", "method", method, "path", endpoint, "err", err)
		return fmt.Errorf("opensubtitles request failed: %w", err)
	}
	slog.Debug("opensubtitles: request", "method", method, "path", endpoint, "status", response.StatusCode, "duration_ms", time.Since(started).Milliseconds())
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return fmt.Errorf("read opensubtitles response: %w", err)
	}
	if len(data) > maxJSONBytes {
		return errors.New("opensubtitles response exceeds the size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		classified := classifyHTTPError(response.StatusCode, data)
		slog.Warn("opensubtitles: request rejected", "method", method, "path", endpoint, "status", response.StatusCode, "err", classified)
		return classified
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode opensubtitles response: %w", err)
	}
	return nil
}

const maxJSONBytes = 8 << 20

// classifyHTTPError maps API errors onto the typed errors the pipeline stops on.
func classifyHTTPError(status int, body []byte) error {
	snippet := strings.TrimSpace(string(body))
	if len(snippet) > 300 {
		snippet = snippet[:300]
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: HTTP %d %s", ErrUnauthorized, status, snippet)
	case http.StatusNotAcceptable, http.StatusPaymentRequired:
		return fmt.Errorf("%w: HTTP %d %s", ErrQuota, status, snippet)
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w: HTTP %d %s", ErrRateLimited, status, snippet)
	case http.StatusNotFound:
		return fmt.Errorf("%w: HTTP %d %s", ErrNotFound, status, snippet)
	default:
		return fmt.Errorf("opensubtitles HTTP %d: %s", status, snippet)
	}
}

// IsHardStop reports whether an error must halt the whole batch.
func IsHardStop(err error) bool {
	return errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrQuota) || errors.Is(err, ErrRateLimited)
}

// Login authenticates and returns the account information for the status view.
func (c *Client) Login(ctx context.Context) (LoginResponse, error) {
	var response LoginResponse
	if c.username == "" || c.password == "" {
		return response, errors.New("opensubtitles credentials are not configured")
	}
	err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/login", nil, map[string]string{
		"username": c.username,
		"password": c.password,
	}, &response)
	if err != nil {
		return LoginResponse{}, err
	}
	if response.Token != "" {
		c.mu.Lock()
		c.token = response.Token
		c.tokenAt = time.Now()
		c.mu.Unlock()
	}
	return response, nil
}

// SearchQuery describes one /subtitles search.
type SearchQuery struct {
	Query      string
	Year       int
	FeatureID  string
	MovieHash  string
	MovieBytes int64
	Page       int
	Languages  string
	Type       string
}

// Search runs one /subtitles query. Search results do not consume download
// quota, which is why the manager caches them.
func (c *Client) Search(ctx context.Context, query SearchQuery) (SearchResponse, error) {
	params := url.Values{}
	languages := query.Languages
	if languages == "" {
		languages = "sv"
	}
	params.Set("languages", languages)
	mediaType := query.Type
	if mediaType == "" {
		mediaType = "movie"
	}
	params.Set("type", mediaType)
	if query.Query != "" {
		params.Set("query", query.Query)
	}
	if query.Year > 0 {
		params.Set("year", strconv.Itoa(query.Year))
	}
	if query.FeatureID != "" {
		params.Set("id", query.FeatureID)
	}
	if query.MovieHash != "" {
		params.Set("moviehash", query.MovieHash)
		if query.MovieBytes > 0 {
			params.Set("moviebytesize", strconv.FormatInt(query.MovieBytes, 10))
		}
	}
	if query.Page > 1 {
		params.Set("page", strconv.Itoa(query.Page))
	}
	return c.searchItems(ctx, params)
}

func (c *Client) searchItems(ctx context.Context, params url.Values) (SearchResponse, error) {
	// The API returns a heterogeneous data array; decode each entry on its own so
	// one odd item cannot break a whole page, and keep the raw JSON for audit.
	var envelope struct {
		TotalPages int               `json:"total_pages"`
		TotalCount int               `json:"total_count"`
		Page       int               `json:"page"`
		Data       []json.RawMessage `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/subtitles", params, nil, &envelope); err != nil {
		return SearchResponse{}, err
	}
	out := SearchResponse{TotalPages: envelope.TotalPages, TotalCount: envelope.TotalCount, Page: envelope.Page}
	for _, raw := range envelope.Data {
		var item Item
		if err := json.Unmarshal(raw, &item); err != nil {
			continue
		}
		item.Raw = append(json.RawMessage(nil), raw...)
		out.Data = append(out.Data, item)
	}
	return out, nil
}

// Features resolves the canonical movie feature for a title.
func (c *Client) Features(ctx context.Context, query string) (FeatureResponse, error) {
	params := url.Values{}
	params.Set("query", query)
	var envelope struct {
		TotalPages int               `json:"total_pages"`
		TotalCount int               `json:"total_count"`
		Page       int               `json:"page"`
		Data       []json.RawMessage `json:"data"`
	}
	if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/features", params, nil, &envelope); err != nil {
		return FeatureResponse{}, err
	}
	out := FeatureResponse{TotalPages: envelope.TotalPages, TotalCount: envelope.TotalCount, Page: envelope.Page}
	for _, raw := range envelope.Data {
		var feature Feature
		if err := json.Unmarshal(raw, &feature); err != nil {
			continue
		}
		feature.Raw = append(json.RawMessage(nil), raw...)
		out.Data = append(out.Data, feature)
	}
	return out, nil
}

// UserInfo reports the daily download allowance and remaining quota.
func (c *Client) UserInfo(ctx context.Context) (UserInfoResponse, error) {
	var response UserInfoResponse
	if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/infos/user", nil, nil, &response); err != nil {
		return UserInfoResponse{}, err
	}
	return response, nil
}

// RemainingFromUserInfo extracts the remaining downloads from either envelope
// shape the API has used, mirroring `remaining_quota`.
func RemainingFromUserInfo(info UserInfoResponse) (remaining int, allowed int, known bool) {
	values := []Intish{info.Data.RemainingDownloads, info.RemainingDownloads}
	for _, value := range values {
		if int(value) > 0 {
			remaining = int(value)
			known = true
			break
		}
	}
	allowedValues := []Intish{info.Data.AllowedDownloads, info.AllowedDownloads}
	for _, value := range allowedValues {
		if int(value) > 0 {
			allowed = int(value)
			break
		}
	}
	return remaining, allowed, known
}

// Download requests a temporary download link for one subtitle file.
func (c *Client) Download(ctx context.Context, fileID int) (DownloadResponse, error) {
	var response DownloadResponse
	if fileID <= 0 {
		return response, errors.New("opensubtitles file id must be positive")
	}
	err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/download", nil, map[string]any{
		"file_id":    fileID,
		"sub_format": "srt",
	}, &response)
	if err != nil {
		return DownloadResponse{}, err
	}
	if strings.TrimSpace(response.Link) == "" {
		return DownloadResponse{}, errors.New("opensubtitles download endpoint returned no link")
	}
	slog.Info("opensubtitles: download link issued",
		"file_id", fileID,
		"file_name", response.FileName,
		"remaining", response.Remaining.Int(),
		"reset_time", response.ResetTime)
	return response, nil
}

// DownloadBytes fetches a subtitle file from the link returned by Download. The
// URL must be https and on an opensubtitles.com host, exactly like the Python
// client, and the payload is size-limited.
func (c *Client) DownloadBytes(ctx context.Context, link string) ([]byte, error) {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil, errors.New("opensubtitles returned an invalid download URL")
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || !strings.HasSuffix(parsed.Hostname(), "opensubtitles.com") {
		return nil, errors.New("opensubtitles returned an unexpected download URL")
	}
	if err := c.pace(ctx); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, errors.New("build opensubtitles download request")
	}
	request.Header.Set("Api-Key", c.apiKey)
	request.Header.Set("User-Agent", c.userAgent)
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	c.markCall()
	if err != nil {
		return nil, errors.New("opensubtitles subtitle download failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("opensubtitles subtitle download HTTP %d", response.StatusCode)
	}
	maxBytes := int64(25 * 1024 * 1024)
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read opensubtitles subtitle download: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("downloaded subtitle exceeds the 25 MiB safety limit")
	}
	if len(data) == 0 {
		return nil, errors.New("opensubtitles returned an empty subtitle file")
	}
	slog.Debug("opensubtitles: subtitle file downloaded", "host", parsed.Hostname(), "bytes", len(data))
	return data, nil
}
