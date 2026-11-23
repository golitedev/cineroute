package jobs

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cineroute/internal/music/library"
	"cineroute/internal/music/qobuz"
	"cineroute/internal/music/verify"
)

// librarySnapshot contains tagged albums found by a manual library scan.
type librarySnapshot struct {
	Albums map[string]string `json:"albums"`
}

func (m *Manager) scanLibrary() librarySnapshot {
	out := librarySnapshot{Albums: map[string]string{}}
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
		if !strings.EqualFold(filepath.Ext(path), ".flac") {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			return nil
		}
		if id := verify.AlbumID(path); id != "" && out.Albums[id] == "" {
			out.Albums[id] = path
		}
		return nil
	})
	return out
}

func withinLibrary(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// InLibrary checks saved album locations and the expected album folder.
func (m *Manager) InLibrary(album qobuz.Album) bool {
	if album.ID == "" || album.Title == "" || album.Artist == "" {
		return false
	}
	if m.albumIDPath(album.ID) != "" {
		return true
	}
	rel, err := m.folderFor(album, library.Quality{})
	return err == nil && (m.albumAudio(rel) != "" || m.albumAudio(rel+" - INCOMPLETE") != "")
}

// RefreshLibrary rebuilds the saved list of tagged albums for an explicit scan.
func (m *Manager) RefreshLibrary() { m.libraryCache.Refresh() }

func (m *Manager) StartLibraryScan() error {
	if !m.libraryScan.CompareAndSwap(false, true) {
		return nil
	}
	dir, err := os.Open(m.cfg.Paths.Library)
	if err != nil {
		m.libraryScan.Store(false)
		return fmt.Errorf("music library directory is unavailable")
	}
	info, err := dir.Stat()
	dir.Close()
	if err != nil || !info.IsDir() {
		m.libraryScan.Store(false)
		return fmt.Errorf("music library directory is unavailable")
	}
	go func() {
		defer m.libraryScan.Store(false)
		m.RefreshLibrary()
	}()
	return nil
}

func (m *Manager) LibraryScanRunning() bool { return m.libraryScan.Load() }

// albumIDPath checks one indexed album without walking the library.
func (m *Manager) albumIDPath(id string) string {
	if id == "" {
		return ""
	}
	entry := m.imports.location(id)
	if m.importedAlbumPresent(id, entry) {
		return entry.folder
	}
	index, ready := m.libraryCache.Peek()
	if !ready {
		return ""
	}
	path := index.Albums[id]
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

func (m *Manager) importedAlbumPresent(id string, entry albumLocation) bool {
	if entry.folder == "" || filepath.IsAbs(entry.folder) {
		return false
	}
	folder := filepath.Join(m.cfg.Paths.Library, entry.folder)
	if folder == filepath.Clean(m.cfg.Paths.Library) || !withinLibrary(m.cfg.Paths.Library, folder) {
		return false
	}
	if entry.sample != "" {
		path := filepath.Join(folder, entry.sample)
		if withinLibrary(folder, path) {
			if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				return (info.Size() == entry.size && info.ModTime().UnixNano() == entry.modified) || verify.AlbumID(path) == id
			}
		}
	}
	path := m.albumAudio(entry.folder)
	if path == "" {
		return false
	}
	tag := verify.AlbumID(path)
	if entry.sample != "" {
		return tag == id
	}
	// Older receipts recorded only the folder. Reject a different tagged release.
	return tag == "" || tag == id
}

func (m *Manager) albumAudio(rel string) string {
	root := filepath.Join(m.cfg.Paths.Library, rel)
	if root == filepath.Clean(m.cfg.Paths.Library) || !withinLibrary(m.cfg.Paths.Library, root) {
		return ""
	}
	var sample string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path != root && d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if path != root && d.Type().IsRegular() && audioExt[strings.ToLower(filepath.Ext(path))] {
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				sample = path
				return fs.SkipAll
			}
		}
		return nil
	})
	return sample
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
