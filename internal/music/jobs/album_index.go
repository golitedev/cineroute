package jobs

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"

	"cineroute/internal/music/verify"
	_ "modernc.org/sqlite"
)

type albumLocation struct {
	folder, sample string
	size, modified int64
}

type albumIndex struct {
	mu        sync.RWMutex
	path      string
	root      string
	locations map[string]albumLocation
}

func (i *albumIndex) open() (*sql.DB, error) {
	file, err := os.OpenFile(i.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", i.path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{"PRAGMA busy_timeout = 5000", `CREATE TABLE IF NOT EXISTS album_locations (
		library TEXT NOT NULL, album_id TEXT NOT NULL, folder TEXT NOT NULL,
		sample TEXT NOT NULL, size INTEGER NOT NULL, modified INTEGER NOT NULL,
		PRIMARY KEY(library, album_id))`} {
		if _, err := db.Exec(query); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func (i *albumIndex) load(jobs []*Job) error {
	history := make(map[string]*Job)
	for _, job := range jobs {
		if job.Status != StatusCompleted || job.AlbumID == "" || job.LibraryPath == "" {
			continue
		}
		old := history[job.AlbumID]
		if old == nil || old.FinishedAt == nil || (job.FinishedAt != nil && !job.FinishedAt.Before(*old.FinishedAt)) {
			history[job.AlbumID] = job
		}
	}
	for id, job := range history {
		i.locations[id] = albumLocation{folder: job.LibraryPath}
	}
	db, err := i.open()
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.Query("SELECT album_id, folder, sample, size, modified FROM album_locations WHERE library = ?", i.root)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var entry albumLocation
		if err := rows.Scan(&id, &entry.folder, &entry.sample, &entry.size, &entry.modified); err != nil {
			return err
		}
		i.locations[id] = entry
		delete(history, id)
	}
	if err := rows.Err(); err != nil || len(history) == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, job := range history {
		if _, err := tx.Exec("INSERT OR IGNORE INTO album_locations VALUES (?, ?, ?, '', 0, 0)", i.root, id, job.LibraryPath); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (i *albumIndex) record(id, folder string, track verify.File) error {
	entry := albumLocation{folder: folder, sample: track.Rel}
	if info, err := os.Lstat(filepath.Join(i.root, folder, track.Rel)); err == nil && info.Mode().IsRegular() {
		entry.size, entry.modified = info.Size(), info.ModTime().UnixNano()
	}
	i.mu.Lock()
	i.locations[id] = entry
	i.mu.Unlock()
	db, err := i.open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO album_locations VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(library, album_id) DO UPDATE SET folder=excluded.folder, sample=excluded.sample,
		size=excluded.size, modified=excluded.modified`, i.root, id, folder, entry.sample, entry.size, entry.modified)
	return err
}

func (i *albumIndex) location(id string) albumLocation {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.locations[id]
}
