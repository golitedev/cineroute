package genres

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestTranslate(t *testing.T) {
	tr := New(nil)
	tests := []struct{ name, in, want string }{
		{"whole phrase", "Bandes originales de films", "Original Soundtracks"},
		{"streamrip", "Électronique, Musique de film", "Electronic, Film Music"},
		{"English list", "Pop, Rock", "Pop, Rock"},
		{"English formatting", "  jAzZ  ", "  jAzZ  "},
		{"mixed", "Classique / Opéra; Jazz", "Classical / Opera; Jazz"},
		{"compact", "Classique/Opéra;Jazz→Métal", "Classical/Opera;Jazz→Metal"},
		{"spaced hyphen", "Classique - Opéra", "Classical - Opera"},
		{"unknown preserved", "Classique;  my   UNKNOWN style  /Opéra", "Classical;  my   UNKNOWN style  /Opera"},
		{"entirely unknown", "  Inconnu   bizarre  ", "  Inconnu   bizarre  "},
		{"no token guessing", "jeux étranges, séries inconnues, musique pour robots", "jeux étranges, séries inconnues, musique pour robots"},
		{"dedupe translated", "Électronique, Electronic", "Electronic"},
		{"dedupe untranslated", "Other; OTHER / Jazz", "Other / Jazz"},
		{"dedupe separator", "Classique; Classical → Opéra", "Classical → Opera"},
		{"dedupe ligature", "Œuf, oeuf", "Œuf"},
		{"dedupe NFD", "Inconnu é, INCONNU e\u0301", "Inconnu é"},
		{"case and spaces", " \tMUSIQUE\n DE   CHAMBRE\u00a0", "Chamber Music"},
		{"curly apostrophe", "Extraits d’Opéra", "Opera Extracts"},
		{"left apostrophe", "Extraits d‘Opéra", "Opera Extracts"},
		{"modifier apostrophe", "Extraits dʼOpéra", "Opera Extracts"},
		{"NFD", "E\u0301lectronique", "Electronic"},
		{"ligature", "OEUVRES POUR PIANO", "Solo Piano"},
		{"ligature uppercase", "ŒUVRES POUR PIANO", "Solo Piano"},
		{"unaccented", "variete francaise", "French Variety"},
		{"India", "Inde", "India"},
		{"indie", "Indé", "Indie"},
		{"India NFD indie", "Inde, Inde\u0301", "India, Indie"},
		{"hip hop", "Hip-Hop", "Hip-Hop"},
		{"k pop", "K-Pop", "K-Pop"},
		{"compound rap", "Rap/Hip-Hop", "Rap/Hip-Hop"},
		{"compound hip hop", "Hip-Hop/Rap", "Hip-Hop/Rap"},
		{"unspaced hyphen", "Classique-Opéra", "Classique-Opéra"},
		{"one sided hyphen", "Classique -Opéra", "Classique -Opéra"},
		{"unicode delimiters", "Classique\u00a0/\u2003Opéra", "Classical\u00a0/\u2003Opera"},
		{"empty", "", ""},
		{"only delimiters", " ; / → ", " ; / → "},
		{"empty segments", "Classique,,Opéra;", "Classical,,Opera;"},
		{"whitespace", " \t ", " \t "},
		{"parenthetical whole", "Messes, Passions, Requiems", "Masses, Passions, Requiems"},
		{"classical subgenre", "Musique chorale (pour chœur)", "Choral Music (Choirs)"},
		{"regional subgenre", "Musique coréenne traditionnelle", "Korean traditional"},
		{"preferences", "Chanson française, Amérique latine", "French Song, Latin"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tr.Translate(tt.in); got != tt.want {
				t.Errorf("Translate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtra(t *testing.T) {
	extra := map[string]string{
		"  E\u0301LECTRONIQUE ": "Electronics", "style nouveau": "New Style",
		"CLASSIQUE": "", "un / composé": "Whole", "cœur d’artiste": "Artist's Heart",
		"æther": "Ether", "inconnu": "Jazz, Jazz",
	}
	tr := New(extra)
	extra["CLASSIQUE"] = "Mutated"
	tests := []struct{ in, want string }{
		{"Électronique", "Electronics"}, {"STYLE  NOUVEAU", "New Style"},
		{"  Classique  ", "  Classique  "}, {"Classique; Opéra", "Classique; Opera"},
		{"un / composé", "Whole"}, {"COEUR D'ARTISTE", "Artist's Heart"},
		{"AETHER", "Ether"}, {"inconnu", "Jazz, Jazz"},
	}
	for _, tt := range tests {
		if got := tr.Translate(tt.in); got != tt.want {
			t.Errorf("Translate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	// Conflicting normalized keys resolve consistently, independent of map order.
	for range 20 {
		if got := New(map[string]string{"JAZZ": "first", "jazz": "last"}).Translate("Jazz"); got != "last" {
			t.Fatal(got)
		}
	}
}

func TestTranslateAllAndChanged(t *testing.T) {
	tr := New(nil)
	tests := []struct {
		name     string
		in, want []string
		changed  bool
	}{
		{"nil", nil, nil, false},
		{"empty", []string{}, []string{}, false},
		{"unchanged", []string{"Pop, Rock", "Jazz"}, []string{"Pop, Rock", "Jazz"}, false},
		{"translated", []string{"Classique", "Jazz"}, []string{"Classical", "Jazz"}, true},
		{"dedupe", []string{"Électronique", "Electronic", " Opéra ", "Opera", "Unknown", " UNKNOWN "}, []string{"Electronic", "Opera", "Unknown"}, true},
		{"empty values", []string{"", " ", "Jazz"}, []string{"", "Jazz"}, true},
		{"accents significant", []string{"Inde", "Indé"}, []string{"India", "Indie"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string(nil), tt.in...)
			if got := tr.TranslateAll(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("TranslateAll = %#v, want %#v", got, tt.want)
			}
			if got := tr.Changed(tt.in); got != tt.changed {
				t.Errorf("Changed = %v, want %v", got, tt.changed)
			}
			if !equalStrings(tt.in, original) {
				t.Fatal("modified input")
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUnknown(t *testing.T) {
	tests := []struct {
		name     string
		extra    map[string]string
		in, want []string
	}{
		{name: "heuristic", in: []string{"Électronique, musique oubliée / Jazz", " MUSIQUE OUBLIE\u0301E ", "son pour robots; Pop", "étrange→melodie du soir", "Unknown, Super Rock, Hip-Hop"}, want: []string{"musique oubliée", "son pour robots", "étrange", "melodie du soir"}},
		{name: "function word boundaries", in: []string{"Et les robots", "robots de Mars", "musique des robots", "son et bruit", "pour robots", "Detroit", "duet", "deep house"}, want: []string{"Et les robots", "robots de Mars", "musique des robots", "son et bruit", "pour robots"}},
		{name: "known whole", in: []string{"Musique vocale (profane et sacrée)", "Messes, Passions, Requiems", "Art Songs, Mélodies & Lieder", "Musique Concrète"}},
		{name: "overrides known", extra: map[string]string{"son pour robots": "", "étrange / oublié": "Custom"}, in: []string{"son pour robots", "étrange / oublié", "nouveauté"}, want: []string{"nouveauté"}},
		{name: "ligature", in: []string{"cœur inconnu", "COEUR INCONNU", "æther mystérieux"}, want: []string{"cœur inconnu", "æther mystérieux"}},
		{name: "empty", in: []string{"", " ", ";/"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := New(tt.extra).Unknown(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Unknown = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestNormalization(t *testing.T) {
	for _, letter := range []struct{ nfd, nfc string }{
		{"e\u0301", "é"}, {"e\u0300", "è"}, {"e\u0302", "ê"}, {"e\u0308", "ë"},
		{"a\u0300", "à"}, {"a\u0302", "â"}, {"c\u0327", "ç"},
		{"i\u0302", "î"}, {"i\u0308", "ï"}, {"o\u0302", "ô"},
		{"u\u0302", "û"}, {"u\u0300", "ù"}, {"u\u0308", "ü"}, {"y\u0308", "ÿ"},
	} {
		for _, value := range []string{letter.nfd, strings.ToUpper(letter.nfd), letter.nfc, strings.ToUpper(letter.nfc)} {
			if got := normalize(value); got != letter.nfc {
				t.Errorf("normalize(%q) = %q", value, got)
			}
		}
	}
	if normalize("Inde") == normalize("Indé") {
		t.Fatal("accents lost")
	}
	if normalize(" ŒUVRE\t D’ÆTHER ") != "oeuvre d'aether" {
		t.Fatal("ligature/apostrophe normalization")
	}
}

func TestBuiltinConsistency(t *testing.T) {
	seen := make(map[string]string)
	for key, value := range builtins {
		normalized := normalize(key)
		if prior, ok := seen[normalized]; ok && prior != value {
			t.Errorf("conflicting builtins for %q: %q / %q", key, prior, value)
		}
		seen[normalized] = value
	}
	// Every configured phrase must be recognized, including phrases containing
	// separators, and its output must not be reported as an unknown French genre.
	tr := New(nil)
	for key, value := range builtins {
		want := value
		if normalize(key) == normalize(value) {
			want = key
		}
		if got := tr.Translate(key); got != want {
			t.Errorf("builtin %q = %q, want %q", key, got, want)
		}
		if got := tr.Unknown([]string{key, value}); len(got) != 0 {
			t.Errorf("known builtin reported unknown: %q", got)
		}
	}
}

func TestConcurrent(t *testing.T) {
	tr := New(map[string]string{"nouveauté": "New"})
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			for range 50 {
				if got := tr.Translate("Électronique, Electronic; nouveauté"); got != "Electronic; New" {
					t.Errorf("Translate = %q", got)
				}
				if got := tr.TranslateAll([]string{"Classique", "Classical"}); !reflect.DeepEqual(got, []string{"Classical"}) {
					t.Error(got)
				}
				if !tr.Changed([]string{"Opéra"}) {
					t.Error("Changed = false")
				}
				if got := tr.Unknown([]string{"oublié, nouveauté"}); !reflect.DeepEqual(got, []string{"oublié"}) {
					t.Error(got)
				}
			}
		})
	}
	wg.Wait()
}
