// Package tmdb is a minimal TMDB v3 search client.
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Result struct {
	ID            int     `json:"id"`
	Title         string  `json:"title"`
	Name          string  `json:"name"`
	OriginalTitle string  `json:"original_title"`
	OriginalName  string  `json:"original_name"`
	ReleaseDate   string  `json:"release_date"`
	FirstAirDate  string  `json:"first_air_date"`
	Overview      string  `json:"overview"`
	Popularity    float64 `json:"popularity"`
	VoteCount     int     `json:"vote_count"`
}

// DisplayTitle returns the canonical title for the media type.
func (r Result) DisplayTitle() string {
	if r.Title != "" {
		return r.Title
	}
	return r.Name
}

// Year returns the release or first-air year, or 0.
func (r Result) Year() int {
	date := r.ReleaseDate
	if date == "" {
		date = r.FirstAirDate
	}
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	if y > 1800 && y < 3000 {
		return y
	}
	return 0
}

type Client struct {
	base     string
	apiKey   string
	language string
	httpc    *http.Client
}

func New(apiKey, language string, timeout time.Duration) *Client {
	if language == "" {
		language = "en-US"
	}
	return &Client{
		base:     "https://api.themoviedb.org/3",
		apiKey:   apiKey,
		language: language,
		httpc:    &http.Client{Timeout: timeout},
	}
}

// SetBaseURL overrides the API base URL (used by tests).
func (c *Client) SetBaseURL(u string) { c.base = strings.TrimRight(u, "/") }

func (c *Client) search(ctx context.Context, endpoint, query string, year int, yearParam string) ([]Result, error) {
	q := url.Values{}
	q.Set("query", query)
	q.Set("language", c.language)
	q.Set("include_adult", "false")
	if year > 0 {
		q.Set(yearParam, strconv.Itoa(year))
	}
	u := c.base + "/" + endpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tmdb returned %d for %s", resp.StatusCode, endpoint)
	}
	var body struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Results, nil
}

func (c *Client) SearchMovie(ctx context.Context, query string, year int) ([]Result, error) {
	results, err := c.search(ctx, "search/movie", query, year, "primary_release_year")
	if err != nil {
		return nil, err
	}
	return Rank(results, query, year), nil
}

func (c *Client) SearchTV(ctx context.Context, query string, year int) ([]Result, error) {
	results, err := c.search(ctx, "search/tv", query, year, "first_air_date_year")
	if err != nil {
		return nil, err
	}
	return Rank(results, query, year), nil
}

// Rank orders candidates by exact normalized title match, exact year match,
// then prefix match, keeping the API order as a tie-breaker.
func Rank(results []Result, wantTitle string, wantYear int) []Result {
	type scored struct {
		r Result
		s int
	}
	want := normalizeTitle(wantTitle)
	out := make([]scored, 0, len(results))
	for _, r := range results {
		s := 0
		display := normalizeTitle(r.DisplayTitle())
		original := normalizeTitle(r.OriginalTitle + " " + r.OriginalName)
		switch {
		case want != "" && display == want:
			s += 5
		case want != "" && original == want:
			s += 4
		case want != "" && strings.HasPrefix(display, want):
			s += 2
		case want != "" && strings.Contains(display, want):
			s += 1
		}
		if wantYear > 0 && r.Year() == wantYear {
			s += 3
		}
		out = append(out, scored{r, s})
	}
	// Stable sort by score descending.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].s > out[j-1].s; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	top := make([]Result, 0, min(5, len(out)))
	for _, s := range out {
		top = append(top, s.r)
	}
	return top
}

func normalizeTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}
