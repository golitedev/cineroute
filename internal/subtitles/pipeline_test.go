package subtitles

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cineroute/internal/library"
	"cineroute/internal/subtitles/opensubtitles"
)

type fakeProber struct {
	streams   map[string]MediaInfo
	reference string
}

func (f *fakeProber) Probe(_ context.Context, path string) (MediaInfo, error) {
	if info, ok := f.streams[path]; ok {
		return info, nil
	}
	return MediaInfo{}, nil
}

func (f *fakeProber) ExtractSubtitle(_ context.Context, _ string, _ int, outputPath string) error {
	return os.WriteFile(outputPath, []byte(f.reference), 0o644)
}

func (f *fakeProber) ConvertToSRT(_ context.Context, inputPath, outputPath string) error {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, data, 0o644)
}

func (f *fakeProber) Version(context.Context) (string, error) { return "ffmpeg test", nil }

// fakeSyncer copies the downloaded subtitle to the output and reports a clean
// alignment, which is what a perfect alass run would produce.
type fakeSyncer struct{}

func (fakeSyncer) Sync(_ context.Context, _, inputPath, outputPath string, _ SyncOptions) (SyncResult, error) {
	data, err := os.ReadFile(inputPath)
	if err != nil {
		return SyncResult{}, err
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Report: AlassReport{
		FPS:    "1.000",
		Blocks: []AlassBlock{{Count: 2, Shift: "0:00:00.000"}},
		Detail: "fps=1.000; shifts=0:00:00.000",
	}}, nil
}

func (fakeSyncer) Version(context.Context) (string, error) { return "alass-cli test", nil }

type fakeOS struct {
	items        []opensubtitles.Item
	downloadText string
	remaining    int
	searches     int
	downloads    int
	downloadErr  error
}

func (f *fakeOS) Configured() bool { return true }

func (f *fakeOS) Search(context.Context, opensubtitles.SearchQuery) (opensubtitles.SearchResponse, error) {
	f.searches++
	return opensubtitles.SearchResponse{Data: f.items}, nil
}

func (f *fakeOS) Features(context.Context, string) (opensubtitles.FeatureResponse, error) {
	return opensubtitles.FeatureResponse{}, nil
}

func (f *fakeOS) Download(context.Context, int) (opensubtitles.DownloadResponse, error) {
	if f.downloadErr != nil {
		return opensubtitles.DownloadResponse{}, f.downloadErr
	}
	f.downloads++
	return opensubtitles.DownloadResponse{
		Link:      "https://dl.opensubtitles.com/sub/file",
		Remaining: opensubtitles.Intish(f.remaining),
		ResetTime: "2026-01-01T00:00:00Z",
	}, nil
}

func (f *fakeOS) DownloadBytes(context.Context, string) ([]byte, error) {
	return []byte(f.downloadText), nil
}

func (f *fakeOS) UserInfo(context.Context) (opensubtitles.UserInfoResponse, error) {
	response := opensubtitles.UserInfoResponse{}
	response.Data.RemainingDownloads = opensubtitles.Intish(f.remaining)
	response.Data.AllowedDownloads = opensubtitles.Intish(100)
	return response, nil
}

const referenceSRT = "1\n00:00:10,000 --> 00:00:12,000\nLine one\n\n2\n00:00:20,000 --> 00:00:22,000\nLine two\n\n"
const swedishSRT = "1\n00:00:10,100 --> 00:00:12,100\nRad ett\n\n2\n00:00:20,100 --> 00:00:22,100\nRad två\n\n"

type testHarness struct {
	manager *Manager
	prober  *fakeProber
	os      *fakeOS
	remote  string
	work    string
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "movies-remote")
	if err := os.MkdirAll(remote, 0o755); err != nil {
		t.Fatalf("create remote root: %v", err)
	}
	work := filepath.Join(base, "work")
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.StatePath = filepath.Join(base, "subtitles.db")
	cfg.WorkDir = work
	cfg.MinVideoBytes = 1
	cfg.MinReferenceCues = 1
	cfg.RequestInterval = 0
	cfg.QuotaReserve = 5
	cfg.MaxCandidates = 3
	cfg.Accept = DefaultAcceptCriteria()

	prober := &fakeProber{streams: map[string]MediaInfo{}, reference: referenceSRT}
	osClient := &fakeOS{downloadText: swedishSRT, remaining: 50}
	scan := library.NewScan([]library.Drive{{
		ID:              "hdd1",
		MovieRoot:       filepath.Join(base, "movies"),
		MovieRemoteRoot: remote,
		TVRoot:          filepath.Join(base, "tv"),
	}})
	manager := newManager(cfg, scan, nil, prober, fakeSyncer{}, osClient)
	if err := manager.StateError(); err != nil {
		t.Fatalf("manager state: %v", err)
	}
	t.Cleanup(func() {
		if manager.store != nil {
			_ = manager.store.close()
		}
	})
	return &testHarness{manager: manager, prober: prober, os: osClient, remote: remote, work: work}
}

