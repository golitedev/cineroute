// Package companion manages the small, manual-review 1080p companion queue.
// It owns library reconciliation, Prowlarr searches and durable workflow
// state, but deliberately does not own qBittorrent submission.
package companion

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"cineroute/internal/config"
	"cineroute/internal/library"
	"cineroute/internal/prowlarr"
)

type searchState struct {
	Candidates []Candidate
	SearchedAt time.Time
}

type companionKind string

const (
	companionMovie companionKind = "movie"
	companionTV    companionKind = "tv"
)

type View struct {
	MediaType                string      `json:"media_type"`
	Enabled                  bool        `json:"enabled"`
	IndexerName              string      `json:"indexer_name"`
	ProwlarrConfigured       bool        `json:"prowlarr_configured"`
	SearchIntervalSeconds    int         `json:"search_interval_seconds"`
	SearchIntervalMinSeconds int         `json:"search_interval_min_seconds"`
	SearchIntervalMaxSeconds int         `json:"search_interval_max_seconds"`
	SearchBatchSize          int         `json:"search_batch_size"`
	SearchBatchMinSize       int         `json:"search_batch_min_size"`
	SearchBatchMaxSize       int         `json:"search_batch_max_size"`
	Movies                   []*Movie    `json:"movies"`
	Open                     *Movie      `json:"open,omitempty"`
	Candidates               []Candidate `json:"candidates,omitempty"`
	SearchedAt               time.Time   `json:"searched_at,omitempty"`
	Batch                    BatchStatus `json:"batch"`
	StateError               string      `json:"state_error,omitempty"`
}

type Manager struct {
	cfg      *config.Config
	lib      *library.Scan
	prowlarr *prowlarr.Client
	store    *stateStore
	kind     companionKind

	mu                    sync.RWMutex
	state                 stateFile
	stateErr              error
	searches              map[string]searchState
	tvApprovals           map[string]map[string]bool
	indexerID             int
	indexerName           string
	batch                 BatchStatus
	searchIntervalSeconds int
	searchBatchSize       int
	batchCancel           context.CancelFunc

	searchMu   sync.Mutex
	lastSearch time.Time
}

func NewManager(cfg *config.Config, lib *library.Scan, prowlarrClient *prowlarr.Client) *Manager {
	statePath := ""
	if cfg != nil {
		statePath = cfg.Companion.StatePath
	}
	return newManager(cfg, lib, prowlarrClient, companionMovie, statePath)
}

func NewTVManager(cfg *config.Config, lib *library.Scan, prowlarrClient *prowlarr.Client) *Manager {
	statePath := ""
	if cfg != nil {
		statePath = tvCompanionStatePath(cfg.Companion.StatePath)
	}
	return newManager(cfg, lib, prowlarrClient, companionTV, statePath)
}

func newManager(cfg *config.Config, lib *library.Scan, prowlarrClient *prowlarr.Client, kind companionKind, statePath string) *Manager {
	m := &Manager{
		cfg:         cfg,
		lib:         lib,
		prowlarr:    prowlarrClient,
		kind:        kind,
		searches:    map[string]searchState{},
		tvApprovals: map[string]map[string]bool{},
		state:       stateFile{Version: stateVersion},
	}
	if cfg == nil {
		m.stateErr = errors.New("companion configuration is missing")
		return m
	}
	m.searchIntervalSeconds = cfg.Companion.SearchIntervalSeconds
	if m.searchIntervalSeconds < config.MinCompanionSearchIntervalSeconds || m.searchIntervalSeconds > config.MaxCompanionSearchIntervalSeconds {
		m.searchIntervalSeconds = config.DefaultCompanionSearchIntervalSeconds
	}
	m.searchBatchSize = config.DefaultCompanionSearchBatchSize
	store, st, searches, err := openStateStore(statePath)
	if err != nil {
		m.stateErr = err
	} else {
		m.store = store
		m.state = st
		m.searches = searches
		changed := normalizeLoadedState(&m.state, m.searches)
		if m.state.SearchIntervalSeconds != 0 {
			if m.state.SearchIntervalSeconds >= config.MinCompanionSearchIntervalSeconds && m.state.SearchIntervalSeconds <= config.MaxCompanionSearchIntervalSeconds {
				m.searchIntervalSeconds = m.state.SearchIntervalSeconds
			} else {
				m.state.SearchIntervalSeconds = m.searchIntervalSeconds
				changed = true
			}
		}
		if m.state.SearchBatchSize != 0 {
			if m.state.SearchBatchSize >= config.MinCompanionSearchBatchSize && m.state.SearchBatchSize <= config.MaxCompanionSearchBatchSize {
				m.searchBatchSize = m.state.SearchBatchSize
			} else {
				m.state.SearchBatchSize = m.searchBatchSize
				changed = true
			}
		}
		if m.state.SearchIntervalSeconds != m.searchIntervalSeconds {
			m.state.SearchIntervalSeconds = m.searchIntervalSeconds
			changed = true
		}
		if m.state.SearchBatchSize != m.searchBatchSize {
			m.state.SearchBatchSize = m.searchBatchSize
			changed = true
		}
		if changed {
			if err := m.store.save(m.state, m.searches, nil); err != nil {
				m.stateErr = fmt.Errorf("recover companion state: %w", err)
			}
		}
	}
	return m
}

