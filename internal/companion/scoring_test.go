package companion

import (
	"os"
	"strings"
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
	if len(got) != 2 || got[0].Guid != "best" {
		t.Fatalf("filtered candidates: %+v", got)
	}
	if got[0].Source != "WEB-DL" || got[0].Codec != "HEVC" {
		t.Fatalf("candidate evidence: %+v", got[0])
	}
	if got[0].LanguageEvidence != "Likely dual audio" {
		t.Fatalf("language evidence: %q", got[0].LanguageEvidence)
	}
}

func TestCompanionRankingPrefersWebDLAndCompatibleBluRay(t *testing.T) {
	seeders := 10
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7}
	got := FilterAndRank([]prowlarr.Release{
		{Guid: "web-small", Title: "Movie.2024.1080p.WEB-DL.H.264-GROUP", Size: 7 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "web-large", Title: "Movie.2024.1080p.WEB-DL.H.264-LATTEAM", Size: 12 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "bluray-x264", Title: "Movie.2024.1080p.BluRay.H.264-GROUP", Size: 14 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "bluray-x265", Title: "Movie.2024.1080p.BluRay.H.265-GROUP", Size: 1 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "extra-1", Title: "Movie.2024.1080p.WEBRip.H.264-GROUP", Size: 9 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "extra-2", Title: "Movie.2024.1080p.WEBRip.H.264-OTHER", Size: 10 << 30, IndexerID: 7, Seeders: &seeders},
	}, "Movie", 2024, 0, policy)
	if len(got) != MaxCandidateResults {
		t.Fatalf("candidate count = %d, want %d", len(got), MaxCandidateResults)
	}
	wantOrder := []string{"web-small", "web-large", "bluray-x264", "bluray-x265"}
	for i, want := range wantOrder {
		if got[i].Guid != want {
			t.Fatalf("candidate %d = %s, want %s (all: %+v)", i, got[i].Guid, want, got)
		}
	}
	if !strings.Contains(strings.Join(got[0].Reasons, " "), "sweet spot") {
		t.Fatalf("small WEB-DL did not receive the sweet-spot reason: %+v", got[0].Reasons)
	}
}

func TestExactProwlarrTitlesAreEligible(t *testing.T) {
	seeders := []int{5, 8, 1, 8}
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7}
	releases := []prowlarr.Release{
		{Title: "12.Angry.Men.1957.1957.1080p.AMZN.WEB-DL.DDP2.0.H.264-LatTeam.mkv SPANISH", Size: 7 << 30, IndexerID: 7, Indexer: "Lat-Team (API)", Seeders: &seeders[0], DownloadURL: "/api/v1/indexer/7/download"},
		{Guid: "bluray-flac", Title: "12.Angry.Men.1957.BluRay.1080p.DD2.0.x264-ARV.mkv SPANISH", Size: 14 << 30, IndexerID: 7, Seeders: &seeders[1]},
		{Guid: "bluray-x265", Title: "12 Angry Men 1957 1080p BluRay AAC 1.0 x265 SPANISH", Size: 1 << 30, IndexerID: 7, Seeders: &seeders[2]},
		{Guid: "spanish-title", Title: "12 hombres en pugna 1957 1080p DD 2.0 MKV x264 BDRip LatTeam.mkv SPANISH", Size: 3 << 30, IndexerID: 7, Seeders: &seeders[3]},
	}
	got := FilterAndRank(releases, "12 Angry Men", 1957, 0, policy)
	if len(got) != 4 || got[0].Title != releases[0].Title || got[0].Guid == "" {
		t.Fatalf("exact Prowlarr titles: %+v", got)
	}
	again := FilterAndRank(releases, "12 Angry Men", 1957, 0, policy)
	if got[0].Guid != again[0].Guid {
		t.Fatalf("guidless release ID changed between searches: %q != %q", got[0].Guid, again[0].Guid)
	}
}

func TestLanguageEvidenceDoesNotChangeRankingScore(t *testing.T) {
	seeders := 3
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7}
	got := FilterAndRank([]prowlarr.Release{
		{Guid: "plain", Title: "Movie.2024.1080p.WEB-DL.H.264-GROUP", Size: 2 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "spanish", Title: "Movie.2024.1080p.WEB-DL.H.264.SPANISH-GROUP", Size: 2 << 30, IndexerID: 7, Seeders: &seeders},
	}, "Movie", 2024, 0, policy)
	if len(got) != 2 || got[0].Score != got[1].Score {
		t.Fatalf("language changed ranking score: %+v", got)
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

func TestExactTMDBMatchAllowsAlternateReleaseTitle(t *testing.T) {
	seeders := 4
	got := FilterAndRank([]prowlarr.Release{{
		Guid:  "original-title",
		Title: "Le Fabuleux Destin d\u0027Amelie.2001.1080p.WEB-DL-GROUP",
		Size:  2 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders,
	}}, "Amelie", 2001, 123, Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != 1 || got[0].Guid != "original-title" {
		t.Fatalf("exact TMDB match should survive alternate title: %+v", got)
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
	if got := inspectMovieFolder(makeMovie("Alien (1979)", "Alien.1979.2160p.mkv", "Alien.1979.1080p.WEB-DL.mkv"), "Alien (1979)"); got.Quality != "1080p" || !got.Has1080pWebDL || got.Has1080pBluRay {
		t.Fatalf("1080p inspection: %+v", got)
	}
	if got := inspectMovieFolder(makeMovie("Heat (1995)", "Heat.1995.1080p.BluRay.REMUX.mkv"), "Heat (1995)"); got.Quality != "1080p" || got.Has1080pWebDL || !got.Has1080pBluRay {
		t.Fatalf("1080p BluRay inspection: %+v", got)
	}
	if got := inspectMovieFolder(makeMovie("1917 (2019)", "one.mkv", "two.mkv"), "1917 (2019)"); got.Quality != "multiple" {
		t.Fatalf("multiple-file inspection: %+v", got)
	}
}
