package subtitles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cineroute/internal/library"
	"cineroute/internal/subtitles/opensubtitles"
)

// Config is the runtime configuration of the subtitle subsystem. It is derived
// from config.Subtitles by the web server.
type Config struct {
	Enabled                bool
	StatePath              string
	WorkDir                string
	WorkRetentionDays      int
	TargetLanguage         string
	ReferenceLanguages     []string
	FFmpegPath             string
	FFprobePath            string
	AlassPath              string
	ScanBatchSize          int
	RunBatchSize           int
	RequestInterval        time.Duration
	QuotaReserve           int
	MaxCandidates          int
	UseMoviehash           bool
	RemovePromoCues        bool
	AllowUnalignedFallback bool
	MinReferenceCues       int
	MinVideoBytes          int64
	SkipSampleFiles        bool
	ProbeTimeout           time.Duration
	ExtractTimeout         time.Duration
	SyncTimeout            time.Duration
	AlassNoSplit           bool
	AlassSplitPenalty      int
	OverwriteExisting      bool
	Accept                 AcceptCriteria
	TitleOverrides         map[string]string
	OpenSubtitles          opensubtitles.Config
}

// DefaultConfig mirrors the defaults documented in config.example.yaml.
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		StatePath:          "/data/subtitles.db",
		WorkDir:            "/tmp/cineroute-subtitles",
		WorkRetentionDays:  7,
		TargetLanguage:     "sv",
		ReferenceLanguages: []string{"en", "es"},
		FFmpegPath:         "ffmpeg",
		FFprobePath:        "ffprobe",
		AlassPath:          "alass",
		ScanBatchSize:      200,
		RunBatchSize:       20,
		RequestInterval:    400 * time.Millisecond,
		QuotaReserve:       5,
		MaxCandidates:      5,
		UseMoviehash:       true,
		RemovePromoCues:    true,
		MinReferenceCues:   20,
		MinVideoBytes:      20 * 1024 * 1024,
		SkipSampleFiles:    true,
		ProbeTimeout:       60 * time.Second,
		ExtractTimeout:     300 * time.Second,
		SyncTimeout:        300 * time.Second,
		AlassSplitPenalty:  7,
		Accept:             DefaultAcceptCriteria(),
		OpenSubtitles: opensubtitles.Config{
			BaseURL:         "https://api.opensubtitles.com/api/v1",
			UserAgent:       "CineRoute/1.0",
			RequestInterval: 400 * time.Millisecond,
			RequestTimeout:  30 * time.Second,
			DownloadTimeout: 60 * time.Second,
		},
	}
}

// retryMaxCandidates is used by the per-item retry action, which is the
// equivalent of the manual "second pass" that tried up to twenty candidates.
const retryMaxCandidates = 20

const (
	settingRunBatchSize           = "run_batch_size"
	settingRequestIntervalMS      = "request_interval_ms"
	settingMaxCandidates          = "max_candidates"
	settingQuotaReserve           = "quota_reserve"
	settingUseMoviehash           = "use_moviehash"
	settingRemovePromoCues        = "remove_promo_cues"
	settingAllowUnalignedFallback = "allow_unaligned_fallback"
	settingMinReferenceCues       = "min_reference_cues"
	settingAlassNoSplit           = "alass_no_split"
	settingAlassSplitPenalty      = "alass_split_penalty"
	settingOverwriteExisting      = "overwrite_existing"
	settingWorkRetentionDays      = "work_retention_days"
)

// Manager owns the subtitle queue, its durable store and the background jobs.
type Manager struct {
	cfg      Config
	lib      *library.Scan
	store    *store
	prober   Prober
	syncer   Syncer
	osClient OSClient

	mu             sync.RWMutex
	items          []*Item
	byID           map[string]*Item
	searches       map[string]map[string]searchRecord
	candidates     map[string][]Candidate
	attempts       map[string][]Attempt
	featureCanon   map[string]string
	batch          BatchStatus
	batchCancel    context.CancelFunc
	quota          QuotaView
	stateErr       error
	workDirErr     error
	lastFeatureErr string

	// onStage is called before each pipeline stage so the HTTP layer can expose
	// progress without the manager knowing about it.
	onStage func(item *Item, stage, status string)

	integrationMu sync.Mutex
	integration   IntegrationView
	integrationAt time.Time
}

// NewManager opens the subtitle store and prepares the subsystem.
func NewManager(cfg Config, lib *library.Scan) *Manager {
	osClient := opensubtitles.New(cfg.OpenSubtitles)
	prober := ExecProber{
		FFmpegPath:     cfg.FFmpegPath,
		FFprobePath:    cfg.FFprobePath,
		ProbeTimeout:   cfg.ProbeTimeout,
		ExtractTimeout: cfg.ExtractTimeout,
	}
	return newManager(cfg, lib, nil, prober, ExecSyncer{Binary: cfg.AlassPath}, osClient)
}

