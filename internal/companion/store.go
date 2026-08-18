package companion

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const companionSchema = `
CREATE TABLE IF NOT EXISTS companion_movies (
  id TEXT PRIMARY KEY,
  state_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS companion_searches (
  movie_id TEXT PRIMARY KEY,
  searched_at TEXT NOT NULL,
  candidates_json TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS companion_search_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  movie_id TEXT NOT NULL,
  query TEXT NOT NULL,
  searched_at TEXT NOT NULL,
  status TEXT NOT NULL,
  candidate_count INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS companion_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

type searchHistoryRecord struct {
	MovieID        string
	Query          string
	SearchedAt     time.Time
	Status         string
	CandidateCount int
	Error          string
}

type stateStore struct {
	db *sql.DB
}

func openStateStore(configuredPath string) (*stateStore, stateFile, map[string]searchState, error) {
	if strings.TrimSpace(configuredPath) == "" {
		return nil, stateFile{}, nil, fmt.Errorf("companion state path is empty")
	}
	dbPath := companionDBPath(configuredPath)
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, stateFile{}, nil, fmt.Errorf("create companion database directory: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, stateFile{}, nil, fmt.Errorf("open companion database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &stateStore{db: db}
	closeOnError := func(err error) (*stateStore, stateFile, map[string]searchState, error) {
		_ = db.Close()
		return nil, stateFile{}, nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return closeOnError(fmt.Errorf("configure companion database: %w", err))
	}
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return closeOnError(fmt.Errorf("configure companion database journal: %w", err))
	}
	if _, err := db.Exec(companionSchema); err != nil {
		return closeOnError(fmt.Errorf("create companion database schema: %w", err))
	}
	if err := store.migrateLegacy(configuredPath, dbPath); err != nil {
		return closeOnError(err)
	}
	state, searches, err := store.load()
	if err != nil {
		return closeOnError(err)
	}
	return store, state, searches, nil
}

func companionDBPath(configuredPath string) string {
	if strings.EqualFold(filepath.Ext(configuredPath), ".json") {
		return strings.TrimSuffix(configuredPath, filepath.Ext(configuredPath)) + ".db"
	}
	return configuredPath
}

func companionLegacyPath(configuredPath, dbPath string) string {
	if strings.EqualFold(filepath.Ext(configuredPath), ".json") {
		return configuredPath
	}
	if strings.EqualFold(filepath.Ext(dbPath), ".db") {
		return strings.TrimSuffix(dbPath, filepath.Ext(dbPath)) + ".json"
	}
	return dbPath + ".json"
}

func (s *stateStore) migrateLegacy(configuredPath, dbPath string) error {
	var movieCount, searchCount, settingCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM companion_movies").Scan(&movieCount); err != nil {
		return fmt.Errorf("inspect companion database movies: %w", err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM companion_searches").Scan(&searchCount); err != nil {
		return fmt.Errorf("inspect companion database searches: %w", err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM companion_settings").Scan(&settingCount); err != nil {
		return fmt.Errorf("inspect companion database settings: %w", err)
	}
	if movieCount > 0 || searchCount > 0 || settingCount > 0 {
		return nil
	}
	legacyPath := companionLegacyPath(configuredPath, dbPath)
	if legacyPath == dbPath {
		return nil
	}
	if _, err := os.Stat(legacyPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect legacy companion state: %w", err)
	}
	legacy, err := loadState(legacyPath)
	if err != nil {
		return fmt.Errorf("migrate legacy companion state: %w", err)
	}
	if err := s.save(legacy, nil, nil); err != nil {
		return fmt.Errorf("save migrated companion state: %w", err)
	}
	return nil
}

func (s *stateStore) load() (stateFile, map[string]searchState, error) {
	state := stateFile{Version: stateVersion}
	searches := map[string]searchState{}
	rows, err := s.db.Query("SELECT state_json FROM companion_movies ORDER BY id")
	if err != nil {
		return stateFile{}, nil, fmt.Errorf("load companion movies: %w", err)
	}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("read companion movie: %w", err)
		}
		var movie Movie
		if err := json.Unmarshal([]byte(raw), &movie); err != nil {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("parse companion movie: %w", err)
		}
		if movie.ID == "" {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("companion database contains a movie without an id")
		}
		state.Movies = append(state.Movies, &movie)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return stateFile{}, nil, fmt.Errorf("load companion movies: %w", err)
	}
	if err := rows.Close(); err != nil {
		return stateFile{}, nil, fmt.Errorf("close companion movies: %w", err)
	}

	rows, err = s.db.Query("SELECT movie_id, searched_at, candidates_json FROM companion_searches ORDER BY movie_id")
	if err != nil {
		return stateFile{}, nil, fmt.Errorf("load companion searches: %w", err)
	}
	for rows.Next() {
		var movieID, searchedAt, candidatesJSON string
		if err := rows.Scan(&movieID, &searchedAt, &candidatesJSON); err != nil {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("read companion search: %w", err)
		}
		at, err := time.Parse(time.RFC3339Nano, searchedAt)
		if err != nil {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("parse companion search timestamp: %w", err)
		}
		var candidates []Candidate
		if err := json.Unmarshal([]byte(candidatesJSON), &candidates); err != nil {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("parse companion candidates: %w", err)
		}
		searches[movieID] = searchState{Candidates: candidates, SearchedAt: at}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return stateFile{}, nil, fmt.Errorf("load companion searches: %w", err)
	}
	if err := rows.Close(); err != nil {
		return stateFile{}, nil, fmt.Errorf("close companion searches: %w", err)
	}

	rows, err = s.db.Query("SELECT key, value FROM companion_settings")
	if err != nil {
		return stateFile{}, nil, fmt.Errorf("load companion settings: %w", err)
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			_ = rows.Close()
			return stateFile{}, nil, fmt.Errorf("read companion setting: %w", err)
		}
		n, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		switch key {
		case "search_interval_seconds":
			state.SearchIntervalSeconds = n
		case "search_batch_size":
			state.SearchBatchSize = n
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return stateFile{}, nil, fmt.Errorf("load companion settings: %w", err)
	}
	if err := rows.Close(); err != nil {
		return stateFile{}, nil, fmt.Errorf("close companion settings: %w", err)
	}
	sortMovies(state.Movies)
	return state, searches, nil
}

func (s *stateStore) save(state stateFile, searches map[string]searchState, history *searchHistoryRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin companion database transaction: %w", err)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DELETE FROM companion_movies"); err != nil {
		return rollback(fmt.Errorf("clear companion movies: %w", err))
	}
	for _, movie := range state.Movies {
		raw, err := json.Marshal(movie)
		if err != nil {
			return rollback(fmt.Errorf("encode companion movie: %w", err))
		}
		if _, err := tx.Exec("INSERT INTO companion_movies (id, state_json) VALUES (?, ?)", movie.ID, string(raw)); err != nil {
			return rollback(fmt.Errorf("save companion movie: %w", err))
		}
	}
	if _, err := tx.Exec("DELETE FROM companion_searches"); err != nil {
		return rollback(fmt.Errorf("clear companion searches: %w", err))
	}
	for movieID, search := range searches {
		raw, err := json.Marshal(search.Candidates)
		if err != nil {
			return rollback(fmt.Errorf("encode companion candidates: %w", err))
		}
		if _, err := tx.Exec("INSERT INTO companion_searches (movie_id, searched_at, candidates_json) VALUES (?, ?, ?)", movieID, search.SearchedAt.UTC().Format(time.RFC3339Nano), string(raw)); err != nil {
			return rollback(fmt.Errorf("save companion search: %w", err))
		}
	}
	if _, err := tx.Exec("INSERT OR REPLACE INTO companion_settings (key, value) VALUES (?, ?), (?, ?)", "search_interval_seconds", strconv.Itoa(state.SearchIntervalSeconds), "search_batch_size", strconv.Itoa(state.SearchBatchSize)); err != nil {
		return rollback(fmt.Errorf("save companion settings: %w", err))
	}
	if history != nil {
		if _, err := tx.Exec(`INSERT INTO companion_search_history
			(movie_id, query, searched_at, status, candidate_count, error)
			VALUES (?, ?, ?, ?, ?, ?)`, history.MovieID, history.Query,
			history.SearchedAt.UTC().Format(time.RFC3339Nano), history.Status,
			history.CandidateCount, history.Error); err != nil {
			return rollback(fmt.Errorf("save companion search history: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit companion database transaction: %w", err)
	}
	return nil
}

func sortMovies(movies []*Movie) {
	sort.SliceStable(movies, func(i, j int) bool {
		return movies[i].ID < movies[j].ID
	})
}
