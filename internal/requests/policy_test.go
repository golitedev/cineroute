package requests

import (
	"testing"

	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

func TestReleaseMatchingRejectsWrongIdentityAndPolicy(t *testing.T) {
	media := tmdb.Details{Result: tmdb.Result{ID: 101, Title: "Example Movie", ReleaseDate: "2024-01-01", MediaType: "movie"}, OriginalLanguage: "en"}
	p := Defaults().Movie
	seeds := 5
	base := prowlarr.Release{Title: "Example.Movie.2024.1080p.WEB-DL.x264.ENG", IndexerID: 1, Seeders: &seeds, Size: 1 << 30}
	if _, err := Evaluate(base, media, p, 1, 1); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*prowlarr.Release){
		"sequel":              func(r *prowlarr.Release) { r.Title = "Example.Movie.Two.2024.1080p.WEB-DL.x264" },
		"remake":              func(r *prowlarr.Release) { r.Title = "Example.Movie.2023.1080p.WEB-DL.x264" },
		"wrong ID":            func(r *prowlarr.Release) { r.TmdbID = 999 },
		"unsupported quality": func(r *prowlarr.Release) { r.Title = "Example.Movie.2024.2160p.REMUX.x265" },
		"unknown seeds":       func(r *prowlarr.Release) { r.Seeders = nil },
		"oversize":            func(r *prowlarr.Release) { r.Size = 50 << 30 },
	} {
		t.Run(name, func(t *testing.T) {
			r := base
			mutate(&r)
			if _, err := Evaluate(r, media, p, 1, 1); err == nil {
				t.Fatal("unacceptable release was accepted")
			}
		})
	}
	p.AudioLanguages = []string{"es"}
	if _, err := Evaluate(base, media, p, 1, 1); err == nil {
		t.Fatal("wrong audio language accepted")
	}
	p.AudioLanguages = []string{}
	p.Keywords = []Keyword{{Pattern: "DUAL", Match: "contains", Mode: "required"}}
	if _, err := Evaluate(base, media, p, 1, 1); err == nil {
		t.Fatal("missing required keyword accepted")
	}
}
func TestTorrentContentsEnforceEpisodeCoverageAndActualSize(t *testing.T) {
	settings := Defaults()
	job := Job{Media: tmdb.Details{Result: tmdb.Result{ID: 202, Name: "Example Show", MediaType: "tv", FirstAirDate: "2024-01-01"}}}
	c := Candidate{Quality: "webdl-1080p", Pack: true}
	meta := &torrentmeta.MetaInfo{Name: "Example.Show.S01.1080p.WEB-DL.x264", Size: 2 << 30, Files: []torrentmeta.File{{FullPath: []string{"Example.Show.S01E01.mkv"}, Length: 1 << 30}, {FullPath: []string{"Example.Show.S01E02.mkv"}, Length: 1 << 30}}}
	wanted := map[string]bool{EpisodeKey(1, 1): true, EpisodeKey(1, 2): true}
	if keys, err := ValidateTorrent(meta, c, job, settings.TV, wanted, map[string]bool{}); err != nil || len(keys) != 2 {
		t.Fatalf("valid pack: %v %v", keys, err)
	}
	if _, err := ValidateTorrent(meta, c, job, settings.TV, wanted, map[string]bool{EpisodeKey(1, 1): true}); err == nil {
		t.Fatal("overlapping pack accepted")
	}
	delete(wanted, EpisodeKey(1, 2))
	if _, err := ValidateTorrent(meta, c, job, settings.TV, wanted, map[string]bool{}); err == nil {
		t.Fatal("unrequested episode accepted")
	}
	wanted[EpisodeKey(1, 2)] = true
	settings.TV.MaxPackSizeGiB = 1
	if _, err := ValidateTorrent(meta, c, job, settings.TV, wanted, map[string]bool{}); err == nil {
		t.Fatal("actual pack size limit ignored")
	}
}
