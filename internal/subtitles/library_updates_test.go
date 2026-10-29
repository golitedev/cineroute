package subtitles

import (
	"path/filepath"
	"testing"
)

func TestInstalledSubtitleOutboxSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subtitles.db")
	st, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{store: st, attempts: map[string][]Attempt{}}
	video := filepath.Join(t.TempDir(), "Movie.mkv")
	item := &Item{VideoPath: video}
	target := &TargetState{Language: "sv"}
	if err := m.installSubtitle(item, target, "1\n00:00:01,000 --> 00:00:02,000\nHello\n", nil, Candidate{}, AlassReport{}); err != nil {
		t.Fatal(err)
	}
	updates, err := m.LibraryUpdates()
	if err != nil || len(updates) != 1 {
		t.Fatalf("missing installation event: %v %v", updates, err)
	}
	st.db.Close()
	st, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.db.Close()
	m = &Manager{store: st}
	for file, revision := range updates {
		if err := m.AcknowledgeLibraryUpdate(file, "older"); err != nil {
			t.Fatal(err)
		}
		got, err := m.LibraryUpdates()
		if err != nil || got[file] != revision {
			t.Fatal("older acknowledgement removed newer change")
		}
		if err := m.AcknowledgeLibraryUpdate(file, revision); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.LibraryUpdates()
	if err != nil || len(got) != 0 {
		t.Fatal("acknowledged change retained")
	}
}
