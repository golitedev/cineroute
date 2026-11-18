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

	"cineroute/internal/music/config"
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
	if trackMatch(track, "Nueva cancion - Remix", "Artist & Guest") != 2 ||
		trackMatch(track, "Nueva canción", "Ártist") != 0 ||
		trackMatch(track, "Nueva canción (Live)", "Ártist") != 0 {
		t.Fatal("incorrect version matching")
	}
}

func TestTrackMatchChecksAllDeclaredCredits(t *testing.T) {
	track := Track{Title: "Song (feat. Guest)", Artists: []string{"Earth, Wind & Fire", "Guest"}}
	for _, tt := range []struct {
		title, artist string
		want          int
	}{
		{"Song", "Earth, Wind & Fire", 1},
		{"Song", "Earth, Wind & Fire, Guest", 2},
		{"Song (feat. Guest)", "Earth, Wind & Fire", 2},
		{"Song (feat. Guest & Other)", "Earth, Wind & Fire", 0},
		{"Song", "Earth, Wind & Fire, Other", 0},
		{"Song (Live)", "Earth, Wind & Fire, Guest", 0},
		{"Song", "Earth, Guest", 0},
	} {
		if got := trackMatch(track, tt.title, tt.artist); got != tt.want {
			t.Errorf("trackMatch(%q, %q) = %d, want %d", tt.title, tt.artist, got, tt.want)
		}
	}
}

func TestChooseRecordingChecksCreditsAndConflictingCopies(t *testing.T) {
	track := Track{Title: "Song", Artists: []string{"Lead", "Guest"}}
	partial := qobuz.Recording{ID: "partial", AlbumID: "album", Title: "Song", Artist: "Lead", Streamable: true}
	complete := qobuz.Recording{ID: "complete", AlbumID: "album", Title: "Song", Artist: "Lead & Guest", ISRC: "USAAA1234567", Duration: 200, Streamable: true}
	unavailable := complete
	unavailable.Streamable = false
	equivalent := complete
	equivalent.ID, equivalent.ISRC, equivalent.Duration = "equivalent", " us-aaa-12-34567 ", 201
	conflicting := unavailable
	conflicting.ID, conflicting.Duration = "conflicting", 220
	wrongCode := unavailable
	wrongCode.ID, wrongCode.ISRC = "wrong-code", "USAAA7654321"
	for _, tt := range []struct {
		name       string
		candidates []qobuz.Recording
		want       string
		reason     string
	}{
		{"prefer all credits", []qobuz.Recording{partial, complete}, "complete", ""},
		{"use an available copy of the same recording", []qobuz.Recording{unavailable, equivalent}, "equivalent", ""},
		{"keep the better match when unavailable", []qobuz.Recording{partial, unavailable}, "", "unavailable"},
		{"detect a different ISRC even when unavailable", []qobuz.Recording{complete, wrongCode}, "", "Multiple"},
		{"detect inconsistent duration even with the same ISRC", []qobuz.Recording{complete, conflicting}, "", "Multiple"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := chooseRecording(track, tt.candidates)
			if got.ID != tt.want || (tt.reason == "" && reason != "") || !strings.Contains(reason, tt.reason) {
				t.Fatalf("recording = %q, reason = %q, want %q and %q", got.ID, reason, tt.want, tt.reason)
			}
		})
	}
}

func TestChooseRecordingRejectsAcousticDuplicates(t *testing.T) {
	track := Track{Title: "Beauty And A Beat", Artists: []string{"Justin Bieber", "Nicki Minaj"}}
	original := qobuz.Recording{ID: "original", AlbumID: "believe", Title: track.Title + " (Album Version)", Artist: "Justin Bieber", Album: "Believe", ISRC: "USUM71205367", Duration: 227, Streamable: true}
	acoustic := qobuz.Recording{ID: "acoustic-untagged", AlbumID: "acoustic-release", Title: track.Title, Artist: "Justin Bieber", Album: "Compilation", ISRC: "USUM71217075", Duration: 143, Streamable: true}
	labeled := acoustic
	labeled.ID, labeled.Title = "acoustic-explicit", track.Title+" (Acoustic Version)"
	if got, reason := chooseRecording(track, []qobuz.Recording{acoustic, labeled, original}); got.ID != original.ID || reason != "" {
		t.Fatalf("catalog match = %q, reason = %q", got.ID, reason)
	}
	if got, reason := chooseRecording(track, []qobuz.Recording{acoustic, labeled}); got.ID != "" || reason != "No matching Qobuz track" {
		t.Fatalf("acoustic-only match = %q, reason = %q", got.ID, reason)
	}
	track.Title = labeled.Title
	track.Artists = []string{"Justin Bieber"}
	if got, reason := chooseRecording(track, []qobuz.Recording{acoustic, labeled, original}); got.ID != labeled.ID || reason != "" {
		t.Fatalf("explicit acoustic match = %q, reason = %q", got.ID, reason)
	}
}