func (m *Manager) MediaType() string {
	if m != nil && m.kind == companionTV {
		return string(companionTV)
	}
	return string(companionMovie)
}

func (m *Manager) itemLabel() string {
	if m.MediaType() == string(companionTV) {
		return "TV show"
	}
	return "movie"
}

func (m *Manager) remotePath(driveID, folderName string) (string, bool) {
	if m == nil || m.lib == nil {
		return "", false
	}
	if m.kind == companionTV {
		return m.lib.TVRemotePath(driveID, folderName)
	}
	return m.lib.MovieRemotePath(driveID, folderName)
}

func (m *Manager) libraryFolders() ([]library.MovieFolder, error) {
	if m.kind == companionTV {
		return m.lib.TVShows()
	}
	return m.lib.Movies()
}

func (m *Manager) parseFolder(folder library.MovieFolder) (string, int, error) {
	if m.kind == companionTV {
		return parseTVFolder(folder)
	}
	return parseMovieFolder(folder)
}

func (m *Manager) inspectFolder(item *Movie, path, remotePath, folderName string) (copyInspection, copyInspection) {
	if m.kind == companionTV {
		return updateTVInspection(item, path, remotePath, folderName)
	}
	return updateMovieInspection(item, path, remotePath, folderName)
}

func (m *Manager) Enabled() bool {
	return m != nil && m.cfg != nil && m.cfg.Companion.Enabled
}

