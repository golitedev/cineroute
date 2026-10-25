package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cineroute/internal/requests"
	"cineroute/internal/tmdb"
)

type historyResponse struct {
	Entries    []requestHistoryEntry `json:"entries"`
	Page       int                   `json:"page"`
	TotalPages int                   `json:"total_pages"`
	Total      int                   `json:"total"`
	Counts     map[string]int        `json:"counts"`
}

func readHistory(t *testing.T, s *Server, query string) historyResponse {
	t.Helper()
	out := userWorkflowCall(t, s, "admin", "GET", "/api/requests/history"+query, "")
	if out.Code != 200 {
		t.Fatalf("history: %d %s", out.Code, out.Body.String())
	}
	var response historyResponse
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}
func TestRequestHistoryRetainsSourceCompletionAndSharedAttribution(t *testing.T) {
	f := newWorkflowFixture(t, false)
	alice := createWorkflowJob(t, f, "alice", false)
	createWorkflowJob(t, f, "bob", false)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	searches := f.searches
	f.mu.Unlock()
	history := readHistory(t, f.s, "")
	if len(history.Entries) != 2 {
		t.Fatalf("shared requests missing: %+v", history)
	}
	for _, entry := range history.Entries {
		if len(entry.Downloads) != 1 {
			t.Fatalf("missing shared download for %s", entry.Owner)
		}
		d := entry.Downloads[0]
		if d.RequestedBy != "alice" || d.RequestID != alice.ID || d.Tracker != "Selected tracker" || d.TrackerURL != "https://tracker.example/torrents/42" {
			t.Fatalf("incorrect provenance: %+v", d)
		}
	}
	f.mu.Lock()
	after := f.searches
	f.mu.Unlock()
	if after != searches {
		t.Fatal("reading history triggered tracker searches")
	}
	finishWorkflowDownload(t, f)
	if err := f.s.engine.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	history = readHistory(t, f.s, "?status=complete")
	if history.Total != 2 {
		t.Fatalf("completed requests absent: %+v", history)
	}
	for _, entry := range history.Entries {
		if entry.Downloads[0].CompletedAt.IsZero() || entry.Downloads[0].Status != "complete" {
			t.Fatalf("completion not recorded: %+v", entry)
		}
	}
	persisted := requests.NewStore(f.cfg.Requests.StatePath)
	downloads, err := persisted.Downloads()
	if err != nil || len(downloads) != 1 || downloads[0].RequestedBy != "alice" || downloads[0].CompletedAt.IsZero() {
		t.Fatalf("source/completion did not survive store reload: %+v %v", downloads, err)
	}
	public := userWorkflowCall(t, f.s, "alice", "GET", "/api/user/requests", "")
	for _, secret := range []string{"tracker.example", "requested_by", "private-download-key", "torrent_data", "content_path"} {
		if strings.Contains(public.Body.String(), secret) {
			t.Errorf("public response leaked %s", secret)
		}
	}
}
func TestRequestHistoryFilteringPaginationAndSafeProjection(t *testing.T) {
	s := securityServer(t)
	for i := 1; i <= 53; i++ {
		status := "complete"
		if i == 1 {
			status = "downloading"
		}
		if i == 2 {
			status = "waiting"
		}
		job, err := s.requests.Create(requests.Job{Owner: "alice", Media: tmdb.Details{Result: tmdb.Result{ID: i, MediaType: "movie", Title: fmt.Sprintf("History Movie %d", i)}}, Target: "remote", Status: status, Message: "Waiting for a matching release", NextSearch: time.Now().Add(time.Hour)}, requests.Defaults(), true)
		if err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			err = s.requests.SaveDownload(requests.Download{Hash: "stalled-hash", RequestID: job.ID, RequestedBy: "alice", MediaType: "movie", TMDBID: i, Target: "remote", Title: "A release", Indexer: "My Tracker", InfoURL: "https://tracker.example/torrents/42", Status: "downloading", TorrentState: "stalledDL", Progress: .25, AddedAt: time.Now(), LastProgress: time.Now(), TorrentData: []byte("PRIVATE_TORRENT_BYTES"), SavePath: "/private/array/path", Files: []string{"/private/file"}})
			if err != nil {
				t.Fatal(err)
			}
			if err = s.requests.SaveDownload(requests.Download{Hash: "other-destination", MediaType: "movie", TMDBID: i, Target: "main", Title: "Wrong destination", Status: "complete"}); err != nil {
				t.Fatal(err)
			}
			// Older attempts remain visible, without guessing a source user or URL.
			if err = s.requests.SaveDownload(requests.Download{Hash: "old-attempt", MediaType: "movie", TMDBID: i, Target: "remote", Title: "Older attempt", Status: "failed", Error: "No peers", AddedAt: time.Now().Add(-time.Hour), InfoURL: "https://tracker.example/download?apikey=PRIVATE_API_KEY"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	first := readHistory(t, s, "")
	second := readHistory(t, s, "?page=2")
	if first.Total != 53 || len(first.Entries) != 50 || second.TotalPages != 2 || len(second.Entries) != 3 || first.Counts["stalled"] != 1 || first.Counts["waiting"] != 1 {
		t.Fatalf("pagination/counts: %+v / %+v", first, second)
	}
	seen := map[string]bool{}
	for _, entry := range first.Entries {
		seen[entry.Request.ID] = true
	}
	for _, entry := range second.Entries {
		if seen[entry.Request.ID] {
			t.Fatal("duplicate history page entry")
		}
	}
	history := readHistory(t, s, "?status=stalled&q=My%20Tracker")
	if history.Total != 1 || len(history.Entries[0].Downloads) != 2 || history.Entries[0].Owner != "alice" {
		t.Fatalf("wrong filter/scope: %+v", history)
	}
	if history.Entries[0].Downloads[1].RequestedBy != "" || history.Entries[0].Downloads[1].TrackerURL != "" {
		t.Fatal("legacy source inferred or secret URL published")
	}
	if readHistory(t, s, "?status=waiting").Total != 1 || readHistory(t, s, "?q=nothing-matches").Total != 0 {
		t.Fatal("status/empty search filter failed")
	}
	out := userWorkflowCall(t, s, "admin", "GET", "/api/requests/history?status=stalled", "")
	for _, secret := range []string{"PRIVATE_TORRENT_BYTES", "PRIVATE_API_KEY", "/private/array/path", "/private/file", "torrent_data", "download_url", "save_path"} {
		if strings.Contains(out.Body.String(), secret) {
			t.Errorf("history contains recovery/private data: %s", secret)
		}
	}
	for _, query := range []string{"?page=0", "?page=abc", "?status=made-up", "?q=" + strings.Repeat("a", 201)} {
		if response := userWorkflowCall(t, s, "admin", "GET", "/api/requests/history"+query, ""); response.Code != 400 {
			t.Errorf("invalid query %q HTTP %d", query, response.Code)
		}
	}
}
func TestRequestHistoryAccessBoundary(t *testing.T) {
	s := securityServer(t)
	s.cfg.Admin.AllowedNetworks = []string{"127.0.0.0/8", "192.168.18.0/24"}
	for _, tc := range []struct {
		handler http.Handler
		user    string
		remote  string
	}{{s.Handler(), "alice", "192.168.18.50:12345"}, {s.Handler(), "admin", "172.21.0.2:12345"}, {s.PublicHandler(), "admin", "192.168.18.50:12345"}, {s.PublicHandler(), "alice", "192.168.18.50:12345"}, {s.Handler(), "", "192.168.18.50:12345"}} {
		r := securityRequest("GET", "/api/requests/history")
		r.RemoteAddr = tc.remote
		if tc.user != "" {
			r.SetBasicAuth(tc.user, tc.user+"-secret")
		}
		out := httptest.NewRecorder()
		tc.handler.ServeHTTP(out, r)
		if out.Code < 400 {
			t.Errorf("history exposed to %q from %s: %d", tc.user, tc.remote, out.Code)
		}
	}
}
func TestHistoryDownloadStates(t *testing.T) {
	now := time.Now()
	settings := requests.Defaults()
	for _, tc := range []struct {
		name     string
		download requests.Download
		want     string
	}{
		{"stalled", requests.Download{Status: "downloading", TorrentState: "stalledDL", LastProgress: now}, "stalled"},
		{"paused", requests.Download{Status: "downloading", TorrentState: "stalledDL", Paused: true}, "paused"},
		{"queued", requests.Download{Status: "downloading", TorrentState: "queuedDL", LastProgress: now.Add(-24 * time.Hour)}, "queued"},
		{"checking", requests.Download{Status: "downloading", TorrentState: "checkingDL"}, "checking"},
		{"metadata", requests.Download{Status: "downloading", TorrentState: "metaDL"}, "metadata"},
		{"no progress", requests.Download{Status: "downloading", LastProgress: now.Add(-24 * time.Hour)}, "stalled"},
		{"failed stays failed", requests.Download{Status: "failed", TorrentState: "stalledDL"}, "failed"},
		{"completed stays complete", requests.Download{Status: "complete", TorrentState: "stalledDL"}, "complete"},
		{"verification error", requests.Download{Status: "planned", Error: "needs review"}, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := historyDownloadState(tc.download, settings, now); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
	if got := historyRequestState(requests.Job{Status: "downloading"}, []requestHistoryDownload{{Status: "failed"}, {Status: "downloading"}}); got != "downloading" {
		t.Fatalf("old failed attempt hid replacement: %s", got)
	}
	if got := historyRequestState(requests.Job{Status: "error"}, []requestHistoryDownload{{Status: "planned"}}); got != "error" {
		t.Fatalf("planned download hid a request requiring administrator attention: %s", got)
	}
}
func TestTrackerPageURLRejectsUnsafeAndCredentialLinks(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "data:text/html,hello", "//tracker.example/42", "/download/42", "https://user:secret@tracker.example/42", "https://tracker.example/42?api_key=secret", "https://tracker.example/42?passkey=secret", "https://tracker.example/42?authToken=secret", "https://tracker.example/42\n", "https://tracker.example/42?x=%zz"} {
		if got := trackerPageURL(raw); got != "" {
			t.Errorf("accepted %q as %q", raw, got)
		}
	}
	for _, raw := range []string{"https://tracker.example/torrents/42", "https://tracker.example/torrents.php?id=42&torrentid=9", "http://tracker.example/details/42#comments"} {
		if got := trackerPageURL(raw); got != raw {
			t.Errorf("rejected tracker details link %q", raw)
		}
	}
}
