package requests

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"cineroute/internal/prowlarr"
	"cineroute/internal/tmdb"
	"cineroute/internal/torrentmeta"
)

type Engine struct {
	Store       *Store
	TMDB        *tmdb.Client
	Prowlarr    *prowlarr.Client
	Backend     Backend
	wake        chan struct{}
	mu          sync.Mutex
	lastNetwork time.Time
	owner       string
	lease       atomic.Bool
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// LibraryChanged runs after a persisted download changes state.
	LibraryChanged func()
}

func NewEngine(store *Store, tmdbClient *tmdb.Client, indexers *prowlarr.Client, backend Backend) *Engine {
	return &Engine{Store: store, TMDB: tmdbClient, Prowlarr: indexers, Backend: backend, wake: make(chan struct{}, 1), owner: NewID()}
}
func (e *Engine) Wake() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
func (e *Engine) Start(ctx context.Context) {
	ctx, e.cancel = context.WithCancel(ctx)
	e.wg.Add(2)
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				ok, err := e.Store.Lease(e.owner, false)
				e.lease.Store(err == nil && ok)
			}
		}
	}()
	go func() {
		defer e.wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		e.Wake()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-e.wake:
			}
			if err := e.Tick(ctx); err != nil && ctx.Err() == nil {
				slog.Error("request worker", "err", err)
			}
		}
	}()
}
func (e *Engine) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
	_, _ = e.Store.Lease(e.owner, true)
}
func (e *Engine) pace(ctx context.Context, seconds int) error {
	if !e.lease.Load() {
		return fmt.Errorf("request worker lease is unavailable")
	}
	if delay := time.Until(e.lastNetwork.Add(time.Duration(seconds) * time.Second)); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	e.lastNetwork = time.Now()
	return nil
}
func (e *Engine) Tick(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	ok, err := e.Store.Lease(e.owner, false)
	if err != nil {
		return err
	}
	e.lease.Store(ok)
	if !ok {
		return nil
	}
	settings, err := e.Store.Load()
	if err != nil {
		return err
	}
	jobs, err := e.Store.Jobs("")
	if err != nil {
		return err
	}
	downloads, err := e.Store.Downloads()
	if err != nil {
		return err
	}
	for i, d := range downloads {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.Status != "planned" && d.Status != "downloading" {
			continue
		}
		if d.Status == "planned" {
			needed := false
			for _, job := range jobs {
				if job.Status != "pending" && !terminal(job.Status) && scope(d, job) {
					needed = true
					break
				}
			}
			if !needed {
				d.Status = "cancelled"
				d.TorrentData = nil
				downloads[i] = d
				if err := e.Store.SaveDownload(d); err != nil {
					return err
				}
				continue
			}
		}
		observed, err := e.Backend.Observe(ctx, d)
		if err != nil {
			observed = d
			observed.Error = err.Error()
		} else if observed.Status != "failed" {
			observed.Error = ""
		}
		if err == nil && settings.RetryFailedDownloads && !observed.Paused && observed.Status == "downloading" && settings.StalledMinutes > 0 && time.Since(observed.LastProgress) > time.Duration(settings.StalledMinutes)*time.Minute {
			if pauseErr := e.Backend.Pause(ctx, observed.Hash); pauseErr == nil {
				observed.Status = "failed"
				observed.Error = "Stalled request torrent paused; partial files retained"
			}
		}
		if observed.Status == "failed" && observed.FailedAt.IsZero() {
			observed.FailedAt = time.Now().UTC()
		}
		if saveErr := e.Store.SaveDownload(observed); saveErr != nil {
			return saveErr
		}
		if d.Status != observed.Status && e.LibraryChanged != nil {
			e.LibraryChanged()
		}
		downloads[i] = observed
	}
	if completion, ok := e.Backend.(CompletionBackend); ok {
		for i, d := range downloads {
			if d.MediaType != "movie" || d.Status != "complete" || !d.AutoSubtitles || d.SubtitlesQueued {
				continue
			}
			if err := completion.QueueMovieSubtitles(ctx, d); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				slog.Warn("request subtitles awaiting queue", "request_id", d.RequestID, "err", err)
				continue
			}
			d.SubtitlesQueued = true
			if err := e.Store.SaveDownload(d); err != nil {
				return err
			}
			downloads[i] = d
		}
	}
	jobs, err = e.Store.Jobs("")
	if err != nil {
		return err
	}
	slices.Reverse(jobs)
	for _, job := range jobs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if job.Status == "pending" || job.Status == "complete" || terminal(job.Status) {
			continue
		}
		if err := e.process(ctx, &job, settings, &downloads); err != nil {
			job.Status = "waiting"
			job.Message = "Waiting for a service or library check"
			job.AdminError = err.Error()
			job.NextSearch = time.Now().Add(time.Duration(settings.RetryIntervalHours) * time.Hour)
		}
		if err = e.Store.Update(job); err != nil {
			return err
		}
	}
	return nil
}

