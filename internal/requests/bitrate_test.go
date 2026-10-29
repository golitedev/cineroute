package requests

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"testing"

	"cineroute/internal/medialanguages"
	"cineroute/internal/prowlarr"
)

func bitrateCandidate(group string, rate, size int64, seeds int) Candidate {
	return Candidate{Quality: "webdl-1080p", Rank: 0, Priority: 22, LanguageScore: 2, SubtitleFormatScore: 2, Release: prowlarr.Release{Title: "Moon.2009.1080p.AMZN.WEB-DL.H.264-" + group, Size: size, Seeders: &seeds, Languages: medialanguages.Metadata{VideoBitrateBPS: rate}}}
}

func TestHigherVideoBitrateWinsMoonTieBeforeSeedersAndSize(t *testing.T) {
	candidates := []Candidate{bitrateCandidate("SyncTeam", 4803000, 5379343099, 10), bitrateCandidate("LatTeam", 9096000, 7578226620, 9)}
	prepareBitrateRanking(candidates, DefaultSelectionOrder(), true)
	if !Better(candidates[1], candidates[0], DefaultSelectionOrder(), true) || Better(candidates[0], candidates[1], DefaultSelectionOrder(), true) {
		t.Fatal("LatTeam bitrate lost to extra seeders or smaller size")
	}
	order := []string{"quality", "language", "text_subtitles", "keywords", "pack", "priority", "seeders", "video_bitrate"}
	prepareBitrateRanking(candidates, order, true)
	if !Better(candidates[0], candidates[1], order, true) {
		t.Fatal("explicit seeders-first order was ignored")
	}
}

func TestBitrateCannotOverrideEarlierPriorities(t *testing.T) {
	for _, criterion := range []string{"quality", "language", "text_subtitles", "keywords", "priority"} {
		t.Run(criterion, func(t *testing.T) {
			lower := bitrateCandidate("Lower", 4803000, 5, 9)
			higher := bitrateCandidate("Higher", 9096000, 7, 9)
			switch criterion {
			case "quality":
				higher.Rank = 1
				higher.Quality = "bluray-1080p"
			case "language":
				higher.LanguageScore--
			case "text_subtitles":
				higher.SubtitleFormatScore--
			case "keywords":
				lower.Score = 10
			case "priority":
				lower.Priority = 1
			}
			c := []Candidate{lower, higher}
			prepareBitrateRanking(c, DefaultSelectionOrder(), true)
			if !Better(c[0], c[1], DefaultSelectionOrder(), true) {
				t.Fatalf("bitrate overrode %s", criterion)
			}
		})
	}
}

func TestBitrateFallsBackForUnknownReportsOrMixedQualityAndCodecs(t *testing.T) {
	for _, mode := range []string{"unknown", "equal", "different codec", "unknown codec", "different quality"} {
		t.Run(mode, func(t *testing.T) {
			a := bitrateCandidate("A", 4803000, 5, 10)
			b := bitrateCandidate("B", 9096000, 7, 9)
			order := []string{"video_bitrate", "quality", "language", "text_subtitles", "keywords", "pack", "priority", "seeders"}
			switch mode {
			case "unknown":
				a.Release.Languages.VideoBitrateBPS = 0
			case "equal":
				a.Release.Languages.VideoBitrateBPS = 9096000
			case "different codec":
				b.Release.Title = "Moon.2009.1080p.WEB-DL.HEVC"
			case "unknown codec":
				b.Release.Title = "Moon.2009.1080p.WEB-DL"
			case "different quality":
				b.Quality = "bluray-1080p"
			}
			c := []Candidate{a, b}
			prepareBitrateRanking(c, order, true)
			if !Better(c[0], c[1], order, true) {
				t.Fatal("bitrate bypassed seeders fallback for incomparable releases")
			}
			c[1].Release.Seeders = c[0].Release.Seeders
			if !Better(c[0], c[1], order, true) {
				t.Fatal("size fallback was lost")
			}
		})
	}
}

func TestUnknownBitrateDoesNotCreateSortCycles(t *testing.T) {
	source := []Candidate{bitrateCandidate("A", 4803000, 5, 9), bitrateCandidate("B", 9096000, 7, 9), bitrateCandidate("Unknown", 0, 6, 9)}
	for _, p := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
		c := []Candidate{source[p[0]], source[p[1]], source[p[2]]}
		prepareBitrateRanking(c, DefaultSelectionOrder(), true)
		sort.SliceStable(c, func(i, j int) bool { return Better(c[i], c[j], DefaultSelectionOrder(), true) })
		if c[0].Release.Size != 5 || c[1].Release.Size != 6 || c[2].Release.Size != 7 {
			t.Fatalf("unknown report made ranking depend on input order: %v", p)
		}
	}
}

func TestLegacySelectionOrderUpgradesAndPersistsCustomPriorities(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	settings := Defaults()
	settings.SelectionOrder = []string{"language", "quality", "text_subtitles", "keywords", "pack", "priority", "seeders"}
	payload, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO request_settings(id,payload) VALUES(1,?)", string(payload)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"language", "quality", "text_subtitles", "keywords", "pack", "priority", "video_bitrate", "seeders"}
	if !slices.Equal(got.SelectionOrder, want) {
		t.Fatalf("wrong migrated order: %v", got.SelectionOrder)
	}
	if err := store.Save(got); err != nil {
		t.Fatal(err)
	}
	got, err = NewStore(store.path).Load()
	if err != nil || !slices.Equal(got.SelectionOrder, want) {
		t.Fatal("migrated order was not persistent")
	}
}
