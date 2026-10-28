package jobs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/flacfix"
	"cineroute/internal/music/genres"
	"cineroute/internal/music/library"
	"cineroute/internal/music/qobuz"
	"cineroute/internal/music/streamrip"
	"cineroute/internal/music/verify"
)

// Errors returned to the API.
var (
	ErrNotFound    = errors.New("job not found")
	ErrQueueFull   = errors.New("too many outstanding music requests")
	ErrNotFinished = errors.New("only finished jobs can be removed")
	ErrNotRetry    = errors.New("only failed, cancelled or skipped jobs can be retried")
	// ErrNotStreamable wraps the error for albums Qobuz doesn't stream.
	ErrNotStreamable = errors.New("not available for streaming")
)

// AlbumSource fetches album metadata (qobuz.Client in production).
type AlbumSource interface {
	Album(ctx context.Context, albumID string) (qobuz.Album, error)
}

// Deps are the parts of the pipeline that tests replace.
type Deps struct {
	Albums AlbumSource
	Rip    func(context.Context, streamrip.RunOptions) (streamrip.Result, error)
	Verify func(context.Context, string, []verify.Track, verify.Options) (verify.Result, error)
	// AfterImport runs after an album was added (e.g. a Navidrome scan).
	AfterImport func(ctx context.Context, libraryPath string)
	Files       streamrip.Files
	Logger      *slog.Logger
	// Sleep waits between attempts; tests make it instant.
	Sleep func(ctx context.Context, d time.Duration) error
	// Fix tidies the FLAC files before they are verified (flacfix.Dir).
	Fix func(context.Context, string, flacfix.Options) (flacfix.Report, error)
}

type Manager struct {
	cfg    *config.Config
	deps   Deps
	st     store
	log    *slog.Logger
	genres *genres.Translator // nil when genre translation is off

	mu      sync.Mutex
	jobs    []*Job
	queued  map[string]time.Time // job id -> time it entered the queue
	running map[string]context.CancelFunc
	wake    chan struct{}
}

func New(cfg *config.Config, deps Deps) (*Manager, error) {
	if deps.Rip == nil {
		deps.Rip = streamrip.Run
	}
	if deps.Verify == nil {
		deps.Verify = verify.Album
	}
	if deps.Sleep == nil {
		deps.Sleep = sleepCtx
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Fix == nil {
		deps.Fix = flacfix.Dir
	}
	m := &Manager{
		cfg:     cfg,
		deps:    deps,
		st:      store{path: filepath.Join(cfg.Paths.Data, "jobs.json")},
		log:     deps.Logger,
		queued:  map[string]time.Time{},
		running: map[string]context.CancelFunc{},
		wake:    make(chan struct{}, 1),
	}
	if cfg.Metadata.TranslateGenres {
		m.genres = genres.New(cfg.Metadata.GenreTranslations)
	}
	for _, d := range []string{m.downloadsDir(), m.logsDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	jobs, err := m.st.load()
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", m.st.path, err)
	}
	now := time.Now()
	for i, j := range jobs {
		if j == nil {
			continue
		}
		if j.Problems == nil {
			j.Problems = []string{}
		}
		if j.ProblemItems == nil {
			// Saved by an older version: keep the English lines.
			j.ProblemItems = []ProblemItem{}
			for _, p := range j.Problems {
				j.ProblemItems = append(j.ProblemItems, ProblemItem{Reason: "text", Text: p})
			}
		}
		if j.MessageArgs == nil {
			j.MessageArgs = map[string]any{}
		}
		if !j.Status.Finished() {
			if j.Status.Active() {
				j.set(M("interrupted_restart"))
			}
			j.Status, j.Attempt, j.TracksDone, j.StartedAt = StatusQueued, 0, 0, nil
			// Keep the previous queue order.
			m.queued[j.ID] = now.Add(time.Duration(i) * time.Millisecond)
		}
		m.jobs = append(m.jobs, j)
	}
	m.cleanDownloads()
	return m, m.saveLocked()
}

func (m *Manager) downloadsDir() string { return filepath.Join(m.cfg.Paths.Staging, "downloads") }
func (m *Manager) failedDir() string    { return filepath.Join(m.cfg.Paths.Staging, "failed") }
func (m *Manager) replacedDir() string  { return filepath.Join(m.cfg.Paths.Staging, "replaced") }
func (m *Manager) logsDir() string      { return filepath.Join(m.cfg.Paths.Data, "logs") }
func (m *Manager) LogPath(id string) string {
	return filepath.Join(m.logsDir(), id+".log")
}

var jobIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// cleanDownloads removes leftovers of downloads that were interrupted. Only
// folders named like job ids are touched.
func (m *Manager) cleanDownloads() {
	entries, err := os.ReadDir(m.downloadsDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() && jobIDRe.MatchString(e.Name()) {
			os.RemoveAll(filepath.Join(m.downloadsDir(), e.Name()))
		}
	}
}

// --- API ----------------------------------------------------------------

// List returns all jobs: active first, then queued in queue order, then
// finished ones newest first.
func (m *Manager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j.clone())
	}
	rank := func(j Job) int {
		switch {
		case j.Status.Active():
			return 0
		case j.Status == StatusQueued:
			return 1
		}
		return 2
	}
	sort.SliceStable(out, func(a, b int) bool {
		ra, rb := rank(out[a]), rank(out[b])
		if ra != rb {
			return ra < rb
		}
		switch ra {
		case 1:
			return m.queued[out[a].ID].Before(m.queued[out[b].ID])
		case 2:
			return finishedAt(out[a]).After(finishedAt(out[b]))
		}
		return false
	})
	return out
}

