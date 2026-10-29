package jobs

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/qobuz"
	"cineroute/internal/music/streamrip"
)

// --- fixtures ---------------------------------------------------------------

type fakeAlbums map[string]qobuz.Album

func (f fakeAlbums) Album(_ context.Context, id string) (qobuz.Album, error) {
	a, ok := f[id]
	if !ok {
		return qobuz.Album{}, &qobuz.APIError{Status: 404, Message: "No result matching given argument"}
	}
	return a, nil
}

func testAlbum(id string, tracks int) qobuz.Album {
	a := qobuz.Album{
		ID: id, Title: "Aquemini", Artist: "Outkast", ReleaseDate: "1998-09-29",
		Streamable: true, MaxBitDepth: 16, MaxSamplingRate: 44.1,
	}
	for i := 1; i <= tracks; i++ {
		a.Tracks = append(a.Tracks, qobuz.Track{
			ID: fmt.Sprint(i), Title: fmt.Sprintf("Song %d", i), Disc: 1, Number: i, DurationSec: 2, Streamable: true,
		})
	}
	return a
}

// writeWAV writes seconds of silence as 8 kHz mono 16-bit PCM.
func writeWAV(t *testing.T, path string, seconds float64) {
	t.Helper()
	const rate = 8000
	n := int(seconds * rate)
	data := make([]byte, n*2)
	for i := 0; i < n; i++ { // a little noise so flac has something to encode
		binary.LittleEndian.PutUint16(data[i*2:], uint16(i%7))
	}
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+len(data)))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, rate)
	b = binary.LittleEndian.AppendUint32(b, rate*2)
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = binary.LittleEndian.AppendUint16(b, 16)
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(data)))
	b = append(b, data...)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeFLAC encodes a tagged FLAC track like streamrip would leave it.
func makeFLAC(t *testing.T, dir string, number int, seconds float64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wav := filepath.Join(t.TempDir(), "in.wav")
	writeWAV(t, wav, seconds)
	out := filepath.Join(dir, fmt.Sprintf("%02d. Song %d.flac", number, number))
	cmd := exec.Command("flac", "--silent", "-f", "-o", out,
		"--tag=TRACKNUMBER="+fmt.Sprint(number), "--tag=DISCNUMBER=1", "--tag=TITLE="+fmt.Sprintf("Song %d", number), wav)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("flac: %v: %s", err, b)
	}
}

type ripFunc func(ctx context.Context, o streamrip.RunOptions, attempt int) (streamrip.Result, error)

type env struct {
	t        *testing.T
	cfg      *config.Config
	m        *Manager
	attempts atomic.Int32
	imported chan string
	cancel   context.CancelFunc
	done     chan struct{}
}

func newEnv(t *testing.T, albums fakeAlbums, rip ripFunc, mutate ...func(*config.Config)) *env {
	t.Helper()
	if _, err := exec.LookPath("flac"); err != nil {
		t.Skip("flac binary not installed")
	}
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Paths.Library = filepath.Join(dir, "music")
	cfg.Paths.Staging = filepath.Join(dir, "staging")
	cfg.Paths.Data = filepath.Join(dir, "data")
	cfg.Qobuz.Quality = 2
	cfg.Download.RetryDelay = 0
	for _, f := range mutate {
		f(&cfg)
	}
	os.MkdirAll(cfg.Paths.Library, 0o755)
	e := &env{t: t, cfg: &cfg, imported: make(chan string, 10)}
	m, err := New(&cfg, Deps{
		Albums: albums,
		Rip: func(ctx context.Context, o streamrip.RunOptions) (streamrip.Result, error) {
			n := int(e.attempts.Add(1))
			return rip(ctx, o, n)
		},
		AfterImport: func(_ context.Context, rel string) { e.imported <- rel },
		Files:       streamrip.FilesIn(cfg.Paths.Data),
		Sleep:       func(ctx context.Context, d time.Duration) error { return ctx.Err() },
	})
	if err != nil {
		t.Fatal(err)
	}
	e.m = m
	return e
}

func (e *env) start() {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel, e.done = cancel, make(chan struct{})
	go func() { e.m.Run(ctx); close(e.done) }()
	e.t.Cleanup(e.stop)
}

func (e *env) stop() {
	if e.cancel != nil {
		e.cancel()
		<-e.done
		e.cancel = nil
	}
}