// newManager allows tests to inject stub dependencies.
func newManager(cfg Config, lib *library.Scan, st *store, prober Prober, syncer Syncer, osClient OSClient) *Manager {
	m := &Manager{
		cfg:          cfg,
		lib:          lib,
		store:        st,
		prober:       prober,
		syncer:       syncer,
		osClient:     osClient,
		byID:         map[string]*Item{},
		searches:     map[string]map[string]searchRecord{},
		candidates:   map[string][]Candidate{},
		attempts:     map[string][]Attempt{},
		featureCanon: map[string]string{},
	}
	if !cfg.Enabled {
		slog.Info("subtitles: subsystem disabled")
		return m
	}
	if st == nil {
		opened, err := openStore(cfg.StatePath)
		if err != nil {
			m.stateErr = err
			slog.Error("subtitles: cannot open the queue database", "state_path", cfg.StatePath, "err", err)
			return m
		}
		m.store = opened
	}
	items, err := m.store.loadItems()
	if err != nil {
		m.stateErr = err
		slog.Error("subtitles: cannot load the queue", "state_path", cfg.StatePath, "err", err)
		return m
	}
	searches, err := m.store.loadSearches()
	if err != nil {
		m.stateErr = err
		slog.Error("subtitles: cannot load cached searches", "state_path", cfg.StatePath, "err", err)
		return m
	}
	settings, err := m.store.loadSettings()
	if err != nil {
		m.stateErr = err
		slog.Error("subtitles: cannot load settings", "state_path", cfg.StatePath, "err", err)
		return m
	}
	m.applySettings(settings)
	m.searches = searches
	changed := false
	for _, item := range items {
		if isTransientStatus(item.Status) {
			item.Status = StatusPending
			item.Step = ""
			changed = true
		}
		m.byID[item.ID] = item
		m.items = append(m.items, item)
	}
	m.sortItemsLocked()
	if changed {
		_ = m.store.saveItems(m.items)
	}
	pruned := m.pruneWorkDirs()
	if err := ensureWorkDir(cfg.WorkDir); err != nil {
		m.workDirErr = err
		slog.Error("subtitles: work directory is not writable; movies cannot be processed",
			"work_dir", cfg.WorkDir,
			"err", err,
			"hint", "the container user must own the directory or have write permission: chown it on the host, or point subtitles.work_dir at a writable path such as /data/subtitles-work")
	} else {
		slog.Info("subtitles: work directory ready", "work_dir", cfg.WorkDir)
	}
	roots := RemoteMovieRoots(m.lib.Drives())
	slog.Info("subtitles: subsystem ready",
		"state_path", cfg.StatePath,
		"work_dir", cfg.WorkDir,
		"target_language", cfg.TargetLanguage,
		"reference_languages", strings.Join(cfg.ReferenceLanguages, ","),
		"movies", len(m.items),
		"remote_roots", len(roots),
		"opensubtitles_configured", m.osClient != nil && m.osClient.Configured(),
		"ffmpeg", cfg.FFmpegPath,
		"alass", cfg.AlassPath,
		"pruned_work_dirs", pruned)
	for _, root := range roots {
		slog.Debug("subtitles: remote root", "drive", root.DriveID, "path", root.Path)
	}
	return m
}

// SetStageHandler registers a progress callback (used by tests and the UI).
func (m *Manager) SetStageHandler(handler func(item *Item, stage, status string)) {
	m.mu.Lock()
	m.onStage = handler
	m.mu.Unlock()
}

// Enabled reports whether the subsystem is usable.
func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled
}

// StateError reports why the subsystem is unavailable.
func (m *Manager) StateError() error {
	if m == nil {
		return errors.New("subtitle subsystem is unavailable")
	}
	return m.stateErr
}

func (m *Manager) applySettings(settings map[string]string) {
	m.cfg.RunBatchSize = settingInt(settings, settingRunBatchSize, m.cfg.RunBatchSize)
	m.cfg.MaxCandidates = settingInt(settings, settingMaxCandidates, m.cfg.MaxCandidates)
	m.cfg.QuotaReserve = settingInt(settings, settingQuotaReserve, m.cfg.QuotaReserve)
	m.cfg.MinReferenceCues = settingInt(settings, settingMinReferenceCues, m.cfg.MinReferenceCues)
	m.cfg.AlassSplitPenalty = settingInt(settings, settingAlassSplitPenalty, m.cfg.AlassSplitPenalty)
	m.cfg.WorkRetentionDays = settingInt(settings, settingWorkRetentionDays, m.cfg.WorkRetentionDays)
	if raw := settingInt(settings, settingRequestIntervalMS, int(m.cfg.RequestInterval/time.Millisecond)); raw > 0 {
		m.cfg.RequestInterval = time.Duration(raw) * time.Millisecond
		m.cfg.OpenSubtitles.RequestInterval = m.cfg.RequestInterval
	}
	m.cfg.UseMoviehash = settingBool(settings, settingUseMoviehash, m.cfg.UseMoviehash)
	m.cfg.RemovePromoCues = settingBool(settings, settingRemovePromoCues, m.cfg.RemovePromoCues)
	m.cfg.AllowUnalignedFallback = settingBool(settings, settingAllowUnalignedFallback, m.cfg.AllowUnalignedFallback)
	m.cfg.AlassNoSplit = settingBool(settings, settingAlassNoSplit, m.cfg.AlassNoSplit)
	m.cfg.OverwriteExisting = settingBool(settings, settingOverwriteExisting, m.cfg.OverwriteExisting)
}

// SettingsView is the editable subset of the configuration.
type SettingsView struct {
	RunBatchSize           int  `json:"run_batch_size"`
	RequestIntervalMS      int  `json:"request_interval_ms"`
	MaxCandidates          int  `json:"max_candidates"`
	QuotaReserve           int  `json:"quota_reserve"`
	UseMoviehash           bool `json:"use_moviehash"`
	RemovePromoCues        bool `json:"remove_promo_cues"`
	AllowUnalignedFallback bool `json:"allow_unaligned_fallback"`
	MinReferenceCues       int  `json:"min_reference_cues"`
	AlassNoSplit           bool `json:"alass_no_split"`
	AlassSplitPenalty      int  `json:"alass_split_penalty"`
	OverwriteExisting      bool `json:"overwrite_existing"`
	WorkRetentionDays      int  `json:"work_retention_days"`
}

