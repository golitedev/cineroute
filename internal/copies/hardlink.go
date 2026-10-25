package copies

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// HardlinkResult describes one completed link between matching library roots.
type HardlinkResult struct {
	SourcePath string `json:"source_path"`
	// DestinationPath is the remote folder that mirrors SourcePath after the
	// operation completes.
	DestinationPath string `json:"destination_path"`
	LinkedFiles     int    `json:"linked_files"`
	ExistingFiles   int    `json:"existing_files"`
	MovedFiles      int    `json:"moved_files,omitempty"`
	// RemovedDuplicateFiles counts remote files that were unlinked because the
	// primary tree does not contain them: old links left behind by renames or
	// replaced subtitles, and unrelated extras. Only used by reconcile, never
	// by the additive hardlinkTree.
	RemovedDuplicateFiles int `json:"removed_duplicate_files,omitempty"`
}

type hardlinkEntry struct {
	source string
	target string
	dir    bool
	same   bool
}

func hardlinkTree(sourceRoot, destinationRoot string) (HardlinkResult, error) {
	sourceRoot = filepath.Clean(sourceRoot)
	destinationRoot = filepath.Clean(destinationRoot)
	result := HardlinkResult{SourcePath: sourceRoot, DestinationPath: destinationRoot}
	if err := validateHardlinkRoots(sourceRoot, destinationRoot); err != nil {
		return result, err
	}

	entries := []hardlinkEntry{{source: sourceRoot, target: destinationRoot, dir: true}}
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
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
