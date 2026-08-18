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

type View struct {
	Enabled            bool        `json:"enabled"`
	IndexerName        string      `json:"indexer_name"`
	ProwlarrConfigured bool        `json:"prowlarr_configured"`
	Movies             []*Movie    `json:"movies"`
	Open               *Movie      `json:"open,omitempty"`
	Candidates         []Candidate `json:"candidates,omitempty"`
	SearchedAt         time.Time   `json:"searched_at,omitempty"`
	Batch              BatchStatus `json:"batch"`
	StateError         string      `json:"state_error,omitempty"`
}

type Manager struct {
	cfg      *config.Config
	lib      *library.Scan
	prowlarr *prowlarr.Client

	mu          sync.RWMutex
	state       stateFile
	stateErr    error
	searches    map[string]searchState
	indexerID   int
	indexerName string
	batch       BatchStatus
}

func NewManager(cfg *config.Config, lib *library.Scan, prowlarrClient *prowlarr.Client) *Manager {
	m := &Manager{
		cfg:      cfg,
		lib:      lib,
		prowlarr: prowlarrClient,
		searches: map[string]searchState{},
		state:    stateFile{Version: stateVersion},
	}
	if cfg == nil {
		m.stateErr = errors.New("companion configuration is missing")
		return m
	}
	st, err := loadState(cfg.Companion.StatePath)
	if err != nil {
		m.stateErr = err
	} else {
		m.state = st
		if normalizeLoadedState(&m.state) {
			if err := saveState(cfg.Companion.StatePath, m.state); err != nil {
				m.stateErr = fmt.Errorf("recover companion state: %w", err)
			}
		}
	}
	return m
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
	view := View{Enabled: m.Enabled()}
	if m == nil || m.cfg == nil {
		view.StateError = "companion manager is unavailable"
		return view
	}
	view.IndexerName = m.cfg.Prowlarr.IndexerName
	if view.IndexerName == "" {
		view.IndexerName = "LAT-Team"
	}
	view.ProwlarrConfigured = m.Enabled() && m.prowlarr != nil && m.prowlarr.Configured()
	m.mu.RLock()
	defer m.mu.RUnlock()
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
			if result, ok := m.searches[openID]; ok {
				view.Candidates = cloneCandidates(result.Candidates)
				view.SearchedAt = result.SearchedAt
			}
			break
		}
	}
	return view
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

