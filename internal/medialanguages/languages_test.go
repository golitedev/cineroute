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

func TestTextVersusImageSubtitlesAndUnknownLanguages(t *testing.T) {
	for _, format := range []string{"UTF-8", "SubRip", "ASS", "SSA", "WebVTT", "Timed Text", "TTML", "S_TEXT/UTF8", "S_TEXT/ASS"} {
		m := Parse("Text\nFormat : "+format+"\nLanguage : Swedish\nForced : No", "")
		if !m.HasTextSubtitles || m.HasImageSubtitles || m.TextPreferenceScore([]string{"sv"}) != 1 {
			t.Errorf("text format %q not preferred: %+v", format, m)
		}
	}
	for _, format := range []string{"PGS", "VobSub", "S_HDMV/PGS", "DVB Subtitle"} {
		m := Parse("Text\nFormat : "+format+"\nLanguage : Swedish\nForced : No", "")
		if m.HasTextSubtitles || !m.HasImageSubtitles || m.TextPreferenceScore(nil) != 0 {
			t.Errorf("image format %q incorrectly preferred: %+v", format, m)
		}
	}
	forced := Parse("Text\nFormat : UTF-8\nLanguage : Swedish\nForced : Yes", "")
	if forced.TextPreferenceScore(nil) != 0 {
		t.Fatal("forced-only text subtitles earned full-subtitle preference")
	}
	unknown := Parse("Text\nFormat : UTF-8\nLanguage : und\nForced : No", "")
	if unknown.TextPreferenceScore(nil) != 1 || unknown.TextPreferenceScore([]string{"sv"}) != 0 {
		t.Fatal("unknown subtitle language bypassed language-specific format preference")
	}
	mixed := Parse("Text #1\nFormat : PGS\nLanguage : Swedish\nText #2\nFormat : UTF-8\nLanguage : English", "")
	if mixed.TextPreferenceScore([]string{"sv"}) != 0 || mixed.TextPreferenceScore([]string{"en"}) != 1 {
		t.Fatal("text format applied to the wrong language")
	}
	jsonReport := Parse(`{"media":{"track":[{"@type":"Text","Language":"sv","CodecID":"S_TEXT/ASS","Forced":"No"}]}}`, "")
	if !jsonReport.HasTextSubtitles {
		t.Fatal("JSON subtitle codec not recognized")
	}
}
