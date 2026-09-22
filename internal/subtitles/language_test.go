package subtitles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cineroute/internal/subtitles/opensubtitles"
)

// TestMultiLanguageInstallsEveryLanguageThatHasACandidate covers the core of the
// multi-language workflow: one search, one reference extraction, and one
// installation per language that has a candidate. A language with nothing to
// download must not stop the others, and must not cost another demux.
func TestMultiLanguageInstallsEveryLanguageThatHasACandidate(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.TargetLanguages = []string{"sv", "es", "en"}
	h.manager.cfg.MaxCandidates = 3

	video := h.addRemoteMovie(t, "Triple (2013)", "Triple.2013.1080p.mkv")
	// A Portuguese reference: usable for alignment, but it is not one of the
	// target languages, so all three of them start out missing.
	h.prober.streams[video] = MediaInfo{
		DurationMS: 6_000_000,
		Streams:    []EmbeddedSubtitle{{Index: 2, Codec: "subrip", Language: "pt", Usable: true}},
	}

	// Swedish and Spanish have candidates, English has none.
	h.os.items = []opensubtitles.Item{
		languageCandidate("Triple", 2013, "sv", "Triple.2013.1080p.WEB-DL", 701),
		languageCandidate("Triple", 2013, "es", "Triple.2013.1080p.WEB-DL", 702),
	}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	status, err := h.manager.processAndPersist(context.Background(), item, runOptions{})
	if status != StatusPartial {
		t.Fatalf("status = %s (%v), want partial: Swedish and Spanish installed, English missing", status, err)
	}

	swedish := filepath.Join(filepath.Dir(video), "Triple.2013.1080p.sv.srt")
	spanish := filepath.Join(filepath.Dir(video), "Triple.2013.1080p.es.srt")
	english := filepath.Join(filepath.Dir(video), "Triple.2013.1080p.en.srt")
	for _, path := range []string{swedish, spanish} {
		if !IsValidSRTFile(path) {
			t.Errorf("%s was not installed", path)
		}
	}
	if _, err := os.Stat(english); !os.IsNotExist(err) {
		t.Errorf("no English subtitle must be written: %v", err)
	}

	// The reference is shared, so it is materialized exactly once even though two
	// languages were installed.
	if len(h.prober.extracted) != 1 {
		t.Errorf("extracted = %v, want one extraction for both languages", h.prober.extracted)
	}

	targets := map[string]TargetState{}
	for _, target := range item.Targets {
		targets[target.Language] = target
	}
	if targets["sv"].Status != StatusAdded || targets["es"].Status != StatusAdded {
		t.Errorf("targets = %+v, want sv and es added", item.Targets)
	}
	if targets["en"].Status != StatusNoMatch {
		t.Errorf("english = %+v, want no_match", targets["en"])
	}
	if !strings.Contains(item.Error, "en no match") {
		t.Errorf("item error = %q, want it to name the missing language", item.Error)
	}
}

// TestSearchRequestsEveryMissingLanguage checks that one search covers all the
// missing languages, and that Spanish is asked for as its regional variants: the
// API reports Latin American Spanish ("ea") and European Spanish ("sp")
// separately from generic Spanish ("es").
func TestSearchRequestsEveryMissingLanguage(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.TargetLanguages = []string{"sv", "es", "en"}

	video := h.addRemoteMovie(t, "Polyglot (2011)", "Polyglot.2011.1080p.mkv")
	h.configureEmbeddedEnglish(video)
	h.os.items = []opensubtitles.Item{languageCandidate("Polyglot", 2011, "sv", "Polyglot.2011.1080p.WEB-DL", 703)}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(h.os.queries) == 0 {
		t.Fatal("no OpenSubtitles search was made")
	}
	for _, query := range h.os.queries {
		for _, want := range []string{"sv", "es", "ea", "sp"} {
			if !hasCSVValue(query.Languages, want) {
				t.Errorf("search languages = %q, want it to contain %q", query.Languages, want)
			}
		}
	}
}

func hasCSVValue(list, value string) bool {
	for _, entry := range strings.Split(list, ",") {
		if strings.TrimSpace(entry) == value {
			return true
		}
	}
	return false
}

