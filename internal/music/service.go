// Package music runs the Qobuz download and import worker inside CineRoute.
package music

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"cineroute/internal/music/config"
	"cineroute/internal/music/jobs"
	"cineroute/internal/music/navidrome"
	"cineroute/internal/music/qobuz"
	"cineroute/internal/music/streamrip"
)

// Catalog is the read-only catalog used by the public portal.
type Catalog interface {
	Search(context.Context, string, int, int) (json.RawMessage, error)
	AlbumRaw(context.Context, string) (json.RawMessage, error)
	ArtistPage(context.Context, string) (json.RawMessage, error)
	ArtistReleases(context.Context, string, string, int, int, int) (json.RawMessage, error)
}

// Service owns the music catalog and durable worker, with no separate server.
type Service struct {
	Catalog Catalog
	Jobs    *jobs.Manager
	cancel  context.CancelFunc
	done    chan struct{}
}

func Start(parent context.Context, cfg *config.Config) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	for _, tool := range []string{cfg.Download.RipCommand, "flac", "metaflac"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("music tool %q: %w", tool, err)
		}
	}
	if stat, err := os.Stat(cfg.Paths.Library); err != nil || !stat.IsDir() {
		return nil, fmt.Errorf("music library %q must be an existing directory", cfg.Paths.Library)
	}
	probe, err := os.CreateTemp(cfg.Paths.Library, ".cineroute-write-test-*")
	if err != nil {
		return nil, fmt.Errorf("music library is not writable: %w", err)
	}
	name := probe.Name()
	probe.Close()
	os.Remove(name)
	files := streamrip.FilesIn(cfg.Paths.Data)
	setup, cancelSetup := context.WithTimeout(parent, time.Minute)
	defer cancelSetup()
	if _, err := streamrip.WriteConfig(setup, cfg, files); err != nil {
		return nil, fmt.Errorf("music downloader configuration: %w", err)
	}
	catalog := qobuz.New(cfg.Qobuz.UserID, cfg.Qobuz.Token, func() string {
		if cfg.Qobuz.AppID != "" {
			return cfg.Qobuz.AppID
		}
		return streamrip.CachedAppID(files)
	})
	var afterImport func(context.Context, string)
	if cfg.Navidrome.URL != "" {
		nd := navidrome.New(cfg.Navidrome.URL, cfg.Navidrome.Username, cfg.Navidrome.Password)
		afterImport = func(ctx context.Context, _ string) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if err := nd.StartScan(ctx); err != nil {
				slog.Warn("music library scan", "err", err)
			}
		}
	}
	manager, err := jobs.New(cfg, jobs.Deps{Albums: catalog, Files: files, Logger: slog.Default(), AfterImport: afterImport})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{Catalog: catalog, Jobs: manager, cancel: cancel, done: make(chan struct{})}
	go func() { defer close(s.done); manager.Run(ctx) }()
	return s, nil
}

func (s *Service) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
		<-s.done
	}
}
