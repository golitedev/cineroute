package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/streamrip"
)

func TestChartJobUsesTheAlbumImportPipeline(t *testing.T) {
	e := newEnv(t, fakeAlbums{"release": testAlbum("release", 2)}, func(ctx context.Context, options streamrip.RunOptions, _ int) (streamrip.Result, error) {
		albumRip("release", []int{1, 2}, 2, t)(ctx, options)
		return streamrip.Result{}, nil
	})
	source := ChartSource{PlaylistID: "co", Playlist: "Top 50 Colombia", SpotifyID: "second-track"}
	job, err := e.m.EnqueueChart(t.Context(), "release", source)
	if err != nil {
		t.Fatal(err)
	}
	e.start()
	result := e.wait(job.ID, StatusCompleted, StatusFailed)
	receipts := e.m.PendingChartImports()
	if result.Status != StatusCompleted || result.TracksDone != 2 || result.LibraryPath == "" ||
		len(receipts) != 1 || len(receipts[0].ChartSources) != 1 || receipts[0].ChartSources[0] != source {
		t.Fatalf("completed job %+v, receipts %+v", result, receipts)
	}
}

func TestChartJobsShareReleasesAndYieldToUserRequests(t *testing.T) {
	cfg := config.Default()
	cfg.Paths = config.Paths{Data: t.TempDir(), Staging: t.TempDir(), Library: t.TempDir()}
	m, err := New(&cfg, Deps{Albums: fakeAlbums{
		"chart": testAlbum("chart", 1),
		"user":  testAlbum("user", 1),
	}})
	if err != nil {
		t.Fatal(err)
	}
	co := ChartSource{PlaylistID: "co", SpotifyID: "first"}
	se := ChartSource{PlaylistID: "se", SpotifyID: "first"}
	bg, err := m.EnqueueChart(t.Context(), "chart", co)
	if err != nil || !bg.Automatic {
		t.Fatalf("background job %+v: %v", bg, err)
	}
	shared, err := m.EnqueueChart(t.Context(), "chart", se)
	if err != nil || bg.ID != shared.ID || len(shared.ChartSources) != 2 {
		t.Fatalf("shared job %+v: %v", shared, err)
	}
	user, err := m.Enqueue(t.Context(), "user", "alice")
	if err != nil {
		t.Fatal(err)
	}
	borrowed, err := m.EnqueueChart(t.Context(), "user", co)
	if err != nil || borrowed.ID != user.ID || borrowed.RequestedBy != "alice" || borrowed.Automatic {
		t.Fatalf("borrowed job %+v: %v", borrowed, err)
	}
	next, _, cancel, ok := m.next()
	if !ok {
		t.Fatal("queue is empty")
	}
	defer cancel()
	if next.ID != user.ID {
		t.Fatalf("next job = %s, want user job %s", next.ID, user.ID)
	}
}

func TestChartImportReceiptSurvivesRestartAndHistoryCleanup(t *testing.T) {
	cfg := config.Default()
	cfg.Paths = config.Paths{Data: t.TempDir(), Staging: t.TempDir(), Library: t.TempDir()}
	const path = "Imported release"
	const sample = "01.flac"
	if err := os.Mkdir(filepath.Join(cfg.Paths.Library, path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Paths.Library, path, sample), []byte("installed audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	j := &Job{ID: "0316540000000001", AlbumID: "release", Status: StatusImporting, Automatic: true,
		ChartSources:   []ChartSource{{PlaylistID: "co", SpotifyID: "song"}},
		PreparedImport: &preparedImport{Path: path, Sample: sample, SampleSize: 15, Tracks: 1, Message: M("added"), At: now}}
	if err := (store{path: filepath.Join(cfg.Paths.Data, "jobs.json")}).save([]*Job{j}); err != nil {
		t.Fatal(err)
	}
	m, err := New(&cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.ClearFinished(); err != nil {
		t.Fatal(err)
	}
	receipts := m.PendingChartImports()
	if len(receipts) != 1 || receipts[0].LibraryPath != path || !receipts[0].FinishedAt.Equal(now) {
		t.Fatalf("recovered receipts %+v", receipts)
	}
	if err := m.ConfirmChartImport(j.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.ClearFinished(); err != nil {
		t.Fatal(err)
	}
	if len(m.List()) != 0 {
		t.Fatal("acknowledged receipt still has a job")
	}
}
