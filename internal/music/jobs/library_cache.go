package jobs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cineroute/internal/music/library"
	"cineroute/internal/music/qobuz"
)

// Mark every parent containing a nonempty audio file, supporting folder
// templates with disc subdirectories without recording every track in memory.
func (m *Manager) scanLibrary() map[string]bool {
	out := map[string]bool{}
	root := filepath.Clean(m.cfg.Paths.Library)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !audioExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil
		}
		for dir := filepath.Dir(path); dir != root && withinLibrary(root, dir); dir = filepath.Dir(dir) {
			out[dir] = true
		}
		return nil
	})
	return out
}

func withinLibrary(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// InLibrary uses cached audio presence and the same naming rules as the
// enqueue-time duplicate check, which intentionally remains uncached.
func (m *Manager) InLibrary(album qobuz.Album) bool {
	if album.ID == "" || album.Title == "" || album.Artist == "" {
		return false
	}
	index := m.libraryCache.Get()
	if rel, err := m.folderFor(album, library.Quality{}); err == nil && index[filepath.Join(m.cfg.Paths.Library, rel)] {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.AlbumID == album.ID && job.LibraryPath != "" && index[filepath.Join(m.cfg.Paths.Library, job.LibraryPath)] {
			return true
		}
	}
	return false
}

// RefreshLibrary rebuilds discovery presence, for explicit library rescans.
// Normal imports notify the background cache worker instead.
func (m *Manager) RefreshLibrary() { m.libraryCache.Refresh() }
