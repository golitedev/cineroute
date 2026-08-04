package classifier

import "testing"

func TestClassifyMovie(t *testing.T) {
	r := Classify("Toy.Story.1995.REPACK.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.HYBRID.REMUX-FraMeSToR.mkv", nil)
	if r.MediaType != "movie" || r.Year != 1995 || r.Title != "Toy Story" {
		t.Fatalf("got %+v", r)
	}
	if r.Confidence != "high" {
		t.Fatalf("confidence: %s", r.Confidence)
	}
}

func TestClassifyTV(t *testing.T) {
	r := Classify("Lost.S02.1080p.DSNP.WEB-DL.DDP5.1.H.264-FLUX", []string{
		"Lost.S02E01.Man.of.Science.Man.of.Faith.1080p.mkv",
		"Lost.S02E02.Adrift.1080p.mkv",
	})
	if r.MediaType != "tv" || r.Season != 2 || r.Title != "Lost" {
		t.Fatalf("got %+v", r)
	}
	if r.Year != 0 {
		t.Fatalf("unexpected year: %d", r.Year)
	}
}

func TestClassifySeasonWord(t *testing.T) {
	r := Classify("Game.of.Thrones.Season.1.COMPLETE.1080p.BluRay.x264-GROUP", nil)
	if r.MediaType != "tv" || r.Season != 1 || r.Title != "Game of Thrones" {
		t.Fatalf("got %+v", r)
	}
}

func TestClassifyXXFormat(t *testing.T) {
	r := Classify("The.Expanse.1x05.1080p.WEB-DL-GROUP", nil)
	if r.MediaType != "tv" || r.Season != 1 || r.Title != "The Expanse" {
		t.Fatalf("got %+v", r)
	}
}

func TestClassifyMultiFileTVDetection(t *testing.T) {
	r := Classify("Some.Collection.VOL.1", []string{
		"Some.Collection.S01E01.mkv",
		"Some.Collection.S01E02.mkv",
		"Some.Collection.S01E03.mkv",
	})
	if r.MediaType != "tv" {
		t.Fatalf("expected tv, got %+v", r)
	}
}

func TestClassifySingleFileMovie(t *testing.T) {
	r := Classify("Dune.Part.Two.2024.1080p.WEB-DL", []string{"Dune.Part.Two.2024.1080p.WEB-DL.mkv"})
	if r.MediaType != "movie" {
		t.Fatalf("expected movie, got %+v", r)
	}
}

func TestClassifyCompleteSeries(t *testing.T) {
	r := Classify("Breaking.Bad.Complete.Series.1080p.BluRay-GROUP", nil)
	if r.MediaType != "tv" {
		t.Fatalf("expected tv, got %+v", r)
	}
}

// Year-like numbers in titles: the year after the title number must win, and
// a trailing title number must remain available as an alternate title.
func TestClassifyYearLikeTitles(t *testing.T) {
	cases := []struct {
		name     string
		title    string
		altTitle string
		year     int
	}{
		{"Blade.Runner.2049.2160p.WEB-DL.DDP5.1", "Blade Runner", "Blade Runner 2049", 2049},
		{"Blade.Runner.2049.2017.1080p.BluRay", "Blade Runner 2049", "Blade Runner 2049 2017", 2017},
		{"2012.2009.1080p.BluRay.x264-GROUP", "2012", "2012 2009", 2009},
		{"2001.A.Space.Odyssey.1968.2160p.UHD.BluRay", "2001 A Space Odyssey", "2001 A Space Odyssey 1968", 1968},
		{"1917.2019.1080p.BluRay.x264", "1917", "1917 2019", 2019},
		{"Alien.Romulus.2024.1080p.WEB-DL.DDP5.1.Atmos", "Alien Romulus", "Alien Romulus 2024", 2024},
		{"Widow's.Bay.2026.S01.1080p", "Widow's Bay", "Widow's Bay 2026", 2026},
	}
	for _, tc := range cases {
		r := Classify(tc.name, []string{tc.name + ".mkv"})
		if r.Title != tc.title || r.AltTitle != tc.altTitle || r.Year != tc.year {
			t.Errorf("%s: got title=%q alt=%q year=%d, want %q %q %d",
				tc.name, r.Title, r.AltTitle, r.Year, tc.title, tc.altTitle, tc.year)
		}
	}
}