func (e *env) wait(id string, want ...Status) Job {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		j, err := e.m.Get(id)
		if err != nil {
			e.t.Fatal(err)
		}
		for _, s := range want {
			if j.Status == s {
				return j
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	j, _ := e.m.Get(id)
	e.t.Fatalf("job stuck in %s (%s), want %v", j.Status, j.Message, want)
	return Job{}
}

// albumRip writes the given tracks of the album into streamrip's layout.
func albumRip(albumID string, tracks []int, seconds float64, t *testing.T) func(context.Context, streamrip.RunOptions) {
	return func(_ context.Context, o streamrip.RunOptions) {
		dir := filepath.Join(o.Folder, albumID)
		for _, n := range tracks {
			makeFLAC(t, dir, n, seconds)
		}
		os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("jpg"), 0o644)
	}
}

// --- tests ------------------------------------------------------------------

func TestCompleteAlbumIsImported(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 3)}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		if o.URL != "https://play.qobuz.com/album/a1" || o.Quality != 2 {
			t.Errorf("unexpected run options %+v", o)
		}
		albumRip("a1", []int{1, 2, 3}, 2, t)(ctx, o)
		return streamrip.Result{}, nil
	})
	if e.m.InLibrary(testAlbum("a1", 3)) {
		t.Fatal("album present before import")
	}
	e.start()
	job, err := e.m.Enqueue(context.Background(), "a1", "")
	if err != nil {
		t.Fatal(err)
	}
	j := e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusCompleted {
		t.Fatalf("status %s: %s %v", j.Status, j.Message, j.Problems)
	}
	if j.LibraryPath != "Outkast/Aquemini (1998)" {
		t.Fatalf("library path %q", j.LibraryPath)
	}
	for _, n := range []string{"01. Song 1.flac", "02. Song 2.flac", "03. Song 3.flac", "cover.jpg"} {
		if _, err := os.Stat(filepath.Join(e.cfg.Paths.Library, j.LibraryPath, n)); err != nil {
			t.Errorf("missing %s in library: %v", n, err)
		}
	}
	if got := <-e.imported; got != j.LibraryPath {
		t.Fatalf("after-import hook got %q", got)
	}
	deadline := time.After(time.Second)
	for !e.m.InLibrary(testAlbum("a1", 3)) {
		select {
		case <-deadline:
			t.Fatal("import did not refresh library cache")
		case <-time.After(time.Millisecond):
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(e.cfg.Paths.Staging, "downloads")); len(entries) != 0 {
		t.Fatalf("staging not cleaned: %v", entries)
	}
	if !strings.Contains(j.Message, "16-bit / 8 kHz") {
		t.Errorf("message should report the quality: %q", j.Message)
	}
}

func TestIncompleteAlbumIsNeverImported(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 3)}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		albumRip("a1", []int{1, 3}, 2, t)(ctx, o) // track 2 fails every time
		return streamrip.Result{Errors: []string{"Persistent error downloading track 'Song 2', skipping: boom"}}, nil
	}, func(c *config.Config) { c.Download.Attempts = 3 })
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	j := e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusFailed {
		t.Fatalf("status %s, want failed", j.Status)
	}
	if got := e.attempts.Load(); got != 3 {
		t.Fatalf("attempts = %d, want 3", got)
	}
	if !strings.Contains(j.Message, "1 of 3 tracks failed") || !strings.Contains(j.Message, "Tried 3 times") {
		t.Fatalf("message %q", j.Message)
	}
	if j.MessageCode != "incomplete" || j.MessageArgs["failed"] != 1 || j.MessageArgs["total"] != 3 || j.MessageArgs["tries"] != 3 {
		t.Fatalf("message code %q args %v", j.MessageCode, j.MessageArgs)
	}
	var missing bool
	for _, p := range j.ProblemItems {
		if p.Reason == "missing" && p.Track == 2 && p.Title == "Song 2" {
			missing = true
		}
	}
	if !missing || len(j.ProblemItems) != len(j.Problems) {
		t.Fatalf("problem items %+v", j.ProblemItems)
	}
	if len(j.Problems) == 0 || !strings.Contains(strings.Join(j.Problems, "\n"), "Song 2") {
		t.Fatalf("problems should name the missing track: %v", j.Problems)
	}
	if entries, _ := os.ReadDir(e.cfg.Paths.Library); len(entries) != 0 {
		t.Fatalf("library must stay untouched, has %v", entries)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.cfg.Paths.Staging, "downloads")); len(entries) != 0 {
		t.Fatalf("failed download not cleaned up: %v", entries)
	}
}

