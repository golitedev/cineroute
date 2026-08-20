package companion

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"cineroute/internal/prowlarr"
)

func TestRankCompanionReleasesKeepsAllQualityVariants(t *testing.T) {
	seeders := 43
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7}
	releases := []prowlarr.Release{
		{Guid: "best", Title: "Movie.2024.1080p.WEB-DL.DDP5.1.DUAL.H.265-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders},
		{Guid: "remux", Title: "Movie.2024.1080p.BluRay.REMUX-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders},
		{Guid: "uhd", Title: "Movie.2024.2160p.WEB-DL.DUAL-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 123, Seeders: &seeders},
		{Guid: "other", Title: "Movie.2024.1080p.WEB-DL-GROUP", Size: 8 << 30, IndexerID: 7, TmdbID: 999, Seeders: &seeders},
	}
	got := FilterAndRank(releases, "Movie", 2024, 123, policy)
	if len(got) != len(releases) || got[0].Guid != "best" {
		t.Fatalf("ranked candidates: %+v", got)
	}
	seen := make(map[string]bool, len(got))
	for _, candidate := range got {
		seen[candidate.Guid] = true
	}
	for _, release := range releases {
		if !seen[release.Guid] {
			t.Fatalf("Prowlarr release %q was filtered: %+v", release.Guid, got)
		}
	}
	if got[0].Source != "WEB-DL" || got[0].Codec != "HEVC" {
		t.Fatalf("candidate evidence: %+v", got[0])
	}
	if got[0].LanguageEvidence != "Likely dual audio" {
		t.Fatalf("language evidence: %q", got[0].LanguageEvidence)
	}
}

func TestTVPackEligibilityClassifiesEpisodesAndPacks(t *testing.T) {
	tests := []struct {
		title    string
		eligible bool
	}{
		{"Breaking.Bad.S03E04.1080p.WEB-DL", false},
		{"Breaking.Bad.3x04.1080p.WEB-DL", false},
		{"Breaking.Bad.S03.1080p.WEB-DL", true},
		{"Breaking.Bad.Season.3.1080p.WEB-DL", true},
		{"Breaking.Bad.Complete.Series.1080p.WEB-DL", true},
		{"Breaking.Bad.1080p.WEB-DL", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			eligible, _ := TVPackEligibility(tt.title)
			if eligible != tt.eligible {
				t.Fatalf("TVPackEligibility(%q) = %v, want %v", tt.title, eligible, tt.eligible)
			}
		})
	}

	releases := []prowlarr.Release{
		{Guid: "episode", Title: "Breaking.Bad.S03E04.1080p.WEB-DL"},
		{Guid: "season", Title: "Breaking.Bad.S03.1080p.WEB-DL"},
	}
	candidates := FilterAndRank(releases, "Breaking Bad", 0, 0, Policy{})
	MarkTVPackCandidates(candidates)
	if len(candidates) != len(releases) {
		t.Fatalf("TV candidates were filtered: %+v", candidates)
	}
	if candidates[0].TVPackEligible == candidates[1].TVPackEligible {
		t.Fatalf("TV pack annotations = %+v", candidates)
	}
}

func TestFilterTVEpisodeReleasesRemovesEpisodeMarkers(t *testing.T) {
	releases := []prowlarr.Release{
		{Guid: "episode-06", Title: "Show.S01E06.1080p.WEB-DL"},
		{Guid: "episode-34", Title: "Show.S04E34.1080p.WEB-DL"},
		{Guid: "attached-episode", Title: "ShowS05E02WEB-DL"},
		{Guid: "season", Title: "Show.S01.1080p.WEB-DL"},
		{Guid: "series", Title: "Show.Complete.Series.1080p.WEB-DL"},
	}
	got := filterTVEpisodeReleases(releases)
	if len(got) != 2 || got[0].Guid != "season" || got[1].Guid != "series" {
		t.Fatalf("TV episode releases were not filtered: %+v", got)
	}
}

func TestSortTVPackCandidatesGroupsSeasonsAndPreservesRank(t *testing.T) {
	candidates := []Candidate{
		{Guid: "s03-best", Title: "Show.S03.1080p.WEB-DL"},
		{Guid: "s01-best", Title: "Show.S01.1080p.WEB-DL"},
		{Guid: "s03-alt", Title: "Show.S03.REPACK.1080p.WEB-DL"},
		{Guid: "series", Title: "Show.Complete.Series.1080p.WEB-DL"},
		{Guid: "s02-best", Title: "Show.S02.1080p.WEB-DL"},
		{Guid: "s01-alt", Title: "Show.S01.REPACK.1080p.WEB-DL"},
	}
	sortTVPackCandidates(candidates)

	want := []string{"s01-best", "s01-alt", "s02-best", "s03-best", "s03-alt", "series"}
	for i, guid := range want {
		if candidates[i].Guid != guid {
			t.Fatalf("candidate order = %v, want %v", candidateGuids(candidates), want)
		}
	}
}

func candidateGuids(candidates []Candidate) []string {
	guids := make([]string, len(candidates))
	for i, candidate := range candidates {
		guids[i] = candidate.Guid
	}
	return guids
}

func TestPossessiveTitleSpellingsRemainEligible(t *testing.T) {
	seeders := 36
	release := prowlarr.Release{
		Guid:      "schindlers-max",
		Title:     "Schindlers.List.1993.1080p.MAX.WEB-DL.DDP2.0.H.264-LatTeam.mkv SPANiSH",
		Size:      11 << 30,
		IndexerID: 7,
		Seeders:   &seeders,
	}
	got := FilterAndRank([]prowlarr.Release{release}, "Schindler's List", 1993, 0, Policy{
		MaxBytes:        15 << 30,
		MinSeeders:      1,
		TargetIndexerID: 7,
	})
	if len(got) != 1 || got[0].Guid != release.Guid {
		t.Fatalf("possessive title spelling was filtered: %+v", got)
	}
	if got[0].Source != "WEB-DL" {
		t.Fatalf("MAX release source = %q, want WEB-DL", got[0].Source)
	}
}

func TestUnrelatedTitleAndYearRemainVisibleButRankLower(t *testing.T) {
	seeders := 10
	got := FilterAndRank([]prowlarr.Release{
		{Guid: "alive", Title: "Alive.1993.1080p.WEB-DL.H.264-GROUP", Size: 5 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "alive-1991", Title: "Alive.1991.1080p.WEB-DL.H.264-GROUP", Size: 5 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "alice-1951", Title: "Alice.In.Wonderland.1951.1080p.WEB-DL.H.264-GROUP", Size: 4 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "alice-2010", Title: "Alice.In.Wonderland.2010.1080p.WEB-DL.H.264-GROUP", Size: 4 << 30, IndexerID: 7, Seeders: &seeders},
	}, "Alive", 1993, 0, Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != 4 || got[0].Guid != "alive" {
		t.Fatalf("unrelated releases were not retained and ranked: %+v", got)
	}
}

func TestMatchingTitlesWithinOneYearRemainReviewable(t *testing.T) {
	seeders := 10
	got := FilterAndRank([]prowlarr.Release{
		{Guid: "portrait-2020", Title: "Portrait.of.a.Lady.on.Fire.2020.1080p.WEB-DL.H.264-LatTeam", Size: 7 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "portrait-2018", Title: "Portrait.of.a.Lady.on.Fire.2018.1080p.WEB-DL.H.264-LatTeam", Size: 7 << 30, IndexerID: 7, Seeders: &seeders},
	}, "Portrait of a Lady on Fire", 2019, 0, Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != 2 {
		t.Fatalf("one-year title matches were filtered: %+v", got)
	}
	if !strings.Contains(strings.Join(got[0].Reasons, " "), "year differs by 1") {
		t.Fatalf("one-year mismatch was not explained: %+v", got[0].Reasons)
	}
}

func TestCompanionRankingPrefersWebDLAndCompatibleBluRay(t *testing.T) {
	seeders := 10
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7}
	releases := []prowlarr.Release{
		{Guid: "web-small", Title: "Movie.2024.1080p.WEB-DL.H.264-GROUP", Size: 7 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "web-large", Title: "Movie.2024.1080p.WEB-DL.H.264-LATTEAM", Size: 12 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "bluray-x264", Title: "Movie.2024.1080p.BluRay.H.264-GROUP", Size: 14 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "bluray-x265", Title: "Movie.2024.1080p.BluRay.H.265-GROUP", Size: 1 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "extra-1", Title: "Movie.2024.1080p.WEBRip.H.264-GROUP", Size: 9 << 30, IndexerID: 7, Seeders: &seeders},
		{Guid: "extra-2", Title: "Movie.2024.1080p.WEBRip.H.264-OTHER", Size: 10 << 30, IndexerID: 7, Seeders: &seeders},
	}
	got := FilterAndRank(releases, "Movie", 2024, 0, policy)
	if len(got) != len(releases) {
		t.Fatalf("candidate count = %d, want %d", len(got), len(releases))
	}
	wantOrder := []string{"web-small", "web-large", "bluray-x264", "bluray-x265", "extra-1", "extra-2"}
	for i, want := range wantOrder {
		if got[i].Guid != want {
			t.Fatalf("candidate %d = %s, want %s (all: %+v)", i, got[i].Guid, want, got)
		}
	}
	if !strings.Contains(strings.Join(got[0].Reasons, " "), "sweet spot") {
		t.Fatalf("small WEB-DL did not receive the sweet-spot reason: %+v", got[0].Reasons)
	}
}

func TestProwlarrTitlesRemainVisibleAndRankWithMetadata(t *testing.T) {
	seeders := []int{5, 8, 1, 8}
	policy := Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 5}
	releases := []prowlarr.Release{
		{Title: "12.Angry.Men.1957.1957.1008p.AMZN.WEB-DL.DDP2.0.H.264-LatTeam.mkv SPANiSH", Size: 7503189132, IndexerID: 5, Indexer: "Lat-Team (API)", Seeders: &seeders[0], DownloadURL: "/api/v1/indexer/5/download"},
		{Guid: "bluray-flac", Title: "12.Angry.Men.1957.BluRay.1080p.DD2.0.x264-ARV.mkv SPANiSH", Size: 15813099620, IndexerID: 5, Seeders: &seeders[1]},
		{Guid: "bluray-x265", Title: "12 Angry Men 1957 1080p BluRay AAC 1.0 x265 SPANiSH", Size: 1422567022, IndexerID: 5, Seeders: &seeders[2]},
		{Guid: "spanish-title", Title: "12 hombres en pugna 1957 1080p DD 2.0 MKV x264 BDRip LatTeam.mkv SPANiSH", Size: 3328724014, IndexerID: 5, Seeders: &seeders[3]},
	}
	got := FilterAndRank(releases, "12 Angry Men", 1957, 0, policy)
	if len(got) != len(releases) || got[0].Title != releases[0].Title || got[0].Guid == "" {
		t.Fatalf("Prowlarr titles: %+v", got)
	}
	if !strings.Contains(strings.Join(got[0].Reasons, " "), "1008p") {
		t.Fatalf("1008p tracker typo was not explained: %+v", got[0].Reasons)
	}
	again := FilterAndRank(releases, "12 Angry Men", 1957, 0, policy)
	if got[0].Guid != again[0].Guid {
		t.Fatalf("guidless release ID changed between searches: %q != %q", got[0].Guid, again[0].Guid)
	}
}

func TestFormerCompanionFiltersOnlyAffectRanking(t *testing.T) {
	zero := 0
	got := FilterAndRank([]prowlarr.Release{
		{Guid: "4k-remux", Title: "Different.Movie.1999.2160p.UHD.BluRay.REMUX", Size: 100 << 30, IndexerID: 99, TmdbID: 999, Seeders: &zero},
		{Guid: "cam", Title: "Unrelated.Movie.2020.480p.CAM", Size: 0, IndexerID: 99},
		{Guid: "alternate", Title: "Ciudad.de.Dios.2002.720p.WEBRip", Size: 2 << 30, IndexerID: 99, Seeders: &zero},
	}, "City of God", 2002, 598, Policy{MaxBytes: 20 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != 3 {
		t.Fatalf("formerly filtered releases were dropped: %+v", got)
	}
	byID := make(map[string]Candidate, len(got))
	for _, candidate := range got {
		byID[candidate.Guid] = candidate
	}
	for _, id := range []string{"4k-remux", "cam", "alternate"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("release %q was filtered: %+v", id, got)
		}
	}
	if byID["4k-remux"].Source != "BluRay REMUX" || byID["cam"].Source != "CAM/TS" {
		t.Fatalf("source ranking evidence missing: %+v", byID)
	}
}

func TestCityOfGodAlternateWEBDLRanksFirstWithoutFilteringOtherRows(t *testing.T) {
	seeders := []int{15, 5, 8, 1, 39}
	releases := []prowlarr.Release{
		{Guid: "webdl", Title: "La.Cite.De.Dieu.2002.1080p.MAX.WEB-DL.DDP2.0.H.264-LatTeam.mkv SPANiSH", Size: 7500000000, IndexerID: 7, Seeders: &seeders[0]},
		{Guid: "bluray-small", Title: "City.Of.God.(2002).1080p.BluRay.LITE.AC3.6ch.h264.mkv SPANiSH", Size: 2530000000, IndexerID: 7, Seeders: &seeders[1]},
		{Guid: "bluray-large", Title: "City.Of.God.2002.1080p.BluRay.DD.+5.1.x264-ARV.mkv SPANiSH", Size: 18880000000, IndexerID: 7, Seeders: &seeders[2]},
		{Guid: "remux", Title: "Ciudad.De.Dios.2002.1080p.REMUX.DTS-HD.H264.mkv SPANiSH", Size: 28300000000, IndexerID: 7, Seeders: &seeders[3]},
		{Guid: "no-resolution", Title: "Ciudad de Dios (2002).mkv SPANiSH", Size: 2500000000, IndexerID: 7, Seeders: &seeders[4]},
	}
	got := FilterAndRank(releases, "City of God", 2002, 0, Policy{MaxBytes: 20 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != len(releases) || got[0].Guid != "webdl" {
		t.Fatalf("City of God releases were not ranked as expected: %+v", got)
	}
	seen := make(map[string]bool, len(got))
	for _, candidate := range got {
		seen[candidate.Guid] = true
	}
	for _, release := range releases {
		if !seen[release.Guid] {
			t.Fatalf("City of God release %q was filtered: %+v", release.Guid, got)
		}
	}
}

func TestRankCapsVisibleProwlarrReleasesAtFifty(t *testing.T) {
	seeders := 10
	releases := make([]prowlarr.Release, MaxCandidateResults+5)
	for i := range releases {
		releases[i] = prowlarr.Release{
			Guid:      "release-" + strconv.Itoa(i),
			Title:     "Movie.2024.1080p.WEB-DL.H.264-GROUP",
			Size:      int64(i+1) << 20,
			Seeders:   &seeders,
			IndexerID: 7,
		}
	}
	got := FilterAndRank(releases, "Movie", 2024, 0, Policy{})
	if len(got) != MaxCandidateResults {
		t.Fatalf("candidate count = %d, want %d", len(got), MaxCandidateResults)
	}
}

func TestDistinctProwlarrReleasesWithSharedGuidRemainEligible(t *testing.T) {
	seeders := 5
	releases := []prowlarr.Release{
		{Guid: "shared-provider-guid", Title: "Movie.2024.1080p.WEB-DL.H.264-GROUP", Size: 7 << 30, IndexerID: 7, Indexer: "Lat-Team (API)", Seeders: &seeders},
		{Guid: "shared-provider-guid", Title: "Movie.2024.1080p.BluRay.x264-GROUP", Size: 12 << 30, IndexerID: 7, Indexer: "Lat-Team (API)", Seeders: &seeders},
	}
	merged := mergeReleases(nil, releases)
	merged = mergeReleases(merged, releases)
	if len(merged) != 2 {
		t.Fatalf("shared GUID merge returned %d releases: %+v", len(merged), merged)
	}
	got := FilterAndRank(merged, "Movie", 2024, 0, Policy{MaxBytes: 15 << 30, MinSeeders: 1, TargetIndexerID: 7})
	if len(got) != 2 || got[0].Guid == got[1].Guid {
		t.Fatalf("shared GUID candidates are missing or ambiguous: %+v", got)
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

func TestCompanionScannerRecognizesMovieCopies(t *testing.T) {
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
	if got := inspectMovieFolder(makeMovie("Dune (2021)", "Dune.2021.2160p.REMUX.mkv"), "Dune (2021)"); got.Quality != "4k" || len(got.Files) != 1 || got.Files[0] != "Dune.2021.2160p.REMUX.mkv" {
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
	nestedMovie := makeMovie("The Amazing Spider-Man (2012)")
	releaseFolder := filepath.Join(nestedMovie, "The.Amazing.Spider-Man.2012.UHD.BluRay.2160p.REMUX")
	if err := os.MkdirAll(releaseFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseFolder, "The.Amazing.Spider-Man.2012.2160p.REMUX.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := inspectMovieFolder(nestedMovie, "The Amazing Spider-Man (2012)"); got.Quality != "4k" || got.Error != "" {
		t.Fatalf("nested 4k inspection: %+v", got)
	}
}
