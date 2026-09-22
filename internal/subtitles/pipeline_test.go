package subtitles

import (
	"context"
	"errors"
	"fmt"
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
	// byIndex overrides the extracted reference per embedded stream index, so a
	// test can model a forced/signs-only track next to a full one.
	byIndex map[int]string
	// extractErr makes the extraction fail for every stream.
	extractErr error
	// cancelDuringExtract cancels the run in the middle of the reference stage,
	// which is what happens when a user presses Cancel while ffmpeg demuxes.
	cancelDuringExtract context.CancelFunc
	// afterProgress runs right after a progress sample was published.
	afterProgress func()
	extracted     []int
}

func (f *fakeProber) Probe(_ context.Context, path string) (MediaInfo, error) {
	if info, ok := f.streams[path]; ok {
		info.Probed = true
		return info, nil
	}
	// A successfully probed video with no subtitle streams is still "analyzed";
	// only a failed or skipped probe leaves Probed false.
	return MediaInfo{Probed: true}, nil
}

func (f *fakeProber) ExtractSubtitle(ctx context.Context, _ string, index int, outputPath string, progress func(ExtractProgress)) error {
	f.extracted = append(f.extracted, index)
	if progress != nil {
		progress(ExtractProgress{PositionMS: 2_500, Elapsed: 2 * time.Second})
	}
	if f.afterProgress != nil {
		f.afterProgress()
	}
	if f.cancelDuringExtract != nil {
		// Model ffmpeg dying with the run: the extraction returns the canceled
		// context error instead of a reference.
		f.cancelDuringExtract()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.extractErr != nil {
		return f.extractErr
	}
	content := f.reference
	if f.byIndex != nil {
		if override, ok := f.byIndex[index]; ok {
			content = override
		}
	}
	return os.WriteFile(outputPath, []byte(content), 0o644)
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

// liveItem returns the manager's own item, not the trimmed list snapshot from
// View(), which omits the embedded stream list on purpose.
func (h *testHarness) liveItem(t *testing.T, id string) *Item {
	t.Helper()
	item := h.manager.itemByID(id)
	if item == nil {
		t.Fatalf("item %s is not in the queue", id)
	}
	return item
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
	item := h.liveItem(t, view.Items[0].ID)
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
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
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
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
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
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
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
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
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

// TestScanReportsExternalSubtitleAvailability covers the queue split the
// Subtitles page exposes: movies that already have an external text subtitle
// file versus movies whose reference must be extracted from the video.
func TestScanReportsExternalSubtitleAvailability(t *testing.T) {
	h := newTestHarness(t)

	withExternal := h.addRemoteMovie(t, "External (2019)", "External.2019.1080p.mkv")
	if err := os.WriteFile(filepath.Join(filepath.Dir(withExternal), "External.2019.1080p.en.srt"), []byte(referenceSRT), 0o644); err != nil {
		t.Fatalf("write external subtitle: %v", err)
	}
	embeddedOnly := h.addRemoteMovie(t, "Embedded (2020)", "Embedded.2020.1080p.mkv")
	h.configureEmbeddedEnglish(embeddedOnly)

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	view := h.manager.View("")
	if len(view.Items) != 2 {
		t.Fatalf("scan produced %d items, want 2", len(view.Items))
	}
	byName := map[string]*Item{}
	for _, item := range view.Items {
		byName[item.VideoName] = item
	}

	external := byName["External.2019.1080p.mkv"]
	if external == nil {
		t.Fatal("external-subtitle movie was not scanned")
	}
	if !external.HasExternalSubtitle || len(external.ExternalSubtitles) != 1 {
		t.Fatalf("external movie = %+v", external)
	}
	if external.ExternalSubtitles[0].Language != "en" || !external.ExternalSubtitles[0].Usable {
		t.Errorf("external ref = %+v", external.ExternalSubtitles[0])
	}

	embedded := byName["Embedded.2020.1080p.mkv"]
	if embedded == nil {
		t.Fatal("embedded-only movie was not scanned")
	}
	if embedded.HasExternalSubtitle || len(embedded.ExternalSubtitles) != 0 {
		t.Fatalf("embedded-only movie = %+v", embedded)
	}
	if !embedded.Probed {
		t.Fatal("the embedded-only movie should be analyzed after the scan")
	}
	// The list snapshot omits the stream list; the live item carries it.
	if live := h.liveItem(t, embedded.ID); len(live.EmbeddedSubStreams) == 0 {
		t.Fatal("embedded subtitle streams were not recorded")
	}

	// The statistics the page header shows must split the queue the same way.
	if view.Stats.WithExternalSubtitle != 1 || view.Stats.NoExternalSubtitle != 1 {
		t.Fatalf("stats = %+v, want one of each", view.Stats)
	}

	// Skipping removes a movie from the work queue but keeps it listed.
	if err := h.manager.Skip(embedded.ID); err != nil {
		t.Fatalf("Skip: %v", err)
	}
	after := h.manager.View("")
	if after.Stats.Skipped != 1 || after.Stats.NoExternalSubtitle != 0 {
		t.Fatalf("stats after skip = %+v", after.Stats)
	}
	if after.Items[0].ID == "" {
		t.Fatal("skipped items must stay visible")
	}
	// A rescan must not resurrect a skipped movie.
	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if got := h.manager.itemByID(embedded.ID); got == nil || got.Status != StatusSkipped {
		t.Fatalf("skipped status after rescan = %+v", got)
	}
}

// TestPipelineProbesUnanalyzedMovieOnDemand covers the scan probe budget: a
// movie the scan never analyzed must be probed when it is processed, instead of
// reporting no_reference from missing data.
func TestPipelineProbesUnanalyzedMovieOnDemand(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.ScanBatchSize = 1

	first := h.addRemoteMovie(t, "Analyzed (2019)", "Analyzed.2019.1080p.mkv")
	h.configureEmbeddedEnglish(first)
	second := h.addRemoteMovie(t, "Pending Probe (2020)", "Pending.Probe.2020.1080p.mkv")
	h.configureEmbeddedEnglish(second)

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	view := h.manager.View("")
	if len(view.Items) != 2 {
		t.Fatalf("scan produced %d items, want 2", len(view.Items))
	}
	analyzed := map[string]bool{}
	for _, item := range view.Items {
		analyzed[item.VideoName] = item.Probed
	}
	if !analyzed["Analyzed.2019.1080p.mkv"] {
		t.Fatal("the first movie should have been probed during the scan")
	}
	if analyzed["Pending.Probe.2020.1080p.mkv"] {
		t.Skip("probe budget did not leave a movie unanalyzed on this filesystem order")
	}
	if view.Stats.NotAnalyzed != 1 {
		t.Fatalf("stats = %+v, want one not-analyzed movie", view.Stats)
	}

	// Processing the unanalyzed movie probes it and finds the embedded stream.
	var pending *Item
	for _, item := range view.Items {
		if item.VideoName == "Pending.Probe.2020.1080p.mkv" {
			pending = item
		}
	}
	candidate := swedishCandidate()
	candidate.Attributes.FeatureDetails.MovieName = "Pending Probe"
	candidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2020)
	candidate.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(901), FileName: "Pending.Probe.2020.1080p.sv.srt"}}
	h.os.items = []opensubtitles.Item{candidate}

	if _, err := h.manager.processAndPersist(context.Background(), pending, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if pending.Status != StatusAdded {
		t.Fatalf("status = %s (%s), want added", pending.Status, pending.Error)
	}
	if pending.ReferenceKind != "embedded" || pending.ReferenceLang != "en" {
		t.Fatalf("reference = %s/%s, want embedded/en", pending.ReferenceKind, pending.ReferenceLang)
	}
	if !pending.Probed {
		t.Error("the movie should be marked analyzed after the on-demand probe")
	}
}

// TestPipelineTriesFurtherReferenceStreams covers a forced/signs-only embedded
// track: the pipeline must reject it and fall through to the next usable stream
// instead of reporting no_reference for the whole movie.
func TestPipelineTriesFurtherReferenceStreams(t *testing.T) {
	h := newTestHarness(t)
	h.manager.cfg.MinReferenceCues = 2

	video := h.addRemoteMovie(t, "Signs Then Full (2018)", "Signs.Then.Full.2018.1080p.mkv")
	h.prober.streams[video] = MediaInfo{
		DurationMS: 6_000_000,
		Streams: []EmbeddedSubtitle{
			{Index: 2, Codec: "subrip", Language: "en", Forced: true, Usable: true},
			{Index: 3, Codec: "subrip", Language: "es", Usable: true},
		},
	}
	// The forced English track extracts to a single sign cue, the Spanish track is
	// a full subtitle.
	h.prober.byIndex = map[int]string{
		2: "1\n00:00:01,000 --> 00:00:02,000\n[signs]\n",
		3: referenceSRT,
	}
	candidate := swedishCandidate()
	candidate.Attributes.FeatureDetails.MovieName = "Signs Then Full"
	candidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2018)
	candidate.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(902), FileName: "Signs.Then.Full.2018.1080p.sv.srt"}}
	h.os.items = []opensubtitles.Item{candidate}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if item.Status != StatusAdded {
		t.Fatalf("status = %s (%s), want added", item.Status, item.Error)
	}
	if item.ReferenceStream != 3 || item.ReferenceLang != "es" {
		t.Fatalf("reference = stream %d/%s, want the Spanish stream 3", item.ReferenceStream, item.ReferenceLang)
	}
	if len(h.prober.extracted) != 2 {
		t.Fatalf("expected both streams to be tried, extracted=%v", h.prober.extracted)
	}
}