// TestLatinAmericanSpanishIsPreferred checks the variant ranking: the API marks
// Latin American Spanish as "ea" and European Spanish as "sp", and the "es"
// target must try the Latin American candidate first even when the Castilian one
// scores higher in the identity audit.
func TestLatinAmericanSpanishIsPreferred(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.TargetLanguages = []string{"es"}

	video := h.addRemoteMovie(t, "Latino (2012)", "Latino.2012.1080p.mkv")
	h.configureEmbeddedEnglish(video)

	latin := languageCandidate("Latino", 2012, "ea", "Latino.2012.1080p.WEB-DL.LATINO", 712)
	castilian := languageCandidate("Latino", 2012, "sp", "Latino.2012.1080p.WEB-DL.CASTELLANO", 711)
	// Give the Castilian candidate a higher identity score, so only the variant
	// preference can put the Latin American one first.
	castilian.Attributes.Ratings = opensubtitles.Number(9)
	h.os.items = []opensubtitles.Item{latin, castilian}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(h.os.downloadedIDs) == 0 {
		t.Fatal("nothing was downloaded")
	}
	if h.os.downloadedIDs[0] != 712 {
		t.Fatalf("download order = %v, want the Latin American candidate (712) first", h.os.downloadedIDs)
	}

	target := item.Target("es")
	if target == nil || target.Variant != VariantLatin {
		t.Fatalf("target = %+v, want the Latin American variant recorded", target)
	}
	// The stored candidate list keeps the variant, which is what the page shows.
	var found bool
	for _, candidate := range h.manager.candidatesFor(item.ID) {
		if candidate.FileID == 712 {
			found = true
			if candidate.Variant != VariantLatin || candidate.Language != "es-419" {
				t.Errorf("candidate = %+v, want the Latin American variant", candidate)
			}
		}
		if candidate.FileID == 711 && candidate.Variant != VariantCastilian {
			t.Errorf("candidate = %+v, want the Castilian variant", candidate)
		}
	}
	if !found {
		t.Error("the Latin American candidate was not stored")
	}
}

// languageCandidate builds a safe candidate in a specific OpenSubtitles language
// code, so a test can model the Spanish variants.
func languageCandidate(title string, year int, language, release string, fileID int) opensubtitles.Item {
	item := swedishCandidate()
	item.Attributes.Language = language
	item.Attributes.FeatureDetails.MovieName = title
	item.Attributes.FeatureDetails.Year = opensubtitles.Intish(year)
	item.Attributes.Release = release
	item.Attributes.Files = []opensubtitles.File{{
		FileID:   opensubtitles.Intish(fileID),
		FileName: release + "." + language + ".srt",
	}}
	item.Raw = []byte(`{"id":"1","attributes":{"release":"` + release + `"}}`)
	return item
}

// TestTargetSubtitleInOtherLanguageIsNotOverwritten pins the safety rule from the
// single-language days: an existing subtitle of a target language is never
// replaced, and a movie that already has every target language needs no work.
func TestExistingTargetsAreNeverRedownloaded(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.TargetLanguages = []string{"sv", "es"}

	video := h.addRemoteMovie(t, "Done (2010)", "Done.2010.1080p.mkv")
	h.prober.streams[video] = MediaInfo{DurationMS: 6_000_000}
	for _, language := range []string{"sv", "es"} {
		path := filepath.Join(filepath.Dir(video), "Done.2010.1080p."+language+".srt")
		if err := os.WriteFile(path, []byte(swedishSRT), 0o644); err != nil {
			t.Fatalf("write %s subtitle: %v", language, err)
		}
	}
	h.os.items = []opensubtitles.Item{languageCandidate("Done", 2010, "sv", "Done.2010.1080p.WEB-DL", 720)}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	if item.Status != StatusHasTargets {
		t.Fatalf("status = %s, want has_targets", item.Status)
	}
	status, err := h.manager.processAndPersist(context.Background(), item, runOptions{})
	if err != nil || status != StatusHasTargets {
		t.Fatalf("process = %s (%v), want has_targets", status, err)
	}
	if h.os.searches != 0 || len(h.prober.extracted) != 0 {
		t.Errorf("a movie with every target language must not search or extract: searches=%d extracted=%v", h.os.searches, h.prober.extracted)
	}
}
