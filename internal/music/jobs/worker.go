package jobs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/flacfix"
	"cineroute/internal/music/library"
	"cineroute/internal/music/naming"
	"cineroute/internal/music/qobuz"
	"cineroute/internal/music/streamrip"
	"cineroute/internal/music/verify"
)

const maxProblems = 30

// Run processes the queue until ctx is cancelled. Albums are handled one at a
// time.
func (m *Manager) Run(ctx context.Context) {
	m.libraryCache.Start(ctx)
	defer m.libraryCache.Stop()
	for {
		job, jctx, cancel, ok := m.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}
		// Stop the job when the server shuts down.
		stop := context.AfterFunc(ctx, cancel)
		m.process(ctx, jctx, job)
		stop()
		cancel()
		m.mu.Lock()
		delete(m.running, job.ID)
		m.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
	}
}

// outcome is how an album ended.
type outcome struct {
	status   Status
	msg      Msg
	problems []ProblemItem
	rel      string
}

func (m *Manager) process(root, ctx context.Context, job Job) {
	log := m.log.With("job", job.ID, "album", job.AlbumID)
	staging := filepath.Join(m.downloadsDir(), job.ID)

	out := m.download(ctx, job, staging, log)

	// A server shutdown is not the user's cancel: queue the job again.
	if root.Err() != nil && out.status == StatusCancelled {
		os.RemoveAll(staging)
		m.update(job.ID, true, func(j *Job) {
			j.Status, j.Attempt, j.TracksDone, j.StartedAt = StatusQueued, 0, 0, nil
			j.set(M("interrupted_shutdown"))
		})
		m.mu.Lock()
		m.queued[job.ID] = time.Now()
		m.mu.Unlock()
		return
	}

	if out.status == StatusFailed && m.cfg.Download.KeepFailed && hasFiles(staging) {
		dst := filepath.Join(m.failedDir(), failedName(job))
		if err := os.MkdirAll(m.failedDir(), 0o755); err == nil && os.Rename(staging, dst) == nil {
			out.msg = out.msg.With("kept_path", dst)
		}
	}
	os.RemoveAll(staging)

	if len(out.problems) > maxProblems {
		extra := len(out.problems) - maxProblems
		out.problems = append(out.problems[:maxProblems], moreProblem(extra))
	}
	m.update(job.ID, true, func(j *Job) {
		j.Status = out.status
		j.set(out.msg)
		j.ProblemItems = append([]ProblemItem{}, out.problems...)
		j.Problems = problemTexts(j.ProblemItems)
		j.LibraryPath = out.rel
		j.FinishedAt = ptr(time.Now())
		if out.status == StatusCompleted {
			j.TracksDone = j.TracksTotal
		}
	})
	m.mu.Lock()
	m.trimLocked()
	_ = m.saveLocked()
	m.mu.Unlock()

	switch out.status {
	case StatusCompleted:
		m.libraryCache.Invalidate()
		log.Info("album added", "path", out.rel)
		if m.deps.AfterImport != nil {
			go m.deps.AfterImport(context.WithoutCancel(root), out.rel)
		}
	case StatusFailed:
		log.Warn("album not added", "reason", out.msg.English(), "problems", len(out.problems))
	default:
		log.Info("album finished", "status", out.status, "message", out.msg.English())
	}
}

