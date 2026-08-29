package companion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"cineroute/internal/library"
)

// HardlinkView is the live inventory returned by the hardlink management API.
// It is deliberately filesystem-backed rather than durable state: hardlinks
// can be moved or renamed outside CineRoute and the next scan should reflect
// that immediately.
type HardlinkView struct {
	Items     []*HardlinkItem `json:"items"`
	Stats     HardlinkStats   `json:"stats"`
	ScannedAt time.Time       `json:"scanned_at"`
	Warnings  []string        `json:"warnings,omitempty"`
}

// HardlinkStats summarizes the folders and files found in the remote roots.
type HardlinkStats struct {
	Total       int   `json:"total"`
	Movies      int   `json:"movies"`
	TVShows     int   `json:"tv_shows"`
	Drives      int   `json:"drives"`
	Healthy     int   `json:"healthy"`
	NeedsRelink int   `json:"needs_relink"`
	Partial     int   `json:"partial"`
	Orphaned    int   `json:"orphaned"`
	LinkedFiles int   `json:"linked_files"`
	LinkedBytes int64 `json:"linked_bytes"`
}

// HardlinkItem describes one remote folder containing files linked to a
// primary library folder. SourcePath is discovered from inode identity, so it
// remains useful when the primary folder has been renamed.
type HardlinkItem struct {
	ID              string         `json:"id"`
	MediaType       string         `json:"media_type"`
	DriveID         string         `json:"drive_id"`
	SourceRoot      string         `json:"source_root"`
	RemoteRoot      string         `json:"remote_root"`
	SourcePath      string         `json:"source_path,omitempty"`
	RemotePath      string         `json:"remote_path"`
	SourceFolder    string         `json:"source_folder,omitempty"`
	RemoteFolder    string         `json:"remote_folder"`
	Status          string         `json:"status"`
	StatusReason    string         `json:"status_reason,omitempty"`
	SourceExists    bool           `json:"source_exists"`
	RemoteExists    bool           `json:"remote_exists"`
	NameMatches     bool           `json:"name_matches"`
	CanRelink       bool           `json:"can_relink"`
	CanRemove       bool           `json:"can_remove"`
	LinkedFiles     int            `json:"linked_files"`
	SourceFileCount int            `json:"source_file_count"`
	RemoteFileCount int            `json:"remote_file_count"`
	ExtraFileCount  int            `json:"extra_file_count"`
	LinkedBytes     int64          `json:"linked_bytes"`
	Files           []HardlinkFile `json:"files,omitempty"`
	ScannedAt       time.Time      `json:"scanned_at"`
}

// HardlinkFile describes a remote file and, when it can be resolved, the
// primary file that shares its inode.
type HardlinkFile struct {
	RelativePath string `json:"relative_path"`
	SourcePath   string `json:"source_path,omitempty"`
	RemotePath   string `json:"remote_path"`
	Size         int64  `json:"size"`
	Linked       bool   `json:"linked"`
}

// HardlinkRemoveResult reports a safe unlink operation. Only files proven to
// be hardlinked to the selected source are removed; unrelated remote files
// are left in place.
type HardlinkRemoveResult struct {
	RemotePath     string `json:"remote_path"`
	RemovedFiles   int    `json:"removed_files"`
	RemainingFiles int    `json:"remaining_files"`
}

// HardlinkManager owns live discovery and safe maintenance of remote
// hardlink trees. It does not depend on Prowlarr or the companion queues, so
// existing hardlinks remain manageable even when companion searching is off.
type HardlinkManager struct {
	lib *library.Scan
	mu  sync.Mutex
}

type hardlinkRoot struct {
	mediaType   string
	driveID     string
	primaryRoot string
	remoteRoot  string
}

type hardlinkSourceFile struct {
	folder   *hardlinkSourceFolder
	path     string
	relative string
	info     os.FileInfo
}

type hardlinkSourceFolder struct {
	path  string
	name  string
	files []hardlinkSourceFile
}

type hardlinkSourceIndex struct {
	files map[hardlinkInode][]*hardlinkSourceFile
}

type hardlinkRemoteFile struct {
	path     string
	relative string
	info     os.FileInfo
	source   *hardlinkSourceFile
	linked   bool
}

type hardlinkCandidate struct {
	folder          *hardlinkSourceFolder
	linkedFiles     int
	linkedBytes     int64
	relativeMatches int
}

type hardlinkInode struct {
	device uint64
	inode  uint64
}

// NewHardlinkManager creates a live hardlink inventory for the configured
// library roots.
func NewHardlinkManager(lib *library.Scan) *HardlinkManager {
	return &HardlinkManager{lib: lib}
}