// addRemoteMovie writes a movie file and returns its path.
func (h *testHarness) addRemoteMovie(t *testing.T, folder, name string) string {
	t.Helper()
	dir := filepath.Join(h.remote, folder)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create movie folder: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not really a video"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}
	return path
}

func (h *testHarness) configureEmbeddedEnglish(videoPath string) {
	h.prober.streams[videoPath] = MediaInfo{
		DurationMS: 6_000_000,
		Streams: []EmbeddedSubtitle{
			{Index: 2, Codec: "subrip", Language: "en", Usable: true},
		},
	}
}

func swedishCandidate() opensubtitles.Item {
	item := opensubtitles.Item{}
	item.Attributes.Release = "Movie.2019.1080p.WEB-DL"
	item.Attributes.Language = "sv"
	item.Attributes.FeatureDetails.MovieName = "Movie"
	item.Attributes.FeatureDetails.Year = opensubtitles.Intish(2019)
	item.Attributes.FeatureDetails.FeatureID = opensubtitles.FlexID("12345")
	item.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(777), FileName: "Movie.2019.1080p.WEB-DL.sv.srt"}}
	item.Raw = []byte(`{"id":"1","attributes":{"release":"Movie.2019.1080p.WEB-DL"}}`)
	return item
}

func TestScanAndInstallSwedishSubtitle(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.WEB-DL.mkv")
	h.configureEmbeddedEnglish(video)
	h.os.items = []opensubtitles.Item{swedishCandidate()}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	view := h.manager.View("")
	if len(view.Items) != 1 {
		t.Fatalf("scan produced %d items, want 1", len(view.Items))
	}
	item := view.Items[0]
	if item.Status != StatusPending || item.Title != "Movie" || item.Year != 2019 {
		t.Fatalf("unexpected item: %+v", item)
	}
	if item.ReferenceKind != "" {
		t.Errorf("reference must be chosen when processing, not scanning: %+v", item)
	}

	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if item.Status != StatusAdded {
		t.Fatalf("status = %s (%s), want added", item.Status, item.Error)
	}
	output := filepath.Join(filepath.Dir(video), "Movie.2019.1080p.WEB-DL.sv.srt")
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("expected %s to exist: %v", output, err)
	}
	if !strings.Contains(string(data), "Rad ett") {
		t.Errorf("output content = %q", string(data))
	}
	if item.ReferenceKind != "embedded" || item.ReferenceLang != "en" {
		t.Errorf("reference = %+v", item)
	}
	if len(h.manager.View(item.ID).Attempts) == 0 {
		t.Error("expected an accepted attempt to be recorded")
	}

	// A second run must be idempotent: the existing subtitle wins.
	before, _ := os.Stat(output)
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("reprocess: %v", err)
	}
	after, _ := os.Stat(output)
	if after.ModTime() != before.ModTime() || item.Status != StatusAdded {
		t.Errorf("reprocessing rewrote the subtitle: status=%s", item.Status)
	}
}

func TestPipelineUsesExternalSpanishReference(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Another (2020)", "Another.2020.1080p.mkv")
	h.prober.streams[video] = MediaInfo{DurationMS: 6_000_000}
	// Only a Spanish external subtitle is available.
	if err := os.WriteFile(filepath.Join(filepath.Dir(video), "Another.2020.1080p.es.srt"), []byte(referenceSRT), 0o644); err != nil {
		t.Fatalf("write external subtitle: %v", err)
	}
	candidate := swedishCandidate()
	candidate.Attributes.FeatureDetails.MovieName = "Another"
	candidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2020)
	h.os.items = []opensubtitles.Item{candidate}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.manager.View("").Items[0]
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if item.Status != StatusAdded || item.ReferenceKind != "external" || item.ReferenceLang != "es" {
		t.Fatalf("item = %+v", item)
	}
}

func TestPipelineReportsNoReferenceForImageSubtitles(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Image Only (2021)", "Image.Only.2021.1080p.mkv")
	h.prober.streams[video] = MediaInfo{
		DurationMS: 6_000_000,
		Streams:    []EmbeddedSubtitle{{Index: 3, Codec: "hdmv_pgs_subtitle", Language: "en", Usable: false}},
	}
	h.os.items = []opensubtitles.Item{swedishCandidate()}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.manager.View("").Items[0]
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err == nil {
		t.Fatal("expected an error for image-only subtitles")
	}
	if item.Status != StatusNoReference {
		t.Fatalf("status = %s, want %s", item.Status, StatusNoReference)
	}
	output := filepath.Join(filepath.Dir(video), "Image.Only.2021.1080p.sv.srt")
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Errorf("no subtitle must be written, but %s exists", output)
	}
}