func (m *Manager) StateError() error {
	if m == nil {
		return errors.New("companion manager is unavailable")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateErr
}

func (m *Manager) View(openID string) View {
	view := View{Enabled: m.Enabled(), MediaType: m.MediaType()}
	if m == nil || m.cfg == nil {
		view.StateError = "companion manager is unavailable"
		return view
	}
	view.IndexerName = m.cfg.Prowlarr.IndexerName
	if view.IndexerName == "" {
		view.IndexerName = "LAT-Team"
	}
	view.ProwlarrConfigured = m.Enabled() && m.prowlarr != nil && m.prowlarr.Configured()
	view.SearchIntervalMinSeconds = config.MinCompanionSearchIntervalSeconds
	view.SearchIntervalMaxSeconds = config.MaxCompanionSearchIntervalSeconds
	view.SearchBatchMinSize = config.MinCompanionSearchBatchSize
	view.SearchBatchMaxSize = config.MaxCompanionSearchBatchSize
	m.mu.RLock()
	defer m.mu.RUnlock()
	view.SearchIntervalSeconds = m.searchIntervalSeconds
	view.SearchBatchSize = m.searchBatchSize
	view.Movies = cloneMovies(m.state.Movies)
	view.Batch = m.batch
	if m.stateErr != nil {
		view.StateError = m.stateErr.Error()
	}
	if openID != "" {
		for _, movie := range m.state.Movies {
			if movie.ID != openID {
				continue
			}
			view.Open = cloneMovie(movie)
			if view.Open != nil && !view.Open.Missing && view.Open.Path != "" {
				remotePath := view.Open.RemotePath
				if configuredPath, ok := m.remotePath(view.Open.DriveID, view.Open.FolderName); ok {
					remotePath = configuredPath
				}
				m.inspectFolder(view.Open, view.Open.Path, remotePath, view.Open.FolderName)
			}
			if result, ok := m.searches[openID]; ok {
				view.Candidates = cloneCandidates(result.Candidates)
				if m.kind == companionTV {
					view.Candidates = filterTVEpisodeCandidates(view.Candidates)
					MarkTVPackCandidates(view.Candidates)
					sortTVPackCandidates(view.Candidates)
					m.markTVCandidateStatusesLocked(view.Open, view.Candidates)
				}
				view.SearchedAt = result.SearchedAt
			}
			break
		}
	}
	return view
}

func (m *Manager) markTVCandidateStatusesLocked(movie *Movie, candidates []Candidate) {
	if movie == nil || m.kind != companionTV {
		return
	}
	for i := range candidates {
		candidates[i].TVPackStatus = ""
		if !candidates[i].TVPackEligible {
			continue
		}
		key := candidateTVPackKey(candidates[i])
		if key == "" {
			continue
		}
		if tvPackApproved(movie, key) {
			candidates[i].TVPackStatus = "added"
		} else if m.tvApprovalPendingLocked(movie.ID, key) {
			candidates[i].TVPackStatus = "submitting"
		}
	}
}

func candidateTVPackKey(candidate Candidate) string {
	if candidate.TVPackKey != "" {
		return candidate.TVPackKey
	}
	return TVPackKey(candidate.Title)
}

func tvPackApproved(movie *Movie, key string) bool {
	if movie == nil || key == "" {
		return false
	}
	for _, approved := range movie.TVApprovedPacks {
		if approved == "series" || approved == key {
			return true
		}
	}
	return false
}

func (m *Manager) tvApprovalPendingLocked(movieID, key string) bool {
	if m.tvApprovals == nil || key == "" {
		return false
	}
	return m.tvApprovals[movieID][key]
}

func (m *Manager) setTVApprovalPendingLocked(movieID, key string) {
	if m.tvApprovals == nil {
		m.tvApprovals = map[string]map[string]bool{}
	}
	if m.tvApprovals[movieID] == nil {
		m.tvApprovals[movieID] = map[string]bool{}
	}
	m.tvApprovals[movieID][key] = true
}

func (m *Manager) clearTVApprovalPendingLocked(movieID, key string) {
	if m.tvApprovals == nil {
		return
	}
	pending := m.tvApprovals[movieID]
	delete(pending, key)
	if len(pending) == 0 {
		delete(m.tvApprovals, movieID)
	}
}

func (m *Manager) hasTVApprovalsLocked(movieID string) bool {
	return len(m.tvApprovals[movieID]) > 0
}

func (m *Manager) SetSearchIntervalSeconds(seconds int) error {
	return m.SetSearchSettings(&seconds, nil)
}

func (m *Manager) SetSearchBatchSize(size int) error {
	return m.SetSearchSettings(nil, &size)
}

func (m *Manager) SetSearchSettings(intervalSeconds, batchSize *int) error {
	if !m.Enabled() {
		return errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return m.stateErr
	}
	newInterval := m.searchIntervalSeconds
	if newInterval == 0 {
		newInterval = config.DefaultCompanionSearchIntervalSeconds
	}
	if intervalSeconds != nil {
		newInterval = *intervalSeconds
	}
	if newInterval < config.MinCompanionSearchIntervalSeconds || newInterval > config.MaxCompanionSearchIntervalSeconds {
		return fmt.Errorf("search interval must be between %d and %d seconds", config.MinCompanionSearchIntervalSeconds, config.MaxCompanionSearchIntervalSeconds)
	}
	newBatchSize := m.searchBatchSize
	if newBatchSize == 0 {
		newBatchSize = config.DefaultCompanionSearchBatchSize
	}
	if batchSize != nil {
		newBatchSize = *batchSize
	}
	if newBatchSize < config.MinCompanionSearchBatchSize || newBatchSize > config.MaxCompanionSearchBatchSize {
		label := "movies"
		if m.kind == companionTV {
			label = "TV shows"
		}
		return fmt.Errorf("search batch size must be between %d and %d %s", config.MinCompanionSearchBatchSize, config.MaxCompanionSearchBatchSize, label)
	}
	previousInterval := m.searchIntervalSeconds
	previousBatchSize := m.searchBatchSize
	previousState := m.state.SearchIntervalSeconds
	previousBatchState := m.state.SearchBatchSize
	m.searchIntervalSeconds = newInterval
	m.searchBatchSize = newBatchSize
	m.state.SearchIntervalSeconds = newInterval
	m.state.SearchBatchSize = newBatchSize
	if err := m.persistLocked(); err != nil {
		m.searchIntervalSeconds = previousInterval
		m.searchBatchSize = previousBatchSize
		m.state.SearchIntervalSeconds = previousState
		m.state.SearchBatchSize = previousBatchState
		return err
	}
	return nil
}

// ProwlarrStatus verifies the configured indexer for the status bar. It
// returns the configured indexer name on success and a safe human-readable
// state otherwise; it never includes the API key or a proxy download URL.
func (m *Manager) ProwlarrStatus(ctx context.Context) (string, string) {
	if m == nil || m.cfg == nil || !m.cfg.Companion.Enabled {
		return "disabled", ""
	}
	name := m.cfg.Prowlarr.IndexerName
	if name == "" {
		name = "LAT-Team"
	}
	if m.prowlarr == nil || !m.prowlarr.Configured() {
		return "not configured", name
	}
	statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := m.resolveIndexer(statusCtx); err != nil {
		return "unavailable: " + err.Error(), name
	}
	return name, name
}

// Scan reconciles the immediate children of the configured primary library
// roots into durable state. TV scans use only TV roots, never TV remote roots.
// Live searching, review and submitting states are preserved; startup recovery
// is handled separately when the state file is loaded. It never renames or
// moves media files.
func (m *Manager) Scan(ctx context.Context) error {
	if !m.Enabled() {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return m.stateErr
	}
	if m.lib == nil {
		return errors.New("library scanner is unavailable")
	}

	previous := make(map[string]*Movie, len(m.state.Movies))
	for _, movie := range m.state.Movies {
		previous[movie.ID] = movie
	}
	folders, err := m.libraryFolders()
	if err != nil {
		return fmt.Errorf("%s companion scan aborted: %w", m.itemLabel(), err)
	}
	seen := make(map[string]bool, len(folders))
	current := make([]*Movie, 0, len(folders)+len(previous))
	for _, folder := range folders {
		id := movieID(folder.DriveID, folder.Name)
		seen[id] = true
		movie := cloneMovie(previous[id])
		if movie == nil {
			movie = &Movie{ID: id, CreatedAt: time.Now()}
		}
		movie.ID = id
		movie.DriveID = folder.DriveID
		movie.Path = folder.Path
		movie.FolderName = folder.Name
		movie.Missing = false
		live := isLiveWorkflowStatus(movie.Status)
		if movie.UpdatedAt.IsZero() {
			movie.UpdatedAt = time.Now()
		}

		title, year, parseErr := m.parseFolder(folder)
		remotePath, _ := m.remotePath(folder.DriveID, folder.Name)
		mainInspection, remoteInspection := m.inspectFolder(movie, folder.Path, remotePath, folder.Name)
		if parseErr != nil {
			if !live {
				movie.Status = StatusNeedsReview
				movie.Error = parseErr.Error()
			}
			movie.UpdatedAt = time.Now()
			current = append(current, movie)
			continue
		}
		movie.Title = title
		movie.Year = year
		if movie.Status == StatusComplete || (m.kind == companionTV && movie.Status == StatusSkipped) {
			current = append(current, movie)
			continue
		}
		if live {
			movie.UpdatedAt = time.Now()
			current = append(current, movie)
			continue
		}
		if legacyTMDBError(movie.Error) {
			movie.Status = StatusPending
			movie.Error = ""
		}
		inspectionErr := movieInspectionError(mainInspection, remoteInspection)
		// Both companion workflows are explicit language/backfill review queues:
		// every parsed item stays searchable, even when it already has a 1080p
		// copy, has a remote copy, or has an inspection warning.
		movie.Status = StatusPending
		movie.Error = inspectionErr
		movie.UpdatedAt = time.Now()
		current = append(current, movie)
	}
	for _, movie := range previous {
		if seen[movie.ID] {
			continue
		}
		missing := cloneMovie(movie)
		missing.Missing = true
		live := isLiveWorkflowStatus(missing.Status)
		if !live && missing.Status != StatusComplete && missing.Status != StatusSkipped {
			missing.Status = StatusError
			missing.Error = m.itemLabel() + " folder is no longer present in the configured library roots"
		}
		missing.UpdatedAt = time.Now()
		current = append(current, missing)
		if !live {
			delete(m.searches, missing.ID)
		}
	}
	sort.SliceStable(current, func(i, j int) bool {
		if current[i].DriveID != current[j].DriveID {
			return current[i].DriveID < current[j].DriveID
		}
		return current[i].FolderName < current[j].FolderName
	})
	m.state.Movies = current
	return m.persistLocked()
}

func (m *Manager) SearchOne(ctx context.Context, id string) ([]Candidate, error) {
	if !m.Enabled() {
		return nil, errors.New("1080p companions are disabled")
	}
	movie, err := m.beginSearch(id)
	if err != nil {
		return nil, err
	}
	candidates, err := m.searchMovie(ctx, &movie)
	if err != nil {
		m.finishSearchFailure(id, movie, err)
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		m.finishSearchFailure(id, movie, err)
		return nil, err
	}
	m.mu.Lock()
	current := m.movieLocked(id)
	if current == nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("companion %s not found", m.itemLabel())
	}
	if current.Status != StatusSearching {
		m.mu.Unlock()
		return nil, errors.New("companion search is no longer active")
	}
	if current.Missing {
		current.Status = StatusError
		current.Error = m.itemLabel() + " folder is no longer present in the configured library roots"
		current.UpdatedAt = time.Now()
		persistErr := m.persistLocked()
		m.mu.Unlock()
		if persistErr != nil {
			return nil, persistErr
		}
		return nil, errors.New(m.itemLabel() + " folder is no longer present in the configured library roots")
	}
	m.searches[id] = searchState{Candidates: cloneCandidates(candidates), SearchedAt: time.Now()}
	if len(candidates) == 0 {
		current.Status = StatusNoMatch
		current.Error = "Prowlarr returned no releases"
	} else {
		current.Status = StatusReview
		current.Error = ""
	}
	current.UpdatedAt = time.Now()
	err = m.persistLocked(searchHistoryRecord{
		MovieID:        id,
		Query:          m.companionSearchHistoryQuery(&movie),
		SearchedAt:     time.Now(),
		Status:         current.Status,
		CandidateCount: len(candidates),
	})
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return cloneCandidates(candidates), nil
}

