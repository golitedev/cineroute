package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	if f.s.catalogInLibrary(movie) {
		t.Fatal("empty folder marked in library")
	}
	path := filepath.Join(root, "Example.Movie.2024.mkv")
	if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}

	if f.s.catalogInLibrary(movie) {
		t.Fatal("search bypassed cached empty library")
	}
	f.s.catalogLibrary.Refresh()
	if !f.s.catalogInLibrary(movie) {
		t.Fatal("existing remote movie absent after refresh")
	}
	if err := f.s.requests.SaveDownload(requests.Download{Hash: "test", MediaType: "movie", TMDBID: 101, Target: "remote", Status: "downloading", FileSizes: map[string]int64{path: 100}}); err != nil {
		t.Fatal(err)
	}
	f.s.catalogLibrary.Refresh()
	if f.s.catalogInLibrary(movie) {
		t.Fatal("unfinished download marked in library")
	}
	if err := f.s.requests.SaveDownload(requests.Download{Hash: "test", MediaType: "movie", TMDBID: 101, Target: "remote", Status: "complete", FileSizes: map[string]int64{path: 100}}); err != nil {
		t.Fatal(err)
	}
	f.s.catalogLibrary.Refresh()
	if f.s.catalogInLibrary(movie) {
		t.Fatal("truncated download marked in library")
	}
	if err := f.s.requests.SaveDownload(requests.Download{Hash: "test", MediaType: "movie", TMDBID: 101, Target: "remote", Status: "complete", FileSizes: map[string]int64{path: 5}}); err != nil {
		t.Fatal(err)
	}
	f.s.catalogLibrary.Refresh()
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
	if !f.s.catalogInLibrary(movie) {
		t.Fatal("warm search unexpectedly read disk")
	}
	f.s.catalogLibrary.Refresh()
	if f.s.catalogInLibrary(movie) {
		t.Fatal("removed movie still marked in library after refresh")
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
	if f.s.catalogInLibrary(show) {
		t.Fatal("sample marked in library")
	}
	path := filepath.Join(root, "Example.Show.S01E01.mkv")
	if err := os.WriteFile(path, []byte("episode"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := f.s.requests.SaveDownload(requests.Download{Hash: "test", MediaType: "tv", TMDBID: 202, Status: "downloading", FileSizes: map[string]int64{path: 7}}); err != nil {
		t.Fatal(err)
	}
	f.s.catalogLibrary.Refresh()
	if f.s.catalogInLibrary(show) {
		t.Fatal("incomplete episode marked in library")
	}
	if err := f.s.requests.SaveDownload(requests.Download{Hash: "test", MediaType: "tv", TMDBID: 202, Status: "complete", FileSizes: map[string]int64{path: 7}}); err != nil {
		t.Fatal(err)
	}
	f.s.catalogLibrary.Refresh()
	if !f.s.catalogInLibrary(show) {
		t.Fatal("existing episode absent")
	}
}

func TestDownloadCompletionRefreshesCatalogCache(t *testing.T) {
	f := newWorkflowFixture(t, false)
	media := tmdb.Result{ID: 101, MediaType: "movie", Title: "Example Movie", ReleaseDate: "2024-01-01"}
	if f.s.catalogInLibrary(media) {
		t.Fatal("movie present before download")
	}
	f.s.catalogLibrary.Start(context.Background())
	defer f.s.catalogLibrary.Stop()
	createWorkflowJob(t, f, "alice", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	finishWorkflowDownload(t, f)
	// Physical files exist, but their unfinished record must still suppress them.
	f.s.catalogLibrary.Refresh()
	if f.s.catalogInLibrary(media) {
		t.Fatal("unfinished file marked present")
	}
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for !f.s.catalogInLibrary(media) {
		select {
		case <-deadline:
			t.Fatal("completion did not refresh catalog cache")
		case <-time.After(time.Millisecond):
		}
	}
}
