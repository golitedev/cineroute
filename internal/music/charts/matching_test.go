package charts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

func TestChartMatchesSoundtrackCredits(t *testing.T) {
	track := Track{Title: "Flower In A Hurricane", Artists: []string{
		"Justice", "Tame Impala", "SZA", "Grand Theft Auto VI", "King Gizzard & The Lizard Wizard",
	}}
	const credits = " (feat. Tame Impala, SZA, King Gizzard & the Lizard Wizard)"
	const soundtrack = " [From GTAVI: Original Score]"
	recording := qobuz.Recording{ID: "459275745", AlbumID: "pp8ysmmn79bic", Title: track.Title + credits + soundtrack,
		Artist: "Justice", ISRC: "FR6P12602660", Duration: 224, Streamable: true}
	for _, identity := range []trackIdentity{{}, {ISRC: recording.ISRC}} {
		if got, reason := chooseRecordingWithISRC(track, []qobuz.Recording{recording}, identity); got.ID != recording.ID || reason != "" {
			t.Errorf("Qobuz match with ISRC %q = %q, %q", identity.ISRC, got.ID, reason)
		}
	}
	song := navidrome.Song{ID: "local", Title: recording.Title, Artist: "Justice", ISRC: []string{recording.ISRC}, Duration: 224}
	if got, ambiguous := chooseSong(track, qobuz.Recording{}, []navidrome.Song{song}); got.ID != song.ID || ambiguous {
		t.Errorf("local match = %q, ambiguous = %t", got.ID, ambiguous)
	}
	track.Title = recording.Title
	if title := trackSearchTitle(track); title != "Flower In A Hurricane" {
		t.Errorf("search title = %q", title)
	}
	for _, title := range []string{
		"Flower In A Hurricane (Remix)" + credits + soundtrack,
		"Flower In A Hurricane (feat. Uncredited Artist)" + soundtrack,
		"Flower In A Hurricane" + credits + " [From Another Game: Original Score]",
	} {
		if got := trackMatch(track, title, recording.Artist); got != 0 {
			t.Errorf("unexpected match for %q", title)
		}
	}
}

func TestChartReusesNavidromeTracksWithoutManualISRC(t *testing.T) {
	brenda := navidrome.Song{ID: "brenda", Title: "Rockin' Around The Christmas Tree", Artist: "Brenda Lee",
		Album: "Merry Christmas From Brenda Lee", ISRC: []string{"USMC15848998"}, Duration: 126}
	bobby := navidrome.Song{ID: "bobby", Title: "Jingle Bell Rock", Artist: "Bobby Helms",
		Album: "Jingle Bell Rock/Captain Santa Claus (And His Reindeer Space Patrol)", ISRC: []string{"USMC15746480"}, Duration: 130}
	cover := navidrome.Song{ID: "cover", Title: bobby.Title, Artist: brenda.Artist,
		Album: brenda.Album, ISRC: []string{"USMC16415002"}, Duration: 130}
	kybba := navidrome.Song{ID: "kybba", Title: "Se Le Ve", Artist: "Kybba, Ryan Castro, BAD GYAL, Topboy TGR",
		Album: "Se Le Ve", ISRC: []string{"USWL12602403"}, Duration: 159}
	broad := make([]navidrome.Song, 50)
	for i := range broad {
		broad[i] = navidrome.Song{ID: fmt.Sprintf("other-%d", i), Title: "Unrelated", Artist: "Other"}
	}
	queries := map[string][]navidrome.Song{
		brenda.Title: {brenda}, bobby.Title: {cover, bobby}, kybba.Title: broad,
		"Se Le Ve Kybba": {kybba},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		songs, ok := queries[query.Get("query")]
		if r.URL.Path != "/rest/search3" || !ok || query.Get("musicFolderId") != "shared" || query.Get("songCount") != "50" {
			t.Errorf("unexpected Navidrome search %s for %q", r.URL.Path, query.Get("query"))
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
			"status": "ok", "searchResult3": map[string]any{"song": songs},
		}})
	}))
	defer server.Close()
	nav := navidrome.New(server.URL, "user", "pass")
	nav.MusicFolderID = "shared"
	m, err := New(config.Paths{Data: t.TempDir()}, catalogRecordings{}, nav, new(testQueue))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.lookupIdentity = func(context.Context, string) (trackIdentity, error) {
		t.Fatal("local match should not need an ISRC lookup")
		return trackIdentity{}, nil
	}
	for _, tt := range []struct {
		song   navidrome.Song
		artist []string
	}{
		{brenda, []string{brenda.Artist}},
		{bobby, []string{bobby.Artist}},
		{kybba, []string{"Kybba", "Ryan Castro", "Bad Gyal", "Topboy TGR"}},
	} {
		t.Run(tt.song.Title, func(t *testing.T) {
			e := entry{Track: Track{SpotifyID: tt.song.ID, Title: tt.song.Title, Artists: tt.artist}}
			if err := m.resolve(t.Context(), playlist{}, &e, time.Now()); err != nil || e.State != "ready" || e.SongID != tt.song.ID {
				t.Fatalf("local match = %q, state = %q, error = %v", e.SongID, e.State, err)
			}
		})
	}
}
