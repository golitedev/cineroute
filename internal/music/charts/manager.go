package charts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"cineroute/internal/music/jobs"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
)

const (
	chartInterval = 3 * time.Hour
	retryFetch    = 15 * time.Minute
	retryTrack    = 24 * time.Hour
	workInterval  = time.Minute
	scanGrace     = 20 * time.Minute
)

type catalog interface {
	SearchRecordings(context.Context, string) ([]qobuz.Recording, error)
}

type downloadQueue interface {
	EnqueueChart(context.Context, string, jobs.ChartSource) (jobs.Job, error)
	Get(string) (jobs.Job, error)
	PendingChartImports() []jobs.Job
	ConfirmChartImport(string) error
}

// Manager retains chart progress separately from the download history.
type Manager struct {
	db      *sql.DB
	catalog catalog
	nav     *navidrome.Client
	jobs    downloadQueue
	fetch   func(context.Context, string) (Snapshot, error)
	wake    chan struct{}
	phases  sync.Map // playlist ID to syncPhase for the current worker pass

	// Settings changes and external playlist writes must not overlap.
	mutations  sync.Mutex
	adminUntil time.Time
	lastScan   time.Time
}

func New(data string, catalog catalog, nav *navidrome.Client, queue downloadQueue) (*Manager, error) {
	db, err := openStore(data)
	if err != nil {
		return nil, err
	}
	return &Manager{db: db, catalog: catalog, nav: nav, jobs: queue, fetch: fetchKworb, wake: make(chan struct{}, 1)}, nil
}

func (m *Manager) Close() error { return m.db.Close() }

func (m *Manager) Available() bool {
	return m.nav != nil && m.nav.Username != "" && m.nav.Password != ""
}

func (m *Manager) Wake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Remove stops future syncs before removing the managed Navidrome playlist.
func (m *Manager) Remove(ctx context.Context, id string) error {
	m.mutations.Lock()
	defer m.mutations.Unlock()
	p, err := m.playlist(ctx, id)
	if err != nil {
		return err
	}
	p.Settings.Enabled = false
	p.Revision++
	if err := m.save(ctx, p, p.Revision-1); err != nil {
		return err
	}
	if m.Available() {
		remoteID := p.NavidromeID
		if remoteID == "" {
			remoteID, err = m.nav.FindOwnedPlaylist(ctx, "CineRoute chart "+p.ID)
		}
		if err == nil && remoteID != "" {
			var remote navidrome.Playlist
			remote, err = m.nav.Playlist(ctx, remoteID)
			if err == nil {
				if !strings.EqualFold(remote.Owner, m.nav.Username) {
					return fmt.Errorf("the Navidrome playlist belongs to another account")
				}
				err = m.nav.DeletePlaylist(ctx, remoteID)
			}
		}
		if err != nil && !errors.Is(err, navidrome.ErrNotFound) {
			return err
		}
	} else if p.NavidromeID != "" {
		return fmt.Errorf("configure the Navidrome playlist owner before removing this playlist")
	}
	_, err = m.db.ExecContext(ctx, "DELETE FROM chart_playlist WHERE id = ?", id)
	return err
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(workInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		m.runOnce(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case <-ticker.C:
		}
	}
}

type fetchResult struct {
	snapshot Snapshot
	err      error
}

type syncPhase struct {
	revision int64
	name     string
}

func (m *Manager) runOnce(ctx context.Context, now time.Time) {
	if err := m.recordImports(ctx); err != nil && ctx.Err() == nil {
		slog.Error("saving chart import history", "err", err)
	}
	if !m.Available() || ctx.Err() != nil {
		return
	}
	playlists, err := m.playlists(ctx)
	if err != nil {
		slog.Error("loading music charts", "err", err)
		return
	}
	fetched := make(map[string]fetchResult)
	for _, p := range playlists {
		if ctx.Err() != nil {
			return
		}
		if !p.Settings.Enabled {
			continue
		}
		if err := m.syncPlaylist(ctx, p, now, fetched); err != nil && !errors.Is(err, ErrStale) && ctx.Err() == nil {
			slog.Error("syncing music chart", "playlist", p.ID, "err", err)
		}
	}
}

