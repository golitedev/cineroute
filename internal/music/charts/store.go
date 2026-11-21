package charts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"cineroute/internal/music/jobs"
	"cineroute/internal/music/qobuz"
	_ "modernc.org/sqlite"
)

var (
	ErrNotFound  = errors.New("chart playlist not found")
	ErrStale     = errors.New("chart playlist settings changed")
	ErrPaused    = errors.New("enable this playlist before syncing it")
	ErrSettings  = errors.New("invalid playlist settings")
	ErrTrackBusy = errors.New("this track is still downloading or indexing")
)

type Settings struct {
	Country string `json:"country"`
	Limit   int    `json:"limit"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (s *Settings) validate() error {
	s.Country = strings.ToLower(strings.TrimSpace(s.Country))
	s.Name = strings.TrimSpace(s.Name)
	if marketLabel(s.Country) == "" {
		return fmt.Errorf("%w: choose a country or Global from the Kworb charts", ErrSettings)
	}
	if s.Limit < 1 || s.Limit > 200 {
		return fmt.Errorf("%w: track count must be between 1 and 200", ErrSettings)
	}
	if utf8.RuneCountInString(s.Name) > 120 {
		return fmt.Errorf("%w: playlist name must be at most 120 characters", ErrSettings)
	}
	return nil
}

func (s Settings) displayName() string {
	if s.Name != "" {
		return s.Name
	}
	return fmt.Sprintf("Top %d %s", s.Limit, marketLabel(s.Country))
}

type PlaylistView struct {
	ID            string      `json:"id"`
	Settings      Settings    `json:"settings"`
	DisplayName   string      `json:"display_name"`
	ChartDate     string      `json:"chart_date,omitempty"`
	PublishedDate string      `json:"published_date,omitempty"`
	PublishedAt   time.Time   `json:"published_at,omitzero"`
	Published     int         `json:"published"`
	Ready         int         `json:"ready"`
	PendingChecks int         `json:"pending_checks,omitempty"`
	SyncRequested bool        `json:"sync_requested,omitempty"`
	SyncPhase     string      `json:"sync_phase,omitempty"`
	FetchError    string      `json:"fetch_error,omitempty"`
	PublishError  string      `json:"publish_error,omitempty"`
	Tracks        []TrackView `json:"tracks"`
}

type TrackView struct {
	Track
	State          string `json:"state"`
	Reason         string `json:"reason,omitempty"`
	Album          string `json:"album,omitempty"`
	ConfirmedISRC  string `json:"confirmed_isrc,omitempty"`
	ConfirmedAlbum string `json:"confirmed_album,omitempty"`
}

type playlist struct {
	ID             string
	Revision       int64
	Settings       Settings
	ChartDate      string
	Entries        []entry
	NextFetch      time.Time
	FetchError     string
	NavidromeID    string
	NavidromeOwner string
	PublishedDate  string
	PublishedAt    time.Time
	PublishedIDs   []string
	PublishedName  string
	PublishError   string
	VerifyRemote   bool
}

type entry struct {
	Track
	State         string
	Reason        string
	SongID        string
	SongAlbum     *string // nil for older entries whose Navidrome album was not stored
	JobID         string
	Recording     qobuz.Recording
	NextAttempt   time.Time
	IndexSince    time.Time
	QobuzFailures int `json:",omitempty"`
}

func (p playlist) view() PlaylistView {
	view := PlaylistView{ID: p.ID, Settings: p.Settings, DisplayName: p.Settings.displayName(),
		ChartDate: p.ChartDate, PublishedDate: p.PublishedDate, PublishedAt: p.PublishedAt,
		Published: len(p.PublishedIDs), FetchError: p.FetchError, PublishError: p.PublishError,
		SyncRequested: p.Settings.Enabled && p.NextFetch.IsZero(),
		Tracks:        make([]TrackView, 0, len(p.Entries))}
	for _, track := range p.Entries {
		album := track.Recording.Album
		if track.SongID != "" {
			view.Ready++
			if track.State == "ready" && track.NextAttempt.IsZero() {
				view.PendingChecks++
			}
			album = ""
			if track.SongAlbum != nil {
				album = *track.SongAlbum
			}
		}
		view.Tracks = append(view.Tracks, TrackView{Track: track.Track, State: track.State, Reason: track.Reason, Album: album})
	}
	return view
}

func openStore(dataDir string) (*sql.DB, error) {
	dir := filepath.Join(dataDir, "charts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "charts.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
		PRAGMA busy_timeout = 5000;
		CREATE TABLE IF NOT EXISTS chart_playlist (id TEXT PRIMARY KEY, revision INTEGER NOT NULL, state TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS chart_recording (spotify_id TEXT PRIMARY KEY, metadata TEXT NOT NULL);
		CREATE TABLE IF NOT EXISTS chart_identity (spotify_id TEXT PRIMARY KEY, isrc TEXT NOT NULL, album TEXT NOT NULL, manual INTEGER NOT NULL, checked_at INTEGER NOT NULL);
		CREATE TABLE IF NOT EXISTS chart_import (job_id TEXT PRIMARY KEY, imported_at INTEGER NOT NULL, receipt TEXT NOT NULL);
		CREATE INDEX IF NOT EXISTS chart_import_date ON chart_import(imported_at DESC, job_id DESC);
	`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

type playlistStore interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (m *Manager) playlists(ctx context.Context) ([]playlist, error) {
	return loadPlaylists(ctx, m.db)
}

func loadPlaylists(ctx context.Context, db playlistStore) ([]playlist, error) {
	rows, err := db.QueryContext(ctx, "SELECT state FROM chart_playlist ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]playlist, 0)
	for rows.Next() {
		var p playlist
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (m *Manager) playlist(ctx context.Context, id string) (playlist, error) {
	var p playlist
	var raw []byte
	err := m.db.QueryRowContext(ctx, "SELECT state FROM chart_playlist WHERE id = ?", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(raw, &p)
}

func (m *Manager) save(ctx context.Context, p playlist, previousRevision int64) error {
	return savePlaylist(ctx, m.db, p, previousRevision)
}

func savePlaylist(ctx context.Context, db playlistStore, p playlist, previousRevision int64) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if previousRevision == 0 {
		_, err := db.ExecContext(ctx, "INSERT INTO chart_playlist(id, revision, state) VALUES (?, ?, ?)", p.ID, p.Revision, raw)
		return err
	}
	result, err := db.ExecContext(ctx, "UPDATE chart_playlist SET revision = ?, state = ? WHERE id = ? AND revision = ?",
		p.Revision, raw, p.ID, previousRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 0 {
		return ErrStale
	}
	return err
}

func (m *Manager) Views(ctx context.Context) ([]PlaylistView, error) {
	playlists, err := m.playlists(ctx)
	if err != nil {
		return nil, err
	}
	manual, err := m.confirmedIdentities(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]PlaylistView, 0, len(playlists))
	for _, p := range playlists {
		view := p.view()
		if value, ok := m.phases.Load(p.ID); ok {
			if phase := value.(syncPhase); phase.revision == p.Revision {
				view.SyncPhase = phase.name
			}
		}
		for i, entry := range p.Entries {
			track := &view.Tracks[i]
			identity := manual[entry.SpotifyID]
			track.ConfirmedISRC, track.ConfirmedAlbum = identity.ISRC, identity.Album
			if entry.JobID == "" {
				continue
			}
			job, err := m.jobs.Get(entry.JobID)
			if err != nil {
				continue
			}
			switch job.Status {
			case jobs.StatusCompleted, jobs.StatusSkipped:
				track.State, track.Reason = "indexing", "Waiting for Navidrome to index the track"
			case jobs.StatusFailed, jobs.StatusCancelled:
				track.State, track.Reason = string(job.Status), job.Message
			default:
				track.State, track.Reason = string(job.Status), ""
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (m *Manager) Configure(ctx context.Context, id string, settings Settings) (PlaylistView, error) {
	if err := settings.validate(); err != nil {
		return PlaylistView{}, err
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	var p playlist
	if id == "" {
		var bytes [12]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return PlaylistView{}, err
		}
		p.ID = hex.EncodeToString(bytes[:])
	} else {
		var err error
		if p, err = m.playlist(ctx, id); err != nil {
			return PlaylistView{}, err
		}
	}
	if p.Settings.Country != settings.Country {
		p.Entries = nil
		p.ChartDate = ""
	} else if len(p.Entries) > settings.Limit {
		p.Entries = p.Entries[:settings.Limit]
	}
	p.Settings = settings
	p.NextFetch = time.Time{}
	p.VerifyRemote = true
	old := p.Revision
	p.Revision++
	if err := m.save(ctx, p, old); err != nil {
		return PlaylistView{}, err
	}
	m.Wake()
	return p.view(), nil
}

func (m *Manager) SyncNow(ctx context.Context, id string) error {
	m.mutations.Lock()
	defer m.mutations.Unlock()
	p, err := m.playlist(ctx, id)
	if err != nil {
		return err
	}
	if !p.Settings.Enabled {
		return ErrPaused
	}
	p.NextFetch = time.Time{}
	p.VerifyRemote = true
	for i := range p.Entries {
		e := &p.Entries[i]
		e.NextAttempt = time.Time{}
		// Keep confirmed tracks in the playlist while the worker checks batches.
		if e.JobID == "" && e.SongID == "" {
			e.State, e.Reason = "pending", ""
		}
	}
	p.Revision++
	if err := m.save(ctx, p, p.Revision-1); err != nil {
		return err
	}
	m.Wake()
	return nil
}

type trackIdentity struct {
	ISRC      string
	Album     string
	CheckedAt int64
}

func (m *Manager) identity(ctx context.Context, spotifyID string) (trackIdentity, error) {
	var result trackIdentity
	err := m.db.QueryRowContext(ctx, "SELECT isrc, album, checked_at FROM chart_identity WHERE spotify_id = ?", spotifyID).Scan(&result.ISRC, &result.Album, &result.CheckedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return trackIdentity{}, nil
	}
	return result, err
}

func (m *Manager) cacheIdentity(ctx context.Context, spotifyID string, identity trackIdentity, now time.Time) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO chart_identity VALUES (?, ?, ?, 0, ?)
		ON CONFLICT(spotify_id) DO UPDATE SET isrc = excluded.isrc, album = excluded.album, checked_at = excluded.checked_at
		WHERE chart_identity.manual = 0`, spotifyID, identity.ISRC, identity.Album, now.Unix())
	return err
}

func (m *Manager) confirmedIdentities(ctx context.Context) (map[string]trackIdentity, error) {
	rows, err := m.db.QueryContext(ctx, "SELECT spotify_id, isrc, album FROM chart_identity WHERE manual = 1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]trackIdentity)
	for rows.Next() {
		var id string
		var identity trackIdentity
		if err := rows.Scan(&id, &identity.ISRC, &identity.Album); err != nil {
			return nil, err
		}
		result[id] = identity
	}
	return result, rows.Err()
}

// SetTrackISRC updates all playlists that include the Spotify track. An empty
// code returns it to automatic matching.
func (m *Manager) SetTrackISRC(ctx context.Context, playlistID, spotifyID, input, album string) error {
	code := normalizeISRC(input)
	if strings.TrimSpace(input) != "" && !validISRC.MatchString(code) {
		return fmt.Errorf("%w: enter a valid 12-character ISRC", ErrSettings)
	}
	album = strings.TrimSpace(album)
	if utf8.RuneCountInString(album) > 256 {
		return fmt.Errorf("%w: album name must be at most 256 characters", ErrSettings)
	}
	m.mutations.Lock()
	defer m.mutations.Unlock()
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	playlists, err := loadPlaylists(ctx, tx)
	if err != nil {
		return err
	}
	selected := slices.IndexFunc(playlists, func(p playlist) bool { return p.ID == playlistID })
	if selected < 0 || !slices.ContainsFunc(playlists[selected].Entries, func(e entry) bool { return e.SpotifyID == spotifyID }) {
		return ErrNotFound
	}
	for _, p := range playlists {
		i := slices.IndexFunc(p.Entries, func(e entry) bool { return e.SpotifyID == spotifyID })
		if i < 0 {
			continue
		}
		e := &p.Entries[i]
		if e.JobID != "" {
			job, err := m.jobs.Get(e.JobID)
			if err != nil && !errors.Is(err, jobs.ErrNotFound) {
				return err
			}
			if err == nil && !job.Status.Finished() {
				return ErrTrackBusy
			}
		}
		e.Recording = qobuz.Recording{}
		e.QobuzFailures = 0
		e.SongID, e.SongAlbum = "", nil
		e.JobID, e.IndexSince = "", time.Time{}
		e.State, e.Reason, e.NextAttempt = "pending", "", time.Time{}
		p.Revision++
		if err := savePlaylist(ctx, tx, p, p.Revision-1); err != nil {
			return err
		}
	}
	if code == "" {
		_, err = tx.ExecContext(ctx, "DELETE FROM chart_identity WHERE spotify_id = ? AND manual = 1", spotifyID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO chart_identity VALUES (?, ?, ?, 1, 0)
			ON CONFLICT(spotify_id) DO UPDATE SET isrc = excluded.isrc, album = excluded.album, manual = 1, checked_at = 0`, spotifyID, code, album)
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	m.Wake()
	return nil
}

func (m *Manager) recording(ctx context.Context, spotifyID string) (qobuz.Recording, error) {
	var raw []byte
	var result qobuz.Recording
	err := m.db.QueryRowContext(ctx, "SELECT metadata FROM chart_recording WHERE spotify_id = ?", spotifyID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	return result, json.Unmarshal(raw, &result)
}

func (m *Manager) remember(ctx context.Context, spotifyID string, match qobuz.Recording) error {
	raw, err := json.Marshal(match)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `INSERT INTO chart_recording VALUES (?, ?)
		ON CONFLICT(spotify_id) DO UPDATE SET metadata = excluded.metadata`, spotifyID, raw)
	return err
}
