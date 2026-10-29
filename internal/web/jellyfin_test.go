package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cineroute/internal/config"
	"cineroute/internal/jellyfin"
)

func TestJellyfinOnlyReportsVerifiedDownloads(t *testing.T) {
	for _, tv := range []bool{false, true} {
		t.Run(map[bool]string{false: "movie", true: "tv"}[tv], func(t *testing.T) {
			f := newWorkflowFixture(t, tv)
			calls, requestsSeen := 0, 0
			var roots []string
			for _, drive := range f.cfg.Drives {
				roots = append(roots, drive.MovieRoot, drive.MovieRemoteRoot, drive.TVRoot, drive.TVRemoteRoot)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestsSeen++
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/Library/VirtualFolders":
					json.NewEncoder(w).Encode([]map[string]any{{"ItemId": "affected-library", "Locations": roots}})
				case r.Method == http.MethodPost && r.URL.Path == "/Items/affected-library/Refresh":
					calls++
					if r.URL.Query().Get("metadataRefreshMode") != "Default" || r.URL.Query().Get("replaceAllMetadata") != "false" {
						t.Errorf("unexpected refresh options: %s", r.URL.RawQuery)
					}
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected Jellyfin request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			var err error
			f.s.jellyfin, err = jellyfin.New(config.Jellyfin{Enabled: true, URL: srv.URL, APIKey: "key", StatePath: filepath.Join(t.TempDir(), "jellyfin.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer f.s.jellyfin.Close()
			createWorkflowJob(t, f, "alice", tv)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := f.s.queueJellyfinUpdates(); err != nil {
				t.Fatal(err)
			}
			if err := f.s.jellyfin.Flush(context.Background()); err != nil {
				t.Fatal(err)
			}
			if requestsSeen != 0 {
				t.Fatal("incomplete files notified Jellyfin")
			}
			finishWorkflowDownload(t, f)
			if err := f.s.engine.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := f.s.queueJellyfinUpdates(); err != nil {
					t.Fatal(err)
				}
				if err := f.s.jellyfin.Flush(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 1 {
				t.Fatalf("want one completion notification, got %d", calls)
			}
		})
	}
}
