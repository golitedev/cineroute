package charts

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

const sweaterSpotifyID = "2QjOHCTQ1Jl3zawyYOpxh6"

func TestCreditsLookupRequiresTheRequestedSpotifyTrack(t *testing.T) {
	const trackURL = "https://open.spotify.com/track/" + sweaterSpotifyID
	for _, tt := range []struct {
		name, url, code, want string
		status                int
		wantError             bool
	}{
		{"exact", trackURL, "us-sm1-13-00080", "USSM11300080", http.StatusOK, false},
		{"different Spotify ID", "https://open.spotify.com/track/other", "USSM11300080", "", http.StatusOK, false},
		{"invalid ISRC", trackURL, "invalid", "", http.StatusOK, false},
		{"not found", "", "", "", http.StatusNotFound, false},
		{"rate limited", "", "", "", http.StatusTooManyRequests, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Query().Get("url") != trackURL || r.Header.Get("x-api-key") != "" {
					t.Error("expected a keyless lookup of the exact Spotify track")
				}
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]string{"type": "track", "spotify_url": tt.url, "isrc": tt.code, "album_name": "I Love You."})
			}))
			defer server.Close()
			got, err := lookupCreditsIdentity(t.Context(), server.Client(), server.URL, sweaterSpotifyID)
			if got.ISRC != tt.want || (err != nil) != tt.wantError || (tt.want != "" && got.Album != "I Love You.") {
				t.Fatalf("lookup = %+v, %v", got, err)
			}
		})
	}
}

type catalogRecordings []qobuz.Recording

func (c catalogRecordings) SearchRecordings(context.Context, string) ([]qobuz.Recording, error) {
	return c, nil
}

