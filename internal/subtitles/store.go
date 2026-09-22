package subtitles

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// store is the durable subtitle state. Unlike the companion store it writes one
// row per item instead of rewriting the whole table, because a remote library
// can hold thousands of video files.
type store struct {
	db *sql.DB
}

const storeSchema = `
CREATE TABLE IF NOT EXISTS subtitle_items (
  id TEXT PRIMARY KEY,
  state_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS subtitle_searches (
  item_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  query TEXT NOT NULL,
  status TEXT NOT NULL,
  result_count INTEGER NOT NULL DEFAULT 0,
  searched_at TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (item_id, kind)
);
CREATE TABLE IF NOT EXISTS subtitle_candidates (
  item_id TEXT NOT NULL,
  file_id INTEGER NOT NULL,
  rank INTEGER NOT NULL,
  score REAL NOT NULL,
  category TEXT NOT NULL,
  safe INTEGER NOT NULL DEFAULT 0,
  release TEXT NOT NULL DEFAULT '',
  language TEXT NOT NULL DEFAULT '',
  variant TEXT NOT NULL DEFAULT '',
  reasons_json TEXT NOT NULL DEFAULT '[]',
  raw_json TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (item_id, file_id)
);
CREATE TABLE IF NOT EXISTS subtitle_features (
  item_id TEXT PRIMARY KEY,
  feature_id TEXT NOT NULL DEFAULT '',
  raw_json TEXT NOT NULL DEFAULT '',
  resolved_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS subtitle_attempts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  item_id TEXT NOT NULL,
  file_id INTEGER NOT NULL,
  language TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  category TEXT NOT NULL DEFAULT '',
  release TEXT NOT NULL DEFAULT '',
  metrics_json TEXT NOT NULL DEFAULT '',
  detail TEXT NOT NULL DEFAULT '',
  at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS subtitle_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS subtitle_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT NOT NULL DEFAULT '',
  total INTEGER NOT NULL DEFAULT 0,
  done INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
`

// searchRecord caches one OpenSubtitles query. Searches do not consume download
// quota, so a cached result is reused on later runs unless refreshed.
type searchRecord struct {
	ItemID      string
	Kind        string
	Query       string
	Status      string
	ResultCount int
	SearchedAt  time.Time
	Error       string
}

func openStore(path string) (*store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("subtitles state path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create subtitles database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open subtitles database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure subtitles database: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure subtitles database journal: %w", err)
	}
	if _, err := db.Exec(storeSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("create subtitles database schema: %w", err)
	}
	if err := migrateStore(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

// migrateStore adds the columns that multi-language support introduced to a
// database written by an older build. SQLite has no "ADD COLUMN IF NOT EXISTS",
// so every column is checked against PRAGMA table_info first; an old database
// keeps working, it just has no language recorded for its cached candidates.
func migrateStore(db *sql.DB) error {
	columns := []struct {
		table  string
		column string
		ddl    string
	}{
		{"subtitle_candidates", "language", "TEXT NOT NULL DEFAULT ''"},
		{"subtitle_candidates", "variant", "TEXT NOT NULL DEFAULT ''"},
		{"subtitle_attempts", "language", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, entry := range columns {
		exists, err := storeHasColumn(db, entry.table, entry.column)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err := db.Exec("ALTER TABLE " + entry.table + " ADD COLUMN " + entry.column + " " + entry.ddl); err != nil {
			return fmt.Errorf("migrate %s.%s: %w", entry.table, entry.column, err)
		}
	}
	return nil
}

func storeHasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid      int
			name     string
			kind     string
			notNull  int
			defaultV sql.NullString
			primaryK int
		)
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultV, &primaryK); err != nil {
			return false, fmt.Errorf("read %s columns: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *store) close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *store) loadItems() ([]*Item, error) {
	rows, err := s.db.Query("SELECT state_json FROM subtitle_items ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("load subtitle items: %w", err)
	}
	defer rows.Close()
	var items []*Item
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("read subtitle item: %w", err)
		}
		var item Item
		if err := json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, fmt.Errorf("parse subtitle item: %w", err)
		}
		if item.ID == "" {
			return nil, errors.New("subtitles database contains an item without an id")
		}
		items = append(items, &item)
	}
	return items, rows.Err()
}

func (s *store) saveItem(item *Item) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode subtitle item: %w", err)
	}
	if _, err := s.db.Exec("INSERT OR REPLACE INTO subtitle_items (id, state_json) VALUES (?, ?)", item.ID, string(raw)); err != nil {
		return fmt.Errorf("save subtitle item: %w", err)
	}
	return nil
}

