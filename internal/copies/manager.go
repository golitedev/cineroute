// Package copies browses movie and TV library pairs and creates hardlinks.
package copies

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cineroute/internal/library"
)

type Item struct {
	ID                string           `json:"id"`
	DriveID           string           `json:"drive_id"`
	Path              string           `json:"path"`
	RemotePath        string           `json:"remote_path"`
	FolderName        string           `json:"folder_name"`
	Status            string           `json:"status"`
	ExistingFiles     []string         `json:"existing_files"`
	ExistingFileSizes map[string]int64 `json:"existing_file_sizes"`
	RemoteFiles       []string         `json:"remote_files"`
	RemoteFileSizes   map[string]int64 `json:"remote_file_sizes"`
}

type View struct {
	MediaType  string    `json:"media_type"`
	Items      []*Item   `json:"items"`
	Open       *Item     `json:"open,omitempty"`
	ScannedAt  time.Time `json:"scanned_at"`
	StateError string    `json:"state_error,omitempty"`
}

// Manager keeps a filesystem inventory, rebuilt on scan and before linking.
// No tracker configuration or stored search history is needed.
type Manager struct {
	lib       *library.Scan
	mediaType string
	mu        sync.RWMutex
	items     []*Item
	scannedAt time.Time
	scanErr   error
}

func NewManager(lib *library.Scan, mediaType string) *Manager {
	return &Manager{lib: lib, mediaType: mediaType, items: []*Item{}}
}

func (m *Manager) View(openID string) View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	view := View{MediaType: m.mediaType, Items: append([]*Item{}, m.items...), ScannedAt: m.scannedAt}
	if m.scanErr != nil {
		view.StateError = m.scanErr.Error()
	}
	for _, item := range m.items {
		if item.ID == openID {
			view.Open = item
			break
		}
	}
	return view
}

func (m *Manager) Scan(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.scanErr = m.scanLocked(ctx)
	return m.scanErr
}

func (m *Manager) scanLocked(ctx context.Context) error {
	var folders []library.Folder
	var err error
	if m.mediaType == "tv" {
		folders, err = m.lib.TVPairs()
	} else {
		folders, err = m.lib.MoviePairs()
	}
	if err != nil {
		return err
	}
	items := make([]*Item, 0, len(folders))
	for _, folder := range folders {
		if err := ctx.Err(); err != nil {
			return err
		}
		remote, _ := m.lib.MovieRemotePath(folder.DriveID, folder.Name)
		if m.mediaType == "tv" {
			remote, _ = m.lib.TVRemotePath(folder.DriveID, folder.Name)
		}
		mainFiles, mainSizes, err := videoFiles(ctx, folder.Path)
		if err != nil {
			return err
		}
		remoteFiles, remoteSizes, err := videoFiles(ctx, remote)
		if err != nil {
			return err
		}
		if len(mainFiles) == 0 && len(remoteFiles) == 0 {
			continue
		}
		status := "main_only"
		if len(mainFiles) == 0 {
			status = "remote_only"
		} else if len(remoteFiles) > 0 {
			status = "both"
		}
		hash := sha256.Sum256([]byte(m.mediaType + "\x00" + folder.DriveID + "\x00" + folder.Name))
		items = append(items, &Item{
			ID: "copy_" + hex.EncodeToString(hash[:16]), DriveID: folder.DriveID,
			Path: folder.Path, RemotePath: remote, FolderName: folder.Name, Status: status,
			ExistingFiles: mainFiles, ExistingFileSizes: mainSizes,
			RemoteFiles: remoteFiles, RemoteFileSizes: remoteSizes,
		})
	}
	// A failed or canceled scan leaves the last complete inventory intact.
	if err := ctx.Err(); err != nil {
		return err
	}
	m.items, m.scannedAt = items, time.Now()
	return nil
}

func (m *Manager) Hardlink(ctx context.Context, id string) (HardlinkResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.scanLocked(ctx); err != nil {
		m.scanErr = err
		return HardlinkResult{}, err
	}
	m.scanErr = nil
	for _, item := range m.items {
		if item.ID != id {
			continue
		}
		if item.RemotePath == "" {
			return HardlinkResult{}, errors.New("this drive has no remote root configured")
		}
		source, target := item.Path, item.RemotePath
		if len(item.ExistingFiles) == 0 {
			source, target = target, source
		}
		result, err := hardlinkTree(source, target)
		if err != nil {
			return result, err
		}
		if err := m.scanLocked(ctx); err != nil {
			m.scanErr = err
			return result, fmt.Errorf("files were hardlinked, but inventory refresh failed: %w", err)
		}
		return result, nil
	}
	return HardlinkResult{}, errors.New("title is no longer in the library; scan again")
}

func videoFiles(ctx context.Context, root string) ([]string, map[string]int64, error) {
	files, sizes := []string{}, map[string]int64{}
	if root == "" {
		return files, sizes, nil
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return files, sizes, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("library folder must be a real directory: %s", root)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mkv", ".mp4", ".m4v", ".avi", ".webm":
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				files = append(files, path)
				sizes[path] = info.Size()
			}
		}
		return nil
	})
	return files, sizes, err
}
