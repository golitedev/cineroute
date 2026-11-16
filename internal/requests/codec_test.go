package requests

import (
	"path/filepath"
	"slices"
	"testing"

	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
)

func TestCodecPreferencesRankMoviesAndTV(t *testing.T) {
	settings := Defaults()
	settings.Movie.Codecs = []string{"hevc", "h264"}
	settings.TV.Codecs = []string{"h264", "hevc"}
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"movie", "tv"} {
		t.Run(kind, func(t *testing.T) {
			p := loaded.Movie
			if kind == "tv" {
				p = loaded.TV
			}
			media := tmdb.Details{Result: tmdb.Result{MediaType: kind, ID: 1, Title: "Severance", Name: "Severance", ReleaseDate: "2022-01-01", FirstAirDate: "2022-01-01"}}
			candidate := func(codec string, seeds int) Candidate {
				c, err := Evaluate(prowlarr.Release{Title: "Severance.2022.S01.1080p.WEB-DL." + codec, Seeders: &seeds}, media, p, 1, 1)
				if err != nil {
					t.Fatal(err)
				}
				return c
			}
			preferred := candidate(p.Codecs[0], 1)
			fallback := candidate(p.Codecs[1], 100)
			if !Better(preferred, fallback, loaded.SelectionOrder, true) || Better(fallback, preferred, loaded.SelectionOrder, true) {
				t.Fatal("codec preference lost to seeders")
			}
			// Moving the global criterion lets seeders take precedence instead.
			order := append([]string{"seeders"}, slices.DeleteFunc(slices.Clone(loaded.SelectionOrder), func(s string) bool { return s == "seeders" })...)
			if !Better(fallback, preferred, order, true) {
				t.Fatal("global ranking order ignored")
			}
			// Earlier quality rules still win over codec preference.
			preferred.Rank = 1
			if !Better(fallback, preferred, loaded.SelectionOrder, true) {
				t.Fatal("codec overrode higher quality")
			}
			if _, err := Evaluate(prowlarr.Release{Title: "Severance.2022.S01.1080p.WEB-DL.AV1"}, media, p, 0, 1); err == nil {
				t.Fatal("unlisted codec was accepted")
			}
		})
	}
}

func TestCodecCriterionMigratesExistingBitrateOrder(t *testing.T) {
	order := []string{"language", "quality", "text_subtitles", "keywords", "pack", "priority", "video_bitrate", "seeders"}
	want := []string{"language", "quality", "codec", "text_subtitles", "keywords", "pack", "priority", "video_bitrate", "seeders"}
	if got := NormalizeSelectionOrder(order); !slices.Equal(got, want) {
		t.Fatalf("migration: %v", got)
	}
	// Explicit codec positioning must survive loading and normalization.
	custom := append([]string{"codec"}, order...)
	if got := NormalizeSelectionOrder(custom); !slices.Equal(got, custom) {
		t.Fatalf("custom order changed: %v", got)
	}
}