func TestRetrySucceedsOnSecondAttempt(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 2)}, func(ctx context.Context, o streamrip.RunOptions, attempt int) (streamrip.Result, error) {
		if attempt == 1 {
			albumRip("a1", []int{1}, 2, t)(ctx, o)
		} else {
			albumRip("a1", []int{1, 2}, 2, t)(ctx, o)
		}
		return streamrip.Result{}, nil
	})
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	j := e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusCompleted || j.Attempt != 2 {
		t.Fatalf("status %s attempt %d: %s", j.Status, j.Attempt, j.Message)
	}
}

func TestPreviewSamplesAreRejected(t *testing.T) {
	a := testAlbum("a1", 1)
	a.Tracks[0].DurationSec = 200
	e := newEnv(t, fakeAlbums{"a1": a}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		albumRip("a1", []int{1}, 30, t)(ctx, o)
		return streamrip.Result{}, nil
	}, func(c *config.Config) { c.Download.Attempts = 1 })
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	j := e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusFailed {
		t.Fatalf("a 30 second preview must not be imported (status %s)", j.Status)
	}
	if !strings.Contains(strings.Join(j.Problems, " "), "preview") || j.ProblemItems[0].Reason != "preview_sample" {
		t.Fatalf("problems should mention the preview: %v %+v", j.Problems, j.ProblemItems)
	}
}

func TestFatalErrorStopsWithoutRetry(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 1)}, func(context.Context, streamrip.RunOptions, int) (streamrip.Result, error) {
		return streamrip.Result{ExitCode: 1, Failure: streamrip.FailureAuth}, nil
	}, func(c *config.Config) { c.Download.Attempts = 3 })
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	j := e.wait(job.ID, StatusFailed, StatusCompleted)
	if e.attempts.Load() != 1 || !strings.Contains(j.Message, "rejected the login") || j.MessageCode != "failure_auth" {
		t.Fatalf("attempts %d, message %q", e.attempts.Load(), j.Message)
	}
}

func TestKeepFailed(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 2)}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		albumRip("a1", []int{1}, 2, t)(ctx, o)
		return streamrip.Result{}, nil
	}, func(c *config.Config) { c.Download.Attempts = 1; c.Download.KeepFailed = true })
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	e.wait(job.ID, StatusFailed)
	entries, _ := os.ReadDir(filepath.Join(e.cfg.Paths.Staging, "failed"))
	if len(entries) != 1 || !strings.Contains(entries[0].Name(), "Outkast - Aquemini [a1]") {
		t.Fatalf("failed download not kept: %v", entries)
	}
}

func TestAlreadyInLibraryIsSkipped(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 1)}, func(context.Context, streamrip.RunOptions, int) (streamrip.Result, error) {
		t.Error("rip must not run for an album that is already in the library")
		return streamrip.Result{}, nil
	})
	os.MkdirAll(filepath.Join(e.cfg.Paths.Library, "Outkast", "Aquemini (1998)"), 0o755)
	e.start()
	j, err := e.m.Enqueue(context.Background(), "a1", "")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != StatusSkipped {
		t.Fatalf("status %s", j.Status)
	}
}

func TestReplaceMovesOldAlbumAside(t *testing.T) {
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 1)}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		albumRip("a1", []int{1}, 2, t)(ctx, o)
		return streamrip.Result{}, nil
	}, func(c *config.Config) { c.Naming.IfExists = config.IfExistsReplace })
	old := filepath.Join(e.cfg.Paths.Library, "Outkast", "Aquemini (1998)")
	os.MkdirAll(old, 0o755)
	os.WriteFile(filepath.Join(old, "old.mp3"), []byte("x"), 0o644)
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	j := e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusCompleted {
		t.Fatalf("status %s %s", j.Status, j.Message)
	}
	if _, err := os.Stat(filepath.Join(old, "old.mp3")); err == nil {
		t.Fatal("old album still in place")
	}
	entries, _ := os.ReadDir(filepath.Join(e.cfg.Paths.Staging, "replaced"))
	if len(entries) != 1 {
		t.Fatalf("old album not kept aside: %v", entries)
	}
}