func (m *Manager) settingsView() SettingsView {
	return SettingsView{
		RunBatchSize:           m.cfg.RunBatchSize,
		RequestIntervalMS:      int(m.cfg.RequestInterval / time.Millisecond),
		MaxCandidates:          m.cfg.MaxCandidates,
		QuotaReserve:           m.cfg.QuotaReserve,
		UseMoviehash:           m.cfg.UseMoviehash,
		RemovePromoCues:        m.cfg.RemovePromoCues,
		AllowUnalignedFallback: m.cfg.AllowUnalignedFallback,
		MinReferenceCues:       m.cfg.MinReferenceCues,
		AlassNoSplit:           m.cfg.AlassNoSplit,
		AlassSplitPenalty:      m.cfg.AlassSplitPenalty,
		OverwriteExisting:      m.cfg.OverwriteExisting,
		WorkRetentionDays:      m.cfg.WorkRetentionDays,
	}
}

// Settings returns a copy of the editable settings.
func (m *Manager) Settings() SettingsView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settingsView()
}

// Quota returns the last known OpenSubtitles download quota.
func (m *Manager) Quota() QuotaView {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.quota
}

// IntegrationView reports tool availability for the status endpoint.
type IntegrationView struct {
	FFmpeg        string `json:"ffmpeg"`
	Alass         string `json:"alass"`
	AlassVersion  string `json:"alass_version,omitempty"`
	OpenSubtitles string `json:"opensubtitles"`
	WorkDir       string `json:"work_dir"`
	WorkDirOK     bool   `json:"work_dir_writable"`
}

// Integration probes the external tools, caching the result briefly because
// each probe spawns a child process.
func (m *Manager) Integration(ctx context.Context) IntegrationView {
	if m == nil {
		return IntegrationView{WorkDir: ""}
	}
	m.integrationMu.Lock()
	if !m.integrationAt.IsZero() && time.Since(m.integrationAt) < time.Minute {
		cached := m.integration
		m.integrationMu.Unlock()
		return cached
	}
	m.integrationMu.Unlock()
	out := m.probeIntegration(ctx)
	m.integrationMu.Lock()
	m.integration = out
	m.integrationAt = time.Now()
	m.integrationMu.Unlock()
	return out
}

// probeIntegration performs the actual tool checks.
func (m *Manager) probeIntegration(ctx context.Context) IntegrationView {
	out := IntegrationView{OpenSubtitles: "not configured", WorkDir: m.cfg.WorkDir}
	if m.osClient != nil && m.osClient.Configured() {
		out.OpenSubtitles = "configured"
	}
	if version, err := m.prober.Version(ctx); err == nil && version != "" {
		out.FFmpeg = "ok"
	} else {
		out.FFmpeg = "unavailable"
	}
	if version, err := m.syncer.Version(ctx); err == nil {
		out.Alass = "ok"
		out.AlassVersion = version
	} else if m.cfg.AlassPath != "" {
		if _, statErr := os.Stat(m.cfg.AlassPath); statErr == nil {
			out.Alass = "ok"
		} else {
			out.Alass = "unavailable"
		}
	}
	if err := os.MkdirAll(m.cfg.WorkDir, 0o755); err == nil {
		if probe, err := os.CreateTemp(m.cfg.WorkDir, ".write-test-*"); err == nil {
			out.WorkDirOK = true
			name := probe.Name()
			_ = probe.Close()
			_ = os.Remove(name)
		}
	}
	return out
}

// View is the payload of GET /api/subtitles.
type View struct {
	Enabled            bool           `json:"enabled"`
	WorkDir            string         `json:"work_dir"`
	TargetLanguage     string         `json:"target_language"`
	ReferenceLanguages []string       `json:"reference_languages"`
	Accept             AcceptCriteria `json:"accept"`
	Settings           SettingsView   `json:"settings"`
	Stats              Stats          `json:"stats"`
	Items              []*Item        `json:"items"`
	Batch              BatchStatus    `json:"batch"`
	Quota              QuotaView      `json:"quota"`
	StateError         string         `json:"state_error,omitempty"`
	WorkDirError       string         `json:"work_dir_error,omitempty"`
	FeatureError       string         `json:"feature_error,omitempty"`
	Open               string         `json:"open,omitempty"`
	OpenItem           *Item          `json:"open_item,omitempty"`
	Candidates         []Candidate    `json:"candidates,omitempty"`
	Attempts           []Attempt      `json:"attempts,omitempty"`
}