func (s *store) saveItems(items []*Item) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin subtitle item transaction: %w", err)
	}
	for _, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("encode subtitle item: %w", err)
		}
		if _, err := tx.Exec("INSERT OR REPLACE INTO subtitle_items (id, state_json) VALUES (?, ?)", item.ID, string(raw)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("save subtitle item: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subtitle items: %w", err)
	}
	return nil
}

func (s *store) deleteItem(id string) error {
	if _, err := s.db.Exec("DELETE FROM subtitle_items WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete subtitle item: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM subtitle_searches WHERE item_id = ?", id); err != nil {
		return fmt.Errorf("delete subtitle searches: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM subtitle_candidates WHERE item_id = ?", id); err != nil {
		return fmt.Errorf("delete subtitle candidates: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM subtitle_features WHERE item_id = ?", id); err != nil {
		return fmt.Errorf("delete subtitle feature: %w", err)
	}
	return nil
}

func (s *store) loadSettings() (map[string]string, error) {
	rows, err := s.db.Query("SELECT key, value FROM subtitle_settings")
	if err != nil {
		return nil, fmt.Errorf("load subtitle settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("read subtitle setting: %w", err)
		}
		out[key] = value
	}
	return out, rows.Err()
}

func (s *store) saveSetting(key, value string) error {
	if _, err := s.db.Exec("INSERT OR REPLACE INTO subtitle_settings (key, value) VALUES (?, ?)", key, value); err != nil {
		return fmt.Errorf("save subtitle setting: %w", err)
	}
	return nil
}

func (s *store) saveSearch(record searchRecord) error {
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO subtitle_searches
		(item_id, kind, query, status, result_count, searched_at, error)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.ItemID, record.Kind, record.Query, record.Status, record.ResultCount,
		record.SearchedAt.UTC().Format(time.RFC3339Nano), record.Error); err != nil {
		return fmt.Errorf("save subtitle search: %w", err)
	}
	return nil
}

func (s *store) loadSearches() (map[string]map[string]searchRecord, error) {
	rows, err := s.db.Query("SELECT item_id, kind, query, status, result_count, searched_at, error FROM subtitle_searches")
	if err != nil {
		return nil, fmt.Errorf("load subtitle searches: %w", err)
	}
	defer rows.Close()
	out := map[string]map[string]searchRecord{}
	for rows.Next() {
		var record searchRecord
		var searchedAt string
		if err := rows.Scan(&record.ItemID, &record.Kind, &record.Query, &record.Status, &record.ResultCount, &searchedAt, &record.Error); err != nil {
			return nil, fmt.Errorf("read subtitle search: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, searchedAt)
		if err != nil {
			at = time.Time{}
		}
		record.SearchedAt = at
		if out[record.ItemID] == nil {
			out[record.ItemID] = map[string]searchRecord{}
		}
		out[record.ItemID][record.Kind] = record
	}
	return out, rows.Err()
}

func (s *store) replaceCandidates(itemID string, candidates []Candidate) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin subtitle candidates transaction: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM subtitle_candidates WHERE item_id = ?", itemID); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear subtitle candidates: %w", err)
	}
	for _, candidate := range candidates {
		reasons, err := json.Marshal(candidate.Reasons)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("encode candidate reasons: %w", err)
		}
		raw := string(candidate.Raw)
		if raw == "" {
			raw = "{}"
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO subtitle_candidates
			(item_id, file_id, rank, score, category, safe, release, language, variant, reasons_json, raw_json)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			itemID, candidate.FileID, candidate.Rank, candidate.Score, candidate.Category,
			boolToInt(candidate.Safe), candidate.Release, candidate.Language, candidate.Variant,
			string(reasons), raw); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("save subtitle candidate: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit subtitle candidates: %w", err)
	}
	return nil
}

func (s *store) loadCandidates(itemID string) ([]Candidate, error) {
	rows, err := s.db.Query(`SELECT file_id, rank, score, category, safe, release, language, variant, reasons_json, raw_json
		FROM subtitle_candidates WHERE item_id = ? ORDER BY rank`, itemID)
	if err != nil {
		return nil, fmt.Errorf("load subtitle candidates: %w", err)
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		var candidate Candidate
		var safe int
		var reasonsJSON, rawJSON string
		if err := rows.Scan(&candidate.FileID, &candidate.Rank, &candidate.Score, &candidate.Category,
			&safe, &candidate.Release, &candidate.Language, &candidate.Variant, &reasonsJSON, &rawJSON); err != nil {
			return nil, fmt.Errorf("read subtitle candidate: %w", err)
		}
		candidate.Safe = safe != 0
		_ = json.Unmarshal([]byte(reasonsJSON), &candidate.Reasons)
		candidate.Raw = json.RawMessage(rawJSON)
		out = append(out, candidate)
	}
	return out, rows.Err()
}

