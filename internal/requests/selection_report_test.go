package requests

import (
	"errors"
	"fmt"
	"io/fs"
	"testing"
)

func TestDiagnosticReasonKeepsFailureWithoutPrivateDetails(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("creating /disk/private: %w", &fs.PathError{Op: "mkdir", Path: "/disk/private", Err: fs.ErrPermission}), "mkdir: permission denied"},
		{errors.New("duplicate check failed: qBittorrent GET /api/v2/torrents/info returned 500: private body"), "duplicate check failed: qBittorrent GET /api/v2/torrents/info returned 500"},
		{errors.New("GET https://tracker.example/download?apikey=test-key failed"), "GET [URL hidden] failed"},
	} {
		if got := diagnosticReason(tt.err); got != tt.want {
			t.Errorf("diagnostic = %q, want %q", got, tt.want)
		}
	}
}
