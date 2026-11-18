package charts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cineroute/internal/music/jobs"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

func TestParseKworbDailyChart(t *testing.T) {
	const firstID = "23XDZWXMOiRrm0qoKXCPbf"
	row := func(rank int, id string) string {
		return fmt.Sprintf(`<tr><td>%d</td><td>=</td><td><div>
			<a href="../artist/1.html">Køddy G</a> - <a href="../track/%s.html">Peli &amp; Terror</a>
			(w/ <a href="../artist/2.html">THE FAVORITES</a>)</div></td>%s</tr>`, rank, id, strings.Repeat("<td>1</td>", 8))
	}
	page := func(rows string) []byte {
		return []byte(`<span class="pagetitle">Spotify Daily Chart - Colombia - 2026/11/16 | <a>Totals</a></span>
		<table id="spotifydaily"><tbody>` + rows + `</tbody></table>`)
	}
	snapshot, err := parseKworb(page(row(1, firstID)))
	if err != nil || len(snapshot.Tracks) != 1 || snapshot.Date != "2026-11-16" {
		t.Fatalf("snapshot %+v, error %v", snapshot, err)
	}
	got := snapshot.Tracks[0]
	if got.Rank != 1 || got.SpotifyID != firstID || got.Title != "Peli & Terror" || !slices.Equal(got.Artists, []string{"Køddy G", "THE FAVORITES"}) {
		t.Fatalf("track %+v", got)
	}
	for _, broken := range [][]byte{page(row(2, firstID)), page(row(1, firstID) + row(2, firstID)), page(""), []byte("<h1>Unavailable</h1>")} {
		if _, err := parseKworb(broken); err == nil {
			t.Errorf("invalid chart accepted: %s", broken)
		}
	}
}

func TestMatchingPreservesTheRecordingVersion(t *testing.T) {
	track := Track{Title: "Nueva canción (Remix) (feat. Guest)", Artists: []string{"Ártist", "Guest"}}
	if title := titleWithoutCredit(track.Title, track.Artists); title != "Nueva canción (Remix)" {
		t.Fatalf("search title %q", title)
	}
	if !matchesTrack(track, "Nueva cancion - Remix", "Artist & Guest") ||
		matchesTrack(track, "Nueva canción", "Ártist") ||
		matchesTrack(track, "Nueva canción (Live)", "Ártist") {
		t.Fatal("incorrect version matching")
	}
}

type testQueue struct {
	job jobs.Job
}

func (q *testQueue) EnqueueChart(_ context.Context, id string, source jobs.ChartSource) (jobs.Job, error) {
	if q.job.ID == "" {
		q.job = jobs.Job{ID: "job-1", AlbumID: id, Title: "Release", Artist: "Ártist", Status: jobs.StatusQueued}
	}
	if id != q.job.AlbumID {
		return jobs.Job{}, fmt.Errorf("unexpected album %q", id)
	}
	if !slices.Contains(q.job.ChartSources, source) {
		q.job.ChartSources = append(q.job.ChartSources, source)
	}
	return q.job, nil
}
func (q *testQueue) Get(id string) (jobs.Job, error) {
	if q.job.ID == id {
		return q.job, nil
	}
	return jobs.Job{}, jobs.ErrNotFound
}
func (q *testQueue) PendingChartImports() []jobs.Job {
	if q.job.Status == jobs.StatusCompleted && !q.job.ChartLogged {
		return []jobs.Job{q.job}
	}
	return nil
}
func (q *testQueue) ConfirmChartImport(_ string) error { q.job.ChartLogged = true; return nil }

