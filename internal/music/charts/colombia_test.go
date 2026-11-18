package charts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

func TestChartFindsExistingTracksWithProducerCreditsAndRemixAliases(t *testing.T) {
	producer := navidrome.Contributor{Role: "producer"}
	producer.Artist.Name = "Fharid On The Beat"
	peli := navidrome.Song{ID: "peli", Title: "Peli De Terror", Artist: "Fharid On The Beat, Køddy G, The Favorites",
		ISRC: []string{"QZWDD2619453"}, Album: "Peli De Terror", Duration: 162, Contributors: []navidrome.Contributor{producer}}
	remix := navidrome.Song{ID: "remix", Title: "Cuando No Era Cantante (Remix)", Artist: "El Bogueto, Anuel Aa, Fuerza Regida, Yung Beef",
		ISRC: []string{"GXD7V2653123"}, Album: "Cuando No Era Cantante (Remix)", Duration: 327}
	sameRemix := remix
	sameRemix.ID, sameRemix.Album = "album-remix", "Eso Si Es De Gangster"
	original := remix
	original.ID, original.Title, original.ISRC = "original", "Cuando No Era Cantante", []string{"AEA3A2366094"}
	covers := map[string][]navidrome.Song{
		"Peli De Terror":                 {peli},
		"Cuando No Era Cantante (Remix)": {original, remix, sameRemix},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// An unexpected query or catalog request must fail the local-only path.
		songs, ok := covers[r.URL.Query().Get("query")]
		if r.URL.Path != "/rest/search3" || !ok {
			t.Errorf("unexpected %s query %q", r.URL.Path, r.URL.Query().Get("query"))
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"subsonic-response": map[string]any{
			"status": "ok", "searchResult3": map[string]any{"song": songs},
		}})
	}))
	defer server.Close()
	m, err := New(config.Paths{Data: t.TempDir()}, nil, navidrome.New(server.URL, "user", "pass"), new(testQueue))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	peliTrack := Track{SpotifyID: "23XDZWXMOiRrm0qoKXCPbf", Title: "Peli De Terror", Artists: []string{"Køddy G", "THE FAVORITES"}}
	remixTrack := Track{SpotifyID: "56V7nzrqr6Sq1uFym4odwa", Title: "Cuando No Era Cantante RMX - Remix",
		Artists: []string{"El Bogueto", "Anuel AA", "Fuerza Regida", "Yung Beef"}}
	for _, tt := range []struct {
		track Track
		id    string
	}{
		{peliTrack, peli.ID},
		{remixTrack, remix.ID},
	} {
		e := entry{Track: tt.track}
		if err := m.resolve(t.Context(), playlist{}, &e, time.Now()); err != nil || e.State != "ready" || e.SongID != tt.id {
			t.Fatalf("%s resolved to %+v, error %v", tt.track.Title, e, err)
		}
	}
	uncredited := peli
	uncredited.Contributors = nil
	missingArtist := peli
	missingArtist.Artist = "Fharid On The Beat, Køddy G"
	if song, _ := chooseSong(peliTrack, qobuz.Recording{}, []navidrome.Song{uncredited, missingArtist}); song.ID != "" {
		t.Fatalf("matched a track with an unverified addition or missing chart artist: %q", song.ID)
	}
	for _, title := range []string{original.Title, "Cuando No Era Cantante (DJ Remix)", "Cuando No Era Cantante (Remix) (Live)"} {
		if score := trackMatch(remixTrack, title, remix.Artist); score != 0 {
			t.Errorf("matched a different recording %q", title)
		}
	}
	candidate := qobuz.Recording{ID: "track", AlbumID: "album", Title: remix.Title, Artist: "El Bogueto", Streamable: true}
	if got, reason := chooseRecording(remixTrack, []qobuz.Recording{candidate}); reason != "" || got.ID != candidate.ID {
		t.Fatalf("catalog remix resolved to %q, reason %q", got.ID, reason)
	}
}
