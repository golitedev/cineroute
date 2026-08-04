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