// View scans the configured primary and remote roots and returns the current
// hardlink inventory.
func (m *HardlinkManager) View(ctx context.Context) (HardlinkView, error) {
	if m == nil || m.lib == nil {
		return HardlinkView{}, errors.New("library scanner is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scanLocked(ctx)
}

func (m *HardlinkManager) scanLocked(ctx context.Context) (HardlinkView, error) {
	scannedAt := time.Now()
	view := HardlinkView{Items: []*HardlinkItem{}, ScannedAt: scannedAt}
	seenDrives := map[string]bool{}
	for _, root := range m.roots() {
		if err := ctx.Err(); err != nil {
			return HardlinkView{}, err
		}
		seenDrives[root.driveID] = true
		items, warnings := scanHardlinkRoot(ctx, root, scannedAt)
		view.Items = append(view.Items, items...)
		view.Warnings = append(view.Warnings, warnings...)
	}
	view.Stats = hardlinkStats(view.Items, seenDrives)
	sort.SliceStable(view.Items, func(i, j int) bool {
		left, right := view.Items[i], view.Items[j]
		if hardlinkStatusRank(left.Status) != hardlinkStatusRank(right.Status) {
			return hardlinkStatusRank(left.Status) < hardlinkStatusRank(right.Status)
		}
		if left.MediaType != right.MediaType {
			return left.MediaType < right.MediaType
		}
		if left.DriveID != right.DriveID {
			return left.DriveID < right.DriveID
		}
		return left.RemotePath < right.RemotePath
	})
	return view, nil
}

// Relink moves a discovered remote folder to the current primary folder name
// and then reapplies the normal source-tree hardlink operation. This handles
// a primary folder rename without asking the user to type filesystem paths.
func (m *HardlinkManager) Relink(ctx context.Context, id string) (HardlinkResult, HardlinkView, error) {
	if m == nil || m.lib == nil {
		return HardlinkResult{}, HardlinkView{}, errors.New("library scanner is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	view, err := m.scanLocked(ctx)
	if err != nil {
		return HardlinkResult{}, HardlinkView{}, err
	}
	item := findHardlinkItem(view.Items, id)
	if item == nil {
		return HardlinkResult{}, view, errors.New("hardlink was not found; scan again to refresh the inventory")
	}
	if !item.SourceExists || item.SourcePath == "" {
		return HardlinkResult{}, view, errors.New("the primary source folder is no longer available, so this hardlink cannot be relinked")
	}
	if !hardlinkPathWithin(item.SourceRoot, item.SourcePath) || !hardlinkPathWithin(item.RemoteRoot, item.RemotePath) {
		return HardlinkResult{}, view, errors.New("hardlink paths are outside the configured library roots")
	}
	destination := filepath.Join(item.RemoteRoot, item.SourceFolder)
	if !hardlinkPathWithin(item.RemoteRoot, destination) {
		return HardlinkResult{}, view, errors.New("the source folder name is not a safe remote destination")
	}
	if filepath.Clean(item.RemotePath) != filepath.Clean(destination) {
		if _, err := os.Lstat(destination); err == nil {
			return HardlinkResult{}, view, fmt.Errorf("remote destination already exists: %s", destination)
		} else if !errors.Is(err, os.ErrNotExist) {
			return HardlinkResult{}, view, fmt.Errorf("inspect remote relink destination: %w", err)
		}
		if err := os.Rename(item.RemotePath, destination); err != nil {
			return HardlinkResult{}, view, fmt.Errorf("rename remote hardlink folder: %w", err)
		}
	}
	result, err := hardlinkTree(item.SourcePath, destination)
	if err != nil {
		return HardlinkResult{}, view, err
	}
	refreshed, scanErr := m.scanLocked(ctx)
	if scanErr != nil {
		return result, HardlinkView{}, scanErr
	}
	return result, refreshed, nil
}

// Remove unlinks only files verified against the source inode. If the source
// is gone, files with more than one link are treated as orphaned hardlinks;
// unrelated files in the remote folder are preserved in either case.
func (m *HardlinkManager) Remove(ctx context.Context, id string) (HardlinkRemoveResult, HardlinkView, error) {
	if m == nil || m.lib == nil {
		return HardlinkRemoveResult{}, HardlinkView{}, errors.New("library scanner is unavailable")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	view, err := m.scanLocked(ctx)
	if err != nil {
		return HardlinkRemoveResult{}, HardlinkView{}, err
	}
	item := findHardlinkItem(view.Items, id)
	if item == nil {
		return HardlinkRemoveResult{}, view, errors.New("hardlink was not found; scan again to refresh the inventory")
	}
	if !item.CanRemove {
		return HardlinkRemoveResult{}, view, errors.New("no removable hardlinked files were found")
	}
	if !hardlinkPathWithin(item.RemoteRoot, item.RemotePath) {
		return HardlinkRemoveResult{}, view, errors.New("hardlink path is outside the configured remote root")
	}
	removed := 0
	for _, file := range item.Files {
		if err := ctx.Err(); err != nil {
			return HardlinkRemoveResult{}, view, err
		}
		if !file.Linked {
			continue
		}
		remoteInfo, statErr := os.Lstat(file.RemotePath)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return HardlinkRemoveResult{}, view, fmt.Errorf("inspect hardlink file %q: %w", file.RemotePath, statErr)
		}
		if remoteInfo.Mode()&os.ModeSymlink != 0 || !remoteInfo.Mode().IsRegular() {
			return HardlinkRemoveResult{}, view, fmt.Errorf("refusing to remove non-regular hardlink path: %s", file.RemotePath)
		}
		if item.SourceExists && file.SourcePath != "" {
			sourceInfo, sourceErr := os.Stat(file.SourcePath)
			if sourceErr != nil {
				return HardlinkRemoveResult{}, view, fmt.Errorf("inspect hardlink source %q: %w", file.SourcePath, sourceErr)
			}
			if !os.SameFile(sourceInfo, remoteInfo) {
				return HardlinkRemoveResult{}, view, fmt.Errorf("hardlink file changed since scan: %s", file.RemotePath)
			}
		} else if hardlinkCount(remoteInfo) < 2 {
			return HardlinkRemoveResult{}, view, fmt.Errorf("refusing to remove an orphaned file without another hardlink: %s", file.RemotePath)
		}
		if err := os.Remove(file.RemotePath); err != nil {
			return HardlinkRemoveResult{}, view, fmt.Errorf("remove hardlink %q: %w", file.RemotePath, err)
		}
		removed++
	}
	if removed == 0 {
		return HardlinkRemoveResult{}, view, errors.New("the selected hardlink files no longer exist; scan again to refresh the inventory")
	}
	if err := pruneEmptyHardlinkDirs(item.RemotePath); err != nil {
		return HardlinkRemoveResult{}, view, err
	}
	refreshed, scanErr := m.scanLocked(ctx)
	if scanErr != nil {
		return HardlinkRemoveResult{RemotePath: item.RemotePath, RemovedFiles: removed}, HardlinkView{}, scanErr
	}
	remaining := 0
	if _, statErr := os.Lstat(item.RemotePath); statErr == nil {
		remaining = countRegularFiles(item.RemotePath)
	}
	return HardlinkRemoveResult{RemotePath: item.RemotePath, RemovedFiles: removed, RemainingFiles: remaining}, refreshed, nil
}

func (m *HardlinkManager) roots() []hardlinkRoot {
	var roots []hardlinkRoot
	for _, drive := range m.lib.Drives() {
		if drive.MovieRoot != "" {
			if remoteRoot, ok := m.lib.MovieRemotePath(drive.ID, ""); ok && remoteRoot != "" {
				roots = append(roots, hardlinkRoot{mediaType: "movie", driveID: drive.ID, primaryRoot: drive.MovieRoot, remoteRoot: remoteRoot})
			}
		}
		if drive.TVRoot != "" {
			if remoteRoot, ok := m.lib.TVRemotePath(drive.ID, ""); ok && remoteRoot != "" {
				roots = append(roots, hardlinkRoot{mediaType: "tv", driveID: drive.ID, primaryRoot: drive.TVRoot, remoteRoot: remoteRoot})
			}
		}
	}
	return roots
}

func scanHardlinkRoot(ctx context.Context, root hardlinkRoot, scannedAt time.Time) ([]*HardlinkItem, []string) {
	index, sourceErr := buildHardlinkSourceIndex(root.primaryRoot)
	var warnings []string
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("%s %s source: %v", root.driveID, root.mediaType, sourceErr))
	}

	entries, err := os.ReadDir(root.remoteRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, warnings
	}
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("%s %s remote root: %v", root.driveID, root.mediaType, err))
		return nil, warnings
	}
	items := make([]*HardlinkItem, 0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			warnings = append(warnings, err.Error())
			return items, warnings
		}
		remotePath := filepath.Join(root.remoteRoot, entry.Name())
		info, infoErr := os.Lstat(remotePath)
		if infoErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		remoteFiles, walkErr := collectHardlinkRemoteFiles(remotePath)
		if walkErr != nil {
			warnings = append(warnings, fmt.Sprintf("inspect %s: %v", remotePath, walkErr))
			continue
		}
		if len(remoteFiles) == 0 {
			continue
		}
		item := makeHardlinkItem(root, remotePath, remoteFiles, index, scannedAt)
		if item != nil {
			items = append(items, item)
		}
	}
	return items, warnings
}

