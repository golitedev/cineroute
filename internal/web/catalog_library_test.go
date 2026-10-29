package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
)

func TestCatalogLibraryBadgeUsesFilesNotRequestHistory(t *testing.T) {
	f := newWorkflowFixture(t, false)
	movie := tmdb.Result{ID: 101, MediaType: "movie", Title: "Example Movie", ReleaseDate: "2024-01-01"}
	root := filepath.Join(f.cfg.Drives[0].MovieRemoteRoot, "Example Movie (2024)")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if f.s.catalogInLibrary(movie, nil, nil) {
		t.Fatal("empty folder marked in library")
	}
	path := filepath.Join(root, "Example.Movie.2024.mkv")
	if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	if !f.s.catalogInLibrary(movie, nil, nil) {
		t.Fatal("existing remote movie absent")
	}
	if f.s.catalogInLibrary(movie, map[string]bool{path: true}, nil) {
		t.Fatal("unfinished download marked in library")
	}
	if f.s.catalogInLibrary(movie, nil, map[string]int64{path: 100}) {
		t.Fatal("truncated download marked in library")
	}
	out := userWorkflowCall(t, f.s, "alice", "GET", "/api/catalog/search?q=Example", "")
	var page struct {
		Results []catalogMedia `json:"results"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if out.Code != 200 || len(page.Results) != 2 || !page.Results[0].InLibrary || page.Results[1].InLibrary {
		t.Fatalf("library badges: %s", out.Body.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if f.s.catalogInLibrary(movie, nil, nil) {
		t.Fatal("removed movie still marked in library")
	}
}

func TestCatalogShowLibraryBadgeRecognizesEpisodes(t *testing.T) {
	f := newWorkflowFixture(t, true)
	show := tmdb.Result{MediaType: "tv", Name: "Example Show", FirstAirDate: "2024-01-01"}
	root := filepath.Join(f.cfg.Drives[0].TVRoot, "Example Show")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	sample := filepath.Join(root, "sample.mkv")
	if err := os.WriteFile(sample, []byte("sample"), 0644); err != nil {
		t.Fatal(err)
	}
	if f.s.catalogInLibrary(show, nil, nil) {
		t.Fatal("sample marked in library")
	}
	path := filepath.Join(root, "Example.Show.S01E01.mkv")
	if err := os.WriteFile(path, []byte("episode"), 0644); err != nil {
		t.Fatal(err)
	}
	excluded, sizes := requests.DownloadFileIndex([]requests.Download{{Status: "downloading", FileSizes: map[string]int64{path: 7}}})
	if f.s.catalogInLibrary(show, excluded, sizes) {
		t.Fatal("incomplete episode marked in library")
	}
	if !f.s.catalogInLibrary(show, nil, nil) {
		t.Fatal("existing episode absent")
	}
}