// View returns the full page payload, optionally with one item's detail.
func (m *Manager) View(openID string) View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	view := View{
		Enabled:            m.cfg.Enabled,
		WorkDir:            m.cfg.WorkDir,
		TargetLanguage:     m.cfg.TargetLanguage,
		ReferenceLanguages: append([]string(nil), m.cfg.ReferenceLanguages...),
		Accept:             m.cfg.Accept,
		Settings:           m.settingsView(),
		Batch:              m.batch,
		Quota:              m.quota,
		FeatureError:       m.lastFeatureErr,
	}
	for _, item := range m.items {
		snapshot := copyItem(item)
		// The embedded stream list can be large (720 movies x several streams) and
		// the list view never renders it; it is included for the open item below.
		snapshot.EmbeddedSubStreams = nil
		view.Items = append(view.Items, snapshot)
		view.Stats.Total++
		if stillNeedsWork(item) {
			if item.HasExternalSubtitle {
				view.Stats.WithExternalSubtitle++
			} else {
				view.Stats.NoExternalSubtitle++
			}
			if !item.Probed {
				view.Stats.NotAnalyzed++
			}
		}
		switch item.Status {
		case StatusPending:
			view.Stats.Pending++
		case StatusHasSwedish:
			view.Stats.HasSwedish++
		case StatusAdded, StatusAddedReview:
			view.Stats.Added++
		case StatusNeedsReview:
			view.Stats.NeedsReview++
		case StatusNoMatch:
			view.Stats.NoMatch++
		case StatusNoReference:
			view.Stats.NoReference++
		case StatusFailed:
			view.Stats.Failed++
		case StatusSkipped:
			view.Stats.Skipped++
		default:
			view.Stats.Processing++
		}
	}
	if m.stateErr != nil {
		view.StateError = m.stateErr.Error()
	}
	if m.workDirErr != nil {
		view.WorkDirError = m.workDirErr.Error()
	}
	if openID != "" {
		if item, ok := m.byID[openID]; ok {
			view.Open = openID
			view.OpenItem = copyItem(item)
			view.Candidates = append([]Candidate(nil), m.candidates[openID]...)
			view.Attempts = append([]Attempt(nil), m.attempts[openID]...)
		}
	}
	return view
}

// Scan job -------------------------------------------------------------------

