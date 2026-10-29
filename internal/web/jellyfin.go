package web

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"cineroute/internal/requests"
)

// queueJellyfinUpdates reconciles durable completion records rather than relying
// on an in-memory callback. A crash between completion and enqueue is recoverable.
func (s *Server) queueJellyfinUpdates() error {
	if s.jellyfin == nil {
		return nil
	}
	downloads, err := s.requests.Downloads()
	if err != nil {
		return err
	}
	for _, d := range downloads {
		if d.Status != "complete" {
			continue
		}
		folders := map[string]bool{}
		for path := range d.FileSizes {
			if requests.IsVideo(path) && !requests.IsExtra(path) {
				folders[filepath.Dir(path)] = true
			}
		}
		for folder := range folders {
			id := fmt.Sprintf("download:%x", sha256.Sum256([]byte(d.Hash+"\x00"+d.Target+"\x00"+folder+"\x00"+d.CompletedAt.String())))
			if err := s.jellyfin.Enqueue(id, folder); err != nil {
				return err
			}
		}
	}
	updates, err := s.subtitles.LibraryUpdates()
	if err != nil {
		return err
	}
	for path, revision := range updates {
		id := fmt.Sprintf("subtitle:%x", sha256.Sum256([]byte(path+"\x00"+revision)))
		if err := s.jellyfin.Enqueue(id, filepath.Dir(path)); err != nil {
			return err
		}
		if err := s.subtitles.AcknowledgeLibraryUpdate(path, revision); err != nil {
			return err
		}
	}
	return nil
}