// TestWorkDirectoryIsValidated covers the most common deployment mistake: the
// container cannot write subtitles.work_dir (usually a /tmp bind mount owned by
// another user). It must be reported once, with the path, instead of surfacing
// only as a per-movie failure.
func TestWorkDirectoryIsValidated(t *testing.T) {
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if err := ensureWorkDir(filepath.Join(blocker, "work")); err == nil {
		t.Fatal("expected ensureWorkDir to fail when the parent is a regular file")
	}

	statePath := filepath.Join(base, "subtitles.db")
	st, err := openStore(statePath)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = st.close() })

	cfg := DefaultConfig()
	cfg.StatePath = statePath
	cfg.WorkDir = filepath.Join(blocker, "work")
	m := newManager(cfg, library.NewScan(nil), st, &fakeProber{}, fakeSyncer{}, &fakeOS{})
	view := m.View("")
	if view.WorkDirError == "" {
		t.Fatalf("view did not report the unwritable work directory: %+v", view)
	}
	if !strings.Contains(view.WorkDirError, "not a directory") {
		t.Errorf("work directory error = %q", view.WorkDirError)
	}
}

// TestLiveStageIsVisibleDuringProcessing checks that a movie being worked on is
// observable while it runs: embedded extraction can take minutes, and the page
// polls the item list rather than just the batch counters.
func TestLiveStageIsVisibleDuringProcessing(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Long Extract (2017)", "Long.Extract.2017.1080p.mkv")
	h.configureEmbeddedEnglish(video)
	candidate := swedishCandidate()
	candidate.Attributes.FeatureDetails.MovieName = "Long Extract"
	candidate.Attributes.FeatureDetails.Year = opensubtitles.Intish(2017)
	candidate.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(903), FileName: "Long.Extract.2017.1080p.sv.srt"}}
	h.os.items = []opensubtitles.Item{candidate}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)

	var observed []string
	h.manager.SetStageHandler(func(staged *Item, stage, status string) {
		live := h.manager.itemByID(staged.ID)
		if live == nil {
			return
		}
		observed = append(observed, live.Status+"/"+live.Step)
	})

	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	if len(observed) == 0 {
		t.Fatal("no stage events were reported")
	}
	foundReference := false
	for _, entry := range observed {
		if strings.HasPrefix(entry, StatusProcessing+"/reference") {
			foundReference = true
		}
	}
	if !foundReference {
		t.Fatalf("the live item never showed the reference stage: %v", observed)
	}
}