func TestChartISRCSelectsAndRemembersTheRecording(t *testing.T) {
	ctx := t.Context()
	now := time.Now()
	track := Track{SpotifyID: sweaterSpotifyID, Title: "Sweater Weather", Artists: []string{"The Neighbourhood"}, Rank: 1}
	const chartISRC = "USSM11300080"
	const otherISRC = "USSM11300891"
	const album = "I Love You."
	qobuzResults := catalogRecordings{
		{ID: "other", AlbumID: "other-album", Title: track.Title, Artist: track.Artists[0], Album: album, ISRC: otherISRC, Duration: 240, Streamable: true},
		{ID: "compilation", AlbumID: "compilation-album", Title: track.Title, Artist: track.Artists[0], Album: "Compilation", ISRC: chartISRC, Duration: 237, Streamable: true},
		{ID: "chart", AlbumID: "chart-album", Title: track.Title, Artist: track.Artists[0], Album: album, ISRC: chartISRC, Duration: 240, Streamable: true},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/search3" {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
			"status": "ok",
			"searchResult3": map[string]any{"song": []navidrome.Song{
				{ID: "wrong-local", Title: track.Title, Artist: track.Artists[0], Album: album, ISRC: []string{"USSM11204647"}, Duration: 240},
				{ID: "manual-local", Title: track.Title, Artist: track.Artists[0], Album: album, ISRC: []string{otherISRC}, Duration: 240},
			}},
		}})
	}))
	defer server.Close()
	nav := navidrome.New(server.URL, "admin", "password")
	queue := &testQueue{}
	paths := config.Paths{Data: t.TempDir(), Library: "/music"}
	m, err := New(paths, qobuzResults, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	lookups := 0
	lookup := func(_ context.Context, id string) (trackIdentity, error) {
		lookups++
		if id != sweaterSpotifyID {
			t.Fatalf("looked up %q", id)
		}
		return trackIdentity{ISRC: chartISRC, Album: album}, nil
	}
	m.lookupIdentity = lookup
	prepare := func(country string) playlist {
		t.Helper()
		view, err := m.Configure(ctx, "", Settings{Country: country, Enabled: true, Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		p, err := m.playlist(ctx, view.ID)
		if err != nil {
			t.Fatal(err)
		}
		p.accept(Snapshot{Date: "2026-11-16", Tracks: []Track{track}})
		if err := m.save(ctx, p, p.Revision); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first, second := prepare("global"), prepare("co")
	for _, p := range []playlist{first, second} {
		e := &p.Entries[0]
		if err := m.resolve(ctx, p, e, now); err != nil {
			t.Fatal(err)
		}
		if e.State != "queued" || e.Recording.ISRC != chartISRC || queue.job.AlbumID != "chart-album" {
			t.Fatalf("Credits.fm selection: entry %+v, job %+v", e, queue.job)
		}
		if err := m.save(ctx, p, p.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if lookups != 1 {
		t.Fatalf("Credits.fm lookups = %d, wanted one", lookups)
	}
	if err := m.ClearTrackMatch(ctx, first.ID, sweaterSpotifyID); !errors.Is(err, ErrTrackBusy) {
		t.Fatalf("changing the match on an active job returned %v", err)
	}
	for _, p := range []playlist{first, second} {
		p.Entries[0].JobID = ""
		if err := m.save(ctx, p, p.Revision); err != nil {
			t.Fatal(err)
		}
	}
	const manualAlbum = "Other Edition"
	// An override created by an older CineRoute version stays active.
	if _, err := m.db.ExecContext(ctx, "UPDATE chart_identity SET isrc = ?, album = ?, manual = 1, checked_at = 0 WHERE spotify_id = ?",
		otherISRC, manualAlbum, sweaterSpotifyID); err != nil {
		t.Fatal(err)
	}
	if err := m.cacheIdentity(ctx, sweaterSpotifyID, trackIdentity{ISRC: chartISRC}, now); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = New(paths, qobuzResults, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	m.lookupIdentity = lookup
	views, err := m.Views(ctx)
	if err != nil || len(views) != 2 || !views[0].Tracks[0].ManualMatch || !views[1].Tracks[0].ManualMatch || views[0].Tracks[0].QobuzMatch != nil {
		t.Fatalf("manual ISRC views %+v, error %v", views, err)
	}
	p, err := m.playlist(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.resolve(ctx, p, &p.Entries[0], now); err != nil {
		t.Fatal(err)
	}
	if lookups != 1 || p.Entries[0].SongID != "manual-local" {
		t.Fatalf("manual match %q, lookups %d", p.Entries[0].SongID, lookups)
	}
	if err := m.ClearTrackMatch(ctx, first.ID, sweaterSpotifyID); err != nil {
		t.Fatal(err)
	}
	if got, err := m.identity(ctx, sweaterSpotifyID); err != nil || got.ISRC != "" {
		t.Fatalf("cleared ISRC %+v, error %v", got, err)
	}
}

func TestVerifiedAlbumDistinguishesSameISRCDifferentDurations(t *testing.T) {
	track := Track{Title: "Rockin' Around The Christmas Tree", Artists: []string{"Brenda Lee"}}
	original := qobuz.Recording{ID: "original", AlbumID: "1964", Title: track.Title, Artist: track.Artists[0],
		Album: "Merry Christmas From Brenda Lee", ISRC: "USMC15848998", Duration: 126, Streamable: true}
	shorter := original
	shorter.ID, shorter.AlbumID, shorter.Album, shorter.Duration = "shorter", "compilation", "A Rockin’ Christmas With Brenda Lee", 123
	differentISRC := original
	differentISRC.ID, differentISRC.ISRC = "different", "USUV71705179"
	candidates := []qobuz.Recording{shorter, differentISRC, original}
	identity := trackIdentity{ISRC: original.ISRC}
	if _, reason := chooseRecordingWithISRC(track, candidates, identity); !strings.Contains(reason, "Use Qobuz link") {
		t.Fatalf("ISRC without edition evidence gave %q", reason)
	}
	identity.Album = original.Album
	if got, reason := chooseRecordingWithISRC(track, candidates, identity); got.ID != original.ID || reason != "" {
		t.Fatalf("verified album gave %q, reason %q", got.ID, reason)
	}
	songs := []navidrome.Song{
		{ID: shorter.ID, Title: track.Title, Artist: track.Artists[0], Album: shorter.Album, ISRC: []string{shorter.ISRC}, Duration: shorter.Duration},
		{ID: original.ID, Title: track.Title, Artist: track.Artists[0], Album: original.Album, ISRC: []string{original.ISRC}, Duration: original.Duration},
	}
	if got, ambiguous := chooseSongWithIdentity(track, qobuz.Recording{}, songs, identity); got.ID != original.ID || ambiguous {
		t.Fatalf("local album gave %q, ambiguous %t", got.ID, ambiguous)
	}
	shorter.Album = original.Album
	if _, reason := chooseRecordingWithISRC(track, []qobuz.Recording{shorter, original}, identity); reason != ambiguousCatalogRecording {
		t.Fatalf("conflicting tracks on the verified album gave %q", reason)
	}
	songs[0].Album = original.Album
	if got, ambiguous := chooseSongWithIdentity(track, qobuz.Recording{}, songs, identity); got.ID != "" || !ambiguous {
		t.Fatalf("conflicting local tracks on the verified album gave %q, ambiguous %t", got.ID, ambiguous)
	}
}
