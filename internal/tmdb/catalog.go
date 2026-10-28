package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

type CatalogPage struct {
	Page           int      `json:"page"`
	TotalPages     int      `json:"total_pages"`
	Results        []Result `json:"results"`
	SimilarMatches bool     `json:"similar_matches,omitempty"`
}
type Season struct {
	Number       int    `json:"season_number"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episode_count"`
	AirDate      string `json:"air_date"`
	PosterPath   string `json:"poster_path"`
}
type Episode struct {
	Season  int    `json:"season_number"`
	Number  int    `json:"episode_number"`
	Name    string `json:"name"`
	AirDate string `json:"air_date"`
	Runtime int    `json:"runtime"`
}
type Details struct {
	Result
	OriginalLanguage string   `json:"original_language"`
	Runtime          int      `json:"runtime"`
	Status           string   `json:"status"`
	InProduction     bool     `json:"in_production"`
	LastEpisodeToAir *Episode `json:"last_episode_to_air"`
	Seasons          []Season `json:"seasons"`
	IMDBID           string   `json:"imdb_id"`
	ExternalIDs      struct {
		IMDBID string `json:"imdb_id"`
		TVDBID int    `json:"tvdb_id"`
	} `json:"external_ids"`
	AlternativeTitles struct {
		Titles []struct {
			Title string `json:"title"`
		} `json:"titles"`
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	} `json:"alternative_titles"`
	ReleaseDates struct {
		Results []struct {
			Dates []struct {
				Type int    `json:"type"`
				Date string `json:"release_date"`
			} `json:"release_dates"`
		} `json:"results"`
	} `json:"release_dates"`
}

func (d Details) Aliases() []string {
	aliases := []string{d.DisplayTitle(), d.OriginalTitle, d.OriginalName}
	for _, a := range d.AlternativeTitles.Titles {
		aliases = append(aliases, a.Title)
	}
	for _, a := range d.AlternativeTitles.Results {
		aliases = append(aliases, a.Title)
	}
	return aliases
}
func (c *Client) getCatalogJSON(ctx context.Context, path string, q url.Values, out any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("language", c.language)
	u := c.base + "/" + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if isV4Token(c.apiKey) {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	} else {
		v := req.URL.Query()
		v.Set("api_key", c.apiKey)
		req.URL.RawQuery = v.Encode()
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("TMDB request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("TMDB returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read TMDB response")
	}
	return json.Unmarshal(body, out)
}
func (c *Client) searchCatalogExact(ctx context.Context, query, kind string, page int) (CatalogPage, error) {
	path := "search/multi"
	if kind == "movie" || kind == "tv" {
		path = "search/" + kind
	}
	var out CatalogPage
	err := c.getCatalogJSON(ctx, path, url.Values{"query": {query}, "page": {strconv.Itoa(page)}, "include_adult": {"false"}}, &out)
	if err != nil {
		return out, err
	}
	items := []Result{}
	for _, r := range out.Results {
		if path != "search/multi" {
			r.MediaType = kind
		}
		if (r.MediaType == "movie" || r.MediaType == "tv") && !r.Adult {
			items = append(items, r)
		}
	}
	out.Results = items
	return out, nil
}
func (c *Client) Details(ctx context.Context, kind string, id int) (Details, error) {
	var out Details
	if (kind != "movie" && kind != "tv") || id <= 0 {
		return out, fmt.Errorf("invalid media identity")
	}
	appendFields := "external_ids,alternative_titles"
	if kind == "movie" {
		appendFields += ",release_dates"
	}
	err := c.getCatalogJSON(ctx, kind+"/"+strconv.Itoa(id), url.Values{"append_to_response": {appendFields}}, &out)
	out.MediaType = kind
	return out, err
}
func (c *Client) Season(ctx context.Context, id, number int) ([]Episode, error) {
	var out struct {
		Episodes []Episode `json:"episodes"`
	}
	err := c.getCatalogJSON(ctx, "tv/"+strconv.Itoa(id)+"/season/"+strconv.Itoa(number), nil, &out)
	return out.Episodes, err
}
