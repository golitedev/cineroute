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
	"cineroute/internal/music/lyrics"
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
	Lyrics  *lyrics.Manager
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
	var scan func(context.Context) error
	if cfg.Navidrome.URL != "" {
		nd := navidrome.New(cfg.Navidrome.URL, cfg.Navidrome.Username, cfg.Navidrome.Password)
		scan = nd.StartScan
		afterImport = func(ctx context.Context, _ string) {
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			if err := nd.StartScan(ctx); err != nil {
				slog.Warn("music library scan", "err", err)
			}
		}
	}
	var lyricQueue *lyrics.Manager
	var enqueue func(string, string) error
	if cfg.Lyrics.Enabled {
		// Packaging failures should be diagnosed at startup, before accepting work.
		for _, path := range []string{cfg.Lyrics.Script, cfg.Lyrics.Engine} {
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("lyrics tool %q is unavailable (install bundled tools or disable music.lyrics.enabled)", path)
			}
		}
		lyricQueue, err = lyrics.New(lyrics.Options{Library: cfg.Paths.Library, Data: cfg.Paths.Data,
			Script: cfg.Lyrics.Script, Engine: cfg.Lyrics.Engine, MaxAttempts: cfg.Lyrics.MaxAttempts,
			RetryDelay: time.Duration(cfg.Lyrics.RetryMinutes) * time.Minute, Timeout: time.Duration(cfg.Lyrics.TimeoutMinutes) * time.Minute,
			VerifiedOnly: cfg.Lyrics.VerifiedOnly, SyncedOnly: cfg.Lyrics.SyncedOnly, RequireAlbumMatch: cfg.Lyrics.RequireAlbumMatch}, nil, scan)
		if err != nil {
			return nil, fmt.Errorf("lyrics queue: %w", err)
		}
		enqueue = lyricQueue.Enqueue
	}
	manager, err := jobs.New(cfg, jobs.Deps{Albums: catalog, Files: files, Logger: slog.Default(), AfterImport: afterImport, EnqueueLyrics: enqueue})
	if err != nil {
		if lyricQueue != nil {
			lyricQueue.Close()
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Service{Catalog: catalog, Jobs: manager, Lyrics: lyricQueue, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		lyricsDone := make(chan struct{})
		reconcileDone := make(chan struct{})
		if lyricQueue != nil {
			go func() { defer close(lyricsDone); lyricQueue.Run(ctx) }()
			// Repair durable handoffs after an interrupted import. Only jobs marked
			// for lyrics when imported are eligible; this never scans the old library.
			go func() {
				defer close(reconcileDone)
				ticker := time.NewTicker(30 * time.Second)
				defer ticker.Stop()
				for {
					for _, job := range manager.List() {
						if job.LyricsRequested && !job.LyricsEnqueued && job.LibraryPath != "" {
							if err := lyricQueue.Enqueue(job.ID, job.LibraryPath); err != nil {
								slog.Warn("reconciling lyrics", "job", job.ID, "err", err)
							} else {
								manager.ConfirmLyricsEnqueued(job.ID)
							}
						}
					}
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
		} else {
			close(lyricsDone)
			close(reconcileDone)
		}
		manager.Run(ctx)
		<-lyricsDone
		<-reconcileDone
		if lyricQueue != nil {
			lyricQueue.Close()
		}
	}()
	return s, nil
}

func (s *Service) Close() {
	if s != nil && s.cancel != nil {
		s.cancel()
		<-s.done
	}
}
