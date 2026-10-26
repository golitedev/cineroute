package subtitles

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cineroute/internal/library"
)

// MovieFile comes only from a verified, complete request download receipt.
type MovieFile struct {
	DriveID, Target, Path string
	Size                  int64
}

// QueueRequestedMovie persists just this movie, without scanning or processing
// any other library files. Repeated completion handoffs are idempotent.
func (m *Manager) QueueRequestedMovie(ctx context.Context, file MovieFile) error {
	if !m.Enabled() {
		return errors.New("subtitles are disabled")
	}
	if err := m.StateError(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.batch.Running {
		return errors.New("subtitle worker is busy; movie will be queued after the current job")
	}
	root, info, err := m.validateRequestedMovie(file)
	if err != nil {
		return err
	}
	relative, _ := filepath.Rel(root, file.Path)
	folder := strings.Split(relative, string(filepath.Separator))[0]
	namespace := file.DriveID
	if file.Target == "main" {
		namespace += ":main"
	}
	id := subtitleItemID(namespace, relative)
	previous := m.byID[id]
	if previous != nil && previous.Automatic && previous.VideoSize == info.Size() && previous.VideoMtime == info.ModTime().Unix() {
		return nil
	}
	external, err := discoverExternalSubtitles(filepath.Dir(file.Path), filepath.Base(file.Path))
	if err != nil {
		return err
	}
	var media MediaInfo
	if previous != nil && previous.Probed && previous.VideoSize == info.Size() && previous.VideoMtime == info.ModTime().Unix() {
		media = MediaInfo{Probed: true, Streams: previous.EmbeddedSubStreams, DurationMS: previous.DurationMS}
	}
	item := applyScanResult(previous, file.DriveID, root, folder, file.Path, info, media, external, m.cfg.TargetLanguages, time.Now())
	item.ID = id
	item.LibraryTarget = file.Target
	item.Automatic = true
	item.AutomaticPending = true
	item.AutomaticRetryAt = time.Time{}
	if err := m.store.saveItem(item); err != nil {
		return err
	}
	if previous == nil {
		m.items = append(m.items, item)
	} else {
		*previous = *item
		item = previous
	}
	m.byID[id] = item
	m.sortItemsLocked()
	select {
	case m.autoWake <- struct{}{}:
	default:
	}
	slog.Info("subtitles: requested movie queued", "id", id, "drive", file.DriveID, "target", file.Target)
	return nil
}

// validateRequestedMovie checks the configured destination and every component
// below it. A moved file or symlink cannot redirect automatic subtitle writes.
func (m *Manager) validateRequestedMovie(file MovieFile) (string, os.FileInfo, error) {
	var root string
	for _, drive := range m.lib.Drives() {
		if drive.ID != file.DriveID {
			continue
		}
		if file.Target == "main" {
			root = drive.MovieRoot
		} else if file.Target == "remote" || file.Target == "" {
			root, _ = library.RemoteMovieRoot(drive)
		}
	}
	if root == "" || !filepath.IsAbs(root) || !filepath.IsAbs(file.Path) || file.Size <= 0 || !requestedMovieVideo(file.Path) {
		return "", nil, errors.New("automatic subtitle movie is outside a configured movie destination")
	}
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, file.Path)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", nil, errors.New("automatic subtitle movie is outside its configured root")
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) < 2 {
		return "", nil, errors.New("automatic subtitle movie has no library folder")
	}
	path := root
	var info os.FileInfo
	for _, part := range parts {
		path = filepath.Join(path, part)
		info, err = os.Lstat(path)
		if err != nil {
			return "", nil, fmt.Errorf("verify automatic subtitle path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, errors.New("automatic subtitle path contains a symlink")
		}
	}
	if !info.Mode().IsRegular() || info.Size() != file.Size {
		return "", nil, errors.New("automatic subtitle movie is incomplete or was replaced")
	}
	return root, info, nil
}

func requestedMovieVideo(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".mp4", ".m4v", ".avi", ".webm", ".ts", ".m2ts", ".mov":
		return true
	}
	return false
}

func (m *Manager) validateAutomaticItem(item *Item) error {
	_, info, err := m.validateRequestedMovie(MovieFile{DriveID: item.DriveID, Target: item.LibraryTarget, Path: item.VideoPath, Size: item.VideoSize})
	if err == nil && info.ModTime().Unix() != item.VideoMtime {
		return errors.New("automatic subtitle movie changed since it was queued")
	}
	return err
}

// StartAutomatic resumes durable requested-movie items alongside manual work.
// It never calls StartRun, which could process unrelated pending library items.
func (m *Manager) StartAutomatic(ctx context.Context) {
	if !m.Enabled() || m.StateError() != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.autoCancel != nil {
		return
	}
	ctx, m.autoCancel = context.WithCancel(ctx)
	m.autoWG.Add(1)
	go func() {
		defer m.autoWG.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			for ctx.Err() == nil && m.runNextAutomatic(ctx) {
			}
			select {
			case <-ctx.Done():
				return
			case <-m.autoWake:
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) StopAutomatic() {
	m.mu.Lock()
	cancel := m.autoCancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		m.autoWG.Wait()
	}
}

func (m *Manager) runNextAutomatic(ctx context.Context) bool {
	m.mu.Lock()
	if m.batch.Running || ctx.Err() != nil {
		m.mu.Unlock()
		return false
	}
	var item *Item
	for _, candidate := range m.items {
		if candidate.AutomaticPending && !time.Now().Before(candidate.AutomaticRetryAt) && candidate.Status != StatusSkipped {
			item = candidate
			break
		}
	}
	if item == nil {
		m.mu.Unlock()
		return false
	}
	jobCtx, cancel := context.WithCancel(ctx)
	m.batch = BatchStatus{Running: true, Kind: "automatic", Total: 1, Current: item.VideoPath}
	m.batchCancel = cancel
	m.mu.Unlock()
	defer cancel()
	defer m.finishJob()
	err := m.validateAutomaticItem(item)
	if err == nil {
		_, err = m.processAndPersist(jobCtx, item, runOptions{refreshQuota: true, requireReference: true})
	} else {
		m.mu.Lock()
		item.AutomaticPending, item.Status, item.Error = false, StatusFailed, err.Error()
		err = m.store.saveItem(item)
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.batch.Done = 1
	if jobCtx.Err() != nil && ctx.Err() == nil {
		// An administrator pressed Cancel; a server shutdown instead resumes later.
		item.AutomaticPending = false
		_ = m.store.saveItem(item)
		m.batch.Canceled = true
	}
	if err != nil {
		m.batch.Error = err.Error()
	}
	m.mu.Unlock()
	return ctx.Err() == nil
}