func TestChooseSongUsesStrongestEvidence(t *testing.T) {
	track := Track{Title: "Song", Artists: []string{"Lead", "Guest"}}
	recording := qobuz.Recording{ID: "qobuz", ISRC: "USAAA1234567", Album: "Release", Duration: 200}
	complete := navidrome.Song{ID: "complete", Title: "Song", Artist: "Lead & Guest", Album: "Release", ISRC: []string{"us-aaa-12-34567"}, Duration: 200}
	partial := complete
	partial.ID, partial.Artist, partial.ISRC = "partial", "Lead", []string{"OTHER"}
	untagged := complete
	untagged.ID, untagged.ISRC = "untagged", []string{" "}
	conflicting := complete
	conflicting.ID, conflicting.Duration = "conflicting", 220
	if got, ambiguous := chooseSong(track, qobuz.Recording{}, []navidrome.Song{partial, complete}); got.ID != complete.ID || ambiguous {
		t.Fatalf("complete credits should win, got %q, ambiguous %t", got.ID, ambiguous)
	}
	if got, ambiguous := chooseSong(track, recording, []navidrome.Song{untagged, complete}); got.ID != complete.ID || ambiguous {
		t.Fatalf("verified ISRC should win, got %q, ambiguous %t", got.ID, ambiguous)
	}
	if got, ambiguous := chooseSong(track, recording, []navidrome.Song{untagged}); got.ID != untagged.ID || ambiguous {
		t.Fatalf("blank ISRC should allow album evidence, got %q, ambiguous %t", got.ID, ambiguous)
	}
	if got, ambiguous := chooseSong(track, qobuz.Recording{}, []navidrome.Song{complete, conflicting}); got.ID != "" || !ambiguous {
		t.Fatalf("conflicting durations should be ambiguous, got %q, ambiguous %t", got.ID, ambiguous)
	}
}

func TestChooseSongPrefersVerifiedSingleVersion(t *testing.T) {
	track := Track{Title: "Last Christmas", Artists: []string{"Wham!"}}
	recording := qobuz.Recording{ID: "qobuz-soundtrack", ISRC: "GBBBM8400019", Duration: 264}
	original := navidrome.Song{ID: "single", Title: "Last Christmas (Single Version)", Artist: "Wham!", Duration: 262, ISRC: []string{"gbbbm8400019"}}
	soundtrack := navidrome.Song{ID: "soundtrack", Title: "Last Christmas", Artist: "Wham!", Duration: 264, ISRC: []string{"GBBBM8400019"}}
	puddingMix := navidrome.Song{ID: "mix", Title: "Last Christmas (Pudding Mix)", Artist: "Wham!", Duration: 398, ISRC: []string{"GBBBM8400004"}}
	wrongISRC := original
	wrongISRC.ISRC = puddingMix.ISRC
	wrongDuration := original
	wrongDuration.Duration = puddingMix.Duration
	for _, tt := range []struct {
		name      string
		recording qobuz.Recording
		songs     []navidrome.Song
		want      string
	}{
		{"use the single before importing the catalog album", recording, []navidrome.Song{puddingMix, original}, "single"},
		{"replace the already imported soundtrack", recording, []navidrome.Song{soundtrack, original}, "single"},
		{"verify against the local soundtrack without the catalog", qobuz.Recording{}, []navidrome.Song{soundtrack, original}, "single"},
		{"require evidence for the single", qobuz.Recording{}, []navidrome.Song{original}, ""},
		{"reject a different ISRC", recording, []navidrome.Song{soundtrack, wrongISRC}, "soundtrack"},
		{"reject a materially different duration", recording, []navidrome.Song{soundtrack, wrongDuration}, "soundtrack"},
		{"keep distinct mix titles separate", recording, []navidrome.Song{puddingMix}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ambiguous := chooseSong(track, tt.recording, tt.songs)
			if got.ID != tt.want || ambiguous {
				t.Fatalf("song = %q, ambiguous = %t, want %q", got.ID, ambiguous, tt.want)
			}
		})
	}
}