// Scan reconciles the immediate children of all movie roots into durable
// state. It never renames, moves or recursively crawls media files.
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
	folders, err := m.lib.Movies()
	if err != nil {
		return fmt.Errorf("companion scan aborted: %w", err)
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
		if movie.UpdatedAt.IsZero() {
			movie.UpdatedAt = time.Now()
		}

		title, year, parseErr := parseMovieFolder(folder)
		inspection := inspectMovieFolder(folder.Path, folder.Name)
		movie.ExistingCopy = inspection.Quality
		movie.JellyfinWarning = inspection.JellyfinWarning
		if parseErr != nil {
			movie.Status = StatusNeedsReview
			movie.Error = parseErr.Error()
			movie.UpdatedAt = time.Now()
			current = append(current, movie)
			continue
		}
		movie.Title = title
		movie.Year = year
		if movie.Status == StatusComplete || movie.Status == StatusSkipped {
			current = append(current, movie)
			continue
		}
		if movie.Status == StatusSubmitting {
			movie.Status = StatusError
			movie.Error = "previous companion submission was interrupted; search and approve again after checking qBittorrent"
		}
		if movie.Status == StatusSearching || movie.Status == StatusReview || legacyTMDBError(movie.Error) {
			movie.Status = StatusPending
			movie.Error = ""
		}
		if inspection.Error != "" {
			movie.Status = StatusNeedsReview
			movie.Error = inspection.Error
			movie.UpdatedAt = time.Now()
			current = append(current, movie)
			continue
		}
		if inspection.Quality == "1080p" {
			movie.Status = StatusAlready1080p
			movie.Error = ""
			movie.UpdatedAt = time.Now()
			current = append(current, movie)
			continue
		}
		if movie.Status == "" {
			movie.Status = StatusPending
		}
		movie.UpdatedAt = time.Now()
		current = append(current, movie)
	}
	for _, movie := range previous {
		if seen[movie.ID] {
			continue
		}
		missing := cloneMovie(movie)
		missing.Missing = true
		if missing.Status != StatusComplete && missing.Status != StatusSkipped {
			missing.Status = StatusError
			missing.Error = "movie folder is no longer present in the configured library roots"
		}
		missing.UpdatedAt = time.Now()
		current = append(current, missing)
		delete(m.searches, missing.ID)
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
	if err := m.stateErrValue(); err != nil {
		return nil, err
	}
	movie, err := m.movieByID(id)
	if err != nil {
		return nil, err
	}
	if movie.Missing {
		return nil, errors.New("movie folder is no longer present in the configured library roots")
	}
	if movie.Status == StatusComplete {
		return nil, errors.New("this companion is already complete")
	}
	if err := m.setStatus(id, StatusSearching, ""); err != nil {
		return nil, err
	}
	candidates, err := m.searchMovie(ctx, &movie)
	if err != nil {
		_ = m.setStatus(id, StatusError, err.Error())
		return nil, err
	}
	m.mu.Lock()
	m.searches[id] = searchState{Candidates: cloneCandidates(candidates), SearchedAt: time.Now()}
	current := m.movieLocked(id)
	if current != nil {
		if len(candidates) == 0 {
			current.Status = StatusNoMatch
			current.Error = "no suitable 1080p releases found"
		} else {
			current.Status = StatusReview
			current.Error = ""
		}
		current.UpdatedAt = time.Now()
	}
	err = m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return cloneCandidates(candidates), nil
}

func (m *Manager) searchMovie(ctx context.Context, movie *Movie) ([]Candidate, error) {
	searchCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	indexerID, err := m.resolveIndexer(searchCtx)
	if err != nil {
		return nil, err
	}
	policy := m.policy(indexerID)
	query := strings.TrimSpace(fmt.Sprintf("%s %d", movie.Title, movie.Year))
	results, err := m.prowlarr.Search(searchCtx, indexerID, query, m.cfg.Companion.SearchLimit)
	if err != nil {
		m.clearIndexer()
		return nil, err
	}
	candidates := FilterAndRank(results, movie.Title, movie.Year, movie.TmdbID, policy)
	if len(candidates) > 0 {
		return candidates, nil
	}
	// One deliberately simple fallback handles trackers that omit the release
	// year from their normalized search matching.
	if query != movie.Title {
		results, err = m.prowlarr.Search(searchCtx, indexerID, movie.Title, m.cfg.Companion.SearchLimit)
		if err != nil {
			m.clearIndexer()
			return nil, err
		}
		candidates = FilterAndRank(results, movie.Title, movie.Year, movie.TmdbID, policy)
	}
	return candidates, nil
}

// StartSearchMissing starts one sequential in-process search worker and
// returns immediately. It only searches pending records; approval remains a
// separate per-movie action.
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
	ids := []string{}
	for _, movie := range m.state.Movies {
		if movie.Missing || movie.Status != StatusPending {
			continue
		}
		ids = append(ids, movie.ID)
	}
	m.batch = BatchStatus{Running: true, Total: len(ids)}
	m.mu.Unlock()
	go m.runBatch(ids)
	return nil
}

