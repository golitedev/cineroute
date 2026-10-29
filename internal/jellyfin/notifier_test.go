package jellyfin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"cineroute/internal/config"
)

func TestDurableBatchedDeliveryAndRetry(t *testing.T) {
	calls := 0
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/jellyfin/Library/Media/Updated" || r.Method != "POST" || r.Header.Get("Authorization") != `MediaBrowser Token="secret"` {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var payload struct{ Updates []update }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if len(payload.Updates) != 1 || payload.Updates[0].Path != "/remote/nested/Movie" || payload.Updates[0].UpdateType != "Modified" {
			t.Errorf("unexpected updates: %+v", payload)
		}
		if fail {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer srv.Close()
	cfg := config.Jellyfin{Enabled: true, URL: srv.URL + "/jellyfin/", APIKey: "secret", StatePath: filepath.Join(t.TempDir(), "queue.db"), PathMappings: []config.JellyfinPathMapping{{Local: "/local", Remote: "/other"}, {Local: "/local/movies", Remote: "/remote/nested"}}}
	n, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"download", "subtitle", "download"} {
		if err := n.Enqueue(id, "/local/movies/Movie"); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Flush(context.Background()); err == nil {
		t.Fatal("503 must retain pending work")
	}
	if err := n.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("retry backoff ignored")
	}
	n.Close()
	n, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if _, err = n.db.Exec("UPDATE notifications SET next_attempt=0"); err != nil {
		t.Fatal(err)
	}
	fail = false
	if err := n.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := n.Enqueue("download", "/local/movies/Movie"); err != nil {
		t.Fatal(err)
	}
	if err := n.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("delivered event repeated")
	}
	if got := n.mapped("/local/movies-other/Movie"); got != "/other/movies-other/Movie" {
		t.Fatalf("mapping escaped root boundary: %s", got)
	}
}

func TestNewEventsDuringDeliveryAreRetained(t *testing.T) {
	var n *Notifier
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			if err := n.Enqueue("later", "/movies/Same"); err != nil {
				t.Error(err)
			}
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	var err error
	n, err = New(config.Jellyfin{Enabled: true, URL: srv.URL, APIKey: "key", StatePath: filepath.Join(t.TempDir(), "queue.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := n.Enqueue("first", "/movies/Same"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := n.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("new change was acknowledged by an older request")
	}
}

func TestRedirectDoesNotReceiveCredentials(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer srv.Close()
	n, err := New(config.Jellyfin{Enabled: true, URL: srv.URL, APIKey: "key", StatePath: filepath.Join(t.TempDir(), "queue.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer n.Close()
	if err := n.Enqueue("event", "/movies/Movie"); err != nil {
		t.Fatal(err)
	}
	if err := n.Flush(context.Background()); err == nil {
		t.Fatal("redirect should remain pending")
	}
	if leaked {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestDisabledIsNoop(t *testing.T) {
	n, err := New(config.Jellyfin{})
	if err != nil || n != nil {
		t.Fatal("disabled notifier initialized")
	}
	if err = n.Enqueue("", ""); err != nil {
		t.Fatal(err)
	}
	n.Start(context.Background(), nil)
	n.Close()
}
