// Package lyrics processes imported releases independently of the download queue.
// SQLite is the durable outbox: dismissing music history never discards lyrics work.
package lyrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Options struct {
	Library, Data, Script, Engine               string
	MaxAttempts                                 int
	RetryDelay, Timeout                         time.Duration
	VerifiedOnly, SyncedOnly, RequireAlbumMatch bool
}

type Summary struct {
	Saved    int `json:"saved"`
	Missing  int `json:"missing"`
	Deferred int `json:"deferred"`
	Errors   int `json:"errors"`
	Skipped  int `json:"skipped"`
}

type State struct {
	Status      string `json:"status"`
	Attempts    int    `json:"attempts"`
	NextAttempt int64  `json:"next_attempt,omitempty"`
	Summary
}

type Task struct{ ID, Path, Report, Log string }
type Runner func(context.Context, Task) (Summary, error)

type Manager struct {
	db   *sql.DB
	opts Options
	run  Runner
	scan func(context.Context) error
	log  *slog.Logger
}

func New(opts Options, run Runner, scan func(context.Context) error) (*Manager, error) {
	if opts.MaxAttempts < 1 || opts.RetryDelay <= 0 || opts.Timeout <= 0 {
		return nil, errors.New("invalid lyrics retry or timeout options")
	}
	if err := os.MkdirAll(filepath.Join(opts.Data, "lyrics"), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(opts.Data, "lyrics", "queue.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000",
		`CREATE TABLE IF NOT EXISTS lyrics_jobs (id TEXT PRIMARY KEY, path TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'queued', attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0, summary TEXT NOT NULL DEFAULT '{}', last_error TEXT NOT NULL DEFAULT '', scan_pending INTEGER NOT NULL DEFAULT 0, next_scan INTEGER NOT NULL DEFAULT 0)`,
		`UPDATE lyrics_jobs SET status='queued', attempts=MAX(0,attempts-1) WHERE status='running'`,
	} {
		if _, err = db.Exec(statement); err != nil {
			db.Close()
			return nil, err
		}
	}
	m := &Manager{db: db, opts: opts, run: run, scan: scan, log: slog.Default()}
	if m.run == nil {
		m.run = m.execute
	}
	return m, nil
}

func (m *Manager) Close() error { return m.db.Close() }

// Enqueue is idempotent; receipts are independent of music history retention.
func (m *Manager) Enqueue(id, rel string) error {
	if id == "" {
		return errors.New("empty lyrics job ID")
	}
	if _, err := albumPath(m.opts.Library, rel); err != nil {
		return err
	}
	_, err := m.db.Exec(`INSERT INTO lyrics_jobs(id,path) VALUES(?,?) ON CONFLICT(id) DO NOTHING`, id, rel)
	return err
}

func (m *Manager) Get(id string) *State {
	var s State
	var summary string
	if err := m.db.QueryRow(`SELECT status,attempts,next_attempt,summary FROM lyrics_jobs WHERE id=?`, id).Scan(&s.Status, &s.Attempts, &s.NextAttempt, &summary); err != nil {
		return nil
	}
	_ = json.Unmarshal([]byte(summary), &s.Summary)
	return &s
}

func (m *Manager) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := m.processNext(ctx); err != nil {
			m.log.Error("lyrics queue", "err", err)
		}
		m.notify(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) processNext(ctx context.Context) error {
	// A transient failure while committing a result must not strand a running
	// receipt until the next server restart. There is exactly one queue worker.
	if _, err := m.db.Exec(`UPDATE lyrics_jobs SET status='queued',attempts=MAX(0,attempts-1) WHERE status='running'`); err != nil {
		return err
	}
	var id, rel string
	err := m.db.QueryRow(`SELECT id,path FROM lyrics_jobs WHERE status IN ('queued','deferred') AND next_attempt<=? ORDER BY next_attempt,rowid LIMIT 1`, time.Now().Unix()).Scan(&id, &rel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = m.db.Exec(`UPDATE lyrics_jobs SET status='running',attempts=attempts+1 WHERE id=?`, id); err != nil {
		return err
	}
	task := m.task(id, rel)
	runctx, cancel := context.WithTimeout(ctx, m.opts.Timeout)
	result, runErr := m.run(runctx, task)
	cancel()
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		// Don't spend an attempt on a server shutdown. Preserve any partial writes
		// and arrange a scan even if the process was stopped before its end record.
		_, err = m.db.Exec(`UPDATE lyrics_jobs SET status='queued',attempts=MAX(0,attempts-1),summary=?,scan_pending=MAX(scan_pending,?) WHERE id=?`, string(payload), boolInt(result.Saved > 0 && m.scan != nil), id)
		return err
	}
	state := m.Get(id)
	if state == nil {
		return fmt.Errorf("lyrics job %s disappeared", id)
	}
	status := "completed"
	next := int64(0)
	message := ""
	if runErr != nil || result.Deferred > 0 || result.Errors > 0 {
		status = "partial"
		if runErr != nil {
			message = runErr.Error()
		}
		if state.Attempts < m.opts.MaxAttempts {
			status = "deferred"
			delay := m.opts.RetryDelay * time.Duration(1<<min(state.Attempts-1, 6))
			next = time.Now().Add(delay).Unix()
		}
	}
	_, err = m.db.Exec(`UPDATE lyrics_jobs SET status=?,next_attempt=?,summary=?,last_error=?,scan_pending=MAX(scan_pending,?) WHERE id=?`, status, next, string(payload), message, boolInt(result.Saved > 0 && m.scan != nil), id)
	if err == nil {
		m.log.Info("lyrics release processed", "job", id, "status", status, "saved", result.Saved, "missing", result.Missing, "deferred", result.Deferred, "errors", result.Errors)
	}
	return err
}

func (m *Manager) notify(ctx context.Context) {
	if m.scan == nil || ctx.Err() != nil {
		return
	}
	var id string
	if m.db.QueryRow(`SELECT id FROM lyrics_jobs WHERE scan_pending=1 AND next_scan<=? LIMIT 1`, time.Now().Unix()).Scan(&id) != nil {
		return
	}
	scanctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	err := m.scan(scanctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		m.log.Warn("lyrics Navidrome scan", "job", id, "err", err)
		_, _ = m.db.Exec(`UPDATE lyrics_jobs SET next_scan=? WHERE id=?`, time.Now().Add(time.Minute).Unix(), id)
	} else {
		_, _ = m.db.Exec(`UPDATE lyrics_jobs SET scan_pending=0 WHERE id=?`, id)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// Retry explicitly rechecks an exhausted release without downloading its audio.
func (m *Manager) Retry(id string) error {
	result, err := m.db.Exec(`UPDATE lyrics_jobs SET status='queued',attempts=0,next_attempt=0 WHERE id=? AND status IN ('partial','completed','deferred')`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("lyrics job cannot be retried in its current state")
	}
	return nil
}