func TestChooseSongReusesOriginalAndArtistAliases(t *testing.T) {
	christmas := Track{Title: "All I Want for Christmas Is You", Artists: []string{"Mariah Carey"}}
	original := navidrome.Song{ID: "original", Title: christmas.Title + " (Original Version)", Artist: "Mariah Carey", Duration: 241, ISRC: []string{"USSM19400325"}}
	deluxe := navidrome.Song{ID: "deluxe", Title: christmas.Title, Artist: "Mariah Carey", Duration: 241, ISRC: []string{"USSM19400325"}}
	catalog := qobuz.Recording{ID: "qobuz", ISRC: "USSM19400325", Duration: 241}
	if got, ambiguous := chooseSong(christmas, catalog, []navidrome.Song{original}); got.ID != original.ID || ambiguous {
		t.Fatalf("original already in the library = %q, ambiguous %v", got.ID, ambiguous)
	}
	if got, ambiguous := chooseSong(christmas, catalog, []navidrome.Song{deluxe, original}); got.ID != original.ID || ambiguous {
		t.Fatalf("original or deluxe = %q, ambiguous %v", got.ID, ambiguous)
	}
	iris := Track{Title: "Iris", Artists: []string{"The Goo Goo Dolls"}}
	local := navidrome.Song{ID: "iris", Title: "Iris", Artist: "Goo Goo Dolls"}
	cover := navidrome.Song{ID: "cover", Title: "Iris", Artist: "Mike Posner"}
	if got, ambiguous := chooseSong(iris, qobuz.Recording{}, []navidrome.Song{cover, local}); got.ID != local.ID || ambiguous {
		t.Fatalf("Iris = %q, ambiguous %v", got.ID, ambiguous)
	}
}