func (m *Manager) download(ctx context.Context, job Job, staging string, log interface {
	Info(string, ...any)
	Warn(string, ...any)
}) outcome {
	cancelled := func() outcome { return outcome{status: StatusCancelled, msg: M("cancelled")} }
	fail := func(msg Msg, problems ...ProblemItem) outcome {
		return outcome{status: StatusFailed, msg: msg, problems: problems}
	}

	album, err := m.deps.Albums.Album(ctx, job.AlbumID)
	if err != nil {
		if ctx.Err() != nil {
			return cancelled()
		}
		return fail(M("error_album", "error", err.Error()))
	}
	m.update(job.ID, false, func(j *Job) {
		j.Title, j.Artist, j.Year, j.TracksTotal = album.FullTitle(), album.Artist, album.Year(), len(album.Tracks)
		if album.Cover != "" {
			j.Cover = album.Cover
		}
	})
	expected := expectedTracks(album)
	maxAttempts := job.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var last outcome
	tries := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		tries = attempt
		if attempt > 1 {
			delay := time.Duration(m.cfg.Download.RetryDelay) * time.Second
			m.update(job.ID, false, func(j *Job) {
				j.Status = StatusDownloading
				j.set(M("retry_wait", "attempt", attempt-1, "seconds", int(delay.Seconds())))
			})
			if err := m.deps.Sleep(ctx, delay); err != nil {
				return cancelled()
			}
		}
		if err := os.RemoveAll(staging); err != nil {
			return fail(M("error_staging", "error", err.Error()))
		}
		if err := os.MkdirAll(staging, 0o755); err != nil {
			return fail(M("error_staging", "error", err.Error()))
		}
		m.update(job.ID, true, func(j *Job) {
			j.Status, j.Attempt, j.TracksDone = StatusDownloading, attempt, 0
			j.set(M("downloading"))
			j.Problems, j.ProblemItems = []string{}, []ProblemItem{}
		})

		res, err := m.rip(ctx, job.ID, album, staging)
		if ctx.Err() != nil {
			return cancelled()
		}
		if err != nil {
			return fail(M("error_streamrip", "error", err.Error()))
		}
		if res.Failure == streamrip.FailureAppID {
			_ = streamrip.ForgetAppID(m.deps.Files)
		}
		if res.Failure.Fatal() {
			return fail(failureMsg(res.Failure), ripErrors(res)...)
		}

		last = m.check(ctx, job, album, expected, staging, res)
		if last.status != StatusFailed {
			return last
		}
		if ctx.Err() != nil {
			return cancelled()
		}
		log.Warn("attempt failed", "attempt", attempt, "reason", last.msg.English())
		if onlyUnavailable(last.problems) {
			// Retrying can't fetch tracks Qobuz doesn't offer.
			break
		}
		if res.Failure == streamrip.FailureRateLimited {
			if err := m.deps.Sleep(ctx, time.Minute); err != nil {
				return cancelled()
			}
		}
	}
	if tries > 1 {
		last.msg = last.msg.With("tries", tries)
	}
	return last
}

func (m *Manager) rip(ctx context.Context, jobID string, album qobuz.Album, staging string) (streamrip.Result, error) {
	pctx, stop := context.WithCancel(ctx)
	defer stop()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-pctx.Done():
				return
			case <-t.C:
				n := countAudio(staging)
				m.update(jobID, false, func(j *Job) {
					if n > j.TracksTotal {
						n = j.TracksTotal
					}
					j.TracksDone = n
				})
			}
		}
	}()
	return m.deps.Rip(ctx, streamrip.RunOptions{
		Command:    m.cfg.Download.RipCommand,
		ConfigPath: m.deps.Files.Config,
		Folder:     staging,
		Quality:    m.cfg.Qobuz.Quality,
		URL:        album.URL(),
		LogPath:    m.LogPath(jobID),
		Timeout:    time.Duration(m.cfg.Download.TimeoutMinutes) * time.Minute,
		Secrets:    []string{m.cfg.Qobuz.Token},
	})
}