func TestDailyChartSyncPublishesVerifiedTracksAndKeepsImportHistory(t *testing.T) {
	ctx := t.Context()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	download := Track{SpotifyID: "download", Rank: 1, Title: "Nueva canción", Artists: []string{"Ártist", "Guest"}}
	existing := Track{SpotifyID: "existing", Rank: 2, Title: "Existing Song", Artists: []string{"Singer"}}
	ambiguous := Track{SpotifyID: "ambiguous", Rank: 3, Title: "Rock (Remix)", Artists: []string{"Band"}}

	var mu sync.Mutex
	local := map[string][]navidrome.Song{
		existing.Title: {{ID: "nd-existing", Title: existing.Title, Artist: "Singer"}},
		download.Title: {{ID: "nd-wrong", Title: "Nueva canción (Live)", Artist: "Ártist"}},
	}
	remote := make(map[string]navidrome.Playlist)
	qobuzLookups := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/catalog/search" {
			query := r.URL.Query().Get("query")
			qobuzLookups[query]++
			switch query {
			case "Nueva canción Ártist":
				fmt.Fprint(w, `{"tracks":{"items":[{"id":99,"title":"Nueva cancion (feat. Guest)","isrc":"TEST00000001","media_number":2,"track_number":3,"duration":200,"streamable":true,"performer":{"name":"Artist"},"album":{"id":"release","title":"Release"}}]}}`)
			case "Rock (Remix) Band":
				fmt.Fprint(w, `{"tracks":{"items":[
					{"id":100,"title":"Rock","version":"Remix","isrc":"TEST00000002","streamable":true,"performer":{"name":"Band"},"album":{"id":"one","title":"One"}},
					{"id":101,"title":"Rock (Remix)","isrc":"TEST00000003","streamable":true,"performer":{"name":"Band"},"album":{"id":"two","title":"Two"}},
					{"id":102,"title":"Rock","streamable":true,"performer":{"name":"Band"},"album":{"id":"three","title":"Three"}}
				]}}`)
			default:
				t.Errorf("unexpected catalog query %s", query)
				http.Error(w, "unexpected request", 500)
			}
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("u") != "admin" || r.Form.Get("t") == "" {
			t.Errorf("expected authenticated Navidrome request to %s", r.URL.Path)
		}
		result := map[string]any{"status": "ok"}
		switch r.URL.Path {
		case "/rest/getUser":
			result["user"] = map[string]bool{"adminRole": true}
		case "/rest/search3":
			if r.Form.Get("musicFolderId") != "shared" {
				t.Error("search did not use configured music folder")
			}
			result["searchResult3"] = map[string]any{"song": local[r.Form.Get("query")]}
		case "/rest/getScanStatus":
			result["scanStatus"] = map[string]bool{"scanning": false}
		case "/rest/startScan":
		case "/rest/getPlaylists":
			var list []navidrome.Playlist
			for _, playlist := range remote {
				list = append(list, playlist)
			}
			result["playlists"] = map[string]any{"playlist": list}
		case "/rest/getPlaylist":
			id := r.Form.Get("id")
			if playlist, ok := remote[id]; ok {
				result["playlist"] = playlist
			} else {
				result = map[string]any{"status": "failed", "error": map[string]int{"code": 70}}
			}
		case "/rest/createPlaylist":
			id := r.Form.Get("playlistId")
			p := remote[id]
			if id == "" {
				id = fmt.Sprintf("playlist-%d", len(remote)+1)
				p = navidrome.Playlist{ID: id, Owner: "admin", Name: r.Form.Get("name")}
			}
			p.Songs = nil
			for _, song := range r.Form["songId"] {
				p.Songs = append(p.Songs, navidrome.Song{ID: song})
			}
			remote[id] = p
			result["playlist"] = p
		case "/rest/updatePlaylist":
			id := r.Form.Get("playlistId")
			p := remote[id]
			p.Name, p.Comment, p.Public = r.Form.Get("name"), r.Form.Get("comment"), r.Form.Get("public") == "true"
			remote[id] = p
		case "/rest/deletePlaylist":
			delete(remote, r.Form.Get("id"))
		default:
			t.Errorf("unexpected Navidrome endpoint %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"subsonic-response": result}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()

	nav := navidrome.New(server.URL, "admin", "password")
	nav.MusicFolderID = "shared"
	catalog := qobuz.New("user", "token", func() string { return "app-id" })
	catalog.BaseURL = server.URL
	queue := new(testQueue)
	dir := t.TempDir()
	manager, err := New(dir, catalog, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { manager.Close() }()
	colombia := Snapshot{Date: "2026-11-16", Tracks: []Track{download, existing, ambiguous}}
	sweden := Snapshot{Date: "2026-11-16", Tracks: []Track{download}}
	fetchFails := false
	fetch := func(_ context.Context, country string) (Snapshot, error) {
		if fetchFails {
			return Snapshot{}, errors.New("Kworb unavailable")
		}
		if country == "co" {
			return colombia, nil
		}
		return sweden, nil
	}
	manager.fetch = fetch

	co, err := manager.Configure(ctx, "", Settings{Country: "co", Limit: 3, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	se, err := manager.Configure(ctx, "", Settings{Country: "se", Limit: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) playlist {
		t.Helper()
		p, err := manager.playlist(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	checkRemote := func(id string, expected ...string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		p := remote[id]
		if !p.Public || p.Owner != "admin" || !sameSongs(p.Songs, expected) {
			t.Fatalf("Navidrome playlist %+v, expected songs %v", p, expected)
		}
	}
	manager.runOnce(ctx, now)
	current := read(co.ID)
	colombiaRemoteID := current.NavidromeID
	checkRemote(colombiaRemoteID, "nd-existing")
	lookups := func(query string) int {
		mu.Lock()
		defer mu.Unlock()
		return qobuzLookups[query]
	}
	if len(queue.job.ChartSources) != 2 || lookups("Nueva canción Ártist") != 1 ||
		current.Entries[2].State != "needs_review" || read(se.ID).NavidromeID != "" {
		t.Fatalf("queue %+v, chart %+v", queue.job, current.Entries)
	}

	queue.job.Status, queue.job.MessageCode = jobs.StatusCompleted, "added_incomplete"
	queue.job.FinishedAt = &now
	now = now.Add(workInterval)
	manager.runOnce(ctx, now)
	if got := read(co.ID).Entries[0].State; got != "indexing" {
		t.Fatalf("state = %s, wanted indexing", got)
	}
	checkRemote(colombiaRemoteID, "nd-existing")
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	manager, err = New(dir, catalog, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	manager.fetch = fetch
	mu.Lock()
	local[download.Title] = append(local[download.Title],
		navidrome.Song{ID: "nd-wrong-isrc", Title: download.Title, Artist: "Artist", Album: "Release", Track: 3, Disc: 2, Duration: 200, ISRC: []string{"OTHER"}},
		navidrome.Song{ID: "nd-downloaded", Title: download.Title, Artist: "Artist", Album: "Release", Track: 3, Disc: 2, Duration: 200, ISRC: []string{"TEST00000001"}},
	)
	mu.Unlock()
	now = now.Add(workInterval)
	manager.runOnce(ctx, now)
	checkRemote(colombiaRemoteID, "nd-downloaded", "nd-existing")
	checkRemote(read(se.ID).NavidromeID, "nd-downloaded")
	imports, more, err := manager.Imports(ctx, 0)
	if err != nil || more || len(imports) != 1 || !imports[0].Partial || len(imports[0].Sources) != 2 {
		t.Fatalf("imports %+v more=%v error=%v", imports, more, err)
	}

	colombia.Date = "2026-11-17"
	existing.Rank, download.Rank = 1, 2
	colombia.Tracks = []Track{existing, download, ambiguous}
	now = now.Add(chartInterval)
	manager.runOnce(ctx, now)
	checkRemote(colombiaRemoteID, "nd-existing", "nd-downloaded")
	if current := read(co.ID); current.PublishedDate != colombia.Date || current.NavidromeID != colombiaRemoteID {
		t.Fatalf("new daily chart %+v", current)
	}
	fetchFails = true
	manager.runOnce(ctx, now.Add(chartInterval))
	checkRemote(colombiaRemoteID, "nd-existing", "nd-downloaded")
	if read(co.ID).FetchError == "" {
		t.Fatal("missing Kworb failure")
	}
	if err := manager.Remove(ctx, co.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.playlist(ctx, co.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed configuration is still available: %v", err)
	}
	_, err = nav.Playlist(ctx, colombiaRemoteID)
	if !errors.Is(err, navidrome.ErrNotFound) {
		t.Fatalf("removed Navidrome playlist is still available: %v", err)
	}
}