// StartScan starts a background library scan.
func (m *Manager) StartScan() error {
	if !m.Enabled() {
		return errors.New("subtitles are disabled")
	}
	if err := m.StateError(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.batch.Running {
		m.mu.Unlock()
		return errors.New("a subtitle job is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.batch = BatchStatus{Running: true, Kind: "scan"}
	m.batchCancel = cancel
	m.mu.Unlock()
	slog.Info("subtitles: scan started")
	go func() {
		defer m.finishJob()
		if err := m.Scan(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("subtitles: scan failed", "err", err)
			m.setBatchError(err.Error())
		}
	}()
	return nil
}

// CancelScan cancels the running job.
func (m *Manager) CancelScan() error {
	m.mu.Lock()
	if !m.batch.Running || m.batchCancel == nil {
		m.mu.Unlock()
		return errors.New("no subtitle job is running")
	}
	cancel := m.batchCancel
	kind := m.batch.Kind
	m.batch.Canceled = true
	m.mu.Unlock()
	slog.Info("subtitles: canceling job", "kind", kind)
	cancel()
	return nil
}

func (m *Manager) finishJob() {
	m.mu.Lock()
	m.batch.Running = false
	m.batch.Stage = ""
	m.batch.Current = ""
	m.batchCancel = nil
	m.mu.Unlock()
}

func (m *Manager) setBatchError(message string) {
	m.mu.Lock()
	m.batch.Error = message
	m.mu.Unlock()
}

// Scan walks every remote movie root, probes new or changed videos and updates
// the durable queue.
func (m *Manager) Scan(ctx context.Context) error {
	if !m.Enabled() {
		return errors.New("subtitles are disabled")
	}
	type videoRef struct {
		driveID    string
		root       string
		folderName string
		folderPath string
		relative   string
	}
	scanStart := time.Now()
	var videos []videoRef
	scannedRoots := map[string]bool{}
	roots := RemoteMovieRoots(m.lib.Drives())
	slog.Info("subtitles: scanning remote movie roots", "roots", len(roots))
	for _, root := range roots {
		entries, err := os.ReadDir(root.Path)
		if err != nil {
			if os.IsNotExist(err) {
				slog.Warn("subtitles: remote root does not exist", "drive", root.DriveID, "root", root.Path)
				continue
			}
			slog.Error("subtitles: cannot read remote root", "drive", root.DriveID, "root", root.Path, "err", err)
			return fmt.Errorf("read remote movie root %q: %w", root.Path, err)
		}
		scannedRoots[root.Path] = true
		rootFolders, rootVideos := 0, 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			folderPath := filepath.Join(root.Path, entry.Name())
			files, err := library.WalkVideoFiles(folderPath)
			if err != nil {
				slog.Warn("subtitles: cannot read movie folder", "folder", folderPath, "err", err)
				continue
			}
			rootFolders++
			rootVideos += len(files)
			for _, relative := range files {
				videos = append(videos, videoRef{
					driveID: root.DriveID, root: root.Path, folderName: entry.Name(),
					folderPath: folderPath, relative: relative,
				})
			}
		}
		slog.Info("subtitles: root scanned", "drive", root.DriveID, "root", root.Path, "folders", rootFolders, "videos", rootVideos)
	}
	m.mu.Lock()
	m.batch.Total = len(videos)
	m.mu.Unlock()
	slog.Info("subtitles: video files found", "videos", len(videos))

	probed := 0
	budgetReached := false
	var updated []*Item
	seen := map[string]bool{}
	now := time.Now()
	for index, video := range videos {
		if ctx.Err() != nil {
			m.mu.Lock()
			m.batch.Canceled = true
			m.mu.Unlock()
			break
		}
		videoPath := filepath.Join(video.folderPath, video.relative)
		info, err := os.Stat(videoPath)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if skipVideoFile(filepath.Base(videoPath), info.Size(), m.cfg.MinVideoBytes, m.cfg.SkipSampleFiles) {
			slog.Debug("subtitles: skipping small or sample video", "video", videoPath, "bytes", info.Size())
			continue
		}
		existing := m.itemByID(subtitleItemID(video.driveID, filepath.Join(video.folderName, video.relative)))
		external, _ := discoverExternalSubtitles(filepath.Dir(videoPath), filepath.Base(videoPath))

		var media MediaInfo
		reuse := existing != nil && existing.VideoSize == info.Size() &&
			existing.VideoMtime == info.ModTime().Unix() && existing.EmbeddedSubStreams != nil
		switch {
		case reuse:
			media = MediaInfo{Streams: existing.EmbeddedSubStreams, DurationMS: existing.DurationMS, Probed: existing.Probed}
		case m.cfg.ScanBatchSize > 0 && probed >= m.cfg.ScanBatchSize:
			// Probe budget for this run is exhausted; keep whatever we already
			// know. Movies that were never probed stay marked "not analyzed" and
			// are probed on demand when they are actually processed.
			budgetReached = true
			if existing != nil {
				media = MediaInfo{Streams: existing.EmbeddedSubStreams, DurationMS: existing.DurationMS, Probed: existing.Probed}
			}
		default:
			probed++
			probedMedia, probeErr := m.prober.Probe(ctx, videoPath)
			if probeErr != nil {
				slog.Warn("subtitles: probe failed", "video", videoPath, "err", probeErr)
				if existing != nil {
					media = MediaInfo{Streams: existing.EmbeddedSubStreams, DurationMS: existing.DurationMS}
				}
			} else {
				slog.Debug("subtitles: probed", "video", videoPath, "streams", len(probedMedia.Streams), "duration_ms", probedMedia.DurationMS)
				media = probedMedia
			}
		}

		item := applyScanResult(existing, video.driveID, video.root, video.folderName, videoPath, info, media, external, m.cfg.TargetLanguage, now)
		if existing == nil || existing.Status != item.Status || existing.HasExternalSubtitle != item.HasExternalSubtitle {
			slog.Info("subtitles: queued movie",
				"id", item.ID,
				"video", item.VideoPath,
				"title", item.Title,
				"year", item.Year,
				"status", item.Status,
				"has_swedish", item.HasSwedish,
				"has_external_subtitle", item.HasExternalSubtitle,
				"external_subtitles", len(item.ExternalSubtitles),
				"embedded_streams", len(item.EmbeddedSubStreams))
		}
		seen[item.ID] = true
		updated = append(updated, item)
		m.mu.Lock()
		m.batch.Done = index + 1
		m.batch.Current = videoPath
		m.mu.Unlock()
	}

	m.mu.Lock()
	var removed []*Item
	for _, item := range m.items {
		if seen[item.ID] {
			continue
		}
		if !scannedRoots[item.RemoteRoot] {
			// The root could not be read; never treat that as a deletion.
			continue
		}
		removed = append(removed, item)
	}
	m.items = updated
	m.byID = map[string]*Item{}
	for _, item := range updated {
		m.byID[item.ID] = item
	}
	m.sortItemsLocked()
	m.mu.Unlock()

	for _, item := range removed {
		_ = m.store.deleteItem(item.ID)
	}
	if err := m.store.saveItems(updated); err != nil {
		slog.Error("subtitles: cannot save the queue", "err", err)
		return err
	}
	queued, notAnalyzed := 0, 0
	for _, item := range updated {
		if !stillNeedsWork(item) {
			continue
		}
		queued++
		if !item.Probed {
			notAnalyzed++
		}
	}
	if budgetReached {
		slog.Warn("subtitles: probe budget reached, some movies are not analyzed yet",
			"scan_batch_size", m.cfg.ScanBatchSize, "not_analyzed", notAnalyzed)
	}
	slog.Info("subtitles: scan finished",
		"videos", len(videos),
		"items", len(updated),
		"needs_subtitles", queued,
		"not_analyzed", notAnalyzed,
		"probed", probed,
		"removed", len(removed),
		"duration_ms", time.Since(scanStart).Milliseconds())
	return nil
}

// Run jobs -------------------------------------------------------------------

// StartRun starts the next batch of pending items.
func (m *Manager) StartRun() error {
	if !m.Enabled() {
		return errors.New("subtitles are disabled")
	}
	if err := m.StateError(); err != nil {
		return err
	}
	m.mu.Lock()
	if m.batch.Running {
		m.mu.Unlock()
		return errors.New("a subtitle job is already running")
	}
	batchSize := m.cfg.RunBatchSize
	if batchSize <= 0 {
		batchSize = 20
	}
	ids := make([]string, 0, batchSize)
	for _, item := range m.items {
		if item.Status != StatusPending || item.HasSwedish {
			continue
		}
		ids = append(ids, item.ID)
		if len(ids) >= batchSize {
			break
		}
	}
	if len(ids) == 0 {
		m.mu.Unlock()
		slog.Info("subtitles: nothing to process", "reason", "no movies need Swedish subtitles")
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.batch = BatchStatus{Running: true, Kind: "run", Total: len(ids)}
	m.batchCancel = cancel
	m.mu.Unlock()
	slog.Info("subtitles: batch started", "movies", len(ids), "batch_size", batchSize)
	go func() {
		defer m.finishJob()
		m.runBatch(ctx, ids, runOptions{refreshQuota: true})
	}()
	return nil
}

// CancelRun cancels the running batch.
func (m *Manager) CancelRun() error { return m.CancelScan() }

func (m *Manager) runBatch(ctx context.Context, ids []string, opts runOptions) {
	runID, _ := m.store.startRun("run", len(ids))
	done := 0
	var runErr string
	added, failed := 0, 0
	defer func() {
		if runID != 0 {
			_ = m.store.finishRun(runID, done, runErr)
		}
		slog.Info("subtitles: batch finished", "processed", done, "total", len(ids), "added", added, "failed", failed, "error", runErr)
	}()
	for index, id := range ids {
		if ctx.Err() != nil {
			m.mu.Lock()
			m.batch.Canceled = true
			m.mu.Unlock()
			runErr = "canceled"
			return
		}
		item := m.itemByID(id)
		if item == nil {
			slog.Warn("subtitles: queued movie disappeared", "id", id)
			continue
		}
		status, err := m.processAndPersist(ctx, item, opts)
		if status == StatusAdded || status == StatusAddedReview {
			added++
		}
		if err != nil {
			failed++
		}
		done = index + 1
		m.mu.Lock()
		m.batch.Done = done
		m.batch.Current = item.VideoPath
		m.mu.Unlock()
		if err == nil {
			continue
		}
		if errors.Is(err, errQuotaStop) {
			slog.Warn("subtitles: stopping batch at the download quota reserve", "reserve", m.cfg.QuotaReserve)
			m.setBatchError("OpenSubtitles download quota reserve reached; run again later")
			runErr = "quota reserve reached"
			return
		}
		if opensubtitles.IsHardStop(err) {
			slog.Warn("subtitles: stopping batch after a hard OpenSubtitles error", "err", err)
			m.setBatchError(err.Error())
			runErr = err.Error()
			return
		}
		if errors.Is(err, context.Canceled) {
			m.mu.Lock()
			m.batch.Canceled = true
			m.mu.Unlock()
			runErr = "canceled"
			return
		}
		// Per-item errors are isolated and already persisted on the item.
	}
}

// RunOne processes a single item immediately. It is used by the per-item action
// and by the retry action.
func (m *Manager) RunOne(id string, refresh bool) error {
	if !m.Enabled() {
		return errors.New("subtitles are disabled")
	}
	if err := m.StateError(); err != nil {
		return err
	}
	item := m.itemByID(id)
	if item == nil {
		return errors.New("subtitle item was not found; scan again")
	}
	m.mu.Lock()
	if m.batch.Running {
		m.mu.Unlock()
		return errors.New("a subtitle job is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.batch = BatchStatus{Running: true, Kind: "run", Total: 1}
	m.batchCancel = cancel
	m.mu.Unlock()
	slog.Info("subtitles: single movie started", "id", id, "video", item.VideoPath, "refresh", refresh)
	go func() {
		defer m.finishJob()
		opts := runOptions{refreshQuota: true, refreshSearch: refresh}
		if refresh {
			opts.maxCandidates = retryMaxCandidates
		}
		_, err := m.processAndPersist(ctx, item, opts)
		switch {
		case err == nil:
		case errors.Is(err, errQuotaStop):
			m.setBatchError("OpenSubtitles download quota reserve reached; run again later")
		case opensubtitles.IsHardStop(err):
			m.setBatchError(err.Error())
		case errors.Is(err, context.Canceled):
			m.mu.Lock()
			m.batch.Canceled = true
			m.mu.Unlock()
		default:
			m.setBatchError(err.Error())
		}
	}()
	return nil
}

// processAndPersist runs the pipeline and writes the resulting status back to
// the store.
func (m *Manager) processAndPersist(ctx context.Context, item *Item, opts runOptions) (string, error) {
	// The pipeline runs on a private copy so a long ffmpeg/alass run never
	// mutates an item another request may be reading. The copy is published back
	// under the lock once, together with its final status.
	m.mu.RLock()
	working := *item
	working.EmbeddedSubStreams = append([]EmbeddedSubtitle(nil), item.EmbeddedSubStreams...)
	opts.previousStatus = item.Status
	m.mu.RUnlock()

	started := time.Now()
	slog.Info("subtitles: processing movie",
		"id", working.ID,
		"video", working.VideoPath,
		"title", working.Title,
		"year", working.Year,
		"previous_status", opts.previousStatus)

	working.Status = StatusProcessing
	status, err := m.processItem(ctx, &working, opts)
	working.Status = status
	working.Error = errorText(err)
	working.Step = ""
	working.UpdatedAt = time.Now()

	attrs := []any{
		"id", working.ID,
		"video", working.VideoPath,
		"status", status,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	if working.ReferenceKind != "" {
		attrs = append(attrs, "reference", working.ReferenceKind+"/"+working.ReferenceLang)
	}
	if working.OutputPath != "" {
		attrs = append(attrs, "output", working.OutputPath)
	}
	if working.Metrics != nil {
		attrs = append(attrs, "within_2s", fmt.Sprintf("%.0f%%", working.Metrics.Within2*100), "p90_s", fmt.Sprintf("%.2f", working.Metrics.P90))
	}
	if err != nil {
		slog.Warn("subtitles: movie finished", append(attrs, "err", err)...)
	} else {
		slog.Info("subtitles: movie finished", attrs...)
	}

	m.mu.Lock()
	*item = working
	m.mu.Unlock()

	if saveErr := m.store.saveItem(&working); saveErr != nil {
		return status, saveErr
	}
	return status, err
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, errQuotaStop) {
		return ""
	}
	return err.Error()
}

// Item actions ---------------------------------------------------------------

// Skip marks an item as skipped.
func (m *Manager) Skip(id string) error {
	item := m.itemByID(id)
	if item == nil {
		return errors.New("subtitle item was not found")
	}
	m.setItemStatus(item, StatusSkipped)
	m.setItemError(item, "")
	slog.Info("subtitles: movie skipped", "id", id, "video", item.VideoPath)
	return m.store.saveItem(item)
}

// Reset clears an item's workflow state so it can be processed again.
func (m *Manager) Reset(id string) error {
	item := m.itemByID(id)
	if item == nil {
		return errors.New("subtitle item was not found")
	}
	if err := m.store.clearItemWork(id); err != nil {
		return err
	}
	m.mu.Lock()
	m.attempts[id] = nil
	m.searches[id] = map[string]searchRecord{}
	m.candidates[id] = nil
	m.featureCanon[id] = ""
	m.mu.Unlock()
	m.setItemStatus(item, StatusPending)
	m.setItemError(item, "")
	m.setItemMetrics(item, nil)
	m.setItemOutput(item, "", 0)
	slog.Info("subtitles: movie reset", "id", id, "video", item.VideoPath)
	return m.store.saveItem(item)
}

func (m *Manager) setItemStatus(item *Item, status string) {
	m.mu.Lock()
	item.Status = status
	item.UpdatedAt = time.Now()
	m.mu.Unlock()
}

func (m *Manager) setItemError(item *Item, message string) {
	m.mu.Lock()
	item.Error = message
	m.mu.Unlock()
}

func (m *Manager) setItemMetrics(item *Item, metrics *Metrics) {
	m.mu.Lock()
	item.Metrics = metrics
	m.mu.Unlock()
}

func (m *Manager) setItemOutput(item *Item, path string, size int64) {
	m.mu.Lock()
	item.OutputPath = path
	item.OutputBytes = size
	m.mu.Unlock()
}

// ClearWork deletes the per-item scratch directories and returns how many were
// removed.
func (m *Manager) ClearWork() (int, error) {
	entries, err := os.ReadDir(m.cfg.WorkDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		path := filepath.Join(m.cfg.WorkDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return removed, err
		}
		removed++
	}
	slog.Info("subtitles: work files cleared", "work_dir", m.cfg.WorkDir, "removed", removed)
	return removed, nil
}

// Quota ----------------------------------------------------------------------

func (m *Manager) refreshQuota(ctx context.Context) {
	if m.osClient == nil || !m.osClient.Configured() {
		return
	}
	info, err := m.osClient.UserInfo(ctx)
	if err != nil {
		m.mu.Lock()
		m.quota.Error = err.Error()
		m.mu.Unlock()
		return
	}
	remaining, allowed, known := opensubtitles.RemainingFromUserInfo(info)
	m.mu.Lock()
	m.quota = QuotaView{Known: known, Remaining: remaining, Allowed: allowed, ResetAt: info.ResetTimeUtc}
	m.mu.Unlock()
	slog.Info("subtitles: OpenSubtitles quota", "remaining", remaining, "allowed", allowed, "reset_at", info.ResetTimeUtc, "known", known)
}

func (m *Manager) updateQuotaFromDownload(response opensubtitles.DownloadResponse) {
	known := false
	remaining := 0
	if value := response.Remaining.Int(); value > 0 || response.Remaining == 0 {
		known = true
		remaining = value
	}
	m.mu.Lock()
	if known {
		m.quota.Known = true
		m.quota.Remaining = remaining
	}
	if response.ResetTime != "" {
		m.quota.ResetAt = response.ResetTime
	}
	m.mu.Unlock()
}

func (m *Manager) quotaRemaining() (int, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.quota.Known {
		return 0, false
	}
	return m.quota.Remaining, true
}

// Search/candidate state helpers --------------------------------------------

func (m *Manager) searchFor(itemID, kind string) (searchRecord, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	byKind, ok := m.searches[itemID]
	if !ok {
		return searchRecord{}, false
	}
	record, ok := byKind[kind]
	return record, ok
}

func (m *Manager) recordSearch(itemID, kind, query, status string, count int, message string) {
	record := searchRecord{ItemID: itemID, Kind: kind, Query: query, Status: status, ResultCount: count, SearchedAt: time.Now(), Error: message}
	m.mu.Lock()
	if m.searches[itemID] == nil {
		m.searches[itemID] = map[string]searchRecord{}
	}
	m.searches[itemID][kind] = record
	m.mu.Unlock()
	_ = m.store.saveSearch(record)
}

func (m *Manager) candidatesFor(itemID string) []Candidate {
	m.mu.RLock()
	if candidates, ok := m.candidates[itemID]; ok {
		m.mu.RUnlock()
		return candidates
	}
	m.mu.RUnlock()
	candidates, err := m.store.loadCandidates(itemID)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	m.candidates[itemID] = candidates
	m.mu.Unlock()
	return candidates
}

func (m *Manager) storeCandidates(itemID string, candidates []Candidate) {
	m.mu.Lock()
	m.candidates[itemID] = candidates
	m.mu.Unlock()
	_ = m.store.replaceCandidates(itemID, candidates)
}

func (m *Manager) recordFeatureCanonical(itemID string, raw json.RawMessage) {
	var feature opensubtitles.Feature
	if err := json.Unmarshal(raw, &feature); err != nil {
		return
	}
	canonical := strings.TrimSpace(feature.Attributes.Title)
	if canonical == "" {
		canonical = strings.TrimSpace(feature.Attributes.OriginalTitle)
	}
	m.mu.Lock()
	m.featureCanon[itemID] = canonical
	m.mu.Unlock()
}

func (m *Manager) featureCanonical(itemID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.featureCanon[itemID]
}

// Item access ----------------------------------------------------------------

func (m *Manager) itemByID(id string) *Item {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byID[id]
}

func (m *Manager) sortItemsLocked() {
	sort.SliceStable(m.items, func(i, j int) bool {
		if m.items[i].DriveID != m.items[j].DriveID {
			return m.items[i].DriveID < m.items[j].DriveID
		}
		if m.items[i].FolderName != m.items[j].FolderName {
			return m.items[i].FolderName < m.items[j].FolderName
		}
		return m.items[i].VideoName < m.items[j].VideoName
	})
}

// pruneWorkDirs removes per-item scratch directories older than the retention
// window so the mounted /tmp never grows without bound.
func (m *Manager) pruneWorkDirs() int {
	days := m.cfg.WorkRetentionDays
	if days <= 0 {
		return 0
	}
	entries, err := os.ReadDir(m.cfg.WorkDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().AddDate(0, 0, -days)
	removed := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.IsDir() {
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(m.cfg.WorkDir, entry.Name())); err == nil {
			removed++
		}
	}
	return removed
}

// UpdateSettings applies and persists a settings patch.
func (m *Manager) UpdateSettings(patch SettingsView) error {
	if !m.Enabled() {
		return errors.New("subtitles are disabled")
	}
	if patch.RunBatchSize < 1 || patch.RunBatchSize > 1000 {
		return errors.New("run_batch_size must be between 1 and 1000")
	}
	if patch.RequestIntervalMS < 0 || patch.RequestIntervalMS > 60000 {
		return errors.New("request_interval_ms must be between 0 and 60000")
	}
	if patch.MaxCandidates < 1 || patch.MaxCandidates > 20 {
		return errors.New("max_candidates must be between 1 and 20")
	}
	if patch.QuotaReserve < 0 {
		return errors.New("quota_reserve must not be negative")
	}
	if patch.MinReferenceCues < 1 {
		return errors.New("min_reference_cues must be at least 1")
	}
	if patch.AlassSplitPenalty < 0 || patch.AlassSplitPenalty > 1000 {
		return errors.New("alass_split_penalty must be between 0 and 1000")
	}
	if patch.WorkRetentionDays < 0 {
		return errors.New("work_retention_days must not be negative")
	}
	values := map[string]string{
		settingRunBatchSize:           fmt.Sprint(patch.RunBatchSize),
		settingRequestIntervalMS:      fmt.Sprint(patch.RequestIntervalMS),
		settingMaxCandidates:          fmt.Sprint(patch.MaxCandidates),
		settingQuotaReserve:           fmt.Sprint(patch.QuotaReserve),
		settingUseMoviehash:           fmt.Sprint(patch.UseMoviehash),
		settingRemovePromoCues:        fmt.Sprint(patch.RemovePromoCues),
		settingAllowUnalignedFallback: fmt.Sprint(patch.AllowUnalignedFallback),
		settingMinReferenceCues:       fmt.Sprint(patch.MinReferenceCues),
		settingAlassNoSplit:           fmt.Sprint(patch.AlassNoSplit),
		settingAlassSplitPenalty:      fmt.Sprint(patch.AlassSplitPenalty),
		settingOverwriteExisting:      fmt.Sprint(patch.OverwriteExisting),
		settingWorkRetentionDays:      fmt.Sprint(patch.WorkRetentionDays),
	}
	for key, value := range values {
		if err := m.store.saveSetting(key, value); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.applySettings(values)
	m.mu.Unlock()
	slog.Info("subtitles: settings updated",
		"run_batch_size", patch.RunBatchSize,
		"max_candidates", patch.MaxCandidates,
		"request_interval_ms", patch.RequestIntervalMS,
		"quota_reserve", patch.QuotaReserve,
		"remove_promo_cues", patch.RemovePromoCues,
		"allow_unaligned_fallback", patch.AllowUnalignedFallback)
	return nil
}

// stillNeedsWork reports whether an item is still waiting for a Swedish
// subtitle, which is what the external-reference statistics count.
func stillNeedsWork(item *Item) bool {
	if item.HasSwedish {
		return false
	}
	switch item.Status {
	case StatusSkipped, StatusAdded, StatusAddedReview, StatusHasSwedish:
		return false
	default:
		return true
	}
}

// copyItem returns a snapshot of an item so JSON encoding never reads fields
// while the pipeline publishes an update.
func copyItem(item *Item) *Item {
	if item == nil {
		return nil
	}
	copied := *item
	copied.ExistingSubLanguages = append([]string(nil), item.ExistingSubLanguages...)
	copied.ExternalSubtitles = append([]ExternalSubtitleRef(nil), item.ExternalSubtitles...)
	copied.EmbeddedSubStreams = append([]EmbeddedSubtitle(nil), item.EmbeddedSubStreams...)
	copied.SwedishSources = append([]string(nil), item.SwedishSources...)
	if item.Metrics != nil {
		metrics := *item.Metrics
		metrics.AlassShifts = append([]string(nil), item.Metrics.AlassShifts...)
		metrics.CoarseIssues = append([]string(nil), item.Metrics.CoarseIssues...)
		copied.Metrics = &metrics
	}
	return &copied
}

// ensureWorkDir creates the work directory and verifies that it is writable, so
// a bad /tmp mount is reported once at startup instead of failing every movie.
func ensureWorkDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("work directory is not configured")
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	probe, err := os.CreateTemp(path, ".write-test-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}