func TestChooseSongReusesVerifiedReleaseLabels(t *testing.T) {
	recording := qobuz.Recording{ID: "catalog", ISRC: "USAAA1234567", Duration: 200}
	for _, tt := range []struct {
		chart, library string
		code           string
		duration       int
		want           bool
	}{
		{"Song", "Song (Album Version)", recording.ISRC, 201, true},
		{"Song", "Song [LP Version]", recording.ISRC, 200, true},
		{"Song", "Song – Bonus Track", recording.ISRC, 200, true},
		{"Song (Album Version)", "Song", recording.ISRC, 200, true},
		{"Song (feat. Guest)", "Song (Album Version) (feat. Guest)", recording.ISRC, 200, true},
		{"Song", "Song (Album Version)", "USAAA7654321", 200, false},
		{"Song", "Song (Album Version)", recording.ISRC, 0, false},
		{"Song", "Song (Bonus Track)", recording.ISRC, 225, false},
		{"Song", "Song (Radio Edit)", recording.ISRC, 200, false},
		{"Song", "Song (Bachata)", recording.ISRC, 200, false},
		{"Song", "Song (2026 Remaster)", recording.ISRC, 200, false},
		{"Song (Live)", "Song (Album Version)", recording.ISRC, 200, false},
	} {
		track := Track{Title: tt.chart, Artists: []string{"Artist", "Guest"}}
		song := navidrome.Song{ID: "local", Title: tt.library, Artist: "Artist", Duration: tt.duration, ISRC: []string{tt.code}}
		got, ambiguous := chooseSong(track, recording, []navidrome.Song{song})
		if (got.ID != "") != tt.want || ambiguous {
			t.Errorf("chart %q, library %q, ISRC %q, duration %d: got %q, ambiguous %t", tt.chart, tt.library, tt.code, tt.duration, got.ID, ambiguous)
		}
	}

	track := Track{Title: "Song", Artists: []string{"Artist"}}
	exact := navidrome.Song{ID: "plain", Title: "Song", Artist: "Artist", Duration: 200, ISRC: []string{recording.ISRC}}
	bonus := exact
	bonus.ID, bonus.Title = "bonus", "Song (Bonus Track)"
	got, ambiguous := chooseSong(track, recording, []navidrome.Song{bonus, exact})
	if got.ID != exact.ID || ambiguous {
		t.Fatalf("match = %q, ambiguous %t, want exact title", got.ID, ambiguous)
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
		existing.Title: {{ID: "nd-existing", Title: existing.Title, Artist: "Singer", Album: "Existing Album"}},
		download.Title: {
			{ID: "nd-wrong", Title: "Nueva canción (Live)", Artist: "Ártist"},
			{ID: "nd-acoustic", Title: download.Title, Artist: "Ártist", Album: "Release Acoustic", ISRC: []string{"ACOUSTIC"}},
		},
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
				fmt.Fprint(w, `{"tracks":{"items":[
					{"id":98,"title":"Nueva cancion","streamable":true,"performer":{"name":"Artist"},"album":{"id":"acoustic","title":"Release Acoustic"}},
					{"id":99,"title":"Nueva cancion (feat. Guest)","version":"Album Version","isrc":"TEST00000001","media_number":2,"track_number":3,"duration":200,"streamable":true,"performer":{"name":"Artist"},"album":{"id":"release","title":"Release"}}
				]}}`)
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
	paths := config.Paths{Data: t.TempDir(), Library: "/music"}
	manager, err := New(paths, catalog, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	manager.lookupIdentity = func(context.Context, string) (trackIdentity, error) { return trackIdentity{}, nil }
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
	if err := manager.remember(ctx, download.SpotifyID, qobuz.Recording{
		ID: "old", AlbumID: "acoustic", Title: download.Title, Artist: "Ártist", Album: "Release Acoustic", ISRC: "ACOUSTIC", Streamable: true,
	}); err != nil {
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
	queued, err := manager.Views(ctx)
	if err != nil || len(queued) != 2 || !queued[0].SyncRequested {
		t.Fatalf("queued playlist views %+v, error %v", queued, err)
	}
	manager.runOnce(ctx, now)
	current := read(co.ID)
	colombiaRemoteID := current.NavidromeID
	checkRemote(colombiaRemoteID, "nd-existing")
	if got := current.view().Tracks[1].Album; got != "Existing Album" {
		t.Fatalf("existing track album = %q, wanted Existing Album", got)
	}
	lookups := func(query string) int {
		mu.Lock()
		defer mu.Unlock()
		return qobuzLookups[query]
	}
	if len(queue.job.ChartSources) != 2 || lookups("Nueva canción Ártist") != 1 ||
		current.Entries[2].State != "needs_review" || read(se.ID).NavidromeID != "" {
		t.Fatalf("queue %+v, chart %+v", queue.job, current.Entries)
	}

	queue.job.Status = jobs.StatusDownloading
	active, err := manager.Views(ctx)
	if err != nil || active[0].SyncRequested || active[0].SyncPhase != "" || active[0].Tracks[0].State != "downloading" {
		t.Fatalf("downloading playlist views %+v, error %v", active, err)
	}

	queue.job.Status, queue.job.MessageCode = jobs.StatusCompleted, "added_incomplete"
	queue.job.LibraryPath = "Ártist/Release - INCOMPLETE"
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
	manager, err = New(paths, catalog, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	manager.lookupIdentity = func(context.Context, string) (trackIdentity, error) { return trackIdentity{}, nil }
	manager.fetch = fetch
	mu.Lock()
	local[download.Title] = append(local[download.Title],
		navidrome.Song{ID: "nd-wrong-isrc", Title: download.Title, Artist: "Artist", Album: "Release", Track: 3, Disc: 2, Duration: 200, ISRC: []string{"OTHER"}},
		navidrome.Song{ID: "nd-downloaded", Title: download.Title, Artist: "Artist", Album: "Library Release", Track: 3, Disc: 2, Duration: 200, ISRC: []string{"TEST00000001"}},
	)
	mu.Unlock()
	now = now.Add(workInterval)
	manager.runOnce(ctx, now)
	checkRemote(colombiaRemoteID, "nd-downloaded", "nd-existing")
	checkRemote(read(se.ID).NavidromeID, "nd-downloaded")
	current = read(co.ID)
	if got := current.view().Tracks[0].Album; got != "Library Release" || current.Entries[0].Recording.Album != "Release" {
		t.Fatalf("track album = %q, catalog album = %q", got, current.Entries[0].Recording.Album)
	}
	current.Entries[0].SongAlbum = nil // Simulate a match saved before Navidrome album names were stored.
	if got := current.view().Tracks[0].Album; got != "" {
		t.Fatalf("track with unknown Navidrome album showed %q", got)
	}
	if err := manager.save(ctx, current, current.Revision); err != nil {
		t.Fatal(err)
	}
	manager.runOnce(ctx, now)
	if got := read(co.ID).view().Tracks[0].Album; got != "Library Release" {
		t.Fatalf("refreshed track album = %q, wanted Library Release", got)
	}
	checkRemote(colombiaRemoteID, "nd-downloaded", "nd-existing")
	imports, more, err := manager.Imports(ctx, 0)
	if err != nil || more || len(imports) != 1 || !imports[0].Partial || len(imports[0].Sources) != 2 ||
		imports[0].FolderPath != "/music/Ártist/Release - INCOMPLETE" {
		t.Fatalf("imports %+v more=%v error=%v", imports, more, err)
	}

	mu.Lock()
	legacy := remote[colombiaRemoteID]
	legacy.Comment = "Managed by CineRoute. Kworb chart date: " + colombia.Date
	remote[colombiaRemoteID] = legacy
	mu.Unlock()
	if err := manager.SyncNow(ctx, co.ID); err != nil {
		t.Fatal(err)
	}
	if queued := read(co.ID).view(); !queued.SyncRequested || queued.Ready != 2 || queued.PendingChecks != 2 {
		t.Fatalf("manual sync should retain confirmed tracks: %+v", queued)
	}
	manager.runOnce(ctx, now)
	updated, err := nav.Playlist(ctx, colombiaRemoteID)
	if err != nil || updated.Comment != "Spotify chart date: "+colombia.Date {
		t.Fatalf("playlist comment = %q, error = %v", updated.Comment, err)
	}
	checkRemote(colombiaRemoteID, "nd-downloaded", "nd-existing")

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