func (m *Manager) finishSearchFailure(id string, movie Movie, searchErr error) {
	if searchErr == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.movieLocked(id)
	if current == nil || current.Status != StatusSearching {
		return
	}
	status := "error"
	if errors.Is(searchErr, context.Canceled) {
		current.Status = StatusPending
		current.Error = ""
		status = "canceled"
	} else {
		current.Status = StatusError
		current.Error = searchErr.Error()
	}
	current.UpdatedAt = time.Now()
	_ = m.persistLocked(searchHistoryRecord{
		MovieID:    id,
		Query:      m.companionSearchHistoryQuery(&movie),
		SearchedAt: time.Now(),
		Status:     status,
		Error:      searchErr.Error(),
	})
}

func (m *Manager) searchMovie(ctx context.Context, movie *Movie) ([]Candidate, error) {
	indexerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	indexerID, err := m.resolveIndexer(indexerCtx)
	if err != nil {
		return nil, err
	}
	policy := m.policy(indexerID)
	queries := m.companionSearchQueries(movie)
	var results []prowlarr.Release
	for _, searchQuery := range queries {
		found, err := m.searchRelease(ctx, indexerID, searchQuery)
		if err != nil {
			m.clearIndexer()
			return nil, err
		}
		results = mergeReleases(results, found)
	}
	if m.kind == companionTV {
		// Filter before FilterAndRank's result cap so individual episodes do
		// not crowd season and series packs out of the review list.
		results = filterTVEpisodeReleases(results)
	}
	candidates := FilterAndRank(results, movie.Title, movie.Year, movie.TmdbID, policy)
	if m.kind == companionTV {
		MarkTVPackCandidates(candidates)
		sortTVPackCandidates(candidates)
	}
	return candidates, nil
}