func (m *Manager) runBatch(ids []string) {
	consecutiveFailures := 0
	for i, id := range ids {
		_, err := m.SearchOne(context.Background(), id)
		if err != nil {
			consecutiveFailures++
		} else {
			consecutiveFailures = 0
		}
		m.mu.Lock()
		m.batch.Done = i + 1
		if consecutiveFailures >= 3 {
			m.batch.Error = "Prowlarr appears unavailable"
			m.batch.Running = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		if i+1 < len(ids) && m.cfg.Companion.SearchDelayMS > 0 {
			timer := time.NewTimer(time.Duration(m.cfg.Companion.SearchDelayMS) * time.Millisecond)
			<-timer.C
		}
	}
	m.mu.Lock()
	m.batch.Running = false
	m.mu.Unlock()
}

func (m *Manager) Skip(id string) error {
	if !m.Enabled() {
		return errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return errors.New("companion movie not found")
	}
	if movie.Status == StatusComplete || movie.Status == StatusSubmitting {
		return errors.New("this companion cannot be skipped in its current state")
	}
	movie.Status = StatusSkipped
	movie.Error = ""
	movie.UpdatedAt = time.Now()
	return m.persistLocked()
}

// UpsertMovie registers a movie already accepted by the normal intake flow.
// It does not search automatically; the UI's explicit companion button does
// that so normal movie routing never adds tracker traffic by surprise.
func (m *Manager) UpsertMovie(driveID, path, folderName, title string, year, tmdbID int) (string, error) {
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
	inspection := inspectMovieFolder(path, folderName)
	movie.ExistingCopy = inspection.Quality
	movie.JellyfinWarning = inspection.JellyfinWarning
	if movie.Status != StatusComplete && movie.Status != StatusSkipped && movie.Status != StatusSubmitting {
		if inspection.Quality == "1080p" {
			movie.Status = StatusAlready1080p
		} else if inspection.Error != "" {
			movie.Status = StatusNeedsReview
			movie.Error = inspection.Error
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
	movie, err := m.movieByID(id)
	if err != nil {
		return Movie{}, Candidate{}, nil, err
	}
	m.mu.Lock()
	current := m.movieLocked(id)
	if current == nil {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("companion movie not found")
	}
	if current.Status == StatusComplete {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("this companion is already complete")
	}
	if current.Status == StatusSubmitting {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, errors.New("this companion is already being submitted")
	}
	current.Status = StatusSubmitting
	current.Error = ""
	current.UpdatedAt = time.Now()
	if err := m.persistLocked(); err != nil {
		m.mu.Unlock()
		return Movie{}, Candidate{}, nil, err
	}
	m.mu.Unlock()

	candidates, err := m.searchMovie(ctx, &movie)
	if err != nil {
		m.markError(id, err)
		return Movie{}, Candidate{}, nil, err
	}
	var selected Candidate
	for _, candidate := range candidates {
		if candidate.Guid == guid {
			selected = candidate
			break
		}
	}
	if selected.Guid == "" {
		err := fmt.Errorf("selected release is no longer available or no longer passes the companion filters")
		m.markError(id, err)
		return Movie{}, Candidate{}, nil, err
	}
	if selected.downloadURL == "" {
		err := errors.New("selected Prowlarr release has no download URL")
		m.markError(id, err)
		return Movie{}, Candidate{}, nil, err
	}
	data, err := m.prowlarr.DownloadTorrent(ctx, selected.downloadURL)
	if err != nil {
		m.markError(id, err)
		return Movie{}, Candidate{}, nil, err
	}
	return movie, selected, data, nil
}

func (m *Manager) MarkComplete(id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return errors.New("companion movie not found")
	}
	movie.Status = StatusComplete
	movie.Error = ""
	movie.QBHash = hash
	movie.UpdatedAt = time.Now()
	return m.persistLocked()
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

func (m *Manager) movieByID(id string) (Movie, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.stateErr != nil {
		return Movie{}, m.stateErr
	}
	movie := m.movieLocked(id)
	if movie == nil {
		return Movie{}, errors.New("companion movie not found")
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

func (m *Manager) setStatus(id, status, message string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	movie := m.movieLocked(id)
	if movie == nil {
		return errors.New("companion movie not found")
	}
	movie.Status = status
	movie.Error = message
	movie.UpdatedAt = time.Now()
	return m.persistLocked()
}

func (m *Manager) markError(id string, err error) {
	m.MarkError(id, err)
}

func (m *Manager) stateErrValue() error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateErr
}

func (m *Manager) persistLocked() error {
	if m.stateErr != nil {
		return m.stateErr
	}
	return saveState(m.cfg.Companion.StatePath, m.state)
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
