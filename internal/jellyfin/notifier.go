// Package jellyfin durably reports library filesystem changes to Jellyfin.
package jellyfin

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cineroute/internal/config"
	_ "modernc.org/sqlite"
)

// Notifier retains delivered event IDs so completion recovery is idempotent.
// Delivery is at least once: a crash after the HTTP call can repeat a report.
type Notifier struct {
	cfg     config.Jellyfin
	db      *sql.DB
	client  *http.Client
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	flushMu sync.Mutex
}

func New(cfg config.Jellyfin) (*Notifier, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.StatePath), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(cfg.StatePath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	if err = os.Chmod(cfg.StatePath, 0600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", cfg.StatePath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA busy_timeout = 5000", `CREATE TABLE IF NOT EXISTS notifications (
 id TEXT PRIMARY KEY, path TEXT NOT NULL, delivered INTEGER NOT NULL DEFAULT 0,
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0,
 refresh_library INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS pending_notifications ON notifications(delivered,next_attempt);`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := migrateNotifications(db); err != nil {
		db.Close()
		return nil, err
	}
	// Never forward the API key to a redirect destination.
	client := &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Notifier{cfg: cfg, db: db, client: client}, nil
}

func (n *Notifier) mapped(path string) string {
	path = filepath.Clean(path)
	best, remote := "", ""
	for _, m := range n.cfg.PathMappings {
		root := filepath.Clean(m.Local)
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) && len(root) > len(best) {
			best, remote = root, filepath.Join(m.Remote, rel)
		}
	}
	if best != "" {
		return remote
	}
	return path
}

// Enqueue reports a changed folder, used for subtitle installations.
// It writes locally and never waits for Jellyfin. Reusing an event ID is safe.
func (n *Notifier) Enqueue(id, folder string) error {
	return n.enqueue(id, folder, false)
}

// EnqueueDownload requests an explicit refresh of the containing library after
// download verification. Existing delivered event IDs are never replayed.
func (n *Notifier) EnqueueDownload(id, folder string) error {
	return n.enqueue(id, folder, true)
}

func (n *Notifier) enqueue(id, folder string, refreshLibrary bool) error {
	if n == nil {
		return nil
	}
	if id == "" || !filepath.IsAbs(folder) {
		return fmt.Errorf("Jellyfin notifications require an event ID and absolute folder path")
	}
	_, err := n.db.Exec("INSERT INTO notifications(id,path,refresh_library) VALUES(?,?,?) ON CONFLICT(id) DO NOTHING", id, n.mapped(folder), refreshLibrary)
	return err
}

// Upgrade existing queues without replaying delivered records. Old download
// notifications already used this prefix; subtitle and other events stay local.
func migrateNotifications(db *sql.DB) error {
	rows, err := db.Query("PRAGMA table_info(notifications)")
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "refresh_library" {
			found = true
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil || found {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("ALTER TABLE notifications ADD COLUMN refresh_library INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE notifications SET refresh_library=1 WHERE delivered=0 AND id LIKE 'download:%'"); err != nil {
		return err
	}
	return tx.Commit()
}

func (n *Notifier) Start(ctx context.Context, reconcile func() error) {
	if n == nil {
		return
	}
	ctx, n.cancel = context.WithCancel(ctx)
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if reconcile != nil {
			if err := reconcile(); err != nil {
				slog.Warn("jellyfin: recover notifications", "err", err)
			}
		}
		// A fixed batching window also prevents continuous additions starving delivery.
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if reconcile != nil {
					if err := reconcile(); err != nil {
						slog.Warn("jellyfin: queue notifications", "err", err)
					}
				}
				if err := n.Flush(ctx); err != nil && ctx.Err() == nil {
					slog.Warn("jellyfin: notification pending; will retry", "err", err)
				}
			}
		}
	}()
}

func (n *Notifier) Close() {
	if n == nil {
		return
	}
	if n.cancel != nil {
		n.cancel()
	}
	n.wg.Wait()
	n.db.Close()
}

type update struct {
	Path       string `json:"Path"`
	UpdateType string `json:"UpdateType"`
}

type event struct {
	id, path       string
	attempts       int
	refreshLibrary bool
}

// Flush sends at most 200 events, grouping downloads by library and subtitles
// by folder. An error for one library does not retry successful library updates.
func (n *Notifier) Flush(ctx context.Context) error {
	if n == nil {
		return nil
	}
	n.flushMu.Lock()
	defer n.flushMu.Unlock()
	rows, err := n.db.QueryContext(ctx, "SELECT id,path,attempts,refresh_library FROM notifications WHERE delivered=0 AND next_attempt<=? ORDER BY rowid LIMIT 200", time.Now().Unix())
	if err != nil {
		return err
	}
	var events []event
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.path, &e.attempts, &e.refreshLibrary); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	results := n.deliver(ctx, events)
	// Persist retry state even when shutdown canceled the HTTP request.
	tx, dbErr := n.db.Begin()
	if dbErr != nil {
		return dbErr
	}
	defer tx.Rollback()
	var firstErr error
	pending := 0
	for i, e := range events {
		deliveryErr := results[i]
		if deliveryErr == nil {
			_, dbErr = tx.Exec("UPDATE notifications SET delivered=1 WHERE id=?", e.id)
		} else {
			pending++
			if firstErr == nil {
				firstErr = deliveryErr
			}
			delay := 30 * time.Second * time.Duration(1<<min(e.attempts, 7))
			if delay > time.Hour {
				delay = time.Hour
			}
			_, dbErr = tx.Exec("UPDATE notifications SET attempts=attempts+1,next_attempt=? WHERE id=?", time.Now().Add(delay).Unix(), e.id)
		}
		if dbErr != nil {
			return dbErr
		}
	}
	if dbErr = tx.Commit(); dbErr != nil {
		return dbErr
	}
	if pending > 0 {
		return fmt.Errorf("%d Jellyfin notifications pending: %w", pending, firstErr)
	}
	return nil
}