func TestCancelRunningJob(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 1)}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return streamrip.Result{}, nil
	})
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	<-started
	if _, err := e.m.Cancel(job.ID); err != nil {
		t.Fatal(err)
	}
	j := e.wait(job.ID, StatusCancelled, StatusFailed, StatusCompleted)
	if j.Status != StatusCancelled {
		t.Fatalf("status %s", j.Status)
	}
	// It can be queued again.
	if _, err := e.m.Retry(job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestQueueOrderDedupAndCancelQueued(t *testing.T) {
	block := make(chan struct{})
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 1), "a2": testAlbum("a2", 1), "a3": testAlbum("a3", 1)},
		func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return streamrip.Result{}, nil
		})
	e.start()
	j1, _ := e.m.Enqueue(context.Background(), "a1", "")
	j2, _ := e.m.Enqueue(context.Background(), "a2", "")
	j3, _ := e.m.Enqueue(context.Background(), "a3", "")
	again, _ := e.m.Enqueue(context.Background(), "a2", "")
	if again.ID != j2.ID {
		t.Fatal("queueing the same album twice must return the existing job")
	}
	e.wait(j1.ID, StatusDownloading)
	if _, err := e.m.Cancel(j2.ID); err != nil {
		t.Fatal(err)
	}
	list := e.m.List()
	if list[0].ID != j1.ID || list[1].ID != j3.ID || list[2].ID != j2.ID {
		t.Fatalf("unexpected order: %s %s %s", list[0].Status, list[1].Status, list[2].Status)
	}
	if err := e.m.Remove(j1.ID); err != ErrNotFinished {
		t.Fatalf("removing an active job: %v", err)
	}
	close(block)
}

func TestRestartRequeuesInterruptedJobs(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	albums := fakeAlbums{"a1": testAlbum("a1", 1)}
	e := newEnv(t, albums, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		os.MkdirAll(filepath.Join(o.Folder, "a1"), 0o755)
		once.Do(func() { close(started) })
		<-ctx.Done()
		return streamrip.Result{}, nil
	})
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	<-started
	e.stop() // server shutdown while downloading

	j, _ := e.m.Get(job.ID)
	if j.Status != StatusQueued {
		t.Fatalf("after shutdown status = %s, want queued", j.Status)
	}
	// A new manager on the same data resumes the job.
	m2, err := New(e.cfg, Deps{
		Albums: albums,
		Rip: func(ctx context.Context, o streamrip.RunOptions) (streamrip.Result, error) {
			albumRip("a1", []int{1}, 2, t)(ctx, o)
			return streamrip.Result{}, nil
		},
		Files: streamrip.FilesIn(e.cfg.Paths.Data),
	})
	if err != nil {
		t.Fatal(err)
	}
	e.m = m2
	e.start()
	j = e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusCompleted {
		t.Fatalf("resumed job %s: %s %v", j.Status, j.Message, j.Problems)
	}
}

func TestEnqueueErrors(t *testing.T) {
	a := testAlbum("a2", 1)
	a.Streamable = false
	e := newEnv(t, fakeAlbums{"a2": a}, func(context.Context, streamrip.RunOptions, int) (streamrip.Result, error) {
		return streamrip.Result{}, nil
	})
	if _, err := e.m.Enqueue(context.Background(), "missing", ""); err == nil {
		t.Fatal("unknown album must fail")
	}
	if _, err := e.m.Enqueue(context.Background(), "a2", ""); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("non streamable album: %v", err)
	}
}

func TestHistoryLimit(t *testing.T) {
	albums := fakeAlbums{}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("a%d", i)
		albums[id] = testAlbum(id, 1)
	}
	e := newEnv(t, albums, func(context.Context, streamrip.RunOptions, int) (streamrip.Result, error) {
		return streamrip.Result{}, nil
	}, func(c *config.Config) { c.Download.HistoryLimit = 2; c.Download.Attempts = 1 })
	e.start()
	for i := 0; i < 5; i++ {
		j, _ := e.m.Enqueue(context.Background(), fmt.Sprintf("a%d", i), "")
		e.wait(j.ID, StatusFailed)
	}
	if n := len(e.m.List()); n != 2 {
		t.Fatalf("history has %d jobs, want 2", n)
	}
}