func companionSearchQueries(movie *Movie) []string {
	title := strings.TrimSpace(movie.Title)
	if movie.Year > 0 {
		return []string{strings.TrimSpace(fmt.Sprintf("%s %d", title, movie.Year))}
	}
	return []string{title}
}

func tvCompanionSearchQueries(movie *Movie) []string {
	return []string{strings.TrimSpace(movie.Title)}
}

func (m *Manager) companionSearchQueries(movie *Movie) []string {
	if m.kind == companionTV {
		return tvCompanionSearchQueries(movie)
	}
	return companionSearchQueries(movie)
}

func companionSearchHistoryQuery(movie *Movie) string {
	return strings.Join(companionSearchQueries(movie), " | ")
}

func (m *Manager) companionSearchHistoryQuery(movie *Movie) string {
	return strings.Join(m.companionSearchQueries(movie), " | ")
}

func mergeReleases(existing, additions []prowlarr.Release) []prowlarr.Release {
	seen := make(map[string]bool, len(existing)+len(additions))
	merged := make([]prowlarr.Release, 0, len(existing)+len(additions))
	for _, release := range append(existing, additions...) {
		key := releaseFingerprint(release)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, release)
	}
	return merged
}

func (m *Manager) searchRelease(ctx context.Context, indexerID int, query string) ([]prowlarr.Release, error) {
	if err := m.waitForSearchInterval(ctx); err != nil {
		return nil, err
	}
	searchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return m.prowlarr.Search(searchCtx, indexerID, query, m.cfg.Companion.SearchLimit)
}

