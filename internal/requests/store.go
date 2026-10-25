package requests

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Load() (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		return Defaults(), nil
	} else if err != nil {
		return Settings{}, err
	}
	db, err := s.open()
	if err != nil {
		return Settings{}, err
	}
	defer db.Close()
	var payload string
	err = db.QueryRow("SELECT payload FROM request_settings WHERE id = 1").Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Defaults(), nil
	}
	if err != nil {
		return Settings{}, fmt.Errorf("read request settings: %w", err)
	}
	var settings Settings
	if err := json.Unmarshal([]byte(payload), &settings); err != nil {
		return Settings{}, fmt.Errorf("decode request settings: %w", err)
	}
	if err := settings.Validate(); err != nil {
		return Settings{}, fmt.Errorf("stored request settings are invalid: %w", err)
	}
	return settings, nil
}

func (s *Store) Save(settings Settings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.open()
	if err != nil {
		return err
	}
	defer db.Close()
	payload, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT INTO request_settings (id, payload) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET payload = excluded.payload", string(payload))
	if err != nil {
		return fmt.Errorf("save request settings: %w", err)
	}
	return nil
}

func (s *Store) open() (*sql.DB, error) {
	if s.path == "" {
		return nil, errors.New("request settings database path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return nil, fmt.Errorf("create request settings directory: %w", err)
	}
	file, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := os.Chmod(s.path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	for _, query := range []string{"PRAGMA busy_timeout = 5000", "CREATE TABLE IF NOT EXISTS request_settings (id INTEGER PRIMARY KEY CHECK(id = 1), payload TEXT NOT NULL)", jobSchema} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return nil, fmt.Errorf("open request settings: %w", err)
		}
	}
	return db, nil
}
