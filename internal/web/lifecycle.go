package web

import (
	"context"
	"fmt"

	"cineroute/internal/music"
)

func (s *Server) Start(ctx context.Context) error {
	if err := s.cfg.ValidateServing(); err != nil {
		return err
	}
	if s.accounts.err != nil {
		return fmt.Errorf("initialize account sessions: %w", s.accounts.err)
	}
	if _, err := s.requests.Load(); err != nil {
		return fmt.Errorf("load request policy: %w", err)
	}
	if s.cfg.Music.Enabled {
		var err error
		s.music, err = music.Start(ctx, &s.cfg.Music.Config)
		if err != nil {
			return fmt.Errorf("start music: %w", err)
		}
	}
	s.subtitles.StartAutomatic(ctx)
	s.engine.Start(ctx)
	return nil
}
func (s *Server) Close() {
	s.music.Close()
	if s.engine != nil {
		s.engine.Stop()
	}
	if s.subtitles != nil {
		s.subtitles.StopAutomatic()
	}
}
