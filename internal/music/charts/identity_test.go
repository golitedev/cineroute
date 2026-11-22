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

func TestPublicTrackMetadata(t *testing.T) {
	const trackURL = spotifyTrackURL + sweaterSpotifyID
	const mbid = "dc8dad55-44ac-41d0-986b-c7a5ae3b09e5"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/track/" + sweaterSpotifyID:
			_, _ = w.Write([]byte(`<head>
				<meta property="og:type" content="music.song">
				<meta property="og:url" content="` + trackURL + `">
				<meta property="og:description" content="The Neighbourhood · I Love You. · Song · 2013">
				<meta name="music:album" content="https://open.spotify.com/album/id">
				<meta name="music:album:track" content="4">
				<meta name="music:duration" content="240">
			</head>`))
		case "/ws/2/url":
			if r.Header.Get("User-Agent") != metadataUserAgent || r.URL.Query().Get("resource") != trackURL {
				t.Errorf("MusicBrainz lookup used wrong track or User-Agent: %s", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resource": trackURL, "relations": []any{map[string]any{"recording": map[string]string{"id": mbid}}},
			})
		case "/ws/2/recording/" + mbid:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": mbid, "length": 240001, "isrcs": []string{"us-sm1-13-00080"}})
		default:
			t.Errorf("unexpected public metadata request: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := identityClient{client: server.Client(), spotify: server.URL + "/track/", musicBrainz: server.URL + "/ws/2"}
	got, err := provider.lookup(t.Context(), sweaterSpotifyID)
	want := trackIdentity{ISRC: "USSM11300080", Album: "I Love You.", Number: 4, Duration: 240}
	if err != nil || got != want {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
	page := strings.NewReader(`<meta property="og:type" content="music.song"><meta property="og:url" content="` + trackURL +
		`"><meta name="music:duration" content="240">`)
	if _, err := parseSpotifyMetadata(page, spotifyTrackURL+"other"); err == nil {
		t.Fatal("accepted metadata for a different Spotify track")
	}
}

type catalogRecordings []qobuz.Recording

func (c catalogRecordings) SearchRecordings(context.Context, string) ([]qobuz.Recording, error) {
	return c, nil
}

type noQobuzSearch struct{ t *testing.T }

func (c noQobuzSearch) SearchRecordings(context.Context, string) ([]qobuz.Recording, error) {
	c.t.Fatal("a resolved Navidrome match should not search Qobuz")
	return nil, nil
}

func TestChartMetadataReusesNavidromeBeforeQobuz(t *testing.T) {
	const title = "It's the Most Wonderful Time of the Year"
	const artist = "Andy Williams"
	const album = "The Andy Williams Christmas Album"
	const spotifyID = "1IcR6RlgvDczfvoWJSSY2A"
	songs := []navidrome.Song{
		{ID: "compilation", Title: title, Artist: artist, Album: "Classic Christmas Album", ISRC: []string{"USSM16300086"}, Track: 10, Duration: 151},
		{ID: "personal", Title: title, Artist: artist, Album: "Personal Christmas Collection", ISRC: []string{"USSM10022761"}, Track: 3, Duration: 150},
		{ID: "christmas", Title: title + " (Album Version)", Artist: "Andy Williams with Robert Mersey & His Orchestra",
			Album: album, ISRC: []string{"USSM16300086"}, Track: 4, Duration: 152},
	}
	nav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/search3" {
			t.Errorf("unexpected Navidrome request %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
			"status": "ok", "searchResult3": map[string]any{"song": songs},
		}})
	}))
	defer nav.Close()
	manager, err := New(config.Paths{Data: t.TempDir()}, noQobuzSearch{t}, navidrome.New(nav.URL, "admin", "pass"), new(testQueue))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	lookups := 0
	manager.lookupIdentity = func(context.Context, string) (trackIdentity, error) {
		lookups++
		return trackIdentity{Album: album, Duration: 151, Number: 4}, errors.New("MusicBrainz unavailable")
	}
	for range 2 {
		e := entry{Track: Track{SpotifyID: spotifyID, Title: title, Artists: []string{artist}}}
		if err := manager.resolve(t.Context(), playlist{}, &e, time.Now()); err != nil || e.State != "ready" || e.SongID != "christmas" {
			t.Fatalf("resolve = %+v, %v", e, err)
		}
	}
	if lookups != 1 {
		t.Fatalf("metadata lookups = %d, want 1", lookups)
	}
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
			t.Fatalf("metadata selection: entry %+v, job %+v", e, queue.job)
		}
		if err := m.save(ctx, p, p.Revision); err != nil {
			t.Fatal(err)
		}
	}
	if lookups != 1 {
		t.Fatalf("metadata lookups = %d, wanted one", lookups)
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
	if _, err := m.db.ExecContext(ctx, "INSERT INTO chart_identity VALUES (?, ?, ?, 1, 0)", sweaterSpotifyID, otherISRC, manualAlbum); err != nil {
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
	if got, err := m.identity(ctx, sweaterSpotifyID); err != nil || got.Manual || got.ISRC != chartISRC || got.Album != album {
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