func (s *store) saveFeature(itemID, featureID string, raw json.RawMessage) error {
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO subtitle_features (item_id, feature_id, raw_json, resolved_at)
		VALUES (?, ?, ?, ?)`, itemID, featureID, string(raw), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("save subtitle feature: %w", err)
	}
	return nil
}

func (s *store) loadFeature(itemID string) (string, json.RawMessage, bool, error) {
	var featureID, raw string
	err := s.db.QueryRow("SELECT feature_id, raw_json FROM subtitle_features WHERE item_id = ?", itemID).Scan(&featureID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, fmt.Errorf("load subtitle feature: %w", err)
	}
	return featureID, json.RawMessage(raw), true, nil
}

func (s *store) addAttempt(attempt Attempt) error {
	var metricsJSON string
	if attempt.Metrics != nil {
		encoded, err := json.Marshal(attempt.Metrics)
		if err != nil {
			return fmt.Errorf("encode subtitle attempt metrics: %w", err)
		}
		metricsJSON = string(encoded)
	}
	if _, err := s.db.Exec(`INSERT INTO subtitle_attempts
		(item_id, file_id, language, status, category, release, metrics_json, detail, at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ItemID, attempt.FileID, attempt.Language, attempt.Status, attempt.Category, attempt.Release,
		metricsJSON, attempt.Detail, attempt.At.UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("save subtitle attempt: %w", err)
	}
	return nil
}

func (s *store) loadAttempts(itemID string) ([]Attempt, error) {
	rows, err := s.db.Query(`SELECT file_id, language, status, category, release, metrics_json, detail, at
		FROM subtitle_attempts WHERE item_id = ? ORDER BY id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("load subtitle attempts: %w", err)
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var attempt Attempt
		var metricsJSON, at string
		if err := rows.Scan(&attempt.FileID, &attempt.Language, &attempt.Status, &attempt.Category, &attempt.Release,
			&metricsJSON, &attempt.Detail, &at); err != nil {
			return nil, fmt.Errorf("read subtitle attempt: %w", err)
		}
		attempt.ItemID = itemID
		if metricsJSON != "" {
			var metrics Metrics
			if err := json.Unmarshal([]byte(metricsJSON), &metrics); err == nil {
				attempt.Metrics = &metrics
			}
		}
		if parsed, err := time.Parse(time.RFC3339Nano, at); err == nil {
			attempt.At = parsed
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (s *store) clearAttempts(itemID string) error {
	if _, err := s.db.Exec("DELETE FROM subtitle_attempts WHERE item_id = ?", itemID); err != nil {
		return fmt.Errorf("clear subtitle attempts: %w", err)
	}
	return nil
}

func (s *store) clearItemWork(itemID string) error {
	if err := s.clearAttempts(itemID); err != nil {
		return err
	}
	if _, err := s.db.Exec("DELETE FROM subtitle_searches WHERE item_id = ?", itemID); err != nil {
		return fmt.Errorf("clear subtitle searches: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM subtitle_candidates WHERE item_id = ?", itemID); err != nil {
		return fmt.Errorf("clear subtitle candidates: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM subtitle_features WHERE item_id = ?", itemID); err != nil {
		return fmt.Errorf("clear subtitle feature: %w", err)
	}
	return nil
}

func (s *store) startRun(kind string, total int) (int64, error) {
	result, err := s.db.Exec(`INSERT INTO subtitle_runs (kind, started_at, total) VALUES (?, ?, ?)`,
		kind, time.Now().UTC().Format(time.RFC3339Nano), total)
	if err != nil {
		return 0, fmt.Errorf("start subtitle run: %w", err)
	}
	return result.LastInsertId()
}

func (s *store) finishRun(id int64, done int, runError string) error {
	if _, err := s.db.Exec(`UPDATE subtitle_runs SET finished_at = ?, done = ?, error = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), done, runError, id); err != nil {
		return fmt.Errorf("finish subtitle run: %w", err)
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// settingInt reads an integer setting with a default.
func settingInt(settings map[string]string, key string, fallback int) int {
	raw, ok := settings[key]
	if !ok {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

// settingBool reads a boolean setting with a default.
func settingBool(settings map[string]string, key string, fallback bool) bool {
	raw, ok := settings[key]
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
