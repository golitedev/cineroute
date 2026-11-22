package charts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"cineroute/internal/music/config"
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
	db             *sql.DB
	catalog        catalog
	nav            *navidrome.Client
	jobs           downloadQueue
	importRoot     string
	fetch          func(context.Context, string) (Snapshot, error)
	lookupIdentity func(context.Context, string) (trackIdentity, error)
	wake           chan struct{}
	phases         sync.Map // playlist ID to syncPhase for the current worker pass

	// Settings changes and external playlist writes must not overlap.
	mutations  sync.Mutex
	adminUntil time.Time
	lastScan   time.Time
}

func New(paths config.Paths, catalog catalog, nav *navidrome.Client, queue downloadQueue) (*Manager, error) {
	db, err := openStore(paths.Data)
	if err != nil {
		return nil, err
	}
	metadata := &identityClient{
		spotify: spotifyTrackURL, musicBrainz: musicBrainzURL,
		client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	importRoot := paths.HostLibrary
	if importRoot == "" {
		importRoot = paths.Library
	}
	m := &Manager{db: db, catalog: catalog, nav: nav, jobs: queue, importRoot: importRoot,
		fetch: fetchKworb, lookupIdentity: metadata.lookup, wake: make(chan struct{}, 1)}
	if err := m.restoreImportPaths(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return m, nil
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
		previousState, previousReason := e.State, e.Reason
		if err := m.resolve(ctx, p, e, now); err != nil {
			if errors.Is(err, ErrStale) {
				return err
			}
			e.State, e.Reason = "retrying", err.Error()
			e.NextAttempt = now.Add(retryFetch)
			dirty = true
			break
		}
		if e.State == "needs_review" && (previousState != e.State || previousReason != e.Reason) {
			slog.Info("chart track needs review", "playlist", p.Settings.displayName(), "chart_date", p.ChartDate,
				"spotify_id", e.SpotifyID, "track", e.Title, "artists", e.Artists, "reason", e.Reason)
		}
		dirty = true
		if e.State == "retrying" && e.QobuzFailures > 0 {
			// Avoid making the rest of this batch's Qobuz requests after a failure.
			break
		}
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
	log := slog.With("playlist", p.Settings.displayName(), "chart_date", p.ChartDate,
		"spotify_id", e.SpotifyID, "rank", e.Rank, "track", e.Title, "artists", e.Artists)
	selected, err := m.qobuzMatch(ctx, e.SpotifyID)
	if err != nil {
		return err
	}
	searchTrack, artist := e.Track, e.Artists[0]
	var identity trackIdentity
	e.Candidates = nil
	if selected.ID != "" {
		e.Recording = selected
		searchTrack.Title = selected.Title
		if selected.Artist != "" {
			artist = selected.Artist
		}
	} else {
		identity, err = m.identity(ctx, e.SpotifyID)
		if err != nil {
			return err
		}
		if e.Recording.ID == "" {
			e.Recording, err = m.recording(ctx, e.SpotifyID)
			if err != nil {
				return err
			}
		}
		if e.Recording.ID != "" && identity.ISRC != "" && !equalISRC(identity.ISRC, e.Recording.ISRC) {
			e.Recording = qobuz.Recording{}
			e.JobID, e.IndexSince = "", time.Time{}
		}
		if e.JobID == "" && e.Recording.ID != "" && (catalogScore(e.Track, e.Recording, identity) == 0 ||
			!compatibleDurations(identity.Duration, e.Recording.Duration)) {
			e.Recording = qobuz.Recording{}
		}
	}
	refreshIdentity := func() (bool, error) {
		delay := retryTrack
		if identity.ISRC != "" || identity.Album != "" || identity.Duration > 0 {
			delay = 7 * 24 * time.Hour
		}
		if identity.Manual || now.Sub(time.Unix(identity.CheckedAt, 0)) < delay {
			return false, nil
		}
		resolved, lookupErr := m.lookupIdentity(ctx, e.SpotifyID)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// Keep useful evidence across partial responses or provider outages.
		if resolved.ISRC == "" {
			resolved.ISRC = identity.ISRC
		}
		if resolved.Album == "" {
			resolved.Album, resolved.Number = identity.Album, identity.Number
		}
		if resolved.Duration == 0 {
			resolved.Duration = identity.Duration
		}
		if err := m.cacheIdentity(ctx, e.SpotifyID, resolved, now); err != nil {
			return false, err
		}
		resolved.CheckedAt = now.Unix()
		log.Info("chart recording metadata lookup", "isrc", resolved.ISRC, "album", resolved.Album,
			"track_number", resolved.Number, "duration_seconds", resolved.Duration, "lookup_error", lookupErr)
		changed := identity.ISRC != resolved.ISRC || identity.Album != resolved.Album ||
			identity.Duration != resolved.Duration || identity.Number != resolved.Number
		identity = resolved
		return changed, nil
	}
	title := trackSearchTitle(searchTrack)
	songs, err := m.nav.Search(ctx, title, artist)
	if err != nil {
		return err
	}
	var song navidrome.Song
	var ambiguous bool
	if selected.ID != "" {
		song = chooseQobuzMatch(selected, songs)
	} else {
		song, ambiguous = chooseSongWithIdentity(e.Track, e.Recording, songs, identity)
		if song.ID == "" && e.JobID == "" && e.Recording.ID == "" && (ambiguous || slices.ContainsFunc(songs, func(song navidrome.Song) bool {
			return song.ID != "" && !acousticConflict(e.Track, song.Title, song.Album) && matchesLead(e.Track, song.Title, song.Artist)
		})) {
			// Try to resolve existing library copies before making a Qobuz request.
			changed, err := refreshIdentity()
			if err != nil {
				return err
			}
			if changed {
				song, ambiguous = chooseSongWithIdentity(e.Track, e.Recording, songs, identity)
			}
		}
	}
	if song.ID != "" {
		log.Debug("chart track reused from Navidrome", "song_id", song.ID, "album", song.Album, "isrc", song.ISRC, "duration_seconds", song.Duration)
		e.setReady(song, now)
		return nil
	}
	e.SongID, e.SongAlbum = "", nil
	if e.JobID != "" {
		if e.IndexSince.IsZero() {
			e.IndexSince = now
		}
		if now.Sub(e.IndexSince) >= scanGrace {
			log.Info("chart track missing after Navidrome indexing", "job_id", e.JobID, "navidrome_query", title,
				"navidrome_results", len(songs), "qobuz_track_id", e.Recording.ID, "qobuz_album_id", e.Recording.AlbumID, "isrc", e.Recording.ISRC)
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
			if ctx.Err() != nil {
				return ctx.Err()
			}
			e.retryQobuz(now, "Qobuz track search failed")
			var response *qobuz.APIError
			httpStatus := 0
			if errors.As(err, &response) {
				httpStatus = response.Status
			}
			log.Warn("chart Qobuz search failed", "query", query, "http_status", httpStatus, "retry_at", e.NextAttempt, "consecutive_failures", e.QobuzFailures)
			return nil
		}
		e.QobuzFailures = 0
		recording, reason, evaluation := evaluateRecordings(e.Track, candidates, identity)
		initialReason := reason
		logSearch := func(result string) {
			// A JSON field preserves the field names even with the default text logger.
			details, _ := json.Marshal(evaluation)
			log.Info("chart Qobuz search evaluated", "query", query, "result", result, "reason", reason,
				"initial_reason", initialReason, "verified_isrc", identity.ISRC, "chart_album", identity.Album,
				"chart_duration_seconds", identity.Duration, "chart_track_number", identity.Number,
				"metadata_checked_at", identity.CheckedAt,
				"navidrome_results", len(songs), "navidrome_ambiguous", ambiguous,
				"qobuz_results", len(candidates), "selected_track_id", recording.ID, "selected_album_id", recording.AlbumID,
				"evaluation", string(details))
		}
		if reason != "" || ambiguous {
			changed, err := refreshIdentity()
			if err != nil {
				return err
			}
			if changed {
				recording, reason, evaluation = evaluateRecordings(e.Track, candidates, identity)
				song, ambiguous = chooseSongWithIdentity(e.Track, qobuz.Recording{}, songs, identity)
				if song.ID != "" {
					logSearch("reused Navidrome track using chart recording metadata")
					e.setReady(song, now)
					return nil
				}
			}
		}
		if reason != "" {
			if ambiguous {
				reason = "Multiple Navidrome recordings match. " + reason
			}
			logSearch("needs review")
			e.Candidates = evaluation.reviewCandidates(e.Track)
			if ambiguous {
				e.Candidates = append(e.Candidates, libraryReviewCandidates(e.Track, songs)...)
			}
			e.State, e.Reason, e.NextAttempt = "needs_review", reason, now.Add(retryTrack)
			return nil
		}
		logSearch("catalog recording selected")
		e.Recording = recording
		if !identity.Manual {
			if err := m.remember(ctx, e.SpotifyID, recording); err != nil {
				return err
			}
		}
		song, ambiguous = chooseSongWithIdentity(e.Track, recording, songs, identity)
		if song.ID != "" {
			e.setReady(song, now)
			return nil
		}
	}
	if ambiguous {
		details, _ := json.Marshal(songs)
		log.Info("chart Navidrome match ambiguous", "query", title, "qobuz_track_id", e.Recording.ID,
			"verified_isrc", identity.ISRC, "candidates", string(details))
		e.State, e.Reason = "needs_review", "Multiple Navidrome recordings match this track"
		e.Candidates = libraryReviewCandidates(e.Track, songs)
		e.NextAttempt = now.Add(retryTrack)
		return nil
	}
	if selected.ID != "" && !selected.Streamable {
		e.State, e.Reason, e.NextAttempt = "needs_review", "The selected track is unavailable for this Qobuz account", now.Add(retryTrack)
		return nil
	}

	job, err := m.enqueue(ctx, p, e)
	switch {
	case errors.Is(err, jobs.ErrQueueFull):
		e.QobuzFailures = 0
		e.State, e.Reason, e.NextAttempt = "waiting", "Waiting for room in the automatic download queue", now.Add(workInterval)
	case errors.Is(err, jobs.ErrNotStreamable):
		e.QobuzFailures = 0
		e.State, e.Reason, e.NextAttempt = "needs_review", "The album is unavailable for this Qobuz account", now.Add(retryTrack)
	case errors.Is(err, ErrStale):
		return err
	case err != nil:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		e.retryQobuz(now, err.Error())
	default:
		e.QobuzFailures = 0
		log.Info("chart album job linked", "job_id", job.ID, "job_status", job.Status, "qobuz_track_id", e.Recording.ID,
			"qobuz_album_id", e.Recording.AlbumID, "isrc", e.Recording.ISRC, "manual_match", selected.ID != "")
		e.State, e.Reason, e.JobID, e.NextAttempt = string(job.Status), "", job.ID, now.Add(workInterval)
		if job.Status.Finished() {
			e.State = "indexing"
		}
	}
	return nil
}

func (e *entry) retryQobuz(now time.Time, reason string) {
	const maxFailures = 6 // 1, 2, 4, 8, 16, then 24 hours.
	e.QobuzFailures = min(e.QobuzFailures+1, maxFailures)
	delay := min(time.Hour<<(e.QobuzFailures-1), retryTrack)
	e.State, e.Reason, e.NextAttempt = "retrying", reason, now.Add(delay)
}

func (e *entry) setReady(song navidrome.Song, now time.Time) {
	album := song.Album
	e.State, e.Reason, e.SongID, e.SongAlbum = "ready", "", song.ID, &album
	e.JobID, e.IndexSince, e.NextAttempt = "", time.Time{}, now.Add(retryTrack)
	e.QobuzFailures = 0
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
