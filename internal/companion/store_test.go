package companion

import (
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/config"
)

func TestSQLiteStatePersistsReviewCandidatesAndSettings(t *testing.T) {
	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	m := NewManager(cfg, nil, nil)
	movie := &Movie{
		ID:         "movie",
		DriveID:    "hdd1",
		Path:       "/m1/Movie (2024)",
		FolderName: "Movie (2024)",
		Title:      "Movie",
		Year:       2024,
		Status:     StatusReview,
	}
	m.mu.Lock()
	m.state.Movies = []*Movie{movie}
	m.searches["movie"] = searchState{Candidates: []Candidate{{Guid: "guid", Title: "Movie.2024.1080p.WEB-DL"}}}
	m.state.SearchIntervalSeconds = 30
	m.state.SearchBatchSize = 12
	m.searchIntervalSeconds = 30
	m.searchBatchSize = 12
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	m.mu.Unlock()

	reloaded := NewManager(cfg, nil, nil)
	view := reloaded.View("movie")
	if view.Open == nil || view.Open.Status != StatusReview {
		t.Fatalf("review movie was not restored: %+v", view.Open)
	}
	if len(view.Candidates) != 1 || view.Candidates[0].Guid != "guid" {
		t.Fatalf("review candidates were not restored: %+v", view.Candidates)
	}
	if view.SearchIntervalSeconds != 30 || view.SearchBatchSize != 12 {
		t.Fatalf("settings were not restored: interval=%d batch=%d", view.SearchIntervalSeconds, view.SearchBatchSize)
	}
}

func TestSQLiteStateMigratesLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "companions.json")
	legacy := stateFile{
		Version:               stateVersion,
		Movies:                []*Movie{{ID: "legacy", FolderName: "Legacy (2020)", Status: StatusPending}},
		SearchIntervalSeconds: 45,
		SearchBatchSize:       7,
	}
	if err := saveState(legacyPath, legacy); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Companion.StatePath = legacyPath
	m := NewManager(cfg, nil, nil)
	view := m.View("")
	if len(view.Movies) != 1 || view.Movies[0].ID != "legacy" {
		t.Fatalf("legacy movie was not migrated: %+v", view.Movies)
	}
	if view.SearchIntervalSeconds != 45 || view.SearchBatchSize != 7 {
		t.Fatalf("legacy settings were not migrated: interval=%d batch=%d", view.SearchIntervalSeconds, view.SearchBatchSize)
	}
	if _, err := os.Stat(filepath.Join(dir, "companions.db")); err != nil {
		t.Fatalf("migrated database was not created: %v", err)
	}
}