func finishedAt(j Job) time.Time {
	if j.FinishedAt != nil {
		return *j.FinishedAt
	}
	return j.CreatedAt
}

// Get returns one job.
func (m *Manager) Get(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.find(id)
	if j == nil {
		return Job{}, ErrNotFound
	}
	return j.clone(), nil
}

// Enqueue adds an album to the queue. An album that is already queued or
// downloading for this user is not added twice; its existing job is returned.
// requestedBy is the user who asked for it ("" when login is off).
func (m *Manager) Enqueue(ctx context.Context, albumID, requestedBy string, limits ...int) (Job, error) {
	if j, ok := m.pending(albumID, requestedBy); ok {
		return j, nil
	}
	album, err := m.deps.Albums.Album(ctx, albumID)
	if err != nil {
		return Job{}, err
	}
	if !album.Streamable {
		return Job{}, fmt.Errorf("%q is %w on Qobuz in your country", album.FullTitle(), ErrNotStreamable)
	}

	now := time.Now()
	job := &Job{
		ID:          newID(),
		AlbumID:     album.ID,
		Title:       album.FullTitle(),
		Artist:      album.Artist,
		Year:        album.Year(),
		Cover:       album.Cover,
		TracksTotal: len(album.Tracks),
		Status:      StatusQueued,
		Problems:    []string{},
		MaxAttempts: m.cfg.Download.Attempts,
		RequestedBy: requestedBy,
		CreatedAt:   now,
	}
	job.set(M("queued"))

	// Skip right away when the album is already in the library.
	if m.cfg.Naming.IfExists == config.IfExistsSkip {
		if rel, err := m.folderFor(album, library.Quality{}); err == nil && library.Exists(m.cfg.Paths.Library, rel) {
			job.Status = StatusSkipped
			job.set(M("already_in_library"))
			job.LibraryPath = rel
			job.FinishedAt = ptr(now)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.pendingLocked(album.ID, requestedBy); existing != nil {
		return existing.clone(), nil
	}
	if m.queueFullLocked(requestedBy, limits) {
		return Job{}, ErrQueueFull
	}
	m.jobs = append(m.jobs, job)
	if job.Status == StatusQueued {
		m.queued[job.ID] = now
	}
	m.trimLocked()
	if err := m.saveLocked(); err != nil {
		m.log.Error("saving queue", "err", err)
	}
	m.signal()
	return job.clone(), nil
}

// Cancel stops a running job or removes a queued one from the queue.
func (m *Manager) Cancel(id string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.find(id)
	if j == nil {
		return Job{}, ErrNotFound
	}
	switch {
	case j.Status == StatusQueued:
		delete(m.queued, j.ID)
		j.Status = StatusCancelled
		j.set(M("cancelled"))
		j.FinishedAt = ptr(time.Now())
		_ = m.saveLocked()
	case j.Status.Active():
		if cancel := m.running[j.ID]; cancel != nil {
			cancel()
		}
		j.set(M("cancelling"))
	}
	return j.clone(), nil
}

// Retry puts a failed, cancelled or skipped job back in the queue.
func (m *Manager) Retry(id string, limits ...int) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.find(id)
	if j == nil {
		return Job{}, ErrNotFound
	}
	if j.Status != StatusFailed && j.Status != StatusCancelled && j.Status != StatusSkipped {
		return Job{}, ErrNotRetry
	}
	if other := m.pendingLocked(j.AlbumID, j.RequestedBy); other != nil {
		return other.clone(), nil
	}
	if m.queueFullLocked(j.RequestedBy, limits) {
		return Job{}, ErrQueueFull
	}
	j.Status = StatusQueued
	j.set(M("queued"))
	j.Problems, j.ProblemItems = []string{}, []ProblemItem{}
	j.Attempt, j.TracksDone = 0, 0
	j.LibraryPath = ""
	j.StartedAt, j.FinishedAt = nil, nil
	j.MaxAttempts = m.cfg.Download.Attempts
	m.queued[j.ID] = time.Now()
	_ = m.saveLocked()
	m.signal()
	return j.clone(), nil
}

// Remove deletes a finished job from the history.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, j := range m.jobs {
		if j.ID != id {
			continue
		}
		if !j.Status.Finished() {
			return ErrNotFinished
		}
		m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
		os.Remove(m.LogPath(id))
		return m.saveLocked()
	}
	return ErrNotFound
}

