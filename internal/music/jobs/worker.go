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
	tracks   int
}

func (m *Manager) process(root, ctx context.Context, job Job) {
	log := m.log.With("job", job.ID, "album", job.AlbumID)
	staging := filepath.Join(m.downloadsDir(), job.ID)
	defer os.RemoveAll(staging + ".verified")

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
			j.LyricsRequested = m.cfg.Lyrics.Enabled && m.deps.EnqueueLyrics != nil
			j.TracksDone = out.tracks
		}
	})
	// Enqueue after the completed-job receipt is durable, before history can be
	// trimmed. A startup reconciler repairs a crash between these two stores.
	if out.status == StatusCompleted && m.cfg.Lyrics.Enabled && m.deps.EnqueueLyrics != nil {
		if err := m.deps.EnqueueLyrics(job.ID, out.rel); err != nil {
			log.Error("enqueueing lyrics", "err", err)
		} else {
			m.ConfirmLyricsEnqueued(job.ID)
		}
	}
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
		last = m.check(ctx, job, album, expected, staging, res, attempt == maxAttempts || res.Failure.Fatal())
		if res.Failure.Fatal() && last.status == StatusFailed {
			return fail(failureMsg(res.Failure), ripErrors(res)...)
		}
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
func (m *Manager) check(ctx context.Context, job Job, album qobuz.Album, expected []verify.Track, staging string, res streamrip.Result, finalAttempt bool) outcome {
	failed := func(msg Msg, problems []ProblemItem) outcome {
		return outcome{status: StatusFailed, msg: msg, problems: problems}
	}
	if finalAttempt {
		if err := restoreVerified(staging); err != nil {
			return failed(M("error_verify", "error", err.Error()), nil)
		}
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
		FlacTest:   true, // Never import undecoded/corrupt audio.
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
		if finalAttempt || onlyUnavailable(problems) {
			return m.importPartial(ctx, job, album, albumDir, expected, vr, problems, fixProblems)
		}
		if err := retainVerified(staging, vr, fixProblems); err != nil {
			return failed(M("error_verify", "error", err.Error()), problems)
		}
		return failed(msg, problems)
	}

	return m.importAlbum(job, album, albumDir, vr)
}

// fixFLAC translates genres after Streamrip has validated and tagged audio. Files it
// can't process (e.g. they fail to decode) are returned as problems, which
// fail the album after verification has listed everything else.
func (m *Manager) fixFLAC(ctx context.Context, job Job, albumDir string) ([]ProblemItem, outcome, bool) {
	if m.genres == nil {
		return nil, outcome{}, true
	}
	m.update(job.ID, false, func(j *Job) { j.set(M("fixing_flac")) })
	opts := flacfix.Options{
		FixMD5:            false,                      // Streamrip validates and fills missing signatures.
		CleanDescriptions: false,                      // Streamrip writes plain-text descriptions.
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
		j.TracksDone = len(vr.Files)
		j.set(M("importing"))
	})
	rel, err := filepath.Rel(filepath.Join(m.downloadsDir(), job.ID), albumDir)
	if err != nil {
		return outcome{status: StatusFailed, msg: M("error_naming", "error", err.Error())}
	}
	incomplete := len(vr.Problems) > 0
	if incomplete {
		rel = strings.TrimSuffix(rel, " - INCOMPLETE") + " - INCOMPLETE"
	}
	lib := m.cfg.Paths.Library
	// Check identity again immediately before import, including albums added
	// by another downloader while this job was running.
	if existing := m.albumIDPath(album.ID); existing != "" && m.cfg.Naming.IfExists == config.IfExistsSkip {
		return outcome{status: StatusSkipped, msg: M("already_in_library"), rel: existing}
	}
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
	if incomplete {
		msg = M("added_incomplete", "saved", len(vr.Files), "total", len(album.Tracks))
	}
	return outcome{status: StatusCompleted, msg: msg, rel: rel, tracks: len(vr.Files)}
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

