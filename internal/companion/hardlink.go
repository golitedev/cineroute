package companion

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// HardlinkResult describes one completed main-to-remote folder link.
type HardlinkResult struct {
	SourcePath      string `json:"source_path"`
	DestinationPath string `json:"destination_path"`
	LinkedFiles     int    `json:"linked_files"`
	ExistingFiles   int    `json:"existing_files"`
}

type hardlinkEntry struct {
	source string
	target string
	dir    bool
	same   bool
}

// Hardlink recreates an item's main-library tree below its configured remote
// folder and links every regular file. Existing links to the same inode make
// the operation safe to retry; any other destination conflict is rejected.
func (m *Manager) Hardlink(id string) (HardlinkResult, error) {
	if !m.Enabled() {
		return HardlinkResult{}, errors.New("1080p companions are disabled")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stateErr != nil {
		return HardlinkResult{}, m.stateErr
	}
	item := m.movieLocked(id)
	if item == nil {
		return HardlinkResult{}, fmt.Errorf("companion %s not found", m.itemLabel())
	}
	if item.Missing {
		return HardlinkResult{}, fmt.Errorf("%s folder is no longer present in the configured library roots", m.itemLabel())
	}
	if item.Status == StatusSearching || item.Status == StatusSubmitting {
		return HardlinkResult{}, fmt.Errorf("this companion is currently %s", item.Status)
	}
	destination, ok := m.remotePath(item.DriveID, item.FolderName)
	if !ok || destination == "" {
		return HardlinkResult{}, fmt.Errorf("drive %s has no %s remote root configured", item.DriveID, m.itemLabel())
	}
	result, err := hardlinkTree(item.Path, destination)
	if err != nil {
		return HardlinkResult{}, err
	}

	now := time.Now()
	item.RemotePath = destination
	item.Status = StatusComplete
	item.Error = ""
	item.QBHash = ""
	item.UpdatedAt = now
	item.AddedAt = &now
	delete(m.searches, id)
	if err := m.persistLocked(); err != nil {
		return HardlinkResult{}, fmt.Errorf("files were hardlinked, but companion state could not be saved: %w", err)
	}
	return result, nil
}

func hardlinkTree(sourceRoot, destinationRoot string) (HardlinkResult, error) {
	sourceRoot = filepath.Clean(sourceRoot)
	destinationRoot = filepath.Clean(destinationRoot)
	result := HardlinkResult{SourcePath: sourceRoot, DestinationPath: destinationRoot}
	if sourceRoot == "." || destinationRoot == "." || sourceRoot == destinationRoot {
		return result, errors.New("hardlink source and destination must be different absolute folders")
	}
	if !filepath.IsAbs(sourceRoot) || !filepath.IsAbs(destinationRoot) {
		return result, errors.New("hardlink source and destination must be absolute folders")
	}
	if pathsOverlap(sourceRoot, destinationRoot) {
		return result, errors.New("hardlink source and destination folders must not contain one another")
	}
	sourceInfo, err := os.Lstat(sourceRoot)
	if err != nil {
		return result, fmt.Errorf("inspect hardlink source: %w", err)
	}
	if !sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return result, errors.New("hardlink source must be a real directory, not a symlink")
	}

	entries := []hardlinkEntry{{source: sourceRoot, target: destinationRoot, dir: true}}
	err = filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceRoot {
			return nil
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) {
			return fmt.Errorf("derive safe hardlink path for %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("hardlink source contains a symlink: %s", relative)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("hardlink source contains a non-regular file: %s", relative)
		}
		entries = append(entries, hardlinkEntry{
			source: path,
			target: filepath.Join(destinationRoot, relative),
			dir:    info.IsDir(),
		})
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("inspect hardlink source tree: %w", err)
	}

	files := 0
	for i := range entries {
		entry := &entries[i]
		if !entry.dir {
			files++
		}
		targetInfo, err := os.Lstat(entry.target)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("inspect hardlink destination %q: %w", entry.target, err)
		}
		if entry.dir {
			if !targetInfo.IsDir() || targetInfo.Mode()&os.ModeSymlink != 0 {
				return result, fmt.Errorf("hardlink destination conflicts with a non-directory: %s", entry.target)
			}
			continue
		}
		sourceInfo, err := os.Stat(entry.source)
		if err != nil {
			return result, fmt.Errorf("reinspect hardlink source %q: %w", entry.source, err)
		}
		if !targetInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, targetInfo) {
			return result, fmt.Errorf("hardlink destination already contains a different file: %s", entry.target)
		}
		entry.same = true
		result.ExistingFiles++
	}
	if files == 0 {
		return result, errors.New("hardlink source contains no regular files")
	}

	for _, entry := range entries {
		if !entry.dir {
			continue
		}
		if err := os.MkdirAll(entry.target, 0o755); err != nil {
			return result, fmt.Errorf("create hardlink destination folder %q: %w", entry.target, err)
		}
	}
	for _, entry := range entries {
		if entry.dir || entry.same {
			continue
		}
		if err := os.Link(entry.source, entry.target); err != nil {
			switch {
			case errors.Is(err, syscall.EXDEV):
				return result, fmt.Errorf("hardlink %q: source and destination are on different mounts or Btrfs subvolumes", entry.target)
			case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
				return result, fmt.Errorf("hardlink %q: permission denied for CineRoute's container user: %w", entry.target, err)
			default:
				return result, fmt.Errorf("hardlink %q: %w", entry.target, err)
			}
		}
		result.LinkedFiles++
	}
	return result, nil
}

func pathsOverlap(first, second string) bool {
	for _, pair := range [][2]string{{first, second}, {second, first}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != ".." && !filepath.IsAbs(relative) && (relative == "." || !startsWithParent(relative)) {
			return true
		}
	}
	return false
}

func startsWithParent(path string) bool {
	return path == ".." || len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}
