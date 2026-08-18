package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestScanQueues1080pBluRayForWebDLCompanion(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Movie (2024)")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "Movie.2024.1080p.BluRay.REMUX.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	m := newTransitionManager(t, StatusAlready1080p)
	m.cfg = cfg
	m.lib = library.NewScan([]library.Drive{{ID: "hdd1", MovieRoot: root}})
	m.state.Movies[0].ID = movieID("hdd1", "Movie (2024)")
	m.state.Movies[0].Path = folder

	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.state.Movies[0].Status; got != StatusPending {
		t.Fatalf("1080p BluRay status = %s, want %s", got, StatusPending)
	}
}

func TestScanRecoversNeedsReviewWhenVideoIsNested(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Movie (2024)")
	releaseFolder := filepath.Join(folder, "Movie.2024.2160p.REMUX-GROUP")
	if err := os.MkdirAll(releaseFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseFolder, "Movie.2024.2160p.REMUX.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	m := newTransitionManager(t, StatusNeedsReview)
	m.cfg = cfg
	m.lib = library.NewScan([]library.Drive{{ID: "hdd1", MovieRoot: root}})
	m.state.Movies[0].ID = movieID("hdd1", "Movie (2024)")
	m.state.Movies[0].Path = folder
	m.state.Movies[0].Error = "no top-level video file found; manual review required"

	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := m.state.Movies[0].Status; got != StatusPending {
		t.Fatalf("nested movie status = %s, want %s", got, StatusPending)
	}
	if got := m.state.Movies[0].ExistingCopy; got != "4k" {
		t.Fatalf("nested movie quality = %s, want 4k", got)
	}
	if got := m.state.Movies[0].Error; got != "" {
		t.Fatalf("nested movie retained review error: %q", got)
	}
}

func TestSearchIntervalSettingPersists(t *testing.T) {
	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	m := NewManager(cfg, nil, nil)
	if err := m.SetSearchIntervalSeconds(30); err != nil {
		t.Fatal(err)
	}
	if got := m.View("").SearchIntervalSeconds; got != 30 {
		t.Fatalf("runtime interval = %d, want 30", got)
	}
	if err := m.SetSearchIntervalSeconds(4); err == nil {
		t.Fatal("expected interval below the minimum to be rejected")
	}
	reloaded := NewManager(cfg, nil, nil)
	if got := reloaded.View("").SearchIntervalSeconds; got != 30 {
		t.Fatalf("persisted interval = %d, want 30", got)
	}
}

func TestSearchIntervalBlocksTheNextRequest(t *testing.T) {
	m := newTransitionManager(t, StatusPending)
	m.searchIntervalSeconds = 5
	if err := m.waitForSearchInterval(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := m.waitForSearchInterval(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second search should wait for the interval, got %v", err)
	}
}

func TestCompanionSearchQueriesMatchTitleOnlyProwlarrSearchFirst(t *testing.T) {
	got := companionSearchQueries(&Movie{Title: "12 Angry Men", Year: 1957})
	want := []string{"12 angry men", "12 angry men 1957"}
	if len(got) != len(want) {
		t.Fatalf("queries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("query %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCanceledSearchReturnsToPending(t *testing.T) {
	m := newTransitionManager(t, StatusSearching)
	m.finishSearchFailure("movie", *m.state.Movies[0], context.Canceled)
	if got := m.state.Movies[0].Status; got != StatusPending {
		t.Fatalf("canceled search status = %s, want pending", got)
	}
	if m.state.Movies[0].Error != "" {
		t.Fatalf("canceled search error = %q, want empty", m.state.Movies[0].Error)
	}
}

func TestCancelSearchMissingSignalsRunningBatch(t *testing.T) {
	m := newTransitionManager(t, StatusPending)
	m.batch = BatchStatus{Running: true, Total: 1}
	called := false
	m.batchCancel = func() { called = true }
	if err := m.CancelSearchMissing(); err != nil {
		t.Fatal(err)
	}
	if !called || !m.batch.Canceled {
		t.Fatalf("batch cancellation was not signaled: called=%v batch=%+v", called, m.batch)
	}
}