// TestCandidateOrderingIsDeterministic pins the download order: safe candidates
// first, then identity score, then a strong audit result over a source fallback,
// then the release score, and finally the file id so repeated runs never spend
// quota on a different candidate.
func TestCandidateOrderingIsDeterministic(t *testing.T) {
	h := newTestHarness(t)
	item := &Item{
		ID:        "test-item",
		VideoName: "Movie.2019.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP.mkv",
		Title:     "Movie",
		Year:      2019,
	}
	accepted := map[string]bool{NormalizedTitle("Movie"): true}

	makeItem := func(fileID int, release, fileName string) opensubtitles.Item {
		item := opensubtitles.Item{}
		item.Attributes.Release = release
		item.Attributes.FeatureDetails.MovieName = "Movie"
		item.Attributes.FeatureDetails.Year = opensubtitles.Intish(2019)
		item.Attributes.Files = []opensubtitles.File{{FileID: opensubtitles.Intish(fileID), FileName: fileName}}
		item.Raw = []byte(`{"id":"x","attributes":{}}`)
		return item
	}
	merged := map[int]opensubtitles.Item{
		10: makeItem(10, "Movie.2019.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", "Movie.2019.1080p.AMZN.WEB-DL.sv.srt"),
		11: makeItem(11, "Movie.2019.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", "Movie.2019.1080p.AMZN.WEB-DL.sv.srt"),
		// A BluRay release of the same movie: safe, but a different source family.
		12: makeItem(12, "Movie.2019.1080p.BluRay.x264-OTHER", "Movie.2019.1080p.BluRay.sv.srt"),
	}

	for attempt := 0; attempt < 5; attempt++ {
		candidates := h.manager.auditCandidates(item, merged, accepted, "")
		if len(candidates) != 3 {
			t.Fatalf("candidates = %+v", candidates)
		}
		if candidates[0].FileID != 10 || candidates[1].FileID != 11 {
			t.Fatalf("attempt %d ordered %d,%d,%d; identical WEB-DL candidates must come first in file-id order",
				attempt, candidates[0].FileID, candidates[1].FileID, candidates[2].FileID)
		}
		if candidates[2].FileID != 12 || candidates[2].Category != CategoryFallback {
			t.Fatalf("the BluRay release should sort last as a fallback: %+v", candidates[2])
		}
	}
}

