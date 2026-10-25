package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogNativeResultsAndPaginationAreUnchanged(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/search/multi" || r.URL.Query().Get("query") != "el lobo de wall street" || r.URL.Query().Get("language") != "sv-SE" || r.URL.Query().Get("include_adult") != "false" {
			t.Errorf("native search altered: %s %s", r.URL.Path, r.URL.Query().Get("query"))
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		json.NewEncoder(w).Encode(CatalogPage{Page: page, TotalPages: 5, Results: []Result{{ID: 2, Title: "Native first", MediaType: "movie"}, {ID: 1, Title: "Native second", MediaType: "movie"}}})
	}))
	defer server.Close()
	client := New("test-key", "sv-SE", time.Second)
	client.SetBaseURL(server.URL)
	for _, page := range []int{1, 2} {
		result, err := client.SearchCatalog(t.Context(), "el lobo de wall street", "all", page)
		if err != nil || len(result.Results) != 2 || result.Results[0].ID != 2 || result.Page != page || result.TotalPages != 5 || result.SimilarMatches || calls.Load() != int32(page) {
			t.Fatalf("native result changed: %+v %v calls=%d", result, err, calls.Load())
		}
	}

}

func TestCatalogFallbackRecoversReportedTitles(t *testing.T) {
	for _, example := range []struct {
		query, fragment, title string
		id                     int
	}{
		{"the wolf of wallstreet", "the wolf of", "The Wolf of Wall Street", 106646},
		{"thebigshort", "short", "The Big Short", 318846},
		{"the shwashank redempion", "redemp", "The Shawshank Redemption", 278},
		{"interstelar", "interste", "Interstellar", 157336},
	} {
		t.Run(example.query, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				query := r.URL.Query().Get("query")
				results := []Result{}
				if query == example.fragment {
					results = []Result{{ID: 99, Title: "An unrelated extremely popular movie", MediaType: "movie", Popularity: 100000}, {ID: example.id, Title: example.title, MediaType: "movie", Popularity: 10}, {ID: example.id, Title: example.title, MediaType: "movie"}, {ID: 100, Title: example.title, MediaType: "person"}, {ID: 101, Title: example.title, MediaType: "movie", Adult: true}}
				}
				json.NewEncoder(w).Encode(CatalogPage{Page: 1, TotalPages: 1, Results: results})
			}))
			defer server.Close()
			client := New("test-key", "en-US", time.Second)
			client.SetBaseURL(server.URL)
			result, err := client.SearchCatalog(t.Context(), example.query, "all", 1)
			if err != nil || len(result.Results) != 1 || result.Results[0].ID != example.id || !result.SimilarMatches || result.TotalPages != 1 {
				t.Fatalf("fallback failed: %+v %v", result, err)
			}
			if calls.Load() > 1+maxFallbackQueries {
				t.Fatalf("unbounded requests: %d", calls.Load())
			}
		})
	}
}

func TestCatalogFallbackMatchesTranslatedAliases(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/movie/106646" { // metadata stays in the configured display language
			w.Write([]byte(`{"id":106646,"title":"The Wolf of Wall Street","original_title":"The Wolf of Wall Street","translations":{"translations":[{"data":{"title":"El lobo de Wall Street"}}]}}`))
			return
		}
		results := []Result{}
		if r.URL.Query().Get("query") == "el lobo de wal" {
			results = []Result{{ID: 106646, Title: "The Wolf of Wall Street", MediaType: "movie"}}
		}
		json.NewEncoder(w).Encode(CatalogPage{Page: 1, TotalPages: 1, Results: results})
	}))
	defer server.Close()
	client := New("test-key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	result, err := client.SearchCatalog(t.Context(), "el lobo de walstreet", "all", 1)
	if err != nil || len(result.Results) != 1 || result.Results[0].ID != 106646 || !result.SimilarMatches {
		t.Fatalf("translated typo failed: %+v %v", result, err)
	}
	if calls.Load() > 1+maxFallbackQueries+maxAliasChecks {
		t.Fatalf("unbounded alias lookups: %d", calls.Load())
	}
}

