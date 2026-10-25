package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDigitallyAvailableRequiresDigitalMovieDatesAndAiredTV(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, raw, kind string
		want            bool
	}{
		{"digital past", `{"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2020-01-01T00:00:00.000Z"}]}]}}`, "movie", true},
		{"digital future", `{"release_date":"2020-01-01","status":"Released","release_dates":{"results":[{"release_dates":[{"type":3,"release_date":"2020-01-01"},{"type":4,"release_date":"2099-01-01"}]}]}}`, "movie", false},
		{"physical only", `{"release_dates":{"results":[{"release_dates":[{"type":5,"release_date":"2020-01-01"}]}]}}`, "movie", false},
		{"released status alone", `{"status":"Released","release_date":"2020-01-01"}`, "movie", false},
		{"later today", `{"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2026-10-25T23:00:00Z"}]}]}}`, "movie", false},
		{"bad date", `{"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"2020-invalid"}]}]}}`, "movie", false},
		{"aired series", `{"first_air_date":"2020-01-01"}`, "tv", true},
		{"future series", `{"first_air_date":"2099-01-01"}`, "tv", false},
		{"unknown series", `{}`, "tv", false},
		{"regular episode aired", `{"last_episode_to_air":{"season_number":1,"episode_number":1,"air_date":"2020-01-01"}}`, "tv", true},
		{"only unaired specials", `{"last_episode_to_air":{"season_number":0,"episode_number":1,"air_date":"2020-01-01"}}`, "tv", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d Details
			if err := json.Unmarshal([]byte(tc.raw), &d); err != nil {
				t.Fatal(err)
			}
			d.MediaType = tc.kind
			if got := DigitallyAvailable(d, now); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
func TestReleasedCatalogPreservesOrderPaginationAndCachesBothOutcomes(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		parts := strings.Split(r.URL.Path, "/")
		id, _ := strconv.Atoi(parts[len(parts)-1])
		if parts[1] == "movie" {
			if r.URL.Query().Get("append_to_response") != "release_dates" {
				t.Error("movie digital dates not requested")
			}
			year := "2099"
			if id == 2 {
				year = "2020"
			}
			fmt.Fprintf(w, `{"id":%d,"release_dates":{"results":[{"release_dates":[{"type":4,"release_date":"%s-01-01"}]}]}}`, id, year)
		} else {
			date := "2099-01-01"
			if id == 3 {
				date = "2020-01-01"
			}
			fmt.Fprintf(w, `{"id":%d,"first_air_date":%q}`, id, date)
		}
	}))
	defer server.Close()
	client := New("test-key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	original := CatalogPage{Page: 1, TotalPages: 3, SimilarMatches: true, Results: []Result{{ID: 1, MediaType: "movie"}, {ID: 2, MediaType: "movie"}, {ID: 3, MediaType: "tv"}, {ID: 4, MediaType: "tv"}}}
	for i := 0; i < 2; i++ {
		filtered, err := client.FilterReleasedCatalog(t.Context(), original)
		if err != nil || len(filtered.Results) != 2 || filtered.Results[0].ID != 2 || filtered.Results[1].ID != 3 || filtered.Page != 1 || filtered.TotalPages != 3 || !filtered.AvailabilityFiltered || !filtered.SimilarMatches {
			t.Fatalf("filter/order/pagination: %+v %v", filtered, err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("positive/negative checks not cached: %d", calls.Load())
	}
}
func TestReleasedCatalogConcurrencyFailureAndCancellation(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		time.Sleep(5 * time.Millisecond)
		id, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/tv/"))
		fmt.Fprintf(w, `{"id":%d,"first_air_date":"2020-01-01"}`, id)
	}))
	defer server.Close()
	client := New("key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	page := CatalogPage{Page: 2, TotalPages: 4}
	for i := 1; i <= 20; i++ {
		page.Results = append(page.Results, Result{ID: i, MediaType: "tv"})
	}
	out, err := client.FilterReleasedCatalog(t.Context(), page)
	if err != nil || len(out.Results) != 20 || peak.Load() > 4 {
		t.Fatalf("concurrency/result bound: %d %+v %v", peak.Load(), out, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = client.FilterReleasedCatalog(ctx, page); err == nil {
		t.Fatal("cancelled check succeeded")
	}
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 500) }))
	defer failure.Close()
	client = New("key", "en-US", time.Second)
	client.SetBaseURL(failure.URL)
	if _, err = client.FilterReleasedCatalog(t.Context(), page); err == nil {
		t.Fatal("TMDB outage silently became an empty catalog")
	}
	// Filtering an empty page must preserve the ability to load later pages.
	empty, err := client.FilterReleasedCatalog(t.Context(), CatalogPage{Page: 1, TotalPages: 3, Results: []Result{}})
	if err != nil || empty.TotalPages != 3 || !slices.Equal(empty.Results, []Result{}) {
		t.Fatal("empty page lost pagination")
	}
}