// TestCancelDuringExtractionStaysPending covers the reported failure mode: the
// user cancels while ffmpeg is demuxing a movie, and the item is then filed as
// "no reference" even though the extraction never finished. A canceled run
// learns nothing about the movie, so it must stay queued and carry no error.
func TestCancelDuringExtractionStaysPending(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Canceled (2016)", "Canceled.2016.1080p.mkv")
	h.prober.streams[video] = MediaInfo{
		DurationMS: 6_000_000,
		Streams: []EmbeddedSubtitle{
			{Index: 2, Codec: "subrip", Language: "en", Usable: true},
			{Index: 3, Codec: "subrip", Language: "es", Usable: true},
		},
	}
	h.os.items = []opensubtitles.Item{swedishCandidate()}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)

	ctx, cancel := context.WithCancel(context.Background())
	h.prober.cancelDuringExtract = cancel
	status, err := h.manager.processAndPersist(ctx, item, runOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if status != StatusPending {
		t.Fatalf("status = %s, want pending so the movie is not filed as no_reference", status)
	}
	if item.Error != "" {
		t.Errorf("item error = %q, want it hidden for a canceled run", item.Error)
	}
	if len(h.prober.extracted) != 1 {
		t.Errorf("extracted = %v, want the candidate loop to stop at the cancel", h.prober.extracted)
	}
	view := h.manager.View(item.ID)
	if view.Stats.NoReference != 0 {
		t.Errorf("stats = %+v, want no no_reference entry for a canceled run", view.Stats)
	}
	if view.OpenItem.StepDetail != "" {
		t.Errorf("step detail = %q, want it cleared when the run ends", view.OpenItem.StepDetail)
	}
}