func TestCatalogFallbackKeepsTypesAndNeverReturnsWeakMatches(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.HasPrefix(r.URL.Path, "/tv/") {
			w.Write([]byte(`{"id":1396,"name":"Breaking Bad"}`))
			return
		}
		if r.URL.Path != "/search/tv" {
			t.Errorf("TV search escaped its type: %s", r.URL.Path)
		}
		results := []Result{}
		if r.URL.Query().Get("query") != "breking bad" {
			results = []Result{{ID: 1396, Name: "Breaking Bad"}, {ID: 2, Name: "Bad Apples"}}
		}
		json.NewEncoder(w).Encode(CatalogPage{Page: 1, TotalPages: 1, Results: results})
	}))
	defer server.Close()
	client := New("test-key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	result, err := client.SearchCatalog(t.Context(), "breking bad", "tv", 1)
	if err != nil || len(result.Results) != 1 || result.Results[0].ID != 1396 || result.Results[0].MediaType != "tv" {
		t.Fatalf("TV fallback: %+v %v", result, err)
	}
	before := calls.Load()
	result, err = client.SearchCatalog(t.Context(), "breking bad", "tv", 2)
	if err != nil || len(result.Results) != 0 || result.SimilarMatches || calls.Load() != before+1 {
		t.Fatalf("fallback repeated on pagination: %+v %v", result, err)
	}
}

func TestCatalogFallbackErrorsAndCancellationStayBounded(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "upstream error", http.StatusTooManyRequests)
	}))
	defer server.Close()
	client := New("test-key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	if _, err := client.SearchCatalog(t.Context(), "thebigshort", "all", 1); err == nil || calls.Load() != 1 {
		t.Fatalf("failed native request should not fan out: calls=%d err=%v", calls.Load(), err)
	}
}

func TestCatalogFragmentAndSimilarityBounds(t *testing.T) {
	for _, query := range []string{"thebigshort", "the shwashank redempion", "el lobo de walstreet", strings.Repeat("a", 200), "   ", "it", "историа игрушек"} {
		fragments := catalogFragments(query)
		if len(fragments) > maxFallbackQueries {
			t.Fatal("query budget exceeded")
		}
		for i, fragment := range fragments {
			if len([]rune(fragment)) < 3 || slices.Contains(fragments[:i], fragment) || fragment == query {
				t.Fatalf("invalid fragment: %q", fragment)
			}
		}
	}
	if titleSimilarity("thebigshort", "The Big Short") != 1 || titleSimilarity("shwashank", "shawshank") < .8 || titleSimilarity("историа игрушек", "История игрушек") < .8 {
		t.Fatal("spacing, transpositions, or Unicode title comparison failed")
	}
	if titleSimilarity("thebigshort", "Teen Wolf") >= .74 {
		t.Fatal("unrelated title received a high score")
	}
}

func TestCatalogFallbackCancelsOutstandingRequests(t *testing.T) {
	started := make(chan struct{}, 3)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("query") == "thebigshort" {
			w.Write([]byte(`{"page":1,"total_pages":0,"results":[]}`))
			return
		}
		started <- struct{}{}
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New("test-key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.SearchCatalog(ctx, "thebigshort", "all", 1); done <- err }()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("fallback did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback ignored cancellation")
	}
	if calls.Load() > 4 {
		t.Fatalf("issued requests after cancellation: %d", calls.Load())
	}
}

func TestCatalogFallbackDoesNotSuggestUnrelatedTitles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "zzzzimaginaryfilm" {
			w.Write([]byte(`{"page":1,"total_pages":0,"results":[]}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/movie/") {
			w.Write([]byte(`{"id":2,"title":"A Completely Different Movie"}`))
			return
		}
		json.NewEncoder(w).Encode(CatalogPage{Page: 1, TotalPages: 1, Results: []Result{{ID: 2, Title: "A Completely Different Movie", MediaType: "movie"}}})
	}))
	defer server.Close()
	client := New("test-key", "en-US", time.Second)
	client.SetBaseURL(server.URL)
	result, err := client.SearchCatalog(t.Context(), "zzzzimaginaryfilm", "all", 1)
	if err != nil || len(result.Results) != 0 || result.SimilarMatches {
		t.Fatalf("unrelated fallback shown: %+v %v", result, err)
	}
}
