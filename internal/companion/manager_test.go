package companion

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"cineroute/internal/config"
	"cineroute/internal/library"
)

func newTransitionManager(t *testing.T, status string) *Manager {
	t.Helper()
	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.json")
	movie := &Movie{
		ID:         "movie",
		DriveID:    "hdd1",
		Path:       "/movies/Movie (2024)",
		FolderName: "Movie (2024)",
		Title:      "Movie",
		Year:       2024,
		Status:     status,
	}
	return &Manager{
		cfg:      cfg,
		state:    stateFile{Version: stateVersion, Movies: []*Movie{movie}},
		searches: map[string]searchState{},
	}
}

func TestSearchOneRejectsActiveOrCompleteStates(t *testing.T) {
	for _, status := range []string{StatusSearching, StatusSubmitting, StatusComplete} {
		t.Run(status, func(t *testing.T) {
			m := newTransitionManager(t, status)
			if _, err := m.SearchOne(context.Background(), "movie"); err == nil {
				t.Fatalf("SearchOne(%s) unexpectedly succeeded", status)
			}
			if got := m.state.Movies[0].Status; got != status {
				t.Fatalf("SearchOne changed %s to %s", status, got)
			}
		})
	}
}

func TestPrepareSelectedRequiresReviewState(t *testing.T) {
	for _, status := range []string{StatusPending, StatusSearching, StatusSubmitting, StatusComplete, StatusNoMatch} {
		t.Run(status, func(t *testing.T) {
			m := newTransitionManager(t, status)
			if _, _, _, err := m.PrepareSelected(context.Background(), "movie", "guid"); err == nil {
				t.Fatalf("PrepareSelected(%s) unexpectedly succeeded", status)
			}
			if got := m.state.Movies[0].Status; got != status {
				t.Fatalf("PrepareSelected changed %s to %s", status, got)
			}
		})
	}
}

func TestSkipRejectsSearchingSubmittingAndComplete(t *testing.T) {
	for _, status := range []string{StatusSearching, StatusSubmitting, StatusComplete} {
		t.Run(status, func(t *testing.T) {
			m := newTransitionManager(t, status)
			if err := m.Skip("movie"); err == nil {
				t.Fatalf("Skip(%s) unexpectedly succeeded", status)
			}
			if got := m.state.Movies[0].Status; got != status {
				t.Fatalf("Skip changed %s to %s", status, got)
			}
		})
	}
}

func TestScanPreservesLiveWorkflowStates(t *testing.T) {
	for _, status := range []string{StatusSearching, StatusReview, StatusSubmitting} {
		t.Run(status, func(t *testing.T) {
			root := t.TempDir()
			folder := filepath.Join(root, "Movie (2024)")
			if err := os.MkdirAll(folder, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(folder, "Movie.2024.2160p.WEB-DL.mkv"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.json")
			m := newTransitionManager(t, status)
			m.cfg = cfg
			m.lib = library.NewScan([]library.Drive{{ID: "hdd1", MovieRoot: root}})
			m.state.Movies[0].ID = movieID("hdd1", "Movie (2024)")
			m.state.Movies[0].Path = folder

			if err := m.Scan(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := m.state.Movies[0].Status; got != status {
				t.Fatalf("Scan changed %s to %s", status, got)
			}
		})
	}
}
