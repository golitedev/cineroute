package jellyfin

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cineroute/internal/config"
)

func TestDownloadRefreshesBatchByMappedLibraryAndRetryIndependently(t *testing.T) {
	calls := map[string]int{}
	failTV := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		if r.Header.Get("Authorization") != `MediaBrowser Token="key"` {
			t.Error("missing authorization")
		}
		switch r.URL.Path {
		case "/base/Library/VirtualFolders":
			if r.Method != "GET" {
				t.Error("library discovery must use GET")
			}
			json.NewEncoder(w).Encode([]virtualFolder{
				{ItemID: "remote-movies", Locations: []string{"/jellyfin/remote/disk1", "/jellyfin/remote/disk2"}},
				{ItemID: "main-movies", Locations: []string{"/jellyfin/main"}},
				{ItemID: "remote-tv", Locations: []string{"/jellyfin/tv"}},
			})
		case "/base/Items/remote-movies/Refresh", "/base/Items/remote-tv/Refresh":
			if r.Method != "POST" || r.URL.Query().Get("metadataRefreshMode") != "Default" || r.URL.Query().Get("imageRefreshMode") != "Default" || r.URL.Query().Get("replaceAllMetadata") != "false" || r.URL.Query().Get("replaceAllImages") != "false" {
				t.Error("unsafe or invalid refresh options")
			}
			if r.URL.Path == "/base/Items/remote-tv/Refresh" && failTV {
				w.WriteHeader(503)
			} else {
				w.WriteHeader(204)
			}
		case "/base/Library/Media/Updated":
			var body struct{ Updates []update }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.Updates) != 1 || body.Updates[0].Path != "/jellyfin/remote/disk1/Movie" {
				t.Errorf("unexpected subtitle update: %+v", body)
			}
			w.WriteHeader(204)
		default:
			t.Errorf("unrelated library or unexpected endpoint refreshed: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	cfg := config.Jellyfin{Enabled: true, URL: srv.URL + "/base/", APIKey: "key", StatePath: filepath.Join(t.TempDir(), "queue.db"), PathMappings: []config.JellyfinPathMapping{{Local: "/local", Remote: "/jellyfin"}}}
	n, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct{ id, path string }{{"movie1", "/local/remote/disk1/Movie"}, {"movie2", "/local/remote/disk2/Movie2"}, {"episode", "/local/tv/Show/Season 1"}} {
		if err := n.EnqueueDownload(e.id, e.path); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Enqueue("subtitle1", "/local/remote/disk1/Movie"); err != nil {
		t.Fatal(err)
	}
	if err := n.Enqueue("subtitle2", "/local/remote/disk1/Movie"); err != nil {
		t.Fatal(err)
	}
	if err := n.Flush(context.Background()); err == nil {
		t.Fatal("failed TV refresh was acknowledged")
	}
	if calls["/base/Items/remote-movies/Refresh"] != 1 || calls["/base/Library/Media/Updated"] != 1 {
		t.Fatalf("batching failed: %v", calls)
	}
	n.Close()
	n, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if _, err = n.db.Exec("UPDATE notifications SET next_attempt=0 WHERE delivered=0"); err != nil {
		t.Fatal(err)
	}
	failTV = false
	if err := n.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls["/base/Items/remote-tv/Refresh"] != 2 || calls["/base/Items/remote-movies/Refresh"] != 1 || calls["/base/Library/Media/Updated"] != 1 {
		t.Fatalf("successful work repeated on retry: %v", calls)
	}
	if err := n.EnqueueDownload("movie1", "/local/remote/disk1/Movie"); err != nil {
		t.Fatal(err)
	}
	if err := n.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls["/base/Items/remote-movies/Refresh"] != 1 {
		t.Fatal("completed download refreshed repeatedly")
	}
}

func TestLibraryLookupFailureDoesNotBlockSubtitles(t *testing.T) {
	for _, mode := range []string{"unavailable", "invalid", "no_match"} {
		t.Run(mode, func(t *testing.T) {
			updates := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Library/VirtualFolders":
					switch mode {
					case "unavailable":
						w.WriteHeader(503)
					case "invalid":
						w.Write([]byte("not json"))
					case "no_match":
						json.NewEncoder(w).Encode([]virtualFolder{{ItemID: "other", Locations: []string{"/movies-other"}}})
					}
				case "/Library/Media/Updated":
					updates++
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected fallback scan: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			n, err := New(config.Jellyfin{Enabled: true, URL: srv.URL, APIKey: "key", StatePath: filepath.Join(t.TempDir(), "queue.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer n.Close()
			if err := n.EnqueueDownload("download", "/movies/Movie"); err != nil {
				t.Fatal(err)
			}
			if err := n.Enqueue("subtitle", "/movies/Movie"); err != nil {
				t.Fatal(err)
			}
			if err := n.Flush(context.Background()); err == nil {
				t.Fatal("library lookup failure discarded")
			}
			var delivered int
			if err := n.db.QueryRow("SELECT delivered FROM notifications WHERE id='subtitle'").Scan(&delivered); err != nil {
				t.Fatal(err)
			}
			if delivered != 1 || updates != 1 {
				t.Fatal("library failure blocked subtitles")
			}
		})
	}
}

func TestExistingQueueMigrationPreservesReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "queue.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE notifications(id TEXT PRIMARY KEY,path TEXT NOT NULL,delivered INTEGER NOT NULL DEFAULT 0,attempts INTEGER NOT NULL DEFAULT 0,next_attempt INTEGER NOT NULL DEFAULT 0);
 INSERT INTO notifications(id,path,delivered) VALUES('download:pending','/movies/New',0),('download:done','/movies/Old',1),('subtitle:pending','/movies/Sub',0);`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	cfg := config.Jellyfin{Enabled: true, URL: "http://jellyfin:8096", APIKey: "key", StatePath: path}
	n, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		id                 string
		refresh, delivered int
	}{{"download:pending", 1, 0}, {"download:done", 0, 1}, {"subtitle:pending", 0, 0}} {
		var refresh, delivered int
		if err := n.db.QueryRow("SELECT refresh_library,delivered FROM notifications WHERE id=?", want.id).Scan(&refresh, &delivered); err != nil {
			t.Fatal(err)
		}
		if refresh != want.refresh || delivered != want.delivered {
			t.Fatalf("bad migration for %s: %d %d", want.id, refresh, delivered)
		}
	}
	if err := n.EnqueueDownload("download:done", "/movies/Old"); err != nil {
		t.Fatal(err)
	}
	n.Close()
	n, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	var count int
	if err := n.db.QueryRow("SELECT count(*) FROM notifications WHERE delivered=0").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("migration replayed receipts: %d", count)
	}
}

func TestMatchingLibrariesPrefersSpecificRootAndRespectsBoundaries(t *testing.T) {
	libs := []virtualFolder{{ItemID: "all", Locations: []string{"/media"}}, {ItemID: "remote", Locations: []string{"/media/movies-remote"}}, {ItemID: "main", Locations: []string{"/media/movies"}}}
	ids := matchingLibraries("/media/movies-remote/Movie", libs)
	if len(ids) != 1 || ids[0] != "remote" {
		t.Fatalf("wrong library selected: %v", ids)
	}
	ids = matchingLibraries("/media/movies-remote-other/Movie", libs)
	if len(ids) != 1 || ids[0] != "all" {
		t.Fatalf("root boundary ignored: %v", ids)
	}
}
