package requests

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSettingsPersistIndependentDestinationsAndRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "requests.db")
	store := NewStore(path)
	settings, err := store.Load()
	if err != nil || !reflect.DeepEqual(settings, Defaults()) {
		t.Fatalf("initial settings: %+v; %v", settings, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("reading defaults created a database: %v", err)
	}
	settings.Movie.Destination = "main"
	settings.Movie.NewTitleDrive = "disk2"
	settings.TV.Destination = "remote"
	settings.Trackers = []Tracker{{ID: 7, Name: "My tracker", Priority: 5}}
	settings.Movie.Keywords = []Keyword{{Pattern: `(?i)\bdual\b`, Match: "regex", Mode: "preferred", Score: 25}}
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(path)
	got, err := restarted.Load()
	if err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatalf("settings after restart: %+v; %v", got, err)
	}
	invalid := settings
	invalid.TV.Destination = "somewhere-else"
	if err := restarted.Save(invalid); err == nil {
		t.Fatal("invalid destination was saved")
	}
	got, err = store.Load()
	if err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatal("invalid update changed the saved settings")
	}
}

func TestRejectInvalidReleasePolicies(t *testing.T) {
	for name, mutate := range map[string]func(*Settings){
		"regex":              func(s *Settings) { s.TV.Keywords = []Keyword{{Pattern: "[", Match: "regex", Mode: "required"}} },
		"size order":         func(s *Settings) { s.Movie.MinSizeGiB = 30; s.Movie.MaxSizeGiB = 10 },
		"nonfinite size":     func(s *Settings) { s.Movie.MaxSizeGiB = math.NaN() },
		"cutoff not allowed": func(s *Settings) { s.TV.UpgradeUntil = "cam" },
		"duplicate quality":  func(s *Settings) { s.Movie.Qualities = append(s.Movie.Qualities, s.Movie.Qualities[0]) },
		"duplicate tracker":  func(s *Settings) { s.Trackers = []Tracker{{ID: 1, Priority: 1}, {ID: 1, Priority: 5}} },
		"rejection score": func(s *Settings) {
			s.Movie.Keywords = []Keyword{{Pattern: "CAM", Match: "contains", Mode: "excluded", Score: 10}}
		},
		"unknown language": func(s *Settings) { s.Movie.AudioLanguages = []string{"English"} },
		"null rules":       func(s *Settings) { s.Movie.Keywords = nil },
	} {
		t.Run(name, func(t *testing.T) {
			settings := Defaults()
			mutate(&settings)
			if err := settings.Validate(); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

func TestCorruptStoredSettingsAreReported(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	if err := store.Save(Defaults()); err != nil {
		t.Fatal(err)
	}
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE request_settings SET payload = 'invalid-json' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := store.Load(); err == nil {
		t.Fatal("corrupt settings were silently replaced with defaults")
	}
}

func TestSelectionOrderValidationAndLegacyDefault(t *testing.T) {
	settings := Defaults()
	settings.SelectionOrder = []string{"language", "quality", "text_subtitles", "keywords", "pack", "priority", "seeders"}
	if err := settings.Validate(); err != nil {
		t.Fatalf("valid order rejected: %v", err)
	}
	store := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || !reflect.DeepEqual(loaded.SelectionOrder, NormalizeSelectionOrder(settings.SelectionOrder)) {
		t.Fatalf("selection order did not persist: %v %v", loaded.SelectionOrder, err)
	}
	for name, order := range map[string][]string{
		"duplicate": {"language", "language", "text_subtitles", "keywords", "pack", "priority", "seeders"},
		"unknown":   {"language", "quality", "text_subtitles", "keywords", "pack", "priority", "bogus"},
		"missing":   {"language", "quality"},
	} {
		t.Run(name, func(t *testing.T) {
			bad := Defaults()
			bad.SelectionOrder = order
			if bad.Validate() == nil {
				t.Fatalf("invalid selection order accepted: %v", order)
			}
		})
	}
	legacy := Defaults()
	legacy.SelectionOrder = nil
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy settings without a selection order rejected: %v", err)
	}
}