// importPartial never imports a rejected file. Re-verify the remaining files
// with full decoding even if normal FLAC verification was disabled.
func (m *Manager) importPartial(ctx context.Context, job Job, album qobuz.Album, dir string, expected []verify.Track, vr verify.Result, problems, fixProblems []ProblemItem) outcome {
	failed := func(msg Msg) outcome { return outcome{status: StatusFailed, msg: msg, problems: problems} }
	bad := map[string]bool{}
	for _, p := range vr.Problems {
		if p.Path != "" {
			bad[p.Path] = true
		}
	}
	for _, p := range fixProblems {
		bad[filepath.Join(dir, p.Title)] = true
	}
	for path := range bad {
		if !withinLibrary(dir, path) {
			return failed(M("error_verify", "error", "rejected file is outside staging"))
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return failed(M("error_verify", "error", err.Error()))
		}
	}
	clean, err := m.deps.Verify(ctx, dir, expected, verify.Options{FlacTest: true, FlacBinary: "flac"})
	if ctx.Err() != nil {
		return outcome{status: StatusCancelled, msg: M("cancelled")}
	}
	if err != nil {
		return failed(M("no_tracks"))
	}
	// Refuse files that fail the second check after staging cleanup.
	var corrupt bool
	for _, p := range clean.Problems {
		if p.Path != "" {
			corrupt = true
			break
		}
	}
	if corrupt {
		for _, p := range clean.Problems {
			if p.Path != "" {
				if !withinLibrary(dir, p.Path) {
					return failed(M("error_verify", "error", "rejected file is outside staging"))
				}
				if err := os.Remove(p.Path); err != nil && !os.IsNotExist(err) {
					return failed(M("error_verify", "error", err.Error()))
				}
			}
		}
		problems = append(problems, describe(album, clean.Problems)...)
		clean, err = m.deps.Verify(ctx, dir, expected, verify.Options{FlacTest: true, FlacBinary: "flac"})
		if ctx.Err() != nil {
			return outcome{status: StatusCancelled, msg: M("cancelled")}
		}
		if err != nil {
			return failed(M("no_tracks"))
		}
	}
	if len(clean.Files) == 0 {
		return failed(M("no_tracks"))
	}
	for _, p := range clean.Problems {
		if p.Reason != "missing" {
			return failed(M("has_problems"))
		}
	}
	out := m.importAlbum(job, album, dir, clean)
	out.problems = problems
	return out
}

// Preserve successfully verified tracks across album retries using same-disk
// renames, not extra copies or a second download database.
func retainVerified(staging string, vr verify.Result, fixes []ProblemItem) error {
	bad := map[string]bool{}
	for _, p := range vr.Problems {
		if p.Path != "" {
			bad[p.Path] = true
		}
	}
	for _, p := range fixes {
		bad[filepath.Join(vr.AlbumDir, p.Title)] = true
	}
	for _, f := range vr.Files {
		if bad[f.Path] {
			continue
		}
		rel, err := filepath.Rel(staging, f.Path)
		if err != nil || !withinLibrary(staging, f.Path) {
			return fmt.Errorf("invalid verified track path")
		}
		dst := filepath.Join(staging+".verified", rel)
		if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		if err = os.Rename(f.Path, dst); err != nil {
			return err
		}
	}
	// Keep the downloaded cover/booklet if a later attempt produces no files.
	if _, err := os.Stat(staging + ".verified"); err == nil {
		if err := filepath.WalkDir(vr.AlbumDir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			if ext != ".jpg" && ext != ".jpeg" && ext != ".png" && ext != ".pdf" {
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			rel, err := filepath.Rel(staging, path)
			if err != nil || !withinLibrary(staging, path) {
				return fmt.Errorf("invalid artwork path")
			}
			dst := filepath.Join(staging+".verified", rel)
			if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
			return os.Rename(path, dst)
		}); err != nil {
			return err
		}
	}

	return nil
}

func restoreVerified(staging string) error {
	root := staging + ".verified"
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || !withinLibrary(root, path) {
			return fmt.Errorf("invalid retained track path")
		}
		dst := filepath.Join(staging, rel)
		if err = os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return os.Rename(path, dst)
	})
}