func (m *Manager) syncPlaylist(ctx context.Context, p playlist, now time.Time, fetched map[string]fetchResult) error {
	defer m.phases.Delete(p.ID)
	dirty := false
	if !now.Before(p.NextFetch) {
		m.phases.Store(p.ID, syncPhase{p.Revision, "fetching"})
		result, ok := fetched[p.Settings.Country]
		if !ok {
			result.snapshot, result.err = m.fetch(ctx, p.Settings.Country)
			fetched[p.Settings.Country] = result
		}
		p.NextFetch = now.Add(chartInterval)
		switch {
		case result.err != nil:
			p.FetchError = result.err.Error()
			p.NextFetch = now.Add(retryFetch)
		case result.snapshot.Date < p.ChartDate:
			p.FetchError = "Kworb returned an older chart date"
			p.NextFetch = now.Add(retryFetch)
		default:
			p.FetchError = ""
			p.accept(result.snapshot)
		}
		dirty = true
		if err := m.save(ctx, p, p.Revision); err != nil {
			return err
		}
	}
	if len(p.Entries) == 0 || ctx.Err() != nil {
		return nil
	}
	if now.After(m.adminUntil) {
		if err := m.nav.CheckAdmin(ctx); err != nil {
			if p.PublishError != err.Error() {
				p.PublishError = err.Error()
				return m.save(ctx, p, p.Revision)
			}
			return nil
		}
		m.adminUntil = now.Add(time.Hour)
	}

	const lookupsPerPass = 12
	lookups := 0
	for i := range p.Entries {
		e := &p.Entries[i]
		missingAlbum := e.State == "ready" && e.SongID != "" && e.SongAlbum == nil
		if now.Before(e.NextAttempt) && !missingAlbum {
			continue
		}
		if e.JobID != "" && m.jobInProgress(e, now) {
			dirty = true
			continue
		}
		if lookups >= lookupsPerPass {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if lookups == 0 {
			m.phases.Store(p.ID, syncPhase{p.Revision, "matching"})
		}
		lookups++
		if err := m.resolve(ctx, p, e, now); err != nil {
			if errors.Is(err, ErrStale) {
				return err
			}
			e.State, e.Reason = "retrying", err.Error()
			e.NextAttempt = now.Add(retryFetch)
			dirty = true
			break
		}
		dirty = true
	}
	if dirty {
		if err := m.save(ctx, p, p.Revision); err != nil {
			return err
		}
	}
	if p.FetchError != "" {
		return nil
	}
	return m.publish(ctx, p, now)
}

func (p *playlist) accept(snapshot Snapshot) {
	previous := make(map[string]entry, len(p.Entries))
	for _, old := range p.Entries {
		previous[old.SpotifyID] = old
	}
	if p.ChartDate != snapshot.Date {
		p.VerifyRemote = true
	}
	var entries []entry
	for _, track := range snapshot.Tracks[:min(p.Settings.Limit, len(snapshot.Tracks))] {
		e, found := previous[track.SpotifyID]
		if !found {
			e.State = "pending"
		}
		if p.ChartDate != snapshot.Date && e.SongID != "" {
			// Recheck local file availability on the new chart without delaying
			// tracks that were already confirmed and still rank.
			e.NextAttempt = time.Time{}
		}
		e.Track = track
		entries = append(entries, e)
	}
	p.ChartDate, p.Entries = snapshot.Date, entries
}

// jobInProgress updates active and failed jobs without spending a provider lookup.
func (m *Manager) jobInProgress(e *entry, now time.Time) bool {
	job, err := m.jobs.Get(e.JobID)
	if errors.Is(err, jobs.ErrNotFound) {
		// Finished receipts can be trimmed, but a playable copy may have indexed.
		return false
	}
	if err != nil {
		e.State, e.Reason = "retrying", "Could not read the album download status"
		e.NextAttempt = now.Add(workInterval)
		return true
	}
	if !job.Status.Finished() {
		e.State, e.Reason = string(job.Status), ""
		e.NextAttempt = now.Add(workInterval)
		return true
	}
	if job.Status == jobs.StatusFailed || job.Status == jobs.StatusCancelled {
		e.State, e.Reason = string(job.Status), job.Message
		e.NextAttempt = now.Add(retryTrack)
		e.JobID, e.IndexSince = "", time.Time{}
		return true
	}
	return false
}

func (m *Manager) resolve(ctx context.Context, p playlist, e *entry, now time.Time) error {
	if e.Recording.ID == "" {
		cached, err := m.recording(ctx, e.SpotifyID)
		if err != nil {
			return err
		}
		e.Recording = cached
	}
	if e.JobID == "" && e.Recording.ID != "" && trackMatch(e.Track, e.Recording.Title, e.Recording.Artist) == 0 {
		e.Recording = qobuz.Recording{}
	}
	title := titleWithoutCredit(e.Title, e.Artists)
	songs, err := m.nav.Search(ctx, title)
	if err != nil {
		return err
	}
	song, ambiguous := chooseSong(e.Track, e.Recording, songs)
	if song.ID != "" {
		e.setReady(song, now)
		return nil
	}
	e.SongID, e.SongAlbum = "", nil
	if e.JobID != "" {
		if e.IndexSince.IsZero() {
			e.IndexSince = now
		}
		if now.Sub(e.IndexSince) >= scanGrace {
			e.State, e.Reason = "needs_review", "The exact track is missing from Navidrome after the album job"
			e.JobID, e.IndexSince, e.NextAttempt = "", time.Time{}, now.Add(retryTrack)
		} else {
			e.State, e.Reason = "indexing", "Waiting for Navidrome to index the exact track"
			e.NextAttempt = now.Add(workInterval)
			if now.Sub(e.IndexSince) >= 2*workInterval {
				return m.scanIfIdle(ctx, now)
			}
		}
		return nil
	}

	if e.Recording.ID == "" {
		query := title + " " + e.Artists[0]
		candidates, err := m.catalog.SearchRecordings(ctx, query)
		if err != nil {
			return fmt.Errorf("Qobuz track search failed")
		}
		recording, reason := chooseRecording(e.Track, candidates)
		if reason != "" {
			if ambiguous {
				reason = "Multiple Navidrome recordings match. " + reason
			}
			e.State, e.Reason, e.NextAttempt = "needs_review", reason, now.Add(retryTrack)
			return nil
		}
		e.Recording = recording
		if err := m.remember(ctx, e.SpotifyID, recording); err != nil {
			return err
		}
		song, ambiguous = chooseSong(e.Track, recording, songs)
		if song.ID != "" {
			e.setReady(song, now)
			return nil
		}
	}
	if ambiguous {
		e.State, e.Reason = "needs_review", "Multiple Navidrome recordings match this track"
		e.NextAttempt = now.Add(retryTrack)
		return nil
	}

	job, err := m.enqueue(ctx, p, e)
	switch {
	case errors.Is(err, jobs.ErrQueueFull):
		e.State, e.Reason, e.NextAttempt = "waiting", "Waiting for room in the automatic download queue", now.Add(workInterval)
	case errors.Is(err, jobs.ErrNotStreamable):
		e.State, e.Reason, e.NextAttempt = "needs_review", "The album is unavailable for this Qobuz account", now.Add(retryTrack)
	case err != nil:
		return err
	default:
		e.State, e.Reason, e.JobID, e.NextAttempt = string(job.Status), "", job.ID, now.Add(workInterval)
		if job.Status.Finished() {
			e.State = "indexing"
		}
	}
	return nil
}

func (e *entry) setReady(song navidrome.Song, now time.Time) {
	album := song.Album
	e.State, e.Reason, e.SongID, e.SongAlbum = "ready", "", song.ID, &album
	e.JobID, e.IndexSince, e.NextAttempt = "", time.Time{}, now.Add(retryTrack)
}

func (m *Manager) enqueue(ctx context.Context, p playlist, e *entry) (jobs.Job, error) {
	m.mutations.Lock()
	defer m.mutations.Unlock()
	current, err := m.playlist(ctx, p.ID)
	if err != nil || current.Revision != p.Revision {
		return jobs.Job{}, stale(err)
	}
	source := jobs.ChartSource{PlaylistID: p.ID, Playlist: p.Settings.displayName(), ChartDate: p.ChartDate, SpotifyID: e.SpotifyID, Track: e.Title}
	return m.jobs.EnqueueChart(ctx, e.Recording.AlbumID, source)
}

func (m *Manager) scanIfIdle(ctx context.Context, now time.Time) error {
	if now.Sub(m.lastScan) < 5*time.Minute {
		return nil
	}
	m.lastScan = now
	scanning, err := m.nav.Scanning(ctx)
	if err != nil || scanning {
		return err
	}
	return m.nav.StartScan(ctx)
}

func (m *Manager) publish(ctx context.Context, p playlist, now time.Time) error {
	var songs []string
	unresolved := false
	for _, e := range p.Entries {
		if e.SongID != "" {
			songs = append(songs, e.SongID)
		} else if e.State != "needs_review" && e.State != "failed" && e.State != "cancelled" {
			unresolved = true
		}
	}
	if len(songs) == 0 && (unresolved || p.NavidromeID == "") {
		return nil
	}
	name := p.Settings.displayName()
	if !p.VerifyRemote && p.PublishError == "" && name == p.PublishedName && p.ChartDate == p.PublishedDate && slices.Equal(songs, p.PublishedIDs) {
		return nil
	}

	m.phases.Store(p.ID, syncPhase{p.Revision, "publishing"})
	m.mutations.Lock()
	defer m.mutations.Unlock()
	current, err := m.playlist(ctx, p.ID)
	if err != nil || current.Revision != p.Revision {
		return stale(err)
	}
	if err := m.writePlaylist(ctx, &p, songs, name); err != nil {
		p.PublishError = err.Error()
	} else {
		p.PublishedDate, p.PublishedAt, p.PublishedIDs, p.PublishedName = p.ChartDate, now, songs, name
		p.VerifyRemote, p.PublishError = false, ""
	}
	return m.save(ctx, p, p.Revision)
}

func (m *Manager) writePlaylist(ctx context.Context, p *playlist, songs []string, name string) error {
	if p.NavidromeOwner != "" && !strings.EqualFold(p.NavidromeOwner, m.nav.Username) {
		return fmt.Errorf("restore the Navidrome account that owns this playlist")
	}
	var remote navidrome.Playlist
	if p.NavidromeID != "" {
		var err error
		remote, err = m.nav.Playlist(ctx, p.NavidromeID)
		if err != nil && !errors.Is(err, navidrome.ErrNotFound) {
			return err
		}
	}
	if remote.ID == "" {
		id, err := m.nav.FindOrCreate(ctx, "CineRoute chart "+p.ID)
		if err != nil {
			return err
		}
		p.NavidromeID, p.NavidromeOwner = id, m.nav.Username
		// Persist before applying the public name so restart can find this ID.
		if err := m.save(ctx, *p, p.Revision); err != nil {
			return err
		}
		remote, err = m.nav.Playlist(ctx, id)
		if err != nil {
			return err
		}
	}
	if !strings.EqualFold(remote.Owner, m.nav.Username) {
		return fmt.Errorf("the Navidrome playlist belongs to another account")
	}
	songsChanged := !sameSongs(remote.Songs, songs)
	if songsChanged {
		if err := m.nav.ReplaceSongs(ctx, p.NavidromeID, songs); err != nil {
			return err
		}
	}
	comment := "Spotify chart date: " + p.ChartDate
	metadataChanged := !remote.Public || remote.Name != name || remote.Comment != comment
	if metadataChanged {
		if err := m.nav.MakePublic(ctx, p.NavidromeID, name, comment); err != nil {
			return err
		}
	}
	if songsChanged || metadataChanged {
		updated, err := m.nav.Playlist(ctx, p.NavidromeID)
		if err != nil {
			return err
		}
		if !updated.Public || updated.Name != name || !sameSongs(updated.Songs, songs) {
			return fmt.Errorf("Navidrome did not save the expected playlist")
		}
	}
	return nil
}

func sameSongs(current []navidrome.Song, wanted []string) bool {
	if len(current) != len(wanted) {
		return false
	}
	for i, song := range current {
		if song.ID != wanted[i] {
			return false
		}
	}
	return true
}

func stale(err error) error {
	if err == nil || errors.Is(err, ErrNotFound) {
		return ErrStale
	}
	return err
}
