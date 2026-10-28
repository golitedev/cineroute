// Package qobuz is a small client for the Qobuz catalog API: search,
// album, artist and login. Downloading itself is left to streamrip.
package qobuz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	DefaultBaseURL = "https://www.qobuz.com/api.json/0.2/"
	userAgent      = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
	// fallbackAppID is the long-standing public web player app id. It is only
	// used when the current one cannot be fetched from play.qobuz.com.
	fallbackAppID = "798273057"
)

// APIError is an error response from Qobuz.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Qobuz API error (HTTP %d)", e.Status)
	}
	return fmt.Sprintf("Qobuz: %s (HTTP %d)", e.Message, e.Status)
}

// ErrLoginRejected means Qobuz refused the user id and token.
var ErrLoginRejected = errors.New("Qobuz rejected the user_id/token in config.yaml (expired or wrong). Copy a fresh token from play.qobuz.com")

// IsAuth reports whether err means the token or app id was rejected.
func IsAuth(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == 401 || ae.Status == 403)
}

type Client struct {
	BaseURL string
	UserID  string
	Token   string
	HTTP    *http.Client
	// AppIDHint returns an app id known from elsewhere (config.yaml or the
	// streamrip config). It may return "".
	AppIDHint func() string

	mu    sync.Mutex
	appID string
}

func New(userID, token string, appIDHint func() string) *Client {
	return &Client{
		BaseURL:   DefaultBaseURL,
		UserID:    userID,
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		AppIDHint: appIDHint,
	}
}

// AppID returns the app id used for API requests, fetching it when needed.
func (c *Client) AppID(ctx context.Context) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.appID != "" {
		return c.appID
	}
	if c.AppIDHint != nil {
		if id := strings.TrimSpace(c.AppIDHint()); id != "" {
			c.appID = id
			return id
		}
	}
	id, err := FetchAppID(ctx, c.HTTP)
	if err != nil || id == "" {
		return fallbackAppID
	}
	c.appID = id
	return id
}

// resetAppID forgets a cached app id so the next request looks it up again.
func (c *Client) resetAppID() {
	c.mu.Lock()
	c.appID = ""
	c.mu.Unlock()
}

// Get calls an API endpoint (e.g. "album/get") and returns the raw JSON body.
func (c *Client) Get(ctx context.Context, endpoint string, params url.Values) (json.RawMessage, error) {
	if c.Token == "" {
		return nil, errors.New("Qobuz token is not set in config.yaml")
	}
	body, status, err := c.do(ctx, endpoint, params)
	if err == nil && status == 400 {
		// A stale app id is reported as 400 "Invalid or missing app_id parameter".
		var ae *APIError
		if errors.As(apiError(status, body), &ae) && strings.Contains(strings.ToLower(ae.Message), "app_id") {
			c.resetAppID()
			body, status, err = c.do(ctx, endpoint, params)
		}
	}
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(status, body)
	}
	return body, nil
}

func (c *Client) do(ctx context.Context, endpoint string, params url.Values) ([]byte, int, error) {
	u, err := url.Parse(strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(endpoint, "/"))
	if err != nil {
		return nil, 0, err
	}
	q := url.Values{}
	for k, v := range params {
		q[k] = v
	}
	appID := c.AppID(ctx)
	q.Set("app_id", appID)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("X-App-Id", appID)
	req.Header.Set("X-User-Auth-Token", c.Token)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Qobuz request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("reading Qobuz response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func apiError(status int, body []byte) error {
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &e)
	return &APIError{Status: status, Message: e.Message}
}

// Account describes the logged in Qobuz user.
type Account struct {
	ID           string
	Name         string
	Subscription string
	// CanDownload is false for free or lapsed accounts.
	CanDownload bool
}

// Login checks the user id and token and reports the account.
func (c *Client) Login(ctx context.Context) (Account, error) {
	if c.UserID == "" || c.Token == "" {
		return Account{}, errors.New("set qobuz.user_id and qobuz.token in config.yaml")
	}
	raw, err := c.Get(ctx, "user/login", url.Values{
		"user_id":         {c.UserID},
		"user_auth_token": {c.Token},
	})
	if err != nil {
		if IsAuth(err) {
			return Account{}, ErrLoginRejected
		}
		return Account{}, err
	}
	var r struct {
		User struct {
			ID          json.Number `json:"id"`
			DisplayName string      `json:"display_name"`
			Login       string      `json:"login"`
			Credential  struct {
				Label      string          `json:"label"`
				Parameters json.RawMessage `json:"parameters"`
			} `json:"credential"`
		} `json:"user"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return Account{}, fmt.Errorf("unexpected Qobuz login response: %w", err)
	}
	params := strings.TrimSpace(string(r.User.Credential.Parameters))
	acct := Account{
		ID:           r.User.ID.String(),
		Name:         firstNonEmpty(r.User.DisplayName, r.User.Login),
		Subscription: r.User.Credential.Label,
		CanDownload:  params != "" && params != "null" && params != "{}" && params != "[]",
	}
	return acct, nil
}

var (
	bundleRe = regexp.MustCompile(`<script src="(/resources/\d+\.\d+\.\d+-[a-z]\d{3}/bundle\.js)"></script>`)
	appIDRe  = regexp.MustCompile(`production:\{api:\{appId:"(\d{9})",appSecret:"(\w{32})`)
)

// FetchAppID reads the current app id from the Qobuz web player, the same
// way streamrip does.
func FetchAppID(ctx context.Context, hc *http.Client) (string, error) {
	get := func(u string) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		return string(b), err
	}
	page, err := get("https://play.qobuz.com/login")
	if err != nil {
		return "", err
	}
	m := bundleRe.FindStringSubmatch(page)
	if m == nil {
		return "", errors.New("Qobuz web player bundle not found")
	}
	bundle, err := get("https://play.qobuz.com" + m[1])
	if err != nil {
		return "", err
	}
	m = appIDRe.FindStringSubmatch(bundle)
	if m == nil {
		return "", errors.New("app id not found in Qobuz web player bundle")
	}
	return m[1], nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
