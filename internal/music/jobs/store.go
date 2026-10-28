package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// store keeps the queue and history in a JSON file so they survive restarts.
type store struct {
	path string
}

type stateFile struct {
	Version int    `json:"version"`
	Jobs    []*Job `json:"jobs"`
}

func (s store) load() ([]*Job, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st stateFile
	if err := json.Unmarshal(b, &st); err != nil {
		// Keep the broken file for inspection and start empty.
		_ = os.Rename(s.path, s.path+".broken")
		return nil, nil
	}
	return st.Jobs, nil
}

func (s store) save(jobs []*Job) error {
	b, err := json.MarshalIndent(stateFile{Version: 1, Jobs: jobs}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
