package prowlarr

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newIPv4TestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}

func TestClientSearchUsesAPIKeyAndIndexer(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Errorf("API key header = %q", r.Header.Get("X-Api-Key"))
		}
		if r.URL.Path != "/api/v1/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("query") != "Dune 2021" || q.Get("type") != "search" || q.Get("indexerIds") != "7" || q.Get("limit") != "50" {
			t.Errorf("query = %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"guid":"g","title":"Dune.2021.1080p.WEB-DL","size":12,"indexerId":7,"seeders":4}]`))
	}))
	client := New(server.URL, "secret", time.Second)
	got, err := client.Search(context.Background(), 7, "Dune 2021", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Guid != "g" || got[0].Seeders == nil || *got[0].Seeders != 4 {
		t.Fatalf("releases = %+v", got)
	}
}

func TestDownloadTorrentHonorsLimit(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			t.Errorf("API key header = %q", r.Header.Get("X-Api-Key"))
		}
		w.Write([]byte("123456"))
	}))
	client := New(server.URL, "secret", time.Second, 5)
	if _, err := client.DownloadTorrent(context.Background(), "/api/v1/download/g"); err == nil || !strings.Contains(err.Error(), "download limit") {
		t.Fatalf("expected download limit error, got %v", err)
	}
}

func TestDownloadTorrentDoesNotLeakURLInErrors(t *testing.T) {
	server := newIPv4TestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	client := New(server.URL, "secret", time.Second)
	secretURL := server.URL + "/download?apikey=secret"
	if _, err := client.DownloadTorrent(context.Background(), secretURL); err == nil || strings.Contains(err.Error(), url.QueryEscape("secret")) || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("download error leaked URL: %v", err)
	}
}