// check verifies what streamrip left in staging and, when everything is
// there and intact, imports it into the library.
func (m *Manager) check(ctx context.Context, job Job, album qobuz.Album, expected []verify.Track, staging string, res streamrip.Result) outcome {
	failed := func(msg Msg, problems []ProblemItem) outcome {
		return outcome{status: StatusFailed, msg: msg, problems: problems}
	}
	if res.TimedOut {
		return failed(M("timeout", "minutes", m.cfg.Download.TimeoutMinutes), ripErrors(res))
	}

	m.update(job.ID, false, func(j *Job) {
		j.Status = StatusVerifying
		j.TracksDone = countAudio(staging)
		j.set(M("checking", "tracks", len(expected)))
	})
	albumDir, err := verify.FindAlbumDir(staging)
	if err != nil {
		msg := M("no_tracks")
		if res.Failure != streamrip.FailureNone {
			msg = failureMsg(res.Failure)
		}
		return failed(msg, ripErrors(res))
	}

	fixProblems, out, ok := m.fixFLAC(ctx, job, albumDir)
	if !ok {
		return out
	}

	vr, err := m.deps.Verify(ctx, albumDir, expected, verify.Options{
		FlacTest:   m.cfg.Download.VerifyFLAC,
		FlacBinary: "flac",
	})
	if err != nil {
		if ctx.Err() != nil {
			return outcome{status: StatusCancelled, msg: M("cancelled")}
		}
		return failed(M("error_verify", "error", err.Error()), nil)
	}
	if !vr.OK() || len(fixProblems) > 0 {
		problems := append(describe(album, vr.Problems), fixProblems...)
		bad := countTracks(vr.Problems)
		msg := M("incomplete", "failed", bad, "total", len(expected))
		if bad == 0 {
			msg = M("has_problems")
		}
		if res.Failure == streamrip.FailureRateLimited {
			msg = msg.With("rate_limited", true)
		}
		return failed(msg, problems)
	}

	return m.importAlbum(job, album, albumDir, vr)
}

// fixFLAC writes missing MD5 signatures and translates genres. Files it
// can't process (e.g. they fail to decode) are returned as problems, which
// fail the album after verification has listed everything else.
func (m *Manager) fixFLAC(ctx context.Context, job Job, albumDir string) ([]ProblemItem, outcome, bool) {
	if !m.cfg.Download.FixMD5 && m.genres == nil && !m.cfg.Metadata.CleanDescriptions {
		return nil, outcome{}, true
	}
	m.update(job.ID, false, func(j *Job) { j.set(M("fixing_flac")) })
	opts := flacfix.Options{
		FixMD5:            m.cfg.Download.FixMD5,
		CleanDescriptions: m.cfg.Metadata.CleanDescriptions,
		Confirm:           !m.cfg.Download.VerifyFLAC, // otherwise verify runs flac -t next
		FlacBin:           "flac",
		MetaflacBin:       "metaflac",
	}
	if m.genres != nil {
		opts.Genres = m.genres
	}
	rep, err := m.deps.Fix(ctx, albumDir, opts)
	if err != nil {
		if ctx.Err() != nil {
			return nil, outcome{status: StatusCancelled, msg: M("cancelled")}, false
		}
		return nil, outcome{status: StatusFailed, msg: M("error_fix", "error", err.Error())}, false
	}

	md5s, genreFiles, errs := rep.Counts()
	descriptions := rep.Descriptions()
	var lines []string
	if descriptions > 0 {
		lines = append(lines, fmt.Sprintf("Description cleaned in %d of %d FLAC files", descriptions, rep.Files))
	}
	var problems []ProblemItem
	if md5s > 0 {
		lines = append(lines, fmt.Sprintf("MD5 signature written to %d of %d FLAC files", md5s, rep.Files))
	}
	for _, c := range rep.Changes {
		rel, _ := filepath.Rel(albumDir, c.Path)
		if c.GenreTo != nil {
			lines = append(lines, fmt.Sprintf("Genre %q -> %q: %s", strings.Join(c.GenreFrom, "; "), strings.Join(c.GenreTo, "; "), rel))
		}
		if c.Err != nil {
			lines = append(lines, fmt.Sprintf("Error: %s: %v", rel, c.Err))
			problems = append(problems, fileProblem(rel, c.Err))
		}
	}
	if len(rep.UnknownGenres) > 0 {
		lines = append(lines, "Genres without an English translation (add them to [metadata.genre_translations]): "+strings.Join(rep.UnknownGenres, ", "))
		m.log.Warn("untranslated genres", "job", job.ID, "genres", strings.Join(rep.UnknownGenres, ", "))
	}
	m.appendLog(job.ID, lines...)
	if genreFiles > 0 || md5s > 0 || descriptions > 0 || errs > 0 {
		m.log.Info("flac files fixed", "job", job.ID, "md5", md5s, "genres", genreFiles, "descriptions", descriptions, "errors", errs)
	}
	return problems, outcome{}, true
}