// Qobuz delivers FLACs without an MD5 signature and sometimes with French
// genres. CineRoute fixes both and the audio stays identical.
func TestQobuzStyleFilesAreFixedBeforeImport(t *testing.T) {
	if _, err := exec.LookPath("metaflac"); err != nil {
		t.Skip("metaflac not installed")
	}
	var frames [][]byte
	e := newEnv(t, fakeAlbums{"a1": testAlbum("a1", 2)}, func(ctx context.Context, o streamrip.RunOptions, _ int) (streamrip.Result, error) {
		dir := filepath.Join(o.Folder, "a1")
		for n := 1; n <= 2; n++ {
			makeFLAC(t, dir, n, 2)
			p := filepath.Join(dir, fmt.Sprintf("%02d. Song %d.flac", n, n))
			if b, err := exec.Command("metaflac", "--set-tag=GENRE=Électronique, Musique de film", p).CombinedOutput(); err != nil {
				t.Fatalf("metaflac: %v %s", err, b)
			}
			b, _ := os.ReadFile(p)
			copy(b[26:42], make([]byte, 16)) // empty MD5, like Qobuz
			os.WriteFile(p, b, 0o644)
			frames = append(frames, audioOnly(b))
		}
		return streamrip.Result{}, nil
	})
	e.start()
	job, _ := e.m.Enqueue(context.Background(), "a1", "")
	j := e.wait(job.ID, StatusCompleted, StatusFailed)
	if j.Status != StatusCompleted {
		t.Fatalf("status %s: %s %v", j.Status, j.Message, j.Problems)
	}
	for n := 1; n <= 2; n++ {
		p := filepath.Join(e.cfg.Paths.Library, j.LibraryPath, fmt.Sprintf("%02d. Song %d.flac", n, n))
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(b[26:42]) == string(make([]byte, 16)) {
			t.Fatalf("track %d still has no MD5", n)
		}
		if string(audioOnly(b)) != string(frames[n-1]) {
			t.Fatalf("track %d audio changed", n)
		}
		out, _ := exec.Command("metaflac", "--show-tag=GENRE", p).Output()
		if got := strings.TrimSpace(string(out)); got != "GENRE=Electronic, Film Music" && !strings.HasPrefix(got, "GENRE=Electronic") {
			t.Fatalf("track %d genre %q", n, got)
		}
		if b, err := exec.Command("flac", "-t", "-s", p).CombinedOutput(); err != nil || len(b) > 0 {
			t.Fatalf("flac -t: %v %s", err, b)
		}
	}
	logged, _ := os.ReadFile(e.m.LogPath(j.ID))
	if !strings.Contains(string(logged), "MD5 signature written to 2 of 2") {
		t.Fatalf("job log: %s", logged)
	}
}

// audioOnly returns a FLAC file's bytes after its metadata blocks.
func audioOnly(b []byte) []byte {
	i := 4
	for i+4 <= len(b) {
		last := b[i]&0x80 != 0
		n := int(b[i+1])<<16 | int(b[i+2])<<8 | int(b[i+3])
		i += 4 + n
		if last {
			return b[i:]
		}
	}
	return nil
}

func TestPerUserQueueLimitAndRetryOwnership(t *testing.T) {
	cfg := config.Default()
	root := t.TempDir()
	cfg.Paths.Library = filepath.Join(root, "library")
	cfg.Paths.Staging = filepath.Join(root, "staging")
	cfg.Paths.Data = filepath.Join(root, "data")
	m, err := New(&cfg, Deps{Albums: fakeAlbums{"1": testAlbum("1", 1), "2": testAlbum("2", 1)}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := m.Enqueue(ctx, "1", "alice", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, "2", "alice", 1); err != ErrQueueFull {
		t.Fatalf("limit ignored: %v", err)
	}
	other, err := m.Enqueue(ctx, "1", "bob", 1)
	if err != nil || first.ID == other.ID {
		t.Fatalf("user requests not independent: %v", err)
	}
	if _, err := m.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Enqueue(ctx, "2", "alice", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Retry(first.ID, 1); err != ErrQueueFull {
		t.Fatalf("retry bypassed limit: %v", err)
	}
}

func TestInLibraryChecksConfiguredNamingAndRecordedPaths(t *testing.T) {
	e := newEnv(t, fakeAlbums{}, nil)
	album := testAlbum("a1", 1)
	path := filepath.Join(e.cfg.Paths.Library, "Outkast", "Aquemini (1998)")
	if e.m.InLibrary(album) {
		t.Fatal("missing album marked in library")
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	e.m.RefreshLibrary()
	if e.m.InLibrary(album) {
		t.Fatal("empty album folder marked in library")
	}
	if err := os.WriteFile(filepath.Join(path, "01. Song.flac"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	if e.m.InLibrary(album) {
		t.Fatal("search bypassed cached library")
	}
	e.m.RefreshLibrary()
	if !e.m.InLibrary(album) {
		t.Fatal("existing album without job absent")
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(e.cfg.Paths.Library, "old-import", "Disc 1")
	if err := os.MkdirAll(old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "song.mp3"), []byte("audio"), 0644); err != nil {
		t.Fatal(err)
	}
	e.m.jobs = append(e.m.jobs, &Job{AlbumID: "a1", LibraryPath: "old-import", Status: StatusCompleted})
	e.m.RefreshLibrary()
	if !e.m.InLibrary(album) {
		t.Fatal("recorded import path absent")
	}
	if err := os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	if !e.m.InLibrary(album) {
		t.Fatal("warm search unexpectedly read disk")
	}
	e.m.RefreshLibrary()
	if e.m.InLibrary(album) {
		t.Fatal("deleted import still in library")
	}
}
