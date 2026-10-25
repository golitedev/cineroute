package requests

import (
	"cineroute/internal/medialanguages"
	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
	"path/filepath"
	"slices"
	"testing"
)

func TestLanguagePreferencesValidateAndPersistWithoutChangingOtherPolicies(t *testing.T) {
	s := Defaults()
	s.Movie.PreferredAudioLanguages = []string{"es", "original"}
	s.Movie.PreferredSubtitleLanguages = []string{"es", "sv"}
	s.TV.PreferredSubtitleLanguages = []string{"en"}
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	if err := store.Save(s); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(store.path).Load()
	if err != nil || !slices.Equal(got.Movie.PreferredAudioLanguages, s.Movie.PreferredAudioLanguages) || !slices.Equal(got.TV.PreferredSubtitleLanguages, s.TV.PreferredSubtitleLanguages) {
		t.Fatalf("preferences not persisted: %+v %v", got, err)
	}
	for _, codes := range [][]string{{"es", "es"}, {"Spanish"}, {"zz"}, {"es-ES"}, {"https://attacker"}} {
		bad := Defaults()
		bad.Movie.PreferredAudioLanguages = codes
		if bad.Validate() == nil {
			t.Errorf("accepted invalid preferences %v", codes)
		}
	}
	bad := Defaults()
	bad.Movie.PreferredSubtitleLanguages = []string{"original"}
	if bad.Validate() == nil {
		t.Fatal("original subtitle preference accepted")
	}
	legacy := Defaults()
	legacy.Movie.PreferredAudioLanguages = nil
	legacy.Movie.PreferredSubtitleLanguages = nil
	legacy.TV.PreferredAudioLanguages = nil
	legacy.TV.PreferredSubtitleLanguages = nil
	if legacy.Validate() != nil {
		t.Fatal("old settings without new fields rejected")
	}
}
func TestLanguageRankRespectsQualityFiltersAndKeywordMinimum(t *testing.T) {
	base := Candidate{Rank: 0, Score: 100, Priority: 1}
	preferred := base
	preferred.LanguageScore = 2
	preferred.Score = 0
	if !Better(preferred, base, true) {
		t.Fatal("language preference lost to keyword score")
	}
	preferred.Rank = 1
	if Better(preferred, base, true) {
		t.Fatal("language preference overrode quality order")
	}
	media := tmdb.Details{Result: tmdb.Result{ID: 1, Title: "Example", MediaType: "movie", ReleaseDate: "2024-01-01"}, OriginalLanguage: "en"}
	p := Defaults().Movie
	p.PreferredAudioLanguages = []string{"es"}
	p.PreferredSubtitleLanguages = []string{"sv"}
	r := prowlarr.Release{Title: "Example.2024.1080p.WEB-DL.x264", Languages: medialanguages.Metadata{Status: "reported", Audio: []string{"es"}, Subtitles: []string{"sv"}}}
	if c, err := Evaluate(r, media, p, 0, 1); err != nil || c.LanguageScore != 2 || c.Score != 0 {
		t.Fatalf("wrong preference score: %+v %v", c, err)
	}
	p.MinimumScore = 1
	if _, err := Evaluate(r, media, p, 0, 1); err == nil {
		t.Fatal("language points bypassed minimum keyword score")
	}
	p.MinimumScore = 0
	p.AudioLanguages = []string{"en"}
	p.AllowUnknownLanguage = true
	if _, err := Evaluate(r, media, p, 0, 1); err == nil {
		t.Fatal("language preference bypassed explicit allowed audio filter")
	}
	p.AudioLanguages = []string{"es"}
	if _, err := Evaluate(r, media, p, 0, 1); err != nil {
		t.Fatal("reported audio could not satisfy the audio filter")
	}
}
