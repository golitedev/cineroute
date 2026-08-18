package companion

import (
	"os"
	"testing"

	"cineroute/internal/prowlarr"
)

func TestFilterAndRankCompanionReleases(t *testing.T) {
	seeders := 43
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7}
	releases := []prowlarr.Release{
		{Guid: "best", Title: "Movie.2024.1080p.WEB-DL.DDP5.1.DUAL.H.265-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders},
		{Guid: "remux", Title: "Movie.2024.1080p.BluRay.REMUX-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders},
		{Guid: "uhd", Title: "Movie.2024.2160p.WEB-DL.DUAL-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders},
		{Guid: "other", Title: "Movie.2024.1080p.WEB-DL-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 999, Seeders: &seeders},
	}
	got := FilterAndRank(releases, "Movie", 2024, 123, policy)
	if len(got) != 1 || got[0].Guid != "best" {
		t.Fatalf("filtered candidates: %+v", got)
	}
	if got[0].Source != "WEB-DL" || got[0].Codec != "HEVC" {
		t.Fatalf("candidate evidence: %+v", got[0])
	}
	if got[0].LanguageEvidence != "Likely dual audio" {
		t.Fatalf("language evidence: %q", got[0].LanguageEvidence)
	}
}

func TestLATTeamGroupNameIsNotSpanishEvidence(t *testing.T) {
	seeders := 3
	got := FilterAndRank([]prowlarr.Release{{
		Guid: "latteam", Title: "Movie.2024.1080p.WEB-DL.H.265-LATTEAM", Size: 2 << 30,
		IndexerID: 7, Seeders: &seeders,
	}}, "Movie", 2024, 0, Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != 1 {
		t.Fatalf("expected LATTEAM release to remain reviewable: %+v", got)
	}
	if got[0].LanguageEvidence != "Unknown — inspect tracker" {
		t.Fatalf("LATTEAM was treated as language evidence: %q", got[0].LanguageEvidence)
	}
	h264 := 2
	avc := FilterAndRank([]prowlarr.Release{{
		Guid: "avc", Title: "Movie.2024.1080p.WEB-DL.H.264-GROUP", Size: 2 << 30,
		IndexerID: 7, Seeders: &h264,
	}}, "Movie", 2024, 0, Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(avc) != 1 || avc[0].Codec != "AVC" {
		t.Fatalf("H.264 evidence: %+v", avc)
	}
}

func TestCompanionScannerRecognizesTopLevelCopies(t *testing.T) {
	root := t.TempDir()
	makeMovie := func(name string, files ...string) string {
		t.Helper()
		path := root + "/" + name
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if err := os.WriteFile(path+"/"+file, nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	if got := inspectMovieFolder(makeMovie("Dune (2021)", "Dune.2021.2160p.REMUX.mkv"), "Dune (2021)"); got.Quality != "4k" {
		t.Fatalf("4k inspection: %+v", got)
	}
	if got := inspectMovieFolder(makeMovie("Alien (1979)", "Alien.1979.2160p.mkv", "Alien.1979.1080p.WEB-DL.mkv"), "Alien (1979)"); got.Quality != "1080p" {
		t.Fatalf("1080p inspection: %+v", got)
	}
	if got := inspectMovieFolder(makeMovie("1917 (2019)", "one.mkv", "two.mkv"), "1917 (2019)"); got.Quality != "multiple" {
		t.Fatalf("multiple-file inspection: %+v", got)
	}
}
