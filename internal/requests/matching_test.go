package requests

import (
	"strings"
	"testing"

	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
)

func finalProject() tmdb.Details {
	return tmdb.Details{Result: tmdb.Result{ID: 327814, MediaType: "tv", Name: "Final Project", OriginalName: "Proyecto final", FirstAirDate: "2026-07-29"}, OriginalLanguage: "es"}
}

func TestFinalProjectAitherReleaseWinsByTrackerPriority(t *testing.T) {
	p := Defaults().TV
	aitherSeeds, latSeeds := 3, 4
	aither, err := Evaluate(prowlarr.Release{Title: "Final Project AKA Proyecto Final S01 SPANISH 1080p NF WEB-DL DD+ 5.1 H.264-playWEB", TmdbID: 327814, IndexerID: 1, Size: 7220529083, Seeders: &aitherSeeds}, finalProject(), p, 1, 20)
	if err != nil {
		t.Fatalf("suitable Aither release rejected: %v", err)
	}
	latTeam, err := Evaluate(prowlarr.Release{Title: "Proyecto Final 2026 S01 1080p NF WEB-DL DD+ 5.1 H.264-LatTeam SPANiSH", TmdbID: 327814, IndexerID: 17, Size: 7216825902, Seeders: &latSeeds}, finalProject(), p, 1, 22)
	if err != nil {
		t.Fatal(err)
	}
	if !Better(aither, latTeam, true) || Better(latTeam, aither, true) {
		t.Fatal("tracker priority lost to seed count or size for equivalent releases")
	}
}

func TestKnownAlternativeTitlesAndReleaseYears(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Final Project AKA Proyecto Final S01 1080p WEB-DL H.264", true},
		{"Proyecto.Final.a.k.a.Final.Project.2026.S01.1080p.WEB-DL.H.264", true},
		{"Final Project (2026) AKA Proyecto Final S01 1080p WEB-DL H.264", true},
		{"Final Project AKA Unrelated Show S01 1080p WEB-DL H.264", false},
		{"Unrelated Show AKA Final Project S01 1080p WEB-DL H.264", false},
		{"Final Project AKA Proyecto Final Two S01 1080p WEB-DL H.264", false},
		{"Final Project 2026 AKA Unrelated Show S01 1080p WEB-DL H.264", false},
		{"Final Project AKA Proyecto Final 2025 S01 1080p WEB-DL H.264", false},
		{"Final Project 2025 S01 1080p WEB-DL H.264", false},
		{"Final Project Two S01 1080p WEB-DL H.264", false},
	} {
		t.Run(tc.title, func(t *testing.T) {
			if got := MatchTitle(tc.title, finalProject(), false); got != tc.want {
				t.Errorf("MatchTitle = %v, want %v", got, tc.want)
			}
		})
	}
	movie := tmdb.Details{Result: tmdb.Result{ID: 1, MediaType: "movie", Title: "Example Movie", OriginalTitle: "Otra Pelicula", ReleaseDate: "2024-01-01"}}
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Example Movie AKA Otra Pelicula 2024 1080p WEB-DL x264", true},
		{"Otra Pelicula 2024 AKA Example Movie 1080p WEB-DL x264", true},
		{"Example Movie EXTENDED 2024 1080p WEB-DL x264", true},
		{"Example Movie AKA Otra Pelicula 1080p WEB-DL x264", false},
		{"Example Movie AKA Otra Pelicula 1990 1080p WEB-DL x264", false},
		{"Example Movie 2024 AKA Unrelated Movie 1080p WEB-DL x264", false},
	} {
		if got := MatchTitle(tc.title, movie, true); got != tc.want {
			t.Errorf("%s: MatchTitle = %v, want %v", tc.title, got, tc.want)
		}
	}
}

func TestSlashInTrackerTitleIsNotAPath(t *testing.T) {
	movie := tmdb.Details{Result: tmdb.Result{ID: 754, MediaType: "movie", Title: "Face/Off", ReleaseDate: "1997-01-01"}}
	for _, title := range []string{"Face/Off 1997 1080p BluRay x264", "Face.Off.1997.1080p.BluRay.x264.mkv"} {
		if !MatchTitle(title, movie, true) {
			t.Errorf("valid slash/punctuation title rejected: %s", title)
		}
	}
	if MatchTitle("Unrelated/Face.Off.1997.1080p.BluRay.x264", movie, true) {
		t.Fatal("unrelated title prefix was discarded as a directory")
	}
}

func TestCommonQualitySpellings(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"WEB-DL", "webdl-1080p"}, {"WEB.DL", "webdl-1080p"}, {"WEB DL", "webdl-1080p"},
		{"WEBRip", "webrip-1080p"}, {"WEB-Rip", "webrip-1080p"}, {"WEB.Rip", "webrip-1080p"}, {"WEB Rip", "webrip-1080p"},
		{"BluRay", "bluray-1080p"}, {"Blu Ray", "bluray-1080p"}, {"Blu-Ray", "bluray-1080p"},
		{"BDRip", "bluray-1080p"}, {"BD-Rip", "bluray-1080p"}, {"BRRip", "bluray-1080p"},
	} {
		if got := Quality("Example.2024.1080p." + tc.source + ".x264"); got != tc.want {
			t.Errorf("%s: quality %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestAlternativeTitleChainsAreBounded(t *testing.T) {
	media := finalProject()
	// Identical aliases commonly occur across TMDB translations. Repeated AKA
	// clauses must not turn those aliases into exponential matching work.
	for range 100 {
		media.AlternativeTitles.Results = append(media.AlternativeTitles.Results, struct {
			Title string `json:"title"`
		}{Title: "Final Project"})
	}
	if MatchTitle(strings.Repeat("Final Project AKA ", 20)+"Final Project S01 1080p WEB-DL", media, false) {
		t.Fatal("excessive alternative-title chain accepted")
	}
}
