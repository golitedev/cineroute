package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/copies"
	"cineroute/internal/prowlarr"
)

func TestLibraryCopiesScanAndHardlinkWithoutTrackerRequests(t *testing.T) {
	var trackerRequests atomic.Int32
	tracker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackerRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer tracker.Close()
	base := t.TempDir()
	cfg := config.Default()
	cfg.Subtitles.Enabled = false
	cfg.Drives = []config.Drive{{ID: "disk1", MovieRoot: filepath.Join(base, "movies"), MovieRemoteRoot: filepath.Join(base, "movies-remote"), TVRoot: filepath.Join(base, "tv"), TVRemoteRoot: filepath.Join(base, "tv-remote")}}
	for _, root := range []string{cfg.Drives[0].MovieRoot, cfg.Drives[0].MovieRemoteRoot, cfg.Drives[0].TVRoot, cfg.Drives[0].TVRemoteRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	handler := testAdminHandler(New(cfg, nil, nil, prowlarr.New(tracker.URL, "configured-key", time.Second)))
	for _, tc := range []struct {
		kind, root, destination, file string
	}{
		{"movie", cfg.Drives[0].MovieRoot, cfg.Drives[0].MovieRemoteRoot, "Movie.mkv"},
		{"tv", cfg.Drives[0].TVRemoteRoot, cfg.Drives[0].TVRoot, "Season 1/Show.S01E01.mkv"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			source := filepath.Join(tc.root, "Title (2024)", tc.file)
			if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte("video"), 0o644); err != nil {
				t.Fatal(err)
			}
			scan := httptest.NewRecorder()
			handler.ServeHTTP(scan, httptest.NewRequest(http.MethodPost, "/api/copies/scan?type="+tc.kind, nil))
			var view copies.View
			if err := json.Unmarshal(scan.Body.Bytes(), &view); err != nil || scan.Code != http.StatusOK || len(view.Items) != 1 || view.MediaType != tc.kind {
				t.Fatalf("scan = %d %s; error %v", scan.Code, scan.Body.String(), err)
			}
			link := httptest.NewRecorder()
			handler.ServeHTTP(link, httptest.NewRequest(http.MethodPost, "/api/copies/"+view.Items[0].ID+"/hardlink?type="+tc.kind, nil))
			if link.Code != http.StatusOK {
				t.Fatalf("link = %d %s", link.Code, link.Body.String())
			}
			sourceInfo, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			targetInfo, err := os.Stat(filepath.Join(tc.destination, "Title (2024)", tc.file))
			if err != nil || !os.SameFile(sourceInfo, targetInfo) {
				t.Fatalf("destination is not a hardlink: %v", err)
			}
		})
	}
	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if !strings.Contains(status.Body.String(), `"prowlarr":"configured"`) || trackerRequests.Load() != 0 {
		t.Fatalf("Prowlarr status = %s; tracker calls = %d", status.Body.String(), trackerRequests.Load())
	}
	page := httptest.NewRecorder()
	handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(page.Body.String(), `id="moviesTab"`) || !strings.Contains(page.Body.String(), `id="tvTab"`) {
		t.Fatal("movie and TV tabs are missing")
	}
	for _, removed := range []string{"Companions", "Find companion", "/api/companions", "LAT-Team", "Search next batch", "Clear reviews"} {
		if strings.Contains(page.Body.String(), removed) {
			t.Errorf("removed search feature remains in page: %s", removed)
		}
	}
	for _, path := range []string{"/api/companions", "/api/companions/search-missing", "/api/companions/item/search", "/api/companions/item/approve", "/api/intakes/item/companion-search"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, nil))
			if response.Code != http.StatusNotFound {
				t.Errorf("removed route %s %s returned %d", method, path, response.Code)
			}
		}
	}
}
