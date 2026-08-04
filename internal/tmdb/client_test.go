package tmdb

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestV3KeySentAsQueryParam(t *testing.T) {
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("api_key")
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer srv.Close()

	c := New("0123456789abcdef0123456789abcdef", "en-US", 5*time.Second)
	c.SetBaseURL(srv.URL)
	if _, err := c.SearchMovie(t.Context(), "Toy Story", 0); err != nil {
		t.Fatal(err)
	}
	if gotKey != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("v3 key should be sent as api_key param, got %q", gotKey)
	}
	if gotAuth != "" {
		t.Fatalf("v3 key must not be sent as Bearer, got %q", gotAuth)
	}
}

func TestV4TokenSentAsBearer(t *testing.T) {
	var gotKey, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("api_key")
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer srv.Close()

	token := "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiIxMjMifQ.signature"
	c := New(token, "en-US", 5*time.Second)
	c.SetBaseURL(srv.URL)
	if _, err := c.SearchTV(t.Context(), "Lost", 0); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer "+token {
		t.Fatalf("v4 token should be sent as Bearer, got %q", gotAuth)
	}
	if gotKey != "" {
		t.Fatalf("v4 token must not be sent as api_key param, got %q", gotKey)
	}
}

func TestRankTrimsToTen(t *testing.T) {
	results := make([]Result, 20)
	for i := range results {
		results[i] = Result{ID: i, Title: fmt.Sprintf("Movie %02d", i)}
	}
	top := Rank(results, "zz-no-match", 0)
	if len(top) != 10 {
		t.Fatalf("expected 10 results, got %d", len(top))
	}
}

func TestRankExactMatchFirst(t *testing.T) {
	results := []Result{
		{ID: 1, Title: "Blade Runner", ReleaseDate: "1982-06-25"},
		{ID: 2, Title: "Blade Runner 2049", ReleaseDate: "2017-10-06"},
	}
	top := Rank(results, "Blade Runner 2049", 2017)
	if len(top) == 0 || top[0].ID != 2 {
		t.Fatalf("expected Blade Runner 2049 first, got %+v", top)
	}
}

func TestResultYear(t *testing.T) {
	if y := (Result{ReleaseDate: "1995-11-22"}).Year(); y != 1995 {
		t.Fatalf("movie year: %d", y)
	}
	if y := (Result{FirstAirDate: "2004-09-22"}).Year(); y != 2004 {
		t.Fatalf("tv year: %d", y)
	}
	if y := (Result{}).Year(); y != 0 {
		t.Fatalf("empty year: %d", y)
	}
	if strings.TrimSpace((Result{Name: "Lost"}).DisplayTitle()) != "Lost" {
		t.Fatal("DisplayTitle should fall back to name")
	}
}
