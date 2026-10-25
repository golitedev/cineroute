package medialanguages

import (
	"reflect"
	"strings"
	"testing"
)

func TestMediaInfoTracksAreSeparateAndForcedOrCommentaryDoNotEarnPoints(t *testing.T) {
	report := `General
Language : Spanish
Audio #1
Language : English
Audio #2
Language : Spanish (ES)
Title : Commentary by director
Audio #3
Language : Spanish (Latin America)
Text #1
Language : Swedish
Forced : No
Text #2
Language : Spanish
Forced : Yes
Text #3
Language : English
Title : Commentary
Text #4
Language : French
Title : Signs & Songs
Text #5
Language : Swedish
Title : SDH
Forced : No`
	m := Parse(report, "")
	if !reflect.DeepEqual(m.Audio, []string{"en", "es"}) || !reflect.DeepEqual(m.Subtitles, []string{"sv"}) || !reflect.DeepEqual(m.ForcedSubtitles, []string{"es", "fr"}) || m.Status != "reported" {
		t.Fatalf("wrong tracks: %+v", m)
	}
	if score := m.PreferenceScore([]string{"en", "original"}, []string{"es", "sv"}, "en"); score != 2 {
		t.Fatalf("forced tracks or duplicate original earned points: %d", score)
	}
}
func TestJSONAndBDInfoLanguageReports(t *testing.T) {
	m := Parse(`{"media":{"track":[{"@type":"Audio","Language":"spa"},{"@type":"Text","Language":"swe","Forced":"No"},{"@type":"Text","Language":"eng","Forced":true}]}}`, "")
	if !reflect.DeepEqual(m.Audio, []string{"es"}) || !reflect.DeepEqual(m.Subtitles, []string{"sv"}) || !reflect.DeepEqual(m.ForcedSubtitles, []string{"en"}) {
		t.Fatalf("JSON: %+v", m)
	}
	m = Parse("", `AUDIO:
Codec Language Bitrate Description
DTS-HD Master Audio Spanish 3000 kbps
Dolby Digital English 640 kbps Commentary
SUBTITLES:
Codec Language Bitrate Description
Presentation Graphics Swedish 22 kbps
Presentation Graphics Spanish 10 kbps Forced
VIDEO:
English title`)
	if !reflect.DeepEqual(m.Audio, []string{"es"}) || !reflect.DeepEqual(m.Subtitles, []string{"sv"}) || !reflect.DeepEqual(m.ForcedSubtitles, []string{"es"}) {
		t.Fatalf("BDInfo: %+v", m)
	}
	m = Parse("", "* Audio: English / DTS-HD / 5.1\n* Subtitle: Swedish / PGS")
	if !reflect.DeepEqual(m.Audio, []string{"en"}) || !reflect.DeepEqual(m.Subtitles, []string{"sv"}) {
		t.Fatalf("BDInfo summary: %+v", m)
	}
}
func TestUnknownAndRegionalLanguages(t *testing.T) {
	for input, want := range map[string]string{"spa": "es", "Spanish (ES)": "es", "es-419": "es", "Swedish": "sv", "Norwegian Bokmal": "no", "pt-BR": "pt", "日本語": "", "Multi": "", "und": ""} {
		if got := Normalize(input); got != want {
			t.Errorf("%q: %q want %q", input, got, want)
		}
	}
	for _, report := range []string{"", "General\nLanguage : Spanish", `{"invalid":true}`, strings.Repeat("a", (512<<10)+1)} {
		if m := Parse(report, ""); m.Status != "unknown" || len(m.Audio)+len(m.Subtitles) > 0 {
			t.Fatalf("invented languages: %+v", m)
		}
	}
}
