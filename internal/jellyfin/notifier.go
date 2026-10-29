// Package jellyfin durably reports library filesystem changes to Jellyfin.
package jellyfin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
 attempts INTEGER NOT NULL DEFAULT 0, next_attempt INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX IF NOT EXISTS pending_notifications ON notifications(delivered,next_attempt);`} {
		if _, err = db.Exec(q); err != nil {
			db.Close()
			return nil, err
		}
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

// Enqueue writes locally; it never waits for Jellyfin. Reusing an event ID is safe.
func (n *Notifier) Enqueue(id, folder string) error {
	if n == nil {
		return nil
	}
	if id == "" || !filepath.IsAbs(folder) {
		return fmt.Errorf("Jellyfin notifications require an event ID and absolute folder path")
	}
	_, err := n.db.Exec("INSERT INTO notifications(id,path) VALUES(?,?) ON CONFLICT(id) DO NOTHING", id, n.mapped(folder))
	return err
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

// Flush sends at most 200 queued events, combining events for the same folder.
func (n *Notifier) Flush(ctx context.Context) error {
	if n == nil {
		return nil
	}
	n.flushMu.Lock()
	defer n.flushMu.Unlock()
	rows, err := n.db.QueryContext(ctx, "SELECT id,path,attempts FROM notifications WHERE delivered=0 AND next_attempt<=? ORDER BY rowid LIMIT 200", time.Now().Unix())
	if err != nil {
		return err
	}
	type event struct {
		id, path string
		attempts int
	}
	var events []event
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.path, &e.attempts); err != nil {
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
	seen := map[string]bool{}
	updates := []update{}
	for _, e := range events {
		if !seen[e.path] {
			seen[e.path] = true
			updates = append(updates, update{e.path, "Modified"})
		}
	}
	body, err := json.Marshal(struct {
		Updates []update `json:"Updates"`
	}{updates})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(n.cfg.URL, "/")+"/Library/Media/Updated", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("construct Jellyfin notification")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Emby-Token", n.cfg.APIKey)
	resp, sendErr := n.client.Do(req)
	if sendErr != nil {
		err = fmt.Errorf("Jellyfin request failed (connection, timeout or cancellation)")
	} else {
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err = fmt.Errorf("Jellyfin returned HTTP %d", resp.StatusCode)
		}
	}
	// Persist retry state even when shutdown canceled the HTTP request.
	tx, dbErr := n.db.Begin()
	if dbErr != nil {
		return dbErr
	}
	defer tx.Rollback()
	for _, e := range events {
		if err == nil {
			_, dbErr = tx.Exec("UPDATE notifications SET delivered=1 WHERE id=?", e.id)
		} else {
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
	if err == nil {
		slog.Info("jellyfin: folder updates reported", "folders", len(updates))
	}
	return err
}