func TestPipelineStopsAtQuotaReserve(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Quota (2022)", "Quota.2022.1080p.mkv")
	h.configureEmbeddedEnglish(video)
	quotaCandidate := swedishCandidate()
	quotaCandidate.Attributes.FeatureDetails.MovieName = "Quota"
	quotaCandidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2022)
	quotaCandidate.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(778), FileName: "Quota.2022.1080p.sv.srt"}}
	h.os.items = []opensubtitles.Item{quotaCandidate}
	h.os.remaining = 3 // below the configured reserve of 5

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.manager.View("").Items[0]
	_, err := h.manager.processAndPersist(context.Background(), item, runOptions{refreshQuota: true})
	if !errors.Is(err, errQuotaStop) {
		t.Fatalf("err = %v, want errQuotaStop", err)
	}
	if item.Status != StatusPending {
		t.Errorf("status = %s, want pending so the item can run later", item.Status)
	}
	if h.os.downloads != 0 {
		t.Errorf("expected no download, got %d", h.os.downloads)
	}
}

func TestPipelineSkipsExistingSwedishSubtitle(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Has Sub (2018)", "Has.Sub.2018.1080p.mkv")
	h.prober.streams[video] = MediaInfo{DurationMS: 6_000_000}
	if err := os.WriteFile(filepath.Join(filepath.Dir(video), "Has.Sub.2018.1080p.sv.srt"), []byte(swedishSRT), 0o644); err != nil {
		t.Fatalf("write subtitle: %v", err)
	}
	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.manager.View("").Items[0]
	if item.Status != StatusHasSwedish || !item.HasSwedish {
		t.Fatalf("item = %+v", item)
	}
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if h.os.searches != 0 {
		t.Errorf("expected no OpenSubtitles search, got %d", h.os.searches)
	}
}

func TestManagerBatchRunsPendingItems(t *testing.T) {
	h := newTestHarness(t)
	first := h.addRemoteMovie(t, "First (2019)", "First.2019.1080p.mkv")
	h.configureEmbeddedEnglish(first)
	firstCandidate := swedishCandidate()
	firstCandidate.Attributes.FeatureDetails.MovieName = "First"
	firstCandidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2019)
	firstCandidate.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(779), FileName: "First.2019.1080p.sv.srt"}}
	h.os.items = []opensubtitles.Item{firstCandidate}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if err := h.manager.StartRun(); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		view := h.manager.View("")
		if !view.Batch.Running {
			if view.Stats.Added != 1 {
				t.Fatalf("stats = %+v, want one added", view.Stats)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("batch did not finish")
}

func TestSettingsRoundTrip(t *testing.T) {
	h := newTestHarness(t)
	settings := h.manager.Settings()
	settings.RunBatchSize = 7
	settings.MaxCandidates = 12
	settings.RemovePromoCues = false
	if err := h.manager.UpdateSettings(settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	updated := h.manager.Settings()
	if updated.RunBatchSize != 7 || updated.MaxCandidates != 12 || updated.RemovePromoCues {
		t.Fatalf("settings = %+v", updated)
	}
	invalid := updated
	invalid.MaxCandidates = 99
	if err := h.manager.UpdateSettings(invalid); err == nil {
		t.Error("expected validation error for max_candidates")
	}
}

// TestConcurrentViewDuringRun exercises the UI polling path while the pipeline
// is mutating item state; run with -race.
func TestConcurrentViewDuringRun(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Concurrent (2020)", "Concurrent.2020.1080p.mkv")
	h.configureEmbeddedEnglish(video)
	candidate := swedishCandidate()
	candidate.Attributes.FeatureDetails.MovieName = "Concurrent"
	candidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2020)
	candidate.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(555), FileName: "Concurrent.2020.1080p.sv.srt"}}
	h.os.items = []opensubtitles.Item{candidate}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	itemID := h.manager.View("").Items[0].ID
	pollerDone := make(chan struct{})
	go func() {
		defer close(pollerDone)
		for i := 0; i < 300; i++ {
			_ = h.manager.View(itemID)
		}
	}()
	if err := h.manager.StartRun(); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !h.manager.View("").Batch.Running {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	<-pollerDone
	if status := h.manager.View("").Items[0].Status; status != StatusAdded {
		t.Fatalf("status = %s, want added", status)
	}
}
