package librarycache

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const snapshotVersion = 1

type snapshotFile[T any] struct {
	path   string
	source string
}

type snapshot[T any] struct {
	Version int    `json:"version"`
	Source  string `json:"source"`
	Value   *T     `json:"value"`
}

func (s snapshotFile[T]) restore() (T, bool) {
	var zero T
	if s.path == "" {
		return zero, false
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return zero, false
	}
	var saved snapshot[T]
	if err := json.Unmarshal(data, &saved); err != nil || saved.Version != snapshotVersion || saved.Source != s.source || saved.Value == nil {
		return zero, false
	}
	return *saved.Value, true
}

func (s snapshotFile[T]) save(value T) error {
	if s.path == "" {
		return nil
	}
	file, err := os.CreateTemp(filepath.Dir(s.path), ".library-cache-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(snapshot[T]{Version: snapshotVersion, Source: s.source, Value: &value})
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), s.path)
}
