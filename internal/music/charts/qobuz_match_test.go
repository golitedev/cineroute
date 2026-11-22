package charts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

func TestParseQobuzReference(t *testing.T) {
	for _, tt := range []struct {
		ref, id string
		track   bool
	}{
		{" mlbpvkiehio4h ", "mlbpvkiehio4h", false},
		{"https://play.qobuz.com/album/mlbpvkiehio4h", "mlbpvkiehio4h", false},
		{"https://www.qobuz.com/ca-fr/album/go-easy/mlbpvkiehio4h", "mlbpvkiehio4h", false},
		{"https://open.qobuz.com/track/22?utm_source=share", "22", true},
		{"https://open.qobuz.com.example.net/track/22", "", false},
		{"https://open.qobuz.com/track/not-a-track", "", false},
	} {
		id, track, err := parseQobuzReference(tt.ref)
		if id != tt.id || track != tt.track || (err != nil) != (tt.id == "") {
			t.Errorf("parse %q got %q, track=%t, err=%v", tt.ref, id, track, err)
		}
	}
}

func TestQobuzMatchUsesTheSelectedTrackAcrossPlaylists(t *testing.T) {
	var indexed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/album/get":
			if r.URL.Query().Get("album_id") != "chosenAlbum" {
				t.Errorf("wrong album ID: %q", r.URL.Query().Get("album_id"))
			}
			fmt.Fprint(w, `{"id":"chosenAlbum","title":"Chosen Release","artist":{"name":"Artist"},"tracks":{"total":2,"items":[
				{"id":11,"title":"Other Song","track_number":1,"isrc":"USAAA1900011","streamable":true},
				{"id":22,"title":"Selected Song","track_number":2,"isrc":"USAAA1900022","streamable":true,"duration":173}]}}`)
		case "/track/get":
			if r.URL.Query().Get("track_id") != "22" {
				t.Errorf("wrong track ID: %q", r.URL.Query().Get("track_id"))
			}
			fmt.Fprint(w, `{"id":22,"title":"Selected Song","track_number":2,"isrc":"USAAA1900022","streamable":true,"duration":173,
				"album":{"id":"chosenAlbum","title":"Chosen Release","artist":{"name":"Artist"}}}`)
		case "/rest/search3":
			if r.URL.Query().Get("query") != "Selected Song" {
				t.Errorf("searched Navidrome for %q", r.URL.Query().Get("query"))
			}
			songs := "[]"
			if indexed.Load() {
				songs = `[{"id":"wrong","title":"Other Song","album":"Chosen Release","isrc":["USAAA1900011"],"track":1,"duration":173},
					{"id":"indexed","title":"Selected Song","album":"Chosen Release","isrc":["USAAA1900022"],"track":2,"duration":173}]`
			}
			fmt.Fprintf(w, `{"subsonic-response":{"status":"ok","searchResult3":{"song":%s}}}`, songs)
		default:
			t.Errorf("unexpected lookup %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	catalog := qobuz.New("user", "token", func() string { return "123" })
	catalog.BaseURL = server.URL
	queue := new(testQueue)
	paths := config.Paths{Data: t.TempDir()}
	nav := navidrome.New(server.URL, "user", "pass")
	m, err := New(paths, catalog, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	track := Track{SpotifyID: "shared", Title: "Spotify Title", Artists: []string{"Artist"}, Rank: 1}
	ids := make([]string, 0, 2)
	for _, country := range []string{"global", "co"} {
		view, err := m.Configure(ctx, "", Settings{Country: country, Limit: 1, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		p, err := m.playlist(ctx, view.ID)
		if err != nil {
			t.Fatal(err)
		}
		p.accept(Snapshot{Date: "2026-11-22", Tracks: []Track{track}})
		if err := m.save(ctx, p, p.Revision); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	const albumLink = "https://www.qobuz.com/us-en/album/chosen-release/chosenAlbum"
	choices, err := m.QobuzChoices(ctx, ids[0], track.SpotifyID, albumLink)
	if err != nil || len(choices) != 2 || choices[1].ID != "22" || choices[1].Duration != 173 {
		t.Fatalf("album choices: %+v, %v", choices, err)
	}
	if err := m.SetQobuzMatch(ctx, ids[0], track.SpotifyID, albumLink, "99"); !errors.Is(err, ErrSettings) {
		t.Fatalf("accepted track from outside the album: %v", err)
	}
	if err := m.SetQobuzMatch(ctx, ids[0], track.SpotifyID, albumLink, "22"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetQobuzMatch(ctx, ids[1], track.SpotifyID, "https://open.qobuz.com/track/22?utm_source=share", ""); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = New(paths, catalog, nav, queue)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.lookupIdentity = func(context.Context, string) (trackIdentity, error) {
		t.Fatal("an explicit match should not use Credits.fm")
		return trackIdentity{}, nil
	}
	views, err := m.Views(ctx)
	if err != nil || len(views) != 2 {
		t.Fatalf("playlist views: %+v, %v", views, err)
	}
	for _, view := range views {
		if match := view.Tracks[0]; !match.ManualMatch || match.QobuzMatch == nil || match.QobuzMatch.ID != "22" {
			t.Fatalf("override missing after reload: %+v", match)
		}
	}
	p, err := m.playlist(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	e := &p.Entries[0]
	if err := m.resolve(ctx, p, e, time.Now()); err != nil || e.State != "queued" || queue.job.AlbumID != "chosenAlbum" {
		t.Fatalf("expected selected album queued: %+v, %v", e, err)
	}
	indexed.Store(true)
	if err := m.resolve(ctx, p, e, time.Now()); err != nil || e.State != "ready" || e.SongID != "indexed" {
		t.Fatalf("expected only the selected track: %+v, %v", e, err)
	}
	if err := m.ClearTrackMatch(ctx, ids[1], track.SpotifyID); err != nil {
		t.Fatal(err)
	}
	if match, err := m.qobuzMatch(ctx, track.SpotifyID); err != nil || match.ID != "" {
		t.Fatalf("override was not cleared: %+v, %v", match, err)
	}
}