func buildHardlinkSourceIndex(root string) (hardlinkSourceIndex, error) {
	index := hardlinkSourceIndex{files: map[hardlinkInode][]*hardlinkSourceFile{}}
	if root == "" {
		return index, nil
	}
	info, err := os.Lstat(root)
	if err != nil {
		return index, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return index, fmt.Errorf("configured source root is not a real directory")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return index, err
	}
	for _, entry := range entries {
		folderPath := filepath.Join(root, entry.Name())
		folderInfo, infoErr := os.Lstat(folderPath)
		if infoErr != nil || !folderInfo.IsDir() || folderInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		files, walkErr := collectHardlinkFiles(folderPath)
		if walkErr != nil {
			return index, fmt.Errorf("inspect %s: %w", folderPath, walkErr)
		}
		folder := &hardlinkSourceFolder{path: folderPath, name: entry.Name(), files: files}
		for i := range folder.files {
			folder.files[i].folder = folder
			key, ok := hardlinkFileInode(folder.files[i].info)
			if !ok {
				continue
			}
			index.files[key] = append(index.files[key], &folder.files[i])
		}
	}
	return index, nil
}

func collectHardlinkFiles(root string) ([]hardlinkSourceFile, error) {
	files := make([]hardlinkSourceFile, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("derive safe relative path for %q", path)
		}
		files = append(files, hardlinkSourceFile{path: path, relative: relative, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return files, nil
}

func collectHardlinkRemoteFiles(root string) ([]hardlinkRemoteFile, error) {
	files := make([]hardlinkRemoteFile, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("derive safe relative path for %q", path)
		}
		files = append(files, hardlinkRemoteFile{path: path, relative: relative, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].relative < files[j].relative })
	return files, nil
}

func makeHardlinkItem(root hardlinkRoot, remotePath string, remoteFiles []hardlinkRemoteFile, index hardlinkSourceIndex, scannedAt time.Time) *HardlinkItem {
	candidates := map[string]*hardlinkCandidate{}
	orphanFiles := 0
	for i := range remoteFiles {
		key, hasInode := hardlinkFileInode(remoteFiles[i].info)
		if hasInode {
			matches := index.files[key]
			if source := chooseHardlinkSource(matches, remoteFiles[i].relative); source != nil {
				remoteFiles[i].source = source
				remoteFiles[i].linked = true
				candidate := candidates[source.folder.path]
				if candidate == nil {
					candidate = &hardlinkCandidate{folder: source.folder}
					candidates[source.folder.path] = candidate
				}
				candidate.linkedFiles++
				candidate.linkedBytes += remoteFiles[i].info.Size()
				if source.relative == remoteFiles[i].relative {
					candidate.relativeMatches++
				}
				continue
			}
			if hardlinkCount(remoteFiles[i].info) >= 2 {
				remoteFiles[i].linked = true
				orphanFiles++
			}
		}
	}

	var best *hardlinkCandidate
	for _, candidate := range candidates {
		if best == nil || betterHardlinkCandidate(candidate, best, filepath.Base(remotePath)) {
			best = candidate
		}
	}
	if best == nil && orphanFiles == 0 {
		return nil
	}

	item := &HardlinkItem{
		ID:           hardlinkID(root.mediaType, root.driveID, remotePath),
		MediaType:    root.mediaType,
		DriveID:      root.driveID,
		SourceRoot:   root.primaryRoot,
		RemoteRoot:   root.remoteRoot,
		RemotePath:   remotePath,
		RemoteFolder: filepath.Base(remotePath),
		RemoteExists: true,
		ScannedAt:    scannedAt,
	}
	for _, remoteFile := range remoteFiles {
		file := HardlinkFile{
			RelativePath: remoteFile.relative,
			RemotePath:   remoteFile.path,
			Size:         remoteFile.info.Size(),
			Linked:       remoteFile.linked,
		}
		if remoteFile.source != nil {
			file.SourcePath = remoteFile.source.path
		}
		item.Files = append(item.Files, file)
	}
	item.RemoteFileCount = len(remoteFiles)
	item.LinkedFiles = orphanFiles
	for _, remoteFile := range remoteFiles {
		if remoteFile.linked && remoteFile.source == nil {
			item.LinkedBytes += remoteFile.info.Size()
		}
	}

	if best == nil {
		item.Status = "orphaned"
		item.StatusReason = "The primary source folder could not be found; only files with another inode link were detected."
		item.ExtraFileCount = item.RemoteFileCount - item.LinkedFiles
		item.CanRemove = item.LinkedFiles > 0
		return item
	}

	item.SourcePath = best.folder.path
	item.SourceFolder = best.folder.name
	item.SourceExists = true
	item.SourceFileCount = len(best.folder.files)
	item.LinkedFiles = best.linkedFiles
	item.LinkedBytes = best.linkedBytes
	item.ExtraFileCount = item.RemoteFileCount - item.LinkedFiles
	item.NameMatches = item.SourceFolder == item.RemoteFolder
	item.CanRelink = true
	item.CanRemove = item.LinkedFiles > 0
	if !item.NameMatches {
		item.Status = "needs_relink"
		item.StatusReason = fmt.Sprintf("Primary folder is %q while the remote folder is %q.", item.SourceFolder, item.RemoteFolder)
	} else if item.LinkedFiles < item.SourceFileCount {
		item.Status = "partial"
		item.StatusReason = fmt.Sprintf("%d of %d primary files are linked.", item.LinkedFiles, item.SourceFileCount)
	} else {
		item.Status = "healthy"
		item.StatusReason = "All primary files are linked with matching folder names."
	}
	return item
}

func chooseHardlinkSource(matches []*hardlinkSourceFile, relative string) *hardlinkSourceFile {
	if len(matches) == 0 {
		return nil
	}
	for _, match := range matches {
		if match.relative == relative {
			return match
		}
	}
	return matches[0]
}

func betterHardlinkCandidate(candidate, current *hardlinkCandidate, remoteFolder string) bool {
	candidateNameMatch := candidate.folder.name == remoteFolder
	currentNameMatch := current.folder.name == remoteFolder
	if candidateNameMatch != currentNameMatch {
		return candidateNameMatch
	}
	if candidate.linkedFiles != current.linkedFiles {
		return candidate.linkedFiles > current.linkedFiles
	}
	if candidate.relativeMatches != current.relativeMatches {
		return candidate.relativeMatches > current.relativeMatches
	}
	return candidate.folder.path < current.folder.path
}

func hardlinkID(mediaType, driveID, remotePath string) string {
	sum := sha256.Sum256([]byte(mediaType + "\x00" + driveID + "\x00" + filepath.Clean(remotePath)))
	return "hl_" + hex.EncodeToString(sum[:])[:16]
}

func hardlinkStats(items []*HardlinkItem, drives map[string]bool) HardlinkStats {
	stats := HardlinkStats{Total: len(items), Drives: len(drives)}
	for _, item := range items {
		switch item.MediaType {
		case "movie":
			stats.Movies++
		case "tv":
			stats.TVShows++
		}
		switch item.Status {
		case "healthy":
			stats.Healthy++
		case "needs_relink":
			stats.NeedsRelink++
		case "partial":
			stats.Partial++
		case "orphaned":
			stats.Orphaned++
		}
		stats.LinkedFiles += item.LinkedFiles
		stats.LinkedBytes += item.LinkedBytes
	}
	return stats
}

func hardlinkStatusRank(status string) int {
	switch status {
	case "needs_relink":
		return 0
	case "partial":
		return 1
	case "orphaned":
		return 2
	case "healthy":
		return 3
	default:
		return 4
	}
}

func findHardlinkItem(items []*HardlinkItem, id string) *HardlinkItem {
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	return nil
}

func hardlinkFileInode(info os.FileInfo) (hardlinkInode, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return hardlinkInode{}, false
	}
	return hardlinkInode{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, true
}

func hardlinkCount(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0
	}
	return uint64(stat.Nlink)
}

func hardlinkPathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "." || path == "." || !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return false
	}
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func pruneEmptyHardlinkDirs(root string) error {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect remote hardlink folder: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("remote hardlink path is not a real directory: %s", root)
	}
	dirs := []string{root}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect remote hardlink folders: %w", err)
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("inspect remote hardlink folder %q: %w", dir, err)
		}
		if len(entries) == 0 {
			if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove empty hardlink folder %q: %w", dir, err)
			}
		}
	}
	return nil
}

func countRegularFiles(root string) int {
	count := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			if info, infoErr := entry.Info(); infoErr == nil && info.Mode().IsRegular() {
				count++
			}
		}
		return nil
	})
	return count
}
