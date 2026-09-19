package subtitles

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/subtitles/opensubtitles"
)

// TestRatioMatchesPythonDifflib pins the similarity metric to the values the
// proven pipeline's thresholds were tuned against.
func TestRatioMatchesPythonDifflib(t *testing.T) {
	cases := []struct {
		left, right string
		want        float64
	}{
		{"Alladin", "Alladin", 1.0},
		{"Aladdin.2019.1080p", "Aladdin.2019.1080p.DSNP.WEB-DL", 0.75},
		{"el laberinto del fauno", "Pan's Labyrinth", 0.3783783784},
		{"avatar the way of water", "Avatar 2", 0.3870967742},
		{"", "abc", 0.0},
		{"12 Angry Men", "12 Angry Men 1957", 0.8275862069},
		{"", "", 1.0},
	}
	for _, tc := range cases {
		got := Ratio(tc.left, tc.right)
		if math.Abs(got-tc.want) > 1e-6 {
			t.Errorf("Ratio(%q, %q) = %.10f, want %.10f", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestParseTitleYear(t *testing.T) {
	cases := []struct {
		basename string
		title    string
		year     int
	}{
		{"12.Angry.Men.1957.1957.1008p.AMZN.WEB-DL.DDP2.0.H.264-LatTeam", "12 Angry Men", 1957},
		{"1984.1984.1080p.AMZN.WEB-DL.DDP2.0.H.264-LatTeam", "1984", 1984},
		{"All.Quiet.On.The.Western.Front.2022.1080p.NF.WEB-DL.DDP.5.1.h264", "All Quiet On The Western Front", 2022},
		{"Blade.Runner.2049.2017.1080p.BluRay.x264", "Blade Runner 2049", 2017},
		{"A.Poet.2025.1080p.AMZN.WEB-DL.H.264-BiOMA", "A Poet", 2025},
		{"Some.Movie.1080p.WEB-DL", "Some Movie", 0},
	}
	for _, tc := range cases {
		title, year := ParseTitleYear(tc.basename)
		if title != tc.title || year != tc.year {
			t.Errorf("ParseTitleYear(%q) = (%q, %d), want (%q, %d)", tc.basename, title, year, tc.title, tc.year)
		}
	}
}

func TestNormalizedTitleAndIdentities(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Pan's Labyrinth", "pans labyrinth"},
		{"Fast & Furious", "fast and furious"},
		{"Avatar: The Way of Water", "avatar the way of water"},
		// Python's normalized_title keeps the ASCII word findall, so an accented
		// title splits at the accent; NormalizedIdentity folds it instead.
		{"AMÉLIE", "am lie"},
	}
	for _, tc := range cases {
		if got := NormalizedTitle(tc.in); got != tc.want {
			t.Errorf("NormalizedTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := NormalizedIdentity("Amélie & Co"); got != "amelie and co" {
		t.Errorf("NormalizedIdentity = %q, want %q", got, "amelie and co")
	}
}

func TestSourceAndEditionFamilies(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Movie.2019.1080p.AMZN.WEB-DL.DDP5.1", "web"},
		{"Movie.2019.1080p.BluRay.x264", "bluray"},
		{"Movie.2019.DVDRip.XviD", "dvd"},
		{"Movie.2019.720p.HDTV.x264", "hdtv"},
		{"Movie.2019.1080p", ""},
	}
	for _, tc := range cases {
		if got := SourceFamily(Tokens(tc.text)); got != tc.want {
			t.Errorf("SourceFamily(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
	families := EditionFamily(Tokens("Movie.2019.Extended.Directors.Cut.1080p"))
	if !families["extended"] || !families["directors-cut"] {
		t.Errorf("EditionFamily = %v, want extended and directors-cut", families)
	}
	if years := FindYears("Movie.2049.2017.1080p"); len(years) != 2 || years[0] != "2049" || years[1] != "2017" {
		t.Errorf("FindYears = %v, want [2049 2017]", years)
	}
}

func TestOpenSubtitlesHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "video.mkv")
	data := make([]byte, 200000)
	for i := range data {
		data[i] = byte((i*7 + 3) % 256)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	got, err := OpenSubtitlesHash(path)
	if err != nil {
		t.Fatalf("OpenSubtitlesHash: %v", err)
	}
	// Reference value computed with the standard Python implementation.
	if got != "60a0df1f5fa2cd40" {
		t.Errorf("OpenSubtitlesHash = %s, want 60a0df1f5fa2cd40", got)
	}
}

const sampleSRT = "1\n00:00:01,000 --> 00:00:03,000\nHello\n\n2\n00:00:04,500 --> 00:00:06,000\nWorld\n\n"

func TestDecodeSubtitlePayload(t *testing.T) {
	plain, err := DecodeSubtitlePayload([]byte(sampleSRT))
	if err != nil {
		t.Fatalf("plain payload: %v", err)
	}
	// Decoding normalizes trailing newlines to exactly one, like the Python
	// helper (`text.rstrip() + "\n"`).
	if want := strings.TrimRight(sampleSRT, "\n") + "\n"; plain != want {
		t.Errorf("plain payload normalized to %q, want %q", plain, want)
	}

	// cp1252 fallback: a lone 0xE9 byte is not valid UTF-8.
	cp1252 := []byte("1\n00:00:01,000 --> 00:00:03,000\nCaf\xe9\n\n")
	text, err := DecodeSubtitlePayload(cp1252)
	if err != nil {
		t.Fatalf("cp1252 payload: %v", err)
	}
	if !bytes.Contains([]byte(text), []byte("Café")) {
		t.Errorf("cp1252 payload decoded to %q", text)
	}

	var gzipped bytes.Buffer
	writer := gzip.NewWriter(&gzipped)
	_, _ = writer.Write([]byte(sampleSRT))
	_ = writer.Close()
	if _, err := DecodeSubtitlePayload(gzipped.Bytes()); err != nil {
		t.Errorf("gzip payload: %v", err)
	}

	var zipped bytes.Buffer
	archive := zip.NewWriter(&zipped)
	entry, _ := archive.Create("Movie.sv.srt")
	_, _ = entry.Write([]byte(sampleSRT))
	_ = archive.Close()
	if _, err := DecodeSubtitlePayload(zipped.Bytes()); err != nil {
		t.Errorf("zip payload: %v", err)
	}

	if _, err := DecodeSubtitlePayload([]byte("not a subtitle at all")); err == nil {
		t.Error("expected an error for a payload without SRT timestamps")
	}
}

func TestCueTimesAndCleanup(t *testing.T) {
	cues, err := CueTimesFromText(sampleSRT)
	if err != nil {
		t.Fatalf("CueTimesFromText: %v", err)
	}
	if len(cues) != 2 || cues[0].Start != 1000 || cues[1].End != 6000 {
		t.Errorf("cues = %+v", cues)
	}
	if _, err := CueTimesFromText("1\n00:00:05,000 --> 00:00:01,000\nbackwards\n"); err != ErrCueEndBeforeStart {
		t.Errorf("expected ErrCueEndBeforeStart, got %v", err)
	}

	promos := "1\n00:00:01,000 --> 00:00:02,000\nVisit https://example.com\n\n" +
		"2\n00:00:03,000 --> 00:00:04,000\nReal line\n\n"
	cleaned, removed := RemovePromoCues(promos)
	if removed != 1 || bytes.Contains([]byte(cleaned), []byte("example.com")) {
		t.Errorf("RemovePromoCues removed %d, cleaned=%q", removed, cleaned)
	}

	malformed := "1\n00:00:01,000 --> 00:20:00,000\nBroken long cue\n\n" +
		"2\n00:00:05,000 --> 00:00:06,000\nFine\n\n"
	repaired, dropped := RepairMalformedCues(malformed)
	if dropped != 1 || bytes.Contains([]byte(repaired), []byte("Broken long cue")) {
		t.Errorf("RepairMalformedCues dropped %d, repaired=%q", dropped, repaired)
	}
}

func TestComputeMetricsAcceptsAndRejects(t *testing.T) {
	reference := make([]TimeSpan, 0, 10)
	for i := 0; i < 10; i++ {
		start := int64(i) * 10000
		reference = append(reference, TimeSpan{Start: start, End: start + 3000})
	}

	shifted := make([]TimeSpan, 0, len(reference))
	for _, span := range reference {
		shifted = append(shifted, TimeSpan{Start: span.Start + 50, End: span.End + 50})
	}
	good := ComputeMetrics(shifted, reference, DefaultAcceptCriteria())
	if !good.Acceptable {
		t.Errorf("closed-caption offset should be acceptable: %+v", good)
	}
	if good.Within2 != 1 || good.ZeroStartCues != 0 {
		t.Errorf("unexpected metrics: %+v", good)
	}

	far := make([]TimeSpan, 0, len(reference))
	for _, span := range reference {
		far = append(far, TimeSpan{Start: span.Start + 300000, End: span.End + 300000})
	}
	bad := ComputeMetrics(far, reference, DefaultAcceptCriteria())
	if bad.Acceptable {
		t.Errorf("five-minute offset must be rejected: %+v", bad)
	}

	p50, p90, within := NearestMetrics(shifted, reference)
	if math.Abs(p50-0.05) > 1e-9 || math.Abs(p90-0.05) > 1e-9 || within != 1 {
		t.Errorf("NearestMetrics = (%v, %v, %v)", p50, p90, within)
	}
}

func TestParseAlassOutput(t *testing.T) {
	output := "\u001b[32mINFO\u001b[0m 50% [====>]\n" +
		"Guessed fps ratio is 1.042\n" +
		"shifted block of 120 subtitles (00:00:01.000 -> 00:20:00.000) by 0:00:00.500\n" +
		"shifted block of 30 subtitles (00:20:00.000 -> 00:30:00.000) by -0:00:01.250\n"
	report := ParseAlassOutput(output)
	if report.FPS != "1.042" {
		t.Errorf("FPS = %q", report.FPS)
	}
	if len(report.Blocks) != 2 || report.Blocks[0].Count != 120 || report.Blocks[1].Shift != "-0:00:01.250" {
		t.Errorf("blocks = %+v", report.Blocks)
	}
	if bytes.Contains([]byte(report.Raw), []byte("\u001b")) {
		t.Error("ANSI escapes were not stripped")
	}
	if got := SignedTimeSeconds("-0:00:01.250"); math.Abs(got+1.25) > 1e-9 {
		t.Errorf("SignedTimeSeconds = %v", got)
	}
}

func candidateItem(featureName string, year int, release string, fileName string, mutate func(*opensubtitles.ItemAttributes)) opensubtitles.Item {
	item := opensubtitles.Item{}
	item.Attributes.Release = release
	item.Attributes.FeatureDetails.MovieName = featureName
	item.Attributes.FeatureDetails.Year = opensubtitles.Intish(year)
	item.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(42), FileName: fileName}}
	if mutate != nil {
		mutate(&item.Attributes)
	}
	return item
}

func TestScoreAndAuditCandidate(t *testing.T) {
	reference := Reference{Basename: "Movie.2019.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", Title: "Movie", Year: 2019}
	item := candidateItem("Movie", 2019, "Movie.2019.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", "Movie.2019.1080p.AMZN.WEB-DL.sv.srt", func(attributes *opensubtitles.ItemAttributes) {
		attributes.FromTrusted = true
		attributes.Ratings = opensubtitles.Number(6)
	})

	score, reasons, fileID, ok := ScoreCandidate(reference, item)
	if !ok || fileID != 42 {
		t.Fatalf("ScoreCandidate ok=%v fileID=%d reasons=%v", ok, fileID, reasons)
	}
	if score < 400 {
		t.Errorf("expected a high score for an exact release match, got %v (%v)", score, reasons)
	}
	if category, reasons := AuditCandidate(reference, item, item.Attributes.Release); category != CategoryStrong {
		t.Errorf("AuditCandidate = %s (%v), want strong", category, reasons)
	}

	wrongYear := candidateItem("Movie", 2016, "Movie.2016.1080p.AMZN.WEB-DL", "Movie.2016.1080p.sv.srt", nil)
	if category, _ := AuditCandidate(reference, wrongYear, wrongYear.Attributes.Release); category != CategoryReview {
		t.Errorf("AuditCandidate for a wrong year = %s, want review", category)
	}

	forced := candidateItem("Movie", 2019, "Movie.2019.1080p.WEB-DL.Forced", "Movie.2019.1080p.forced.sv.srt", nil)
	if category, _ := AuditCandidate(reference, forced, forced.Attributes.Release); category != CategoryReview {
		t.Errorf("AuditCandidate for a forced release = %s, want review", category)
	}

	noFile := opensubtitles.Item{}
	if _, _, _, ok := ScoreCandidate(reference, noFile); ok {
		t.Error("a candidate without files must be rejected")
	}
}

func TestCandidateSafety(t *testing.T) {
	reference := Reference{Basename: "Movie.2019.1080p.WEB-DL", Title: "Movie", Year: 2019}
	accepted := map[string]bool{NormalizedTitle("Movie"): true}
	item := candidateItem("Movie", 2019, "Movie.2019.1080p.WEB-DL", "Movie.2019.1080p.sv.srt", nil)
	safe, score, reasons, movieTitle, featureYear := CandidateSafety(reference, item, item.Attributes.Release, accepted, nil, "")
	if !safe {
		t.Fatalf("expected safe, reasons=%v", reasons)
	}
	if movieTitle != "Movie" || featureYear != 2019 {
		t.Errorf("movieTitle=%q featureYear=%d", movieTitle, featureYear)
	}
	if score <= 0 {
		t.Errorf("identity score = %v", score)
	}

	other := candidateItem("Completely Different", 2014, "Other.2014.1080p.WEB-DL", "Other.2014.sv.srt", nil)
	if safe, _, _, _, _ := CandidateSafety(reference, other, other.Attributes.Release, accepted, nil, ""); safe {
		t.Error("a different movie must not be safe")
	}

	// An exact feature id bypasses the strict title check.
	exact := candidateItem("Completely Different", 2014, "Other.2014.1080p.WEB-DL", "Other.2014.sv.srt", func(attributes *opensubtitles.ItemAttributes) {
		attributes.FeatureDetails.FeatureID = opensubtitles.FlexID("999")
	})
	if safe, _, _, _, _ := CandidateSafety(reference, exact, exact.Attributes.Release, accepted, nil, "999"); !safe {
		t.Error("exact feature id should bypass the title check")
	}

	// A trusted English title override widens the allowed year delta to three.
	overrides := map[string]string{NormalizedTitle("Movie"): "Movie"}
	andThen := candidateItem("Completely Different", 2022, "Completely.Different.2022.1080p", "x.sv.srt", nil)
	if safe, _, _, _, _ := CandidateSafety(reference, andThen, andThen.Attributes.Release, accepted, overrides, ""); safe {
		t.Error("the override must only apply when the found title matches the override")
	}
}