// ClearFinished deletes every finished job from the history.
func (m *Manager) ClearFinished() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if j.Status.Finished() {
			os.Remove(m.LogPath(j.ID))
			continue
		}
		kept = append(kept, j)
	}
	m.jobs = kept
	return m.saveLocked()
}

// --- internals ------------------------------------------------------------

func (m *Manager) find(id string) *Job {
	for _, j := range m.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (m *Manager) pending(albumID string, owners ...string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.pendingLocked(albumID, owners...); j != nil {
		return j.clone(), true
	}
	return Job{}, false
}

func (m *Manager) pendingLocked(albumID string, owners ...string) *Job {
	for _, j := range m.jobs {
		if j.AlbumID == albumID && !j.Status.Finished() && (len(owners) == 0 || j.RequestedBy == owners[0]) {
			return j
		}
	}
	return nil
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// trimLocked drops the oldest finished jobs beyond the history limit.
func (m *Manager) trimLocked() {
	limit := m.cfg.Download.HistoryLimit
	var finished []*Job
	for _, j := range m.jobs {
		if j.Status.Finished() {
			finished = append(finished, j)
		}
	}
	if len(finished) <= limit {
		return
	}
	sort.Slice(finished, func(a, b int) bool {
		return finishedAt(*finished[a]).After(finishedAt(*finished[b]))
	})
	drop := map[string]bool{}
	for _, j := range finished[limit:] {
		drop[j.ID] = true
		os.Remove(m.LogPath(j.ID))
	}
	kept := m.jobs[:0]
	for _, j := range m.jobs {
		if !drop[j.ID] {
			kept = append(kept, j)
		}
	}
	m.jobs = kept
}

func (m *Manager) saveLocked() error { return m.st.save(m.jobs) }

// update changes a job under the lock and optionally persists it.
func (m *Manager) update(id string, persist bool, fn func(j *Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.find(id)
	if j == nil {
		return
	}
	fn(j)
	if persist {
		if err := m.saveLocked(); err != nil {
			m.log.Error("saving queue", "err", err)
		}
	}
}

// next takes the oldest queued job and marks it as started.
func (m *Manager) next() (Job, context.Context, context.CancelFunc, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var pick *Job
	for _, j := range m.jobs {
		if j.Status != StatusQueued {
			continue
		}
		if pick == nil || m.queued[j.ID].Before(m.queued[pick.ID]) {
			pick = j
		}
	}
	if pick == nil {
		return Job{}, nil, nil, false
	}
	delete(m.queued, pick.ID)
	pick.Status = StatusDownloading
	pick.set(M("starting"))
	pick.StartedAt = ptr(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	m.running[pick.ID] = cancel
	_ = m.saveLocked()
	return pick.clone(), ctx, cancel, true
}

func (m *Manager) folderFor(a qobuz.Album, q library.Quality) (string, error) {
	if m.genres != nil {
		a.Genre = m.genres.Translate(a.Genre)
	}
	return library.FolderFor(m.cfg.Naming.Folder, a, q, m.cfg.Qobuz.Quality, m.cfg.Naming.RestrictCharacters)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var audioExt = map[string]bool{
	".flac": true, ".mp3": true, ".m4a": true, ".mp4": true, ".aac": true, ".alac": true,
	".ogg": true, ".opus": true, ".wav": true, ".aiff": true, ".aif": true,
}

// countAudio counts audio files below dir (used for progress only).
func countAudio(dir string) int {
	n := 0
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && audioExt[strings.ToLower(filepath.Ext(d.Name()))] {
			n++
		}
		return nil
	})
	return n
}

// Check the limit while holding the same lock used to enqueue and retry jobs.
func (m *Manager) queueFullLocked(owner string, limits []int) bool {
	if len(limits) == 0 || limits[0] <= 0 {
		return false
	}
	count := 0
	for _, j := range m.jobs {
		if j.RequestedBy == owner && !j.Status.Finished() {
			count++
		}
	}
	return count >= limits[0]
}
