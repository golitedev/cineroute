package companion

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func TestSearchOneRejectsActiveStates(t *testing.T) {
	for _, status := range []string{StatusSearching, StatusSubmitting} {
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

func TestBeginSearchReopensCompleteAndSkippedCompanions(t *testing.T) {
	for _, status := range []string{StatusComplete, StatusSkipped} {
		t.Run(status, func(t *testing.T) {
			m := newTransitionManager(t, status)
			m.searches["movie"] = searchState{Candidates: []Candidate{{Guid: "stale"}}}
			if _, err := m.beginSearch("movie"); err != nil {
				t.Fatalf("beginSearch(%s) failed: %v", status, err)
			}
			if got := m.state.Movies[0].Status; got != StatusSearching {
				t.Fatalf("beginSearch(%s) status = %s, want %s", status, got, StatusSearching)
			}
			if _, ok := m.searches["movie"]; ok {
				t.Fatalf("beginSearch(%s) retained stale candidates", status)
			}
		})
	}
}

func TestClearReviewsResetsMoviesAndRemovesCurrentCandidates(t *testing.T) {
	m := newTransitionManager(t, StatusReview)
	m.searches["movie"] = searchState{Candidates: []Candidate{{Guid: "candidate", Title: "Movie release"}}}
	pending := &Movie{ID: "pending", FolderName: "Pending (2024)", Status: StatusPending, UpdatedAt: time.Unix(10, 0)}
	m.state.Movies = append(m.state.Movies, pending)
	m.searches[pending.ID] = searchState{Candidates: []Candidate{{Guid: "pending-candidate"}}}

	cleared, err := m.ClearReviews()
	if err != nil {
		t.Fatal(err)
	}
	if cleared != 1 {
		t.Fatalf("cleared = %d, want 1", cleared)
	}
	if got := m.state.Movies[0].Status; got != StatusPending {
		t.Fatalf("review movie status = %s, want %s", got, StatusPending)
	}
	if _, ok := m.searches["movie"]; ok {
		t.Fatal("review movie candidates were not removed")
	}
	if got := m.state.Movies[1].Status; got != StatusPending {
		t.Fatalf("pending movie status = %s, want %s", got, StatusPending)
	}
	if _, ok := m.searches[pending.ID]; !ok {
		t.Fatal("pending movie candidates were unexpectedly removed")
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

func TestScanRecognizes1080pCompanionInRemoteRoot(t *testing.T) {
	mainRoot := t.TempDir()
	remoteRoot := t.TempDir()
	folderName := "Apollo 13 (1995)"
	mainFolder := filepath.Join(mainRoot, folderName)
	remoteFolder := filepath.Join(remoteRoot, folderName)
	if err := os.MkdirAll(mainFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainFolder, "Apollo.13.1995.UHD.BluRay.2160p.REMUX.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	remoteFile := filepath.Join(remoteFolder, "Apollo.13.1995.REMASTERED.1080p.BluRay.WEB-DL.mkv")
	if err := os.WriteFile(remoteFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	m := newTransitionManager(t, StatusPending)
	m.cfg = cfg
	m.lib = library.NewScan([]library.Drive{{ID: "hdd4", MovieRoot: mainRoot, MovieRemoteRoot: remoteRoot}})
	m.state.Movies[0].ID = movieID("hdd4", folderName)
	m.state.Movies[0].Path = mainFolder

	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	movie := m.state.Movies[0]
	if movie.Status != StatusPending {
		t.Fatalf("remote WEB-DL status = %s, want %s", movie.Status, StatusPending)
	}
	if movie.RemotePath != remoteFolder {
		t.Fatalf("remote path = %q, want %q", movie.RemotePath, remoteFolder)
	}
	if movie.RemoteCopy != "1080p" {
		t.Fatalf("remote quality = %q, want 1080p", movie.RemoteCopy)
	}
	if len(movie.RemoteFiles) != 1 || movie.RemoteFiles[0] != remoteFile {
		t.Fatalf("remote files = %v, want %q", movie.RemoteFiles, remoteFile)
	}
}

func TestScanRequeuesSkippedMoviesButPreservesAddedMovies(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"Skipped (2020)", "Added (2021)"} {
		folder := filepath.Join(root, name)
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, name+".1080p.WEB-DL.mkv"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	addedAt := time.Unix(123, 0)
	m := newTransitionManager(t, StatusSkipped)
	m.cfg = cfg
	m.lib = library.NewScan([]library.Drive{{ID: "hdd1", MovieRoot: root}})
	m.state.Movies[0].ID = movieID("hdd1", "Skipped (2020)")
	m.state.Movies[0].Path = filepath.Join(root, "Skipped (2020)")
	m.state.Movies[0].FolderName = "Skipped (2020)"
	m.state.Movies = append(m.state.Movies, &Movie{
		ID:         movieID("hdd1", "Added (2021)"),
		DriveID:    "hdd1",
		Path:       filepath.Join(root, "Added (2021)"),
		FolderName: "Added (2021)",
		Title:      "Added",
		Year:       2021,
		Status:     StatusComplete,
		QBHash:     "existing-hash",
		AddedAt:    &addedAt,
	})

	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	byFolder := make(map[string]*Movie, len(m.state.Movies))
	for _, movie := range m.state.Movies {
		byFolder[movie.FolderName] = movie
	}
	if got := byFolder["Skipped (2020)"].Status; got != StatusPending {
		t.Fatalf("skipped movie status = %s, want %s", got, StatusPending)
	}
	added := byFolder["Added (2021)"]
	if added.Status != StatusComplete || added.QBHash != "existing-hash" || added.AddedAt == nil || !added.AddedAt.Equal(addedAt) {
		t.Fatalf("added movie changed during scan: %+v", added)
	}
}

func TestScanRecoversNeedsReviewWhenVideoIsNested(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Movie (2024)")
	releaseFolder := filepath.Join(folder, "Movie.2024.2160p.REMUX-GROUP")
	if err := os.MkdirAll(releaseFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	video := []byte("movie")
	if err := os.WriteFile(filepath.Join(releaseFolder, "Movie.2024.2160p.REMUX.mkv"), video, 0o644); err != nil {
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
	wantFile := filepath.Join(releaseFolder, "Movie.2024.2160p.REMUX.mkv")
	if got := m.state.Movies[0].ExistingFiles; len(got) != 1 || got[0] != wantFile {
		t.Fatalf("nested movie files = %v, want %q", got, wantFile)
	}
	if got := m.state.Movies[0].ExistingFileSizes[wantFile]; got != int64(len(video)) {
		t.Fatalf("nested movie file size = %d, want %d", got, len(video))
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
	if err := m.SetSearchIntervalSeconds(0); err == nil {
		t.Fatal("expected zero-second interval to be rejected")
	}
	reloaded := NewManager(cfg, nil, nil)
	if got := reloaded.View("").SearchIntervalSeconds; got != 30 {
		t.Fatalf("persisted interval = %d, want 30", got)
	}
}

func TestViewRefreshesExistingFilesForOpenMovie(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "Movie (2024)")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(folder, "Movie.2024.2160p.REMUX.mkv")
	video := []byte("movie")
	if err := os.WriteFile(file, video, 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTransitionManager(t, StatusReview)
	m.state.Movies[0].Path = folder
	m.state.Movies[0].ExistingFiles = nil
	view := m.View("movie")
	if view.Open == nil || len(view.Open.ExistingFiles) != 1 || view.Open.ExistingFiles[0] != file {
		t.Fatalf("open movie files = %v, want %q", view.Open, file)
	}
	if got := view.Open.ExistingFileSizes[file]; got != int64(len(video)) {
		t.Fatalf("open movie file size = %d, want %d", got, len(video))
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

func TestCompanionSearchUsesOnlyYearQualifiedSearch(t *testing.T) {
	got := companionSearchQueries(&Movie{Title: "12 Angry Men", Year: 1957})
	want := []string{"12 Angry Men 1957"}
	if len(got) != len(want) {
		t.Fatalf("queries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("query %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestTVCompanionUsesTitleOnlySearch(t *testing.T) {
	got := tvCompanionSearchQueries(&Movie{Title: "Breaking Bad", Year: 2008})
	if len(got) != 1 || got[0] != "Breaking Bad" {
		t.Fatalf("TV queries = %v, want [Breaking Bad]", got)
	}
}

func TestTVCompanionScansPrimaryRootsAndInspectsRemoteCopy(t *testing.T) {
	primary := t.TempDir()
	remote := t.TempDir()
	folderName := "Breaking Bad (2008)"
	mainFolder := filepath.Join(primary, folderName)
	remoteFolder := filepath.Join(remote, folderName)
	if err := os.MkdirAll(filepath.Join(mainFolder, "Breaking.Bad.S01.2160p.WEB-DL"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mainFolder, "Breaking.Bad.S01.2160p.WEB-DL", "Breaking.Bad.S01E01.2160p.WEB-DL.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteFolder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteFolder, "Breaking.Bad.S01.1080p.WEB-DL.mkv"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(remote, "Remote Only (2024)"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(primary, "Empty Show (2020)"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Companion.StatePath = filepath.Join(t.TempDir(), "companions.db")
	m := NewTVManager(cfg, library.NewScan([]library.Drive{{ID: "hdd1", TVRoot: primary, TVRemoteRoot: remote}}), nil)
	if err := m.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := m.View("")
	if view.MediaType != "tv" || len(view.Movies) != 2 {
		t.Fatalf("TV companion view = %+v, want two primary-root shows", view)
	}
	shows := make(map[string]*Movie, len(view.Movies))
	for _, show := range view.Movies {
		shows[show.FolderName] = show
		if show.Status != StatusPending {
			t.Fatalf("TV show %q status = %s, want pending", show.FolderName, show.Status)
		}
	}
	show := shows[folderName]
	if show == nil || show.Title != "Breaking Bad" || show.Year != 2008 {
		t.Fatalf("TV show state = %+v", show)
	}
	if show.RemotePath != remoteFolder || show.RemoteCopy != "1080p" {
		t.Fatalf("TV remote inspection = %+v", show)
	}
	empty := shows["Empty Show (2020)"]
	if empty == nil || !strings.Contains(empty.Error, "no video file") {
		t.Fatalf("empty TV show should remain searchable with an inspection note: %+v", empty)
	}
}

func TestTVCandidateApprovalStatusIsPerSeason(t *testing.T) {
	m := newTransitionManager(t, StatusReview)
	m.kind = companionTV
	m.state.Movies[0].Path = ""
	m.state.Movies[0].TVApprovedPacks = []string{"season:1"}
	m.searches["movie"] = searchState{Candidates: []Candidate{
		{Guid: "s1", Title: "Friends.1994.S01.1080p.WEB-DL"},
		{Guid: "s1-alt", Title: "Friends.1994.S01.1080p.HMAX.WEB-DL"},
		{Guid: "s2", Title: "Friends.1994.S02.1080p.WEB-DL"},
	}}
	m.tvApprovals = map[string]map[string]bool{"movie": {"season:2": true}}

	view := m.View("movie")
	if len(view.Candidates) != 3 {
		t.Fatalf("candidate count = %d, want 3", len(view.Candidates))
	}
	statuses := make(map[string]string, len(view.Candidates))
	for _, candidate := range view.Candidates {
		statuses[candidate.Guid] = candidate.TVPackStatus
	}
	if statuses["s1"] != "added" || statuses["s1-alt"] != "added" {
		t.Fatalf("season 1 statuses = %q, %q, want added", statuses["s1"], statuses["s1-alt"])
	}
	if statuses["s2"] != "submitting" {
		t.Fatalf("season 2 status = %q, want submitting", statuses["s2"])
	}
}

func TestMarkTVCompleteKeepsOtherSeasonsInReview(t *testing.T) {
	m := newTransitionManager(t, StatusReview)
	m.kind = companionTV
	m.state.Movies[0].Path = ""
	m.searches["movie"] = searchState{Candidates: []Candidate{
		{Guid: "s1", Title: "Friends.1994.S01.1080p.WEB-DL"},
		{Guid: "s2", Title: "Friends.1994.S02.1080p.WEB-DL"},
	}}
	m.tvApprovals = map[string]map[string]bool{"movie": {"season:1": true}}

	candidate := Candidate{Guid: "s1", Title: "Friends.1994.S01.1080p.WEB-DL", TVPackEligible: true}
	if err := m.MarkTVComplete("movie", candidate, "hash-s1"); err != nil {
		t.Fatal(err)
	}
	if got := m.state.Movies[0].Status; got != StatusReview {
		t.Fatalf("TV show status = %s, want %s", got, StatusReview)
	}
	if len(m.state.Movies[0].TVApprovedPacks) != 1 || m.state.Movies[0].TVApprovedPacks[0] != "season:1" {
		t.Fatalf("approved TV packs = %v, want [season:1]", m.state.Movies[0].TVApprovedPacks)
	}
	if m.hasTVApprovalsLocked("movie") {
		t.Fatal("completed season approval is still marked pending")
	}
	if _, ok := m.searches["movie"]; !ok {
		t.Fatal("other TV season candidates were removed")
	}

	view := m.View("movie")
	statuses := make(map[string]string, len(view.Candidates))
	for _, item := range view.Candidates {
		statuses[item.Guid] = item.TVPackStatus
	}
	if statuses["s1"] != "added" {
		t.Fatalf("season 1 status = %q, want added", statuses["s1"])
	}
	if statuses["s2"] != "" {
		t.Fatalf("season 2 status = %q, want review/available", statuses["s2"])
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
