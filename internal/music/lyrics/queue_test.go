package lyrics

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (Options, string) {
	t.Helper()
	root := t.TempDir()
	library := filepath.Join(root, "music")
	rel := "Artist/Album; quoted ' name"
	if err := os.MkdirAll(filepath.Join(library, rel, "Disc 2"), 0755); err != nil {
		t.Fatal(err)
	}
	return Options{Library: library, Data: filepath.Join(root, "data"), MaxAttempts: 3, RetryDelay: time.Minute, Timeout: time.Minute, VerifiedOnly: true}, rel
}
func openQueue(t *testing.T, o Options, run Runner, scan func(context.Context) error) *Manager {
	t.Helper()
	m, err := New(o, run, scan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func TestDurableQueueIsIdempotentAndResumesDeferredWork(t *testing.T) {
	o, rel := fixture(t)
	var calls int
	run := func(_ context.Context, task Task) (Summary, error) {
		calls++
		if task.Path != rel {
			t.Fatal(task)
		}
		return Summary{Saved: 2, Deferred: 1}, nil
	}
	m := openQueue(t, o, run, nil)
	for range 2 {
		if err := m.Enqueue("release", rel); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := m.Get("release"); s.Status != "deferred" || s.Attempts != 1 || s.Saved != 2 || s.Deferred != 1 {
		t.Fatalf("%+v", s)
	}
	if err := m.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("backoff was ignored")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	m2 := openQueue(t, o, func(context.Context, Task) (Summary, error) { return Summary{Saved: 3}, nil }, nil)
	if _, err := m2.db.Exec(`UPDATE lyrics_jobs SET status='running',attempts=2 WHERE id='release'`); err != nil {
		t.Fatal(err)
	}
	m2.Close()
	m3 := openQueue(t, o, func(context.Context, Task) (Summary, error) { return Summary{Saved: 3}, nil }, nil)
	if s := m3.Get("release"); s.Status != "queued" || s.Attempts != 1 {
		t.Fatalf("restart did not recover: %+v", s)
	}
	m3.db.Exec(`UPDATE lyrics_jobs SET next_attempt=0`)
	if err := m3.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := m3.Get("release"); s.Status != "completed" || s.Saved != 3 {
		t.Fatalf("%+v", s)
	}
	if err := m3.Enqueue("release", rel); err != nil {
		t.Fatal(err)
	}
	if m3.Get("release").Status != "completed" {
		t.Fatal("completed receipt was reset")
	}
}
func TestCancellationRetainsPartialResultsAndDoesNotSpendAttempt(t *testing.T) {
	o, rel := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	m := openQueue(t, o, func(context.Context, Task) (Summary, error) { cancel(); return Summary{Saved: 1}, context.Canceled }, func(context.Context) error { return nil })
	m.Enqueue("release", rel)
	if err := m.processNext(ctx); err != nil {
		t.Fatal(err)
	}
	s := m.Get("release")
	if s.Status != "queued" || s.Attempts != 0 || s.Saved != 1 {
		t.Fatalf("%+v", s)
	}
	var pending int
	m.db.QueryRow(`SELECT scan_pending FROM lyrics_jobs`).Scan(&pending)
	if pending != 1 {
		t.Fatal("partial lyric writes must still trigger a scan")
	}
}
func TestRetryLimitManualRetryAndDurableScan(t *testing.T) {
	o, rel := fixture(t)
	o.MaxAttempts = 1
	scans := 0
	m := openQueue(t, o, func(context.Context, Task) (Summary, error) {
		return Summary{Saved: 1, Deferred: 1}, errors.New("provider outage")
	}, func(context.Context) error {
		scans++
		if scans == 1 {
			return errors.New("Navidrome unavailable")
		}
		return nil
	})
	m.Enqueue("release", rel)
	if err := m.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s := m.Get("release"); s.Status != "partial" || s.Attempts != 1 {
		t.Fatalf("%+v", s)
	}
	m.notify(context.Background())
	var pending int
	m.db.QueryRow(`SELECT scan_pending FROM lyrics_jobs`).Scan(&pending)
	if pending != 1 {
		t.Fatal("failed notification was discarded")
	}
	m.db.Exec(`UPDATE lyrics_jobs SET next_scan=0`)
	m.notify(context.Background())
	m.db.QueryRow(`SELECT scan_pending FROM lyrics_jobs`).Scan(&pending)
	if scans != 2 || pending != 0 {
		t.Fatal(scans, pending)
	}
	if err := m.Retry("release"); err != nil {
		t.Fatal(err)
	}
	if s := m.Get("release"); s.Status != "queued" || s.Attempts != 0 {
		t.Fatalf("%+v", s)
	}
	if err := m.Retry("unknown"); err == nil {
		t.Fatal("unknown job accepted")
	}
}
func TestAlbumScopeRejectsLibraryRootTraversalAndEscapingSymlink(t *testing.T) {
	o, rel := fixture(t)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(o.Library, "escape"))
	for _, bad := range []string{"", ".", "..", outside, "escape", "../elsewhere"} {
		if _, err := albumPath(o.Library, bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if got, err := albumPath(o.Library, rel); err != nil || got != filepath.Join(o.Library, rel) {
		t.Fatal(got, err)
	}
}
func writeRows(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, row := range rows {
		row["tool"] = "lrcscript"
		if err := json.NewEncoder(f).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
}
func TestReportUsesLatestTrackResultsAndRequiresCurrentEndRecord(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.jsonl")
	started := time.Now()
	now := float64(started.UnixNano()) / 1e9
	writeRows(t, report,
		map[string]any{"event": "start", "run_id": "old", "time": now - 100},
		map[string]any{"event": "track", "path": "Disc 1/a.flac", "status": "saved"},
		map[string]any{"event": "track", "path": "Disc 2/b.flac", "status": "deferred"},
		map[string]any{"event": "end", "run_id": "old", "pass_complete": true},
		map[string]any{"event": "start", "run_id": "new", "time": now + 0.001},
		map[string]any{"event": "track", "path": "Disc 2/b.flac", "status": "saved"})
	s, complete, err := readReport(report, started)
	if err != nil || complete || s.Saved != 2 || s.Deferred != 0 {
		t.Fatal(s, complete, err)
	}
	writeRows(t, report, map[string]any{"event": "end", "run_id": "new", "pass_complete": true})
	s, complete, err = readReport(report, started)
	if err != nil || !complete || s.Saved != 2 {
		t.Fatal(s, complete, err)
	}
	_, complete, err = readReport(report, time.Now().Add(time.Hour))
	if err != nil || complete {
		t.Fatal("stale report masked failed launch", err)
	}
}
func TestExecuteUsesExactFolderAndConservativeFlagsWithoutShell(t *testing.T) {
	o, rel := fixture(t)
	o.Script = filepath.Join(t.TempDir(), "fake script.py")
	o.Engine = "/not invoked/helper"
	o.SyncedOnly = true
	o.RequireAlbumMatch = true
	script := `import sys,json,time,pathlib
args=sys.argv[1:]
assert args[0]=='download' and '--all' in args and '--verified-only' in args and '--synced-only' in args and '--require-album-match' in args
root=pathlib.Path(args[args.index('--root')+1])
assert root.name=="Album; quoted ' name" and (root/'Disc 2').is_dir()
report=pathlib.Path(args[args.index('--report')+1])
with report.open('a') as f:
 for row in [dict(event='start',run_id='new',time=time.time()),dict(event='track',path='Disc 2/song.flac',status='saved'),dict(event='end',run_id='new',pass_complete=True)]:
  row['tool']='lrcscript';f.write(json.dumps(row)+'\n')
`
	if err := os.WriteFile(o.Script, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	m := openQueue(t, o, nil, nil)
	summary, err := m.execute(context.Background(), m.task("release", rel))
	if err != nil || summary.Saved != 1 {
		t.Fatal(summary, err)
	}
	// A failed invocation must not reuse the successful prior end record.
	os.WriteFile(o.Script, []byte("raise SystemExit(2)"), 0600)
	_, err = m.execute(context.Background(), m.task("release", rel))
	if err == nil {
		t.Fatal("failed launch accepted using an old report")
	}
}
func TestCancellationKillsHelperProcessGroup(t *testing.T) {
	o, rel := fixture(t)
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "child.pid")
	o.Script = filepath.Join(dir, "runner.py")
	script := fmt.Sprintf("import subprocess,pathlib,time\np=subprocess.Popen(['python3','-c','import time;time.sleep(120)'])\npathlib.Path(%q).write_text(str(p.pid))\ntime.sleep(120)\n", pidfile)
	os.WriteFile(o.Script, []byte(script), 0600)
	m := openQueue(t, o, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.execute(ctx, m.task("release", rel)); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidfile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process did not stop")
	}
	// On Linux a killed child can briefly remain a zombie awaiting its subreaper.
	stat, err := os.ReadFile(filepath.Join("/proc", strings.TrimSpace(string(b)), "stat"))
	if err == nil && !strings.Contains(string(stat), ") Z ") {
		t.Fatalf("helper survived cancellation: %s", stat)
	}
}
func TestQueueRunsOnlyOneReleaseAtATime(t *testing.T) {
	o, rel := fixture(t)
	var active, maxActive atomic.Int32
	m := openQueue(t, o, func(context.Context, Task) (Summary, error) {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		defer active.Add(-1)
		return Summary{Missing: 1}, nil
	}, nil)
	m.Enqueue("a", rel)
	m.Enqueue("b", rel)
	for range 2 {
		if err := m.processNext(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if maxActive.Load() != 1 || m.Get("a").Status != "completed" || m.Get("b").Status != "completed" {
		t.Fatal("queue did not finish serially")
	}
}
