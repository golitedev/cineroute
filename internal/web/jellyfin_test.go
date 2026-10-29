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
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var body struct{ Updates []struct{ Path string } }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if len(body.Updates) == 0 {
					t.Error("missing movie/episode folders")
				}
				for _, u := range body.Updates {
					if !filepath.IsAbs(u.Path) {
						t.Errorf("invalid path: %q", u.Path)
					}
				}
				w.WriteHeader(204)
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
			if calls != 0 {
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