// waitForSearchInterval reserves the next search start time. The reservation
// is shared by batch, manual and approval-time searches, so fallback queries
// cannot bypass the configured spacing.
func (m *Manager) waitForSearchInterval(ctx context.Context) error {
	m.searchMu.Lock()
	defer m.searchMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	seconds := m.searchIntervalSeconds
	m.mu.RUnlock()
	if seconds <= 0 {
		seconds = config.DefaultCompanionSearchIntervalSeconds
	}
	interval := time.Duration(seconds) * time.Second
	if !m.lastSearch.IsZero() {
		remaining := interval - time.Since(m.lastSearch)
		if remaining > 0 {
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-timer.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.lastSearch = time.Now()
	return nil
}

// StartSearchMissing starts one sequential in-process search worker and
// returns immediately. It searches at most the configured batch size; approval
// remains a separate per-movie action.
func (m *Manager) StartSearchMissing() error {
	if !m.Enabled() {
		return errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	if m.stateErr != nil {
		err := m.stateErr
		m.mu.Unlock()
		return err
	}
	if m.batch.Running {
		m.mu.Unlock()
		return errors.New("companion search batch is already running")
	}
	batchSize := m.searchBatchSize
	if batchSize < config.MinCompanionSearchBatchSize {
		batchSize = config.DefaultCompanionSearchBatchSize
	}
	ids := make([]string, 0, batchSize)
	for _, movie := range m.state.Movies {
		if movie.Missing || movie.Status != StatusPending {
			continue
		}
		ids = append(ids, movie.ID)
		if len(ids) >= batchSize {
			break
		}
	}
	m.batch = BatchStatus{Running: len(ids) > 0, Total: len(ids)}
	if len(ids) == 0 {
		m.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.batchCancel = cancel
	m.mu.Unlock()
	go m.runBatch(ctx, ids)
	return nil
}

func (m *Manager) CancelSearchMissing() error {
	if !m.Enabled() {
		return errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	if !m.batch.Running || m.batchCancel == nil {
		m.mu.Unlock()
		return errors.New("no companion search batch is running")
	}
	cancel := m.batchCancel
	m.batch.Canceled = true
	m.mu.Unlock()
	cancel()
	return nil
}

// ClearReviews discards the current candidates for every movie waiting in
// review and returns those movies to the pending search queue. Search history
// remains available for audit; only the current review results are removed.
func (m *Manager) ClearReviews() (int, error) {
	if !m.Enabled() {
		return 0, errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return 0, m.stateErr
	}
	type previousMovieState struct {
		movie     *Movie
		status    string
		error     string
		updatedAt time.Time
		search    searchState
		hadSearch bool
	}
	previous := make([]previousMovieState, 0)
	now := time.Now()
	for _, movie := range m.state.Movies {
		if movie.Status != StatusReview {
			continue
		}
		oldSearch, hadSearch := m.searches[movie.ID]
		previous = append(previous, previousMovieState{
			movie:     movie,
			status:    movie.Status,
			error:     movie.Error,
			updatedAt: movie.UpdatedAt,
			search:    oldSearch,
			hadSearch: hadSearch,
		})
		movie.Status = StatusPending
		movie.Error = ""
		movie.UpdatedAt = now
		delete(m.searches, movie.ID)
	}
	if len(previous) == 0 {
		return 0, nil
	}
	if err := m.persistLocked(); err != nil {
		for _, old := range previous {
			old.movie.Status = old.status
			old.movie.Error = old.error
			old.movie.UpdatedAt = old.updatedAt
			if old.hadSearch {
				m.searches[old.movie.ID] = old.search
			} else {
				delete(m.searches, old.movie.ID)
			}
		}
		return 0, err
	}
	return len(previous), nil
}

func (m *Manager) runBatch(ctx context.Context, ids []string) {
	defer func() {
		m.mu.Lock()
		m.batch.Running = false
		m.batchCancel = nil
		m.mu.Unlock()
	}()
	consecutiveFailures := 0
	for i, id := range ids {
		if ctx.Err() != nil {
			m.mu.Lock()
			m.batch.Canceled = true
			m.mu.Unlock()
			return
		}
		_, err := m.SearchOne(ctx, id)
		if errors.Is(err, context.Canceled) {
			m.mu.Lock()
			m.batch.Canceled = true
			m.mu.Unlock()
			return
		}
		if err != nil {
			consecutiveFailures++
		} else {
			consecutiveFailures = 0
		}
		m.mu.Lock()
		m.batch.Done = i + 1
		if consecutiveFailures >= 3 {
			m.batch.Error = "Prowlarr appears unavailable"
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

func (m *Manager) Skip(id string) error {
	if !m.Enabled() {
		return errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return fmt.Errorf("companion %s not found", m.itemLabel())
	}
	if movie.Status == StatusComplete || movie.Status == StatusSearching || movie.Status == StatusSubmitting {
		return errors.New("this companion cannot be skipped in its current state")
	}
	if m.kind == companionTV && m.hasTVApprovalsLocked(id) {
		return errors.New("this TV show has a season approval in progress")
	}
	movie.Status = StatusSkipped
	movie.Error = ""
	movie.UpdatedAt = time.Now()
	delete(m.searches, id)
	return m.persistLocked()
}

// UpsertMovie registers a movie already accepted by the normal intake flow.
// It does not search automatically; the UI's explicit companion button does
// that so normal movie routing never adds tracker traffic by surprise.
func (m *Manager) UpsertMovie(driveID, path, folderName, title string, year, tmdbID int) (string, error) {
	return m.upsertItem(driveID, path, folderName, title, year, tmdbID)
}

// UpsertTV registers a TV show already accepted by the normal intake flow.
func (m *Manager) UpsertTV(driveID, path, folderName, title string, year, tmdbID int) (string, error) {
	return m.upsertItem(driveID, path, folderName, title, year, tmdbID)
}

func (m *Manager) upsertItem(driveID, path, folderName, title string, year, tmdbID int) (string, error) {
	if !m.Enabled() {
		return "", errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return "", m.stateErr
	}
	id := movieID(driveID, folderName)
	movie := m.movieLocked(id)
	if movie == nil {
		movie = &Movie{ID: id, CreatedAt: time.Now()}
		m.state.Movies = append(m.state.Movies, movie)
	}
	movie.DriveID = driveID
	movie.Path = path
	movie.FolderName = folderName
	movie.Title = title
	movie.Year = year
	movie.TmdbID = tmdbID
	movie.Missing = false
	remotePath := ""
	remotePath, _ = m.remotePath(driveID, folderName)
	mainInspection, remoteInspection := m.inspectFolder(movie, path, remotePath, folderName)
	if movie.Status != StatusComplete && movie.Status != StatusSkipped && !isLiveWorkflowStatus(movie.Status) {
		inspectionErr := movieInspectionError(mainInspection, remoteInspection)
		if m.kind == companionTV {
			movie.Status = StatusPending
			movie.Error = inspectionErr
		} else if hasSuitableMovieCopy(mainInspection, remoteInspection) {
			movie.Status = StatusAlready1080p
		} else if inspectionErr != "" {
			movie.Status = StatusNeedsReview
			movie.Error = inspectionErr
		} else {
			movie.Status = StatusPending
			movie.Error = ""
		}
	}
	movie.UpdatedAt = time.Now()
	sort.SliceStable(m.state.Movies, func(i, j int) bool { return m.state.Movies[i].FolderName < m.state.Movies[j].FolderName })
	return id, m.persistLocked()
}

// PrepareSelected performs the approval-time fresh search and downloads the
// selected torrent. It leaves qBittorrent submission to the web package so
// the existing safety transaction remains the only submission path.
func (m *Manager) PrepareSelected(ctx context.Context, id, guid string) (Movie, Candidate, []byte, error) {
	if !m.Enabled() {
		return Movie{}, Candidate{}, nil, errors.New("1080p companions are disabled")
	}
	if strings.TrimSpace(guid) == "" {
		return Movie{}, Candidate{}, nil, errors.New("candidate guid is required")
	}
	m.mu.Lock()
	if m.stateErr != nil {
		err := m.stateErr
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, err
	}
	current := m.movieLocked(id)
	if current == nil {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, fmt.Errorf("companion %s not found", m.itemLabel())
	}
	if current.Missing {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, fmt.Errorf("%s folder is no longer present in the configured library roots", m.itemLabel())
	}
	if current.Status == StatusComplete {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("this companion is already complete")
	}
	if current.Status == StatusSearching {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("this companion is currently being searched")
	}
	if current.Status == StatusSubmitting {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("this companion is already being submitted")
	}
	if current.Status != StatusReview {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("a companion must be in review before approving a candidate")
	}
	tvPackKey := ""
	if m.kind == companionTV {
		search, ok := m.searches[id]
		if !ok {
			m.mu.Unlock()
			return Movie{}, Candidate{}, nil, errors.New("the TV release list is no longer available; search the show again")
		}
		var cached Candidate
		found := false
		for _, candidate := range search.Candidates {
			if candidate.Guid == guid || (candidate.sourceGuid != "" && candidate.sourceGuid == guid) {
				cached = candidate
				found = true
				break
			}
		}
		if !found {
			m.mu.Unlock()
			return Movie{}, Candidate{}, nil, errors.New("the selected TV release is no longer in review; search the show again")
		}
		if !cached.TVPackEligible {
			cached.TVPackEligible, cached.TVPackReason = TVPackEligibility(cached.Title)
		}
		tvPackKey = candidateTVPackKey(cached)
		if !cached.TVPackEligible || tvPackKey == "" {
			m.mu.Unlock()
			return Movie{}, Candidate{}, nil, errors.New("individual episode releases or unrecognized TV releases cannot be approved; select a season or series pack")
		}
		if tvPackApproved(current, tvPackKey) {
			m.mu.Unlock()
			return Movie{}, Candidate{}, nil, errors.New("this TV season or series pack has already been added")
		}
		if m.tvApprovalPendingLocked(id, tvPackKey) {
			m.mu.Unlock()
			return Movie{}, Candidate{}, nil, errors.New("another approval for this TV season or series pack is already in progress")
		}
	}
	movie := *current
	if m.kind == companionTV {
		m.setTVApprovalPendingLocked(id, tvPackKey)
	} else {
		current.Status = StatusSubmitting
	}
	current.Error = ""
	current.UpdatedAt = time.Now()
	if err := m.persistLocked(); err != nil {
		if m.kind == companionTV {
			m.clearTVApprovalPendingLocked(id, tvPackKey)
		}
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, err
	}
	m.mu.Unlock()

	candidates, err := m.searchMovie(ctx, &movie)
	if err != nil {
		if m.kind == companionTV {
			m.markTVApprovalError(id, tvPackKey, err)
		} else {
			m.MarkError(id, err)
		}
		return Movie{}, Candidate{}, nil, err
	}
	var selected Candidate
	for _, candidate := range candidates {
		if candidate.Guid == guid || (candidate.sourceGuid != "" && candidate.sourceGuid == guid) {
			selected = candidate
			break
		}
	}
	if selected.Guid == "" {
		err := fmt.Errorf("selected release is no longer available or no longer passes the companion filters")
		if m.kind == companionTV {
			m.markTVApprovalError(id, tvPackKey, err)
		} else {
			m.MarkError(id, err)
		}
		return Movie{}, Candidate{}, nil, err
	}
	if m.kind == companionTV {
		selectedKey := candidateTVPackKey(selected)
		if !selected.TVPackEligible || selectedKey == "" {
			err := errors.New("individual episode releases or unrecognized TV releases cannot be approved; select a season or series pack")
			m.markTVApprovalError(id, tvPackKey, err)
			return Movie{}, Candidate{}, nil, err
		}
		if selectedKey != tvPackKey {
			err := errors.New("the selected TV release changed seasons; search the show again")
			m.markTVApprovalError(id, tvPackKey, err)
			return Movie{}, Candidate{}, nil, err
		}
	}
	if selected.downloadURL == "" {
		err := errors.New("selected Prowlarr release has no download URL")
		if m.kind == companionTV {
			m.markTVApprovalError(id, tvPackKey, err)
		} else {
			m.MarkError(id, err)
		}
		return Movie{}, Candidate{}, nil, err
	}
	data, err := m.prowlarr.DownloadTorrent(ctx, selected.downloadURL)
	if err != nil {
		if m.kind == companionTV {
			m.markTVApprovalError(id, tvPackKey, err)
		} else {
			m.MarkError(id, err)
		}
		return Movie{}, Candidate{}, nil, err
	}
	return movie, selected, data, nil
}

func (m *Manager) MarkComplete(id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return fmt.Errorf("companion %s not found", m.itemLabel())
	}
	now := time.Now()
	movie.Status = StatusComplete
	movie.Error = ""
	movie.QBHash = hash
	movie.UpdatedAt = now
	movie.AddedAt = &now
	delete(m.searches, id)
	return m.persistLocked()
}

// MarkTVComplete records one approved TV season or series pack while leaving
// the show in review so other seasons in the same search can be approved.
func (m *Manager) MarkTVComplete(id string, candidate Candidate, hash string) error {
	if m.kind != companionTV {
		return errors.New("TV pack approval is only available for TV companions")
	}
	key := candidateTVPackKey(candidate)
	if !candidate.TVPackEligible || key == "" {
		return errors.New("individual episode releases or unrecognized TV releases cannot be approved; select a season or series pack")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return fmt.Errorf("companion %s not found", m.itemLabel())
	}
	if !tvPackApproved(movie, key) {
		movie.TVApprovedPacks = append(movie.TVApprovedPacks, key)
	}
	m.clearTVApprovalPendingLocked(id, key)
	now := time.Now()
	movie.Error = ""
	movie.QBHash = hash
	movie.UpdatedAt = now
	movie.AddedAt = &now
	if key == "series" {
		movie.Status = StatusComplete
		delete(m.searches, id)
	} else {
		movie.Status = StatusReview
	}
	return m.persistLocked()
}

// MarkTVError releases one in-flight TV pack approval without hiding the
// other seasons that are still available for review.
func (m *Manager) MarkTVError(id string, candidate Candidate, err error) error {
	if err == nil {
		return nil
	}
	if m.kind != companionTV {
		return errors.New("TV pack errors are only available for TV companions")
	}
	key := candidateTVPackKey(candidate)
	if key == "" {
		return errors.New("TV pack key is missing")
	}
	m.markTVApprovalError(id, key, err)
	return nil
}

func (m *Manager) markTVApprovalError(id, key string, approvalErr error) {
	if approvalErr == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return
	}
	m.clearTVApprovalPendingLocked(id, key)
	movie.Status = StatusReview
	movie.Error = approvalErr.Error()
	movie.UpdatedAt = time.Now()
	_ = m.persistLocked()
}

func (m *Manager) MarkError(id string, err error) {
	if err == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if movie := m.movieLocked(id); movie != nil {
		movie.Status = StatusError
		movie.Error = err.Error()
		movie.UpdatedAt = time.Now()
		_ = m.persistLocked()
	}
}

func (m *Manager) beginSearch(id string) (Movie, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return Movie{}, m.stateErr
	}
	movie := m.movieLocked(id)
	if movie == nil {
		return Movie{}, fmt.Errorf("companion %s not found", m.itemLabel())
	}
	if movie.Missing {
		return Movie{}, fmt.Errorf("%s folder is no longer present in the configured library roots", m.itemLabel())
	}
	switch movie.Status {
	case StatusSearching:
		return Movie{}, errors.New("this companion search is already running")
	case StatusSubmitting:
		return Movie{}, errors.New("this companion is already being submitted")
	}
	if m.kind == companionTV && m.hasTVApprovalsLocked(id) {
		return Movie{}, errors.New("this TV show has a season approval in progress")
	}
	movie.Status = StatusSearching
	movie.Error = ""
	movie.UpdatedAt = time.Now()
	delete(m.searches, id)
	if err := m.persistLocked(); err != nil {
		return Movie{}, err
	}
	return *movie, nil
}

func (m *Manager) movieLocked(id string) *Movie {
	for _, movie := range m.state.Movies {
		if movie.ID == id {
			return movie
		}
	}
	return nil
}

func (m *Manager) persistLocked(history ...searchHistoryRecord) error {
	if m.stateErr != nil {
		return m.stateErr
	}
	if m.store == nil {
		return nil
	}
	var record *searchHistoryRecord
	if len(history) > 0 {
		record = &history[0]
	}
	return m.store.save(m.state, m.searches, record)
}

func (m *Manager) resolveIndexer(ctx context.Context) (int, error) {
	if m.prowlarr == nil || !m.prowlarr.Configured() {
		return 0, errors.New("Prowlarr not configured (set prowlarr.api_key or CINEROUTE_PROWLARR_API_KEY)")
	}
	m.mu.RLock()
	if m.indexerID > 0 {
		id := m.indexerID
		m.mu.RUnlock()
		return id, nil
	}
	name := m.cfg.Prowlarr.IndexerName
	if name == "" {
		name = "LAT-Team"
	}
	m.mu.RUnlock()
	indexers, err := m.prowlarr.Indexers(ctx)
	if err != nil {
		return 0, err
	}
	var match *prowlarr.Indexer
	for i := range indexers {
		if !indexers[i].Enable || !strings.EqualFold(strings.TrimSpace(indexers[i].Name), strings.TrimSpace(name)) {
			continue
		}
		if match != nil {
			return 0, fmt.Errorf("Prowlarr indexer %q is ambiguous", name)
		}
		match = &indexers[i]
	}
	if match == nil {
		return 0, fmt.Errorf("Prowlarr indexer %q was not found or is disabled", name)
	}
	m.mu.Lock()
	m.indexerID = match.ID
	m.indexerName = match.Name
	m.mu.Unlock()
	return match.ID, nil
}

func (m *Manager) clearIndexer() {
	m.mu.Lock()
	m.indexerID = 0
	m.indexerName = ""
	m.mu.Unlock()
}

func (m *Manager) policy(indexerID int) Policy {
	maxBytes := int64(0)
	if m.cfg.Companion.MaxSizeGiB > 0 {
		maxBytes = m.cfg.Companion.MaxSizeGiB * (1 << 30)
	}
	return Policy{
		MaxBytes:        maxBytes,
		MinSeeders:      m.cfg.Companion.MinSeeders,
		TargetIndexerID: indexerID,
	}
}

func cloneCandidates(candidates []Candidate) []Candidate {
	out := make([]Candidate, len(candidates))
	copy(out, candidates)
	for i := range out {
		out[i].Reasons = append([]string(nil), candidates[i].Reasons...)
	}
	return out
}