// appendLog adds CineRoute's own notes to the job log.
func (m *Manager) appendLog(jobID string, lines ...string) {
	if len(lines) == 0 {
		return
	}
	f, err := os.OpenFile(m.LogPath(jobID), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintf(f, "cineroute: %s\n", l)
	}
}

func (m *Manager) importAlbum(job Job, album qobuz.Album, albumDir string, vr verify.Result) outcome {
	m.update(job.ID, true, func(j *Job) {
		j.Status = StatusImporting
		j.TracksDone = j.TracksTotal
		j.set(M("importing"))
	})
	rel, err := m.folderFor(album, library.Quality{Format: vr.Format, BitDepth: vr.BitDepth, SampleRate: vr.SampleRate})
	if err != nil {
		return outcome{status: StatusFailed, msg: M("error_naming", "error", err.Error())}
	}
	lib := m.cfg.Paths.Library
	if library.Exists(lib, rel) {
		if m.cfg.Naming.IfExists != config.IfExistsReplace {
			return outcome{status: StatusSkipped, msg: M("already_in_library"), rel: rel}
		}
		aside, err := library.MoveAside(lib, rel, m.replacedDir())
		if err != nil {
			return outcome{status: StatusFailed, msg: M("error_move_aside", "error", err.Error())}
		}
		m.log.Info("existing album moved aside", "from", rel, "to", aside)
	}
	if _, err := library.Import(albumDir, lib, rel); err != nil {
		return outcome{status: StatusFailed, msg: M("error_import", "error", err.Error())}
	}
	msg := M("added")
	if vr.BitDepth > 0 && vr.SampleRate > 0 {
		msg = M("added", "format", strings.ToUpper(vr.Format), "bits", vr.BitDepth, "khz", khz(vr.SampleRate))
	}
	return outcome{status: StatusCompleted, msg: msg, rel: rel}
}

// --- helpers ----------------------------------------------------------------

func expectedTracks(a qobuz.Album) []verify.Track {
	out := make([]verify.Track, 0, len(a.Tracks))
	for _, t := range a.Tracks {
		out = append(out, verify.Track{
			ID:          t.ID,
			Disc:        t.Disc,
			Number:      t.Number,
			Title:       t.Title,
			DurationSec: t.DurationSec,
		})
	}
	return out
}

// describe turns verification problems into UI lines and explains tracks
// Qobuz does not offer for streaming.
func describe(a qobuz.Album, problems []verify.Problem) []ProblemItem {
	unavailable := map[[2]int]bool{}
	for _, t := range a.Tracks {
		if !t.Streamable {
			unavailable[[2]int{t.Disc, t.Number}] = true
		}
	}
	out := make([]ProblemItem, 0, len(problems))
	for _, p := range problems {
		out = append(out, verifyProblem(p, p.Reason == "missing" && unavailable[[2]int{p.Disc, p.Number}]))
	}
	return out
}

// onlyUnavailable reports whether every problem is a track Qobuz marks as
// not streamable.
func onlyUnavailable(problems []ProblemItem) bool {
	if len(problems) == 0 {
		return false
	}
	for _, p := range problems {
		if !p.Unavailable {
			return false
		}
	}
	return true
}

func countTracks(problems []verify.Problem) int {
	seen := map[[2]int]bool{}
	n := 0
	for _, p := range problems {
		if p.Number == 0 {
			continue
		}
		k := [2]int{p.Disc, p.Number}
		if !seen[k] {
			seen[k] = true
			n++
		}
	}
	return n
}

func ripErrors(res streamrip.Result) []ProblemItem {
	var out []ProblemItem
	seen := map[string]bool{}
	for _, e := range res.Errors {
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, ripProblem(e))
	}
	return out
}

func hasFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func failedName(j Job) string {
	name, err := naming.Render("{albumartist} - {title} [{id}]", map[string]string{
		"albumartist": j.Artist, "title": j.Title, "id": j.AlbumID,
	}, false)
	if err != nil {
		name = j.AlbumID
	}
	return time.Now().Format("20060102-150405") + " " + name
}

func khz(hz int) string {
	s := fmt.Sprintf("%.1f", float64(hz)/1000)
	return strings.TrimSuffix(s, ".0")
}
