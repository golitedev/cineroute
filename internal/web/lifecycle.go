package web

import (
	"context"
	"fmt"
)

func (s *Server) Start(ctx context.Context) error {
	if s.accounts.err != nil {
		return fmt.Errorf("initialize account sessions: %w", s.accounts.err)
	}
	if _, err := s.requests.Load(); err != nil {
		return fmt.Errorf("load request policy: %w", err)
	}
	s.engine.Start(ctx)
	return nil
}
func (s *Server) Close() {
	if s.engine != nil {
		s.engine.Stop()
	}
}
