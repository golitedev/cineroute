package jobs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cineroute/internal/music/library"
	"cineroute/internal/music/qobuz"
	"cineroute/internal/music/verify"
)

// librarySnapshot is built in the background; requests only read it.
type librarySnapshot struct {
	paths  map[string]bool
	albums map[string]string // release ID -> representative FLAC path
}

func (m *Manager) scanLibrary() librarySnapshot {
	out := librarySnapshot{paths: map[string]bool{}, albums: map[string]string{}}
	root := filepath.Clean(m.cfg.Paths.Library)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !audioExt[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".flac") {
			if id := verify.AlbumID(path); id != "" {
				if out.albums[id] == "" {
					out.albums[id] = path
				}
			}
		}
		for dir := filepath.Dir(path); dir != root && withinLibrary(root, dir); dir = filepath.Dir(dir) {
			out.paths[dir] = true
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
// enqueue-time duplicate check. Cached ID hits are checked against one file.
func (m *Manager) InLibrary(album qobuz.Album) bool {
	if album.ID == "" || album.Title == "" || album.Artist == "" {
		return false
	}
	index, ready := m.libraryCache.Peek()
	if !ready {
		return m.albumNamePath(album) != ""
	}
	if m.albumIDPath(album.ID) != "" {
		return true
	}
	if rel, err := m.folderFor(album, library.Quality{}); err == nil && (index.paths[filepath.Join(m.cfg.Paths.Library, rel)] || index.paths[filepath.Join(m.cfg.Paths.Library, rel+" - INCOMPLETE")]) {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		// Skipped jobs only record a folder collision, not a verified import.
		// Older naming templates could send different editions to that folder.
		if job.Status == StatusCompleted && job.AlbumID == album.ID && job.LibraryPath != "" && index.paths[filepath.Join(m.cfg.Paths.Library, job.LibraryPath)] {
			return true
		}
	}
	return false
}

// RefreshLibrary rebuilds discovery presence, for explicit library rescans.
// Normal imports notify the background cache worker instead.
func (m *Manager) RefreshLibrary() { m.libraryCache.Refresh() }

// albumIDPath never walks the library. Validate only the cached candidate,
// so deleted/replaced files cannot cause a stale duplicate result.
func (m *Manager) albumIDPath(id string) string {
	index, ready := m.libraryCache.Peek()
	if !ready || id == "" {
		return ""
	}
	path := index.albums[id]
	if path == "" || !withinLibrary(m.cfg.Paths.Library, path) {
		return ""
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || verify.AlbumID(path) != id {
		return ""
	}
	dir := filepath.Dir(path)
	if strings.HasPrefix(strings.ToLower(filepath.Base(dir)), "disc ") {
		dir = filepath.Dir(dir)
	}
	rel, _ := filepath.Rel(m.cfg.Paths.Library, dir)
	return rel
}

func (m *Manager) albumNamePath(album qobuz.Album) string {
	if rel, err := m.folderFor(album, library.Quality{}); err == nil {
		if library.Exists(m.cfg.Paths.Library, rel) {
			return rel
		}
		if library.Exists(m.cfg.Paths.Library, rel+" - INCOMPLETE") {
			return rel + " - INCOMPLETE"
		}
	}
	return ""
}