// TestExtractionProgressReachesThePage checks the progress plumbing end to end:
// the sample the prober reports must be formatted against the video duration and
// published on the live item the page polls while the extraction runs.
func TestExtractionProgressReachesThePage(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Movie (2019)", "Movie.2019.1080p.WEB-DL.mkv")
	h.configureEmbeddedEnglish(video)
	h.os.items = []opensubtitles.Item{swedishCandidate()}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)

	var observed []string
	h.prober.afterProgress = func() {
		if live := h.manager.itemByID(item.ID); live != nil && live.StepDetail != "" {
			observed = append(observed, live.StepDetail)
		}
	}

	if _, err := h.manager.processAndPersist(context.Background(), item, runOptions{}); err != nil {
		t.Fatalf("process: %v", err)
	}
	// The fake reports 2.5 s of a 100 minute movie: 0%, still useful because the
	// position shows the demux is moving.
	if len(observed) == 0 {
		t.Fatal("the page never saw extraction progress")
	}
	want := "0% · 0:02 of 1:40:00"
	if observed[0] != want {
		t.Fatalf("progress detail = %q, want %q", observed[0], want)
	}
}

// TestExtractionTimeoutStopsAfterTheFirstStream pins the fail-fast behavior: a
// demux that runs out of time would have to read the whole video again for every
// remaining stream, so the movie is reported as failed after the first timeout
// instead of repeating a 15 minute wait per stream.
func TestExtractionTimeoutStopsAfterTheFirstStream(t *testing.T) {
	h := newTestHarness(t)
	video := h.addRemoteMovie(t, "Slow Disk (2014)", "Slow.Disk.2014.1080p.mkv")
	h.prober.streams[video] = MediaInfo{
		DurationMS: 7_260_000,
		Streams: []EmbeddedSubtitle{
			{Index: 2, Codec: "subrip", Language: "en", Usable: true},
			{Index: 3, Codec: "subrip", Language: "es", Usable: true},
			{Index: 4, Codec: "subrip", Language: "en", Usable: true},
		},
	}
	h.prober.extractErr = fmt.Errorf("ffmpeg subtitle extraction timed out after 15m0s: %w", context.DeadlineExceeded)
	h.os.items = []opensubtitles.Item{swedishCandidate()}

	if err := h.manager.Scan(context.Background()); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	item := h.liveItem(t, h.manager.View("").Items[0].ID)
	status, err := h.manager.processAndPersist(context.Background(), item, runOptions{})
	if status != StatusFailed {
		t.Fatalf("status = %s, want failed for a timed-out extraction", status)
	}
	if err == nil || !strings.Contains(err.Error(), "did not finish in time") {
		t.Fatalf("err = %v, want an explicit timeout message", err)
	}
	if len(h.prober.extracted) != 1 {
		t.Errorf("extracted = %v, want only the first stream to be attempted", h.prober.extracted)
	}
}

func TestFormatExtractProgress(t *testing.T) {
	cases := []struct {
		name     string
		sample   ExtractProgress
		duration int64
		want     string
	}{
		{"percent", ExtractProgress{PositionMS: 3_723_000}, 7_260_000, "51% · 1:02:03 of 2:01:00"},
		{"position only", ExtractProgress{PositionMS: 62_000}, 0, "1:02 read"},
		{"elapsed only", ExtractProgress{Elapsed: 200 * time.Second}, 7_260_000, "3m 20s elapsed"},
		{"clamped", ExtractProgress{PositionMS: 10_000_000}, 7_260_000, "100% · 2:46:40 of 2:01:00"},
	}
	for _, tc := range cases {
		if got := formatExtractProgress(tc.sample, tc.duration); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