func scope(d Download, j Job) bool {
	return (!j.SpanishAudio || slices.Contains(d.Languages.Audio, "es")) && d.MediaType == j.Media.MediaType && d.TMDBID == j.Media.ID && d.Target == j.Target
}
func active(d Download) bool { return d.Status == "planned" || d.Status == "downloading" }

// DownloadFileIndex reports which files belong only to unfinished attempts and
// the sizes expected from completed ones. A completed attempt wins for the same
// path, so a retry through another tracker cannot hide a finished file from the
// library inventory behind the failed attempt it replaced.
func DownloadFileIndex(downloads []Download) (excluded map[string]bool, sizes map[string]int64) {
	excluded = map[string]bool{}
	sizes = map[string]int64{}
	for _, d := range downloads {
		for path, size := range d.FileSizes {
			if d.Status != "complete" {
				excluded[path] = true
				continue
			}
			sizes[path] = size
		}
	}
	for path := range sizes {
		delete(excluded, path)
	}
	return excluded, sizes
}

func (e *Engine) process(ctx context.Context, job *Job, settings Settings, downloads *[]Download) error {
	p := ProfileForJob(settings, *job)
	trackers := settings.TrackersForJob(*job)
	if job.Media.MediaType == "tv" && len(job.Seasons) == 0 {
		job.Status = "waiting"
		job.Message = "Select a season to continue this request"
		return nil
	}
	// A finished request download is final. Do not scan the library, search
	// again, or touch qBittorrent after qBittorrent reported the files at their
	// verified destination. Manual torrent changes are the user's business.
	if job.Media.MediaType == "movie" {
		for _, d := range *downloads {
			if scope(d, *job) && d.Status == "complete" && !d.AddedAt.Before(job.CreatedAt) && (job.RetryAt.IsZero() || d.CompletedAt.After(job.RetryAt)) {
				job.Status = "complete"
				job.Message = "Ready in the library"
				job.Progress = 1
				return nil
			}
		}
	}
	inventory, err := e.Backend.Inventory(ctx, *job, *downloads)
	if err != nil {
		return err
	}
	inProgress := map[string]bool{}
	partialProgress := map[string]float64{}
	failed := map[string]bool{}
	activeCount := 0
	ownActive := false
	progress := 0.0
	for _, d := range *downloads {
		if active(d) {
			activeCount++
		}
		if !scope(d, *job) {
			continue
		}
		if d.Status == "failed" {
			failed[d.ReleaseKey] = true
			if !settings.RetryFailedDownloads && (job.RetryAt.IsZero() || !job.RetryAt.After(d.FailedAt)) {
				job.Status = "error"
				job.Message = "A download needs administrator attention"
				job.AdminError = d.Error
				return nil
			}
		}
		if active(d) && d.Error != "" {
			job.Status = "error"
			job.Message = "A download is awaiting verification"
			job.AdminError = d.Error
			return nil
		}
		if active(d) {
			ownActive = true
			progress = d.Progress
			for _, key := range d.Episodes {
				inProgress[key] = true
				partialProgress[key] = d.Progress
			}
			if d.Error != "" {
				job.AdminError = d.Error
			}
		}
	}
	if job.Media.MediaType == "movie" && inventory.Movie != nil && (!job.SpanishAudio || inventory.Movie.Conforms) && (!p.UpgradeAllowed || atCutoff(*inventory.Movie, p)) {
		job.Status = "complete"
		job.Message = "Ready in the library"
		job.Progress = 1
		return nil
	}
	if job.Media.MediaType == "movie" && ownActive {
		job.Status = "downloading"
		job.Message = "Downloading your movie"
		job.Progress = progress
		return nil
	}
	wanted := map[string]bool{}
	for _, key := range job.Episodes {
		wanted[key] = true
	}
	due := job.NextSearch.IsZero() || !time.Now().Before(job.NextSearch)
	if due {
		if e.TMDB == nil {
			return fmt.Errorf("TMDB is not configured")
		}
		media, err := e.TMDB.Details(ctx, job.Media.MediaType, job.Media.ID)
		if err != nil {
			return err
		}
		if media.Adult {
			return fmt.Errorf("adult titles are not supported")
		}
		job.Media = media
		if job.Media.MediaType == "tv" {
			wanted, err = e.wantedEpisodes(ctx, job, settings)
			if err != nil {
				return err
			}
			job.Episodes = []string{}
			for key := range wanted {
				job.Episodes = append(job.Episodes, key)
			}
			slices.Sort(job.Episodes)
		}
	}
	missing := map[string]bool{}
	available := 0
	for key := range wanted {
		if old, ok := inventory.Episodes[key]; ok && (!job.SpanishAudio || old.Conforms) {
			available++
		} else if !inProgress[key] {
			missing[key] = true
		}
	}
	if job.Media.MediaType == "tv" {
		total := len(wanted)
		if total > 0 {
			sum := float64(available)
			for key, value := range partialProgress {
				if wanted[key] {
					sum += value
				}
			}
			job.Progress = sum / float64(total)
		}
		if ownActive {
			job.Status = "downloading"
			job.Message = "Downloading requested episodes"
			if !due || activeCount >= settings.MaxConcurrentDownloads {
				return nil
			}
		}
		upgradeNeeded := false
		if p.UpgradeAllowed {
			for key := range wanted {
				if old, ok := inventory.Episodes[key]; ok && !atCutoff(old, p) {
					upgradeNeeded = true
					break
				}
			}
		}
		if len(missing) == 0 && !ownActive && !upgradeNeeded && total > 0 {
			job.Progress = 1
			job.Status = "complete"
			job.Message = "Requested episodes are ready in the library"
			future := false
			for _, season := range job.Media.Seasons {
				if slices.Contains(job.Seasons, season.Number) {
					count := 0
					for key := range wanted {
						var n, ep int
						fmt.Sscanf(key, "s%de%d", &n, &ep)
						if n == season.Number {
							count++
						}
					}
					if count < season.EpisodeCount {
						future = true
					}
				}
			}
			if future {
				job.Status = "monitoring"
				job.Message = "Available episodes are ready; monitoring future episodes"
			}
			if due {
				job.NextSearch = time.Now().Add(time.Duration(settings.RetryIntervalHours) * time.Hour)
			}
			return nil
		}
	}
	if !due {
		return nil
	}
	if (job.Media.MediaType == "movie" && !settings.AllowMovies) || (job.Media.MediaType == "tv" && !settings.AllowTV) {
		job.Status = "waiting"
		job.Message = "Requests for this media type are disabled"
		return nil
	}
	job.NextSearch = time.Now().Add(time.Duration(settings.RetryIntervalHours) * time.Hour)
	if settings.ReleaseDelayMinutes > 0 && time.Now().Before(job.ApprovedAt.Add(time.Duration(settings.ReleaseDelayMinutes)*time.Minute)) {
		job.Status = "waiting"
		job.Message = "Waiting for the configured release delay"
		job.NextSearch = job.ApprovedAt.Add(time.Duration(settings.ReleaseDelayMinutes) * time.Minute)
		return nil
	}
	if job.Media.MediaType == "movie" && !movieAvailable(job.Media, settings.MovieAvailability) {
		job.Status = "waiting"
		job.Message = "Waiting for the movie release"
		return nil
	}
	if job.Media.MediaType == "tv" && len(wanted) == 0 {
		job.Status = "waiting"
		job.Message = "Waiting for requested episodes to air"
		return nil
	}
	if job.Media.MediaType == "tv" && len(missing) == 0 && !p.UpgradeAllowed {
		if ownActive {
			return nil
		}
		return nil
	}
	if activeCount >= settings.MaxConcurrentDownloads {
		job.Status = "queued"
		job.Message = "Waiting for a download slot"
		job.NextSearch = time.Now().Add(30 * time.Second)
		return nil
	}
	if !e.Prowlarr.Configured() || len(trackers) == 0 {
		job.Status = "waiting"
		job.Message = "Waiting for configured trackers"
		return nil
	}
	indexers, err := e.Prowlarr.Indexers(ctx)
	if err != nil {
		return fmt.Errorf("Prowlarr indexer configuration is unavailable")
	}
	ids := []int{}
	priorities := map[int]int{}
	for _, tracker := range trackers {
		for _, indexer := range indexers {
			if indexer.ID == tracker.ID && indexer.Enable && indexer.Protocol == "torrent" {
				ids = append(ids, indexer.ID)
				priorities[indexer.ID] = tracker.Priority
			}
		}
	}
	if len(ids) == 0 {
		job.Status = "waiting"
		job.Message = "Selected trackers are unavailable or disabled"
		return nil
	}
	queries := []string{job.Media.DisplayTitle() + " " + strconv.Itoa(job.Media.Year())}
	if job.Media.MediaType == "tv" {
		keys := []string{}
		for key := range missing {
			keys = append(keys, key)
		}
		if len(keys) == 0 {
			for key := range wanted {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		var season, episode int
		fmt.Sscanf(keys[0], "s%de%d", &season, &episode)
		queries = []string{fmt.Sprintf("%s S%02dE%02d", job.Media.DisplayTitle(), season, episode), fmt.Sprintf("%s S%02d", job.Media.DisplayTitle(), season)}
		if settings.PreferSeasonPacks {
			slices.Reverse(queries)
		}
	}
	job.Status = "searching"
	job.Message = "Finding a release that matches the library settings"
	job.AdminError = ""
	if err = e.Store.Update(*job); err != nil {
		return err
	}
	candidates := []Candidate{}
	seen := map[string]bool{}
	rejected := map[string]int{}
	trackerRejected := map[int]map[string]int{}
	accepted := map[int]int{}
	rejectRelease := func(release prowlarr.Release, reason string) {
		rejected[reason]++
		if trackerRejected[release.IndexerID] == nil {
			trackerRejected[release.IndexerID] = map[string]int{}
		}
		trackerRejected[release.IndexerID][reason]++
	}
	for _, query := range queries {
		if err = e.pace(ctx, settings.SearchIntervalSeconds); err != nil {
			return err
		}
		releases, err := e.Prowlarr.SearchIndexers(ctx, ids, query, 100)
		if err != nil {
			return err
		}
		if job.SpanishAudio || len(p.PreferredAudioLanguages)+len(p.PreferredSubtitleLanguages) > 0 || p.PreferTextSubtitles || slices.Contains(settings.SelectionOrder, "video_bitrate") {
			// Read native reports only for otherwise eligible, selected releases.
			// Metadata may also confirm an existing strict audio-language filter.
			precheck := p
			precheck.AudioLanguages = nil
			precheck.RequireSpanishAudio = false
			eligible := []prowlarr.Release{}
			for _, release := range releases {
				priority, allowed := priorities[release.IndexerID]
				key := ReleaseKey(release)
				if !allowed || seen[key] || failed[key] {
					continue
				}
				if _, err := Evaluate(release, job.Media, precheck, settings.MinSeeders, priority); err != nil {
					rejectRelease(release, err.Error())
					continue
				}
				eligible = append(eligible, release)
			}
			job.Message = "Reading tracker media details"
			if err := e.Store.Update(*job); err != nil {
				return err
			}
			releases = e.Prowlarr.EnrichLanguageMetadata(ctx, eligible, query, func() error { return e.pace(ctx, settings.SearchIntervalSeconds) })
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		for _, release := range releases {
			priority, allowed := priorities[release.IndexerID]
			key := ReleaseKey(release)
			if !allowed || seen[key] || failed[key] {
				continue
			}
			seen[key] = true
			candidate, err := Evaluate(release, job.Media, p, settings.MinSeeders, priority)
			if err != nil {
				rejectRelease(release, err.Error())
				continue
			}
			if job.Media.MediaType == "movie" && inventory.Movie != nil && !ImprovesExisting(candidate.Quality, candidate.Score, *inventory.Movie, p) {
				continue
			}
			candidates = append(candidates, candidate)
			accepted[release.IndexerID]++
		}
	}
	for _, id := range ids {
		// Log policy decisions without tracker credentials, proxy URLs or paths.
		slog.Info("request tracker results", "request_id", job.ID, "indexer_id", id, "priority", priorities[id], "accepted", accepted[id], "rejected", trackerRejected[id])
	}
	prepareBitrateRanking(candidates, settings.SelectionOrder, settings.PreferSeasonPacks)
	sort.SliceStable(candidates, func(i, j int) bool {
		if job.SpanishAudio && settings.IsSpanishTracker(candidates[i].Release.IndexerID) != settings.IsSpanishTracker(candidates[j].Release.IndexerID) {
			return settings.IsSpanishTracker(candidates[i].Release.IndexerID)
		}
		return Better(candidates[i], candidates[j], settings.SelectionOrder, settings.PreferSeasonPacks)
	})
	for i, candidate := range candidates {
		if i >= 10 {
			break
		}
		if err = e.pace(ctx, settings.SearchIntervalSeconds); err != nil {
			return err
		}
		raw, err := e.Prowlarr.DownloadTorrent(ctx, candidate.Release.DownloadURL)
		if err != nil {
			rejected["torrent metadata unavailable"]++
			slog.Info("request candidate skipped", "request_id", job.ID, "indexer_id", candidate.Release.IndexerID, "reason", "torrent metadata unavailable")
			continue
		}
		meta, err := torrentmeta.Parse(raw)
		if err != nil {
			rejected["invalid torrent metadata"]++
			slog.Info("request candidate skipped", "request_id", job.ID, "indexer_id", candidate.Release.IndexerID, "reason", "invalid torrent metadata")
			continue
		}
		covered := map[string]bool{}
		for key := range inProgress {
			covered[key] = true
		}
		for key, old := range inventory.Episodes {
			if (!job.SpanishAudio || old.Conforms) && (!p.UpgradeAllowed || !ImprovesExisting(candidate.Quality, candidate.Score, old, p)) {
				covered[key] = true
			}
		}
		keys, err := ValidateTorrent(meta, candidate, *job, p, wanted, covered)
		if err != nil {
			rejected[err.Error()]++
			slog.Info("request candidate skipped", "request_id", job.ID, "indexer_id", candidate.Release.IndexerID, "reason", err.Error())
			continue
		}
		// Recheck lease ownership immediately before a filesystem/download write.
		owned, err := e.Store.Lease(e.owner, false)
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("request worker lease changed")
		}
		planned := false
		recordSource := func(d Download) Download {
			d.AutoSubtitles = job.Media.MediaType == "movie" && settings.MovieAutoSubtitles
			d.RequestID, d.RequestedBy = job.ID, job.Owner
			d.IndexerID, d.Indexer, d.InfoURL = candidate.Release.IndexerID, candidate.Release.Indexer, candidate.Release.InfoURL
			d.Languages, d.LanguageScore = candidate.Release.Languages, candidate.LanguageScore
			d.SubtitleFormatScore = candidate.SubtitleFormatScore
			if d.Indexer == "" {
				for _, tracker := range trackers {
					if tracker.ID == d.IndexerID {
						d.Indexer = tracker.Name
						break
					}
				}
			}
			return d
		}
		download, err := e.Backend.Submit(ctx, *job, candidate, meta, raw, keys, func(d Download) error {
			if err := e.Store.SaveDownload(recordSource(d)); err != nil {
				return err
			}
			planned = true
			return nil
		})
		if err != nil {
			if planned {
				job.Status = "error"
				job.Message = "A download is awaiting verification"
				job.AdminError = err.Error()
				return nil
			}
			rejected[err.Error()]++
			continue
		}
		download = recordSource(download)
		if err = e.Store.SaveDownload(download); err != nil {
			return err
		}
		if e.LibraryChanged != nil {
			e.LibraryChanged()
		}
		slog.Info("request release selected", "request_id", job.ID, "indexer_id", candidate.Release.IndexerID, "priority", candidate.Priority, "quality", candidate.Quality, "language_score", candidate.LanguageScore, "text_subtitle_score", candidate.SubtitleFormatScore, "keyword_score", candidate.Score, "video_bitrate_bps", candidate.Release.Languages.VideoBitrateBPS, "video_bitrate_rank", candidate.videoBitrateRank, "season_pack", candidate.Pack)
		*downloads = append(*downloads, download)
		job.Status = "downloading"
		job.Message = "Download started"
		job.NextSearch = time.Now()
		if download.Status == "complete" {
			job.Message = "Copy added to the library"
		}
		return nil
	}
	job.Status = "waiting"
	job.Message = "Waiting for a release that matches the library settings"
	if inventory.Movie != nil || available > 0 {
		job.Status = "monitoring"
		job.Message = "Available copies are ready; monitoring the remaining preferences"
	}
	if job.SpanishAudio {
		job.Message = "No release with confirmed Spanish audio was found for the requested movie or episodes. Untick Spanish audio and submit again to use normal library settings."
	}
	reasons := []string{}
	for reason, count := range rejected {
		reasons = append(reasons, fmt.Sprintf("%d × %s", count, reason))
	}
	slices.Sort(reasons)
	if len(reasons) > 8 {
		reasons = reasons[:8]
	}
	job.AdminError = strings.Join(reasons, "; ")
	return nil
}
func (e *Engine) wantedEpisodes(ctx context.Context, job *Job, settings Settings) (map[string]bool, error) {
	wanted := map[string]bool{}
	today := time.Now().UTC().Format("2006-01-02")
	for _, season := range job.Media.Seasons {
		if season.Number == 0 && !settings.IncludeSpecials {
			continue
		}
		selected := slices.Contains(job.Seasons, season.Number)
		if !selected {
			continue
		}
		episodes, err := e.TMDB.Season(ctx, job.Media.ID, season.Number)
		if err != nil {
			return nil, err
		}
		for _, episode := range episodes {
			if episode.AirDate == "" || episode.AirDate > today || episode.Number <= 0 {
				continue
			}
			wanted[EpisodeKey(season.Number, episode.Number)] = true
		}
	}
	return wanted, nil
}
func movieAvailable(media tmdb.Details, mode string) bool {
	if mode == "announced" {
		return true
	}
	today := time.Now().UTC().Format("2006-01-02")
	if mode == "in_cinemas" {
		return media.ReleaseDate != "" && media.ReleaseDate <= today
	}
	known := false
	for _, country := range media.ReleaseDates.Results {
		for _, date := range country.Dates {
			if date.Type == 4 || date.Type == 5 {
				known = true
				if len(date.Date) >= 10 && date.Date[:10] <= today {
					return true
				}
			}
		}
	}
	return !known && media.Status == "Released" && media.ReleaseDate != "" && media.ReleaseDate <= today
}

func atCutoff(old Existing, p Profile) bool {
	rank := slices.Index(p.Qualities, old.Quality)
	cutoff := slices.Index(p.Qualities, p.UpgradeUntil)
	return old.Conforms && rank >= 0 && rank <= cutoff && old.Score >= p.UpgradeUntilScore
}
